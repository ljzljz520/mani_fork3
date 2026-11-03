package exec

import (
	"fmt"
	"io"
	"os"

	"github.com/gookit/color"

	"github.com/alajmo/mani/core"
	"github.com/alajmo/mani/core/dao"
	"github.com/alajmo/mani/core/print"
)

type Exec struct {
	Clients  []Client
	Projects []dao.Project
	Tasks    []dao.Task
	Config   dao.Config

	// envResolver evaluates command substitutions lazily at real execution
	// time, once per definition for the whole batch. nil until a run starts,
	// so parsing, listing, checking, TUI browsing and dry-run stay side-effect free.
	envResolver *dao.EnvResolver
}

type TableCmd struct {
	rIndex int
	cIndex int
	client Client
	dryRun bool

	desc     string
	name     string
	shell    string
	env      []dao.EnvVar
	cmd      string
	cmdArr   []string
	numTasks int
	resolver *dao.EnvResolver
}

func (exec *Exec) Run(
	userArgs []string,
	runFlags *core.RunFlags,
	setRunFlags *core.SetRunFlags,
) error {
	projects := exec.Projects
	tasks := exec.Tasks

	err := exec.ParseTask(userArgs, runFlags, setRunFlags)
	if err != nil {
		return err
	}

	clientCh := make(chan Client, len(projects))
	errCh := make(chan error, len(projects))
	err = exec.SetClients(clientCh, errCh)
	if err != nil {
		return err
	}

	// Dynamic env values are evaluated only during real execution below.
	exec.envResolver = dao.NewEnvResolver()

	// Describe task
	if runFlags.Describe {
		out := print.PrintTaskBlock([]dao.Task{tasks[0]}, false, tasks[0].ThemeData.Block, print.GookitFormatter{})
		fmt.Print(out)
	}

	exec.CheckTaskNoColor()

	switch tasks[0].SpecData.Output {
	case "table", "html", "markdown":
		fmt.Println("")
		data := exec.Table(runFlags)
		options := print.PrintTableOptions{
			Theme:            tasks[0].ThemeData,
			Output:           tasks[0].SpecData.Output,
			Color:            *tasks[0].ThemeData.Color,
			AutoWrap:         true,
			OmitEmptyRows:    tasks[0].SpecData.OmitEmptyRows,
			OmitEmptyColumns: tasks[0].SpecData.OmitEmptyColumns,
		}
		print.PrintTable(data.Rows, options, data.Headers[0:1], data.Headers[1:], os.Stdout)
		fmt.Println("")
	default:
		exec.Text(runFlags.DryRun, os.Stdout, os.Stderr)
	}

	return nil
}

func (exec *Exec) RunTUI(
	userArgs []string,
	runFlags *core.RunFlags,
	setRunFlags *core.SetRunFlags,
	output string,
	outWriter io.Writer,
	errWriter io.Writer,
) error {
	projects := exec.Projects
	err := exec.ParseTask(userArgs, runFlags, setRunFlags)
	if err != nil {
		return err
	}

	tasks := exec.Tasks

	// Dynamic env values are evaluated only during real execution below.
	exec.envResolver = dao.NewEnvResolver()

	clientCh := make(chan Client, len(projects))
	errCh := make(chan error, len(projects))
	err = exec.SetClients(clientCh, errCh)
	if err != nil {
		return err
	}

	data := dao.TableOutput{}
	switch output {
	case "table":
		data = exec.Table(runFlags)
		options := print.PrintTableOptions{
			Theme:            tasks[0].ThemeData,
			Output:           tasks[0].SpecData.Output,
			Color:            *tasks[0].ThemeData.Color,
			AutoWrap:         false,
			OmitEmptyRows:    tasks[0].SpecData.OmitEmptyRows,
			OmitEmptyColumns: tasks[0].SpecData.OmitEmptyColumns,
		}
		print.PrintTable(data.Rows, options, data.Headers[0:1], data.Headers[1:], outWriter)
		return nil
	default:
		exec.Text(runFlags.DryRun, outWriter, errWriter)
	}

	return err
}

func (exec *Exec) SetClients(
	clientCh chan Client,
	errCh chan error,
) error {
	config := exec.Config
	ignoreNonExisting := exec.Tasks[0].SpecData.IgnoreNonExisting
	projects := exec.Projects

	var clients []Client
	for i, project := range projects {
		func(i int, project dao.Project) {
			projectPath, err := core.GetAbsolutePath(config.Path, project.Path, project.Name)
			if err != nil {
				errCh <- &core.FailedToParsePath{Name: projectPath}
				return
			}
			if _, err := os.Stat(projectPath); os.IsNotExist(err) && !ignoreNonExisting {
				errCh <- &core.PathDoesNotExist{Path: projectPath}
				return
			}

			client := Client{Path: projectPath, Name: project.Name, Env: project.EnvList}
			clientCh <- client

			clients = append(clients, client)
		}(i, project)
	}

	close(clientCh)
	close(errCh)

	// Return if there's any errors
	for err := range errCh {
		return err
	}

	exec.Clients = clients

	return nil
}

// ParseTask processes and updates task configurations based on runtime flags and user arguments.
// It handles theme, specification, environment variables, and execution settings for each task.
//
// The function performs these operations for each task:
// 1. Merges configuration environment variables (as raw expressions)
// 2. Updates theme if specified
// 3. Updates spec settings if provided
// 4. Applies runtime execution flags
// 5. Merges environment variables for the task and its commands
//
// No command substitution is executed here: expressions are preserved and
// evaluated lazily when a command really runs (see EnvResolver).
//
// Environment variable precedence (highest first):
// 1. User provided arguments
// 2. Command level variables
// 3. Task level variables
// 4. Configuration level variables
// (project level variables are merged at execution time)
func (exec *Exec) ParseTask(userArgs []string, runFlags *core.RunFlags, setRunFlags *core.SetRunFlags) error {
	configEnv := exec.Config.EnvList
	userEnv := dao.ParseUserEnv(userArgs)

	for i := range exec.Tasks {
		// Update theme property if user flag is provided
		if runFlags.Theme != "" {
			theme, err := exec.Config.GetTheme(runFlags.Theme)
			if err != nil {
				return err
			}

			exec.Tasks[i].ThemeData = *theme
		}

		if runFlags.Spec != "" {
			spec, err := exec.Config.GetSpec(runFlags.Spec)
			if err != nil {
				return err
			}
			exec.Tasks[i].SpecData = *spec
		}

		// Update output property if user flag is provided
		if runFlags.Output != "" {
			exec.Tasks[i].SpecData.Output = runFlags.Output
		}

		// TTY
		if setRunFlags.TTY {
			exec.Tasks[i].TTY = runFlags.TTY
		}

		// Omit rows which provide empty output
		if setRunFlags.OmitEmptyRows {
			exec.Tasks[i].SpecData.OmitEmptyRows = runFlags.OmitEmptyRows
		}

		// Omit columns which provide empty output
		if setRunFlags.OmitEmptyColumns {
			exec.Tasks[i].SpecData.OmitEmptyColumns = runFlags.OmitEmptyColumns
		}

		if setRunFlags.IgnoreErrors {
			exec.Tasks[i].SpecData.IgnoreErrors = runFlags.IgnoreErrors
		}

		if setRunFlags.IgnoreNonExisting {
			exec.Tasks[i].SpecData.IgnoreNonExisting = runFlags.IgnoreNonExisting
		}

		// If parallel flag is set to true, then update task specs
		if setRunFlags.Parallel {
			exec.Tasks[i].SpecData.Parallel = runFlags.Parallel
		}

		if setRunFlags.Forks {
			exec.Tasks[i].SpecData.Forks = runFlags.Forks
		}

		// Merge env here instead of at config parse since we're only
		// interested in tasks run, and not all tasks. Also, userArgs is
		// not present in the config. Expressions stay raw.
		exec.Tasks[i].EnvList = dao.ParseTaskEnv(
			exec.Tasks[i].Env,
			exec.Tasks[i].GetContext(),
			userEnv,
			nil,
			configEnv,
		)

		// Set environment variables for sub-commands
		for j := range exec.Tasks[i].Commands {
			exec.Tasks[i].Commands[j].EnvList = dao.ParseTaskEnv(
				exec.Tasks[i].Commands[j].Env,
				exec.Tasks[i].GetContext(),
				userEnv,
				exec.Tasks[i].EnvList,
				configEnv,
			)
		}
	}

	return nil
}

func (exec *Exec) CheckTaskNoColor() {
	task := exec.Tasks[0]

	for _, env := range task.EnvList {
		if env.Name == "NO_COLOR" {
			color.Disable()
			break
		}
	}
}

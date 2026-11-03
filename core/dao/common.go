package dao

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/gookit/color"
	"gopkg.in/yaml.v3"

	"github.com/alajmo/mani/core"
)

// Resource Errors

type ResourceErrors[T any] struct {
	Resource *T
	Errors   []error
}

type Resource interface {
	GetContext() string
	GetContextLine() int
}

func FormatErrors(re Resource, errs []error) error {
	var msg = ""
	partsRe := regexp.MustCompile(`line (\d*): (.*)`)

	context := re.GetContext()

	var errPrefix = color.FgRed.Sprintf("error")
	var ptrPrefix = color.FgBlue.Sprintf("-->")
	for _, err := range errs {
		match := partsRe.FindStringSubmatch(err.Error())
		// In-case matching fails, return unformatted error
		if len(match) != 3 {
			contextLine := re.GetContextLine()

			if contextLine == -1 {
				msg = fmt.Sprintf("%s%s: %s\n  %s %s\n\n", msg, errPrefix, err, ptrPrefix, context)
			} else {
				msg = fmt.Sprintf("%s%s: %s\n  %s %s:%d\n\n", msg, errPrefix, err, ptrPrefix, context, contextLine)
			}
		} else {
			msg = fmt.Sprintf("%s%s: %s\n  %s %s:%s\n\n", msg, errPrefix, match[2], ptrPrefix, context, match[1])
		}
	}

	if msg != "" {
		return &core.ConfigErr{Msg: msg}
	}

	return nil
}

// ENV

// EnvVar is a single environment variable definition loaded from config.
//
// Value always holds the raw config expression: a "$(cmd)" value is a dynamic
// command substitution that is NOT executed while parsing, listing, checking,
// browsing or previewing the config. It is evaluated lazily, only once a task
// is actually executed on a selected project (see EnvResolver).
type EnvVar struct {
	Name  string
	Value string // raw config value, possibly "$(cmd)"

	// Context and Line locate the original definition (file + value line)
	// so that evaluation failures can point back to the config source.
	Context string
	Line    int

	// literal marks values that must never be executed as command
	// substitutions even when they look like "$(cmd)" (CLI user args).
	literal bool
}

// String renders the variable as "name=value" (value stays raw).
func (e EnvVar) String() string {
	return fmt.Sprintf("%v=%v", e.Name, e.Value)
}

// Source returns "file:line" locating the original definition (best effort).
func (e EnvVar) Source() string {
	switch {
	case e.Context == "":
		return ""
	case e.Line > 0:
		return fmt.Sprintf("%s:%d", e.Context, e.Line)
	default:
		return e.Context
	}
}

// DynamicCommand returns the shell command wrapped by a "$(cmd)" value.
// Boolean is false for plain literal values (which includes values that only
// start or end with the substitution markers but do not wrap the whole value).
func (e EnvVar) DynamicCommand() (string, bool) {
	inner, hasPrefix := strings.CutPrefix(e.Value, "$(")
	if !hasPrefix {
		return "", false
	}

	cmdStr, hasSuffix := strings.CutSuffix(inner, ")")
	if !hasSuffix {
		return "", false
	}

	return cmdStr, true
}

func ParseNodeEnv(node yaml.Node, context string) []EnvVar {
	var envs []EnvVar
	count := len(node.Content)

	for i := 0; i < count; i += 2 {
		envs = append(envs, EnvVar{
			Name:    node.Content[i].Value,
			Value:   node.Content[i+1].Value,
			Context: context,
			Line:    node.Content[i+1].Line,
		})
	}

	return envs
}

// ParseUserEnv parses user-provided CLI arguments ("key=value"). User values
// are always treated as literals: even when they look like "$(cmd)" they are
// data passed in from the shell and must never be executed.
func ParseUserEnv(args []string) []EnvVar {
	var envs []EnvVar

	for _, arg := range args {
		name, value, found := strings.Cut(arg, "=")
		if !found {
			continue
		}

		envs = append(envs, EnvVar{Name: name, Value: value, literal: true})
	}

	return envs
}

// MergeEnvs merges environment variable definitions.
// Priority is from highest to lowest (1st env takes precedence over the last entry).
// Values stay raw: a definition inherited from a parent scope is carried as a
// definition and is never re-interpreted as a command substitution.
func MergeEnvs(envs ...[]EnvVar) []EnvVar {
	var mergedEnvs []EnvVar
	seen := make(map[string]bool)

	for _, part := range envs {
		for _, elem := range part {
			elem.Value = strings.TrimSuffix(elem.Value, "\n")

			if !seen[elem.Name] {
				mergedEnvs = append(mergedEnvs, elem)
				seen[elem.Name] = true
			}
		}
	}

	return mergedEnvs
}

// resolvedEnv stores one evaluated dynamic value (and its error) within a batch.
type resolvedEnv struct {
	value string
	err   error
}

// EnvResolver evaluates dynamic ("$(cmd)") env values at real execution time.
//
// Every definition is evaluated at most once per batch and reused based on its
// origin (config file + line + name), which gives explicit scopes:
//   - config-level values: once per run (shared across projects and tasks)
//   - project-level values: once per selected project (shared across its tasks)
//   - task-level values: once per task (shared across the task's projects)
//   - command-level values: once per command (shared across the projects)
//
// Only variables that survive precedence merging and reach an actually
// executed command are evaluated. Evaluated output is consumed as a literal
// value and is never scanned for "$(...)" again.
type EnvResolver struct {
	mu       sync.Mutex
	cache    map[string]resolvedEnv
	inflight map[string]chan struct{}
}

func NewEnvResolver() *EnvResolver {
	return &EnvResolver{
		cache:    make(map[string]resolvedEnv),
		inflight: make(map[string]chan struct{}),
	}
}

func (e EnvVar) cacheKey() string {
	return fmt.Sprintf("%s:%d:%s=%s", e.Context, e.Line, e.Name, e.Value)
}

// Resolve turns the effective (already precedence-merged) variables into
// "name=value" strings ready for exec.Cmd.Env. Literal values pass through
// untouched; dynamic values are executed once (and cached) per definition.
func (r *EnvResolver) Resolve(vars []EnvVar) ([]string, error) {
	envs := make([]string, 0, len(vars))

	for _, v := range vars {
		value, err := r.resolveOne(v)
		if err != nil {
			return nil, err
		}

		envs = append(envs, fmt.Sprintf("%v=%v", v.Name, value))
	}

	return envs, nil
}

func (r *EnvResolver) resolveOne(v EnvVar) (string, error) {
	cmdStr, dynamic := v.DynamicCommand()
	if !dynamic || v.literal {
		return v.Value, nil
	}

	key := v.cacheKey()

	// Either become the leader that evaluates the definition, or wait
	// for an in-flight evaluation by another goroutine: either way the
	// command runs at most once per batch.
	r.mu.Lock()
	if cached, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return cached.value, cached.err
	}
	if done, ok := r.inflight[key]; ok {
		r.mu.Unlock()
		<-done

		r.mu.Lock()
		cached := r.cache[key]
		r.mu.Unlock()

		return cached.value, cached.err
	}
	done := make(chan struct{})
	r.inflight[key] = done
	r.mu.Unlock()

	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	value := strings.TrimSuffix(string(out), "\n")

	var evalErr error
	if err != nil {
		evalErr = &core.ConfigEnvFailed{
			Name:    v.Name,
			Err:     value,
			Context: v.Context,
			Line:    v.Line,
		}
	}

	r.mu.Lock()
	r.cache[key] = resolvedEnv{value: value, err: evalErr}
	delete(r.inflight, key)
	close(done)
	r.mu.Unlock()

	return value, evalErr
}

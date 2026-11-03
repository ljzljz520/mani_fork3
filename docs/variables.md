# Variables

`mani` supports setting variables for both projects and tasks. Variables can be either strings or commands (encapsulated by `$()`).

Command substitutions are **not executed while reading the config**: parsing the config, listing/checking/describing projects and tasks, browsing the TUI, and `--dry-run` only store and display the raw expression. A substitution runs only when a task is really executed on a selected project, and only for variables that survive precedence merging for a command that actually runs.

## Evaluation Scope Within a Run

Within a single run (batch) each definition is evaluated at most once and the result is reused:

| Defined at | Evaluated |
| --- | --- |
| config `env` | once per run, shared across all projects and tasks |
| project `env` | once per selected project, shared across all its tasks |
| task `env` | once per task, shared across the task's projects |
| command `env` | once per command, shared across the projects |

Variables of projects that are not selected and variables of tasks/commands that do not run are never evaluated. The evaluated output is passed on as a plain value: even if it looks like `$(...)` it is not interpreted as a command again. Values passed from the CLI are always treated as literals and are never executed.

If a substitution fails, the error points back to the original definition, e.g. ``failed to evaluate env `date` defined in mani.yaml:21``.

```yaml
projects:
  pinto:
    path: pinto
    url: https://github.com/alajmo/pinto.git
    env:
      foo: bar

tasks:
  ping:
    cmd: |
      echo "$msg"
      echo "$date"
      echo "$foo"
    env:
      msg: text
      date: $(date)
```

## Precedence

When the same variable is defined at several levels, the higher-precedence definition wins and the lower one is not evaluated: project env > user args > command env > task env > config env.

## Pass Variables from CLI

To pass variables from the command line, provide them as arguments. For example:

```bash
mani run msg option=123
```

The environment variable option will then be available for use within the task. CLI values are literal, so `option=$(date)` sets the variable to the literal string `$(date)` rather than executing it.

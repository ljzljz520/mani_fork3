package dao

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/alajmo/mani/core"
)

// bumpCmd renders a shell command that increments a side-effect counter file
// exactly once per invocation and prints a fixed value. It is used to verify
// when command substitutions are (and are not) executed.
func bumpCmd(counterPath string) string {
	return fmt.Sprintf(
		`f=$(cat %s 2>/dev/null); f=${f:-0}; echo $((f+1)) > %s; printf 'val'`,
		counterPath, counterPath,
	)
}

func readCounter(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}

	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n); err != nil {
		t.Fatalf("parse counter %q: %v", string(data), err)
	}

	return n
}

func TestEnvResolver_LiteralPassThrough(t *testing.T) {
	r := NewEnvResolver()
	got, err := r.Resolve([]EnvVar{
		{Name: "FOO", Value: "bar"},
		{Name: "EMPTY", Value: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"FOO=bar", "EMPTY="}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("expected %v, got %v", want, got)
	}
}

func TestEnvResolver_DynamicEvaluatedOnceAndReused(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")
	dyn := EnvVar{
		Name:    "DYN",
		Value:   "$(" + bumpCmd(counter) + ")",
		Context: "/home/test/mani.yaml",
		Line:    21,
	}

	r := NewEnvResolver()

	// Concurrently resolve the same definition across many "projects": the
	// underlying command must still run exactly once for the whole batch.
	var wg sync.WaitGroup
	results := make(chan string, 50)
	errs := make(chan error, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Resolve([]EnvVar{dyn})
			if err != nil {
				errs <- err
				return
			}
			results <- got[0]
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}

	for got := range results {
		if got != "DYN=val" {
			t.Errorf("expected reused value DYN=val, got %q", got)
		}
	}

	if n := readCounter(t, counter); n != 1 {
		t.Errorf("expected command substitution to run once, ran %d times", n)
	}
}

func TestEnvResolver_ParentResultIsLiteralValue(t *testing.T) {
	// A dynamic definition whose evaluated output itself looks like a
	// command substitution: the output must be passed through verbatim and
	// must never be executed a second time. "$(false)" failing would prove
	// the output got re-interpreted as a command.
	parent := EnvVar{
		Name:    "PARENT",
		Value:   `$(printf '%s' '$(false)')`,
		Context: "/home/test/mani.yaml",
		Line:    10,
	}

	r := NewEnvResolver()
	got, err := r.Resolve([]EnvVar{parent})
	if err != nil {
		t.Fatalf("evaluated output must not be re-executed as a command: %v", err)
	}

	if got[0] != "PARENT=$(false)" {
		t.Errorf("expected literal output %q, got %q", "PARENT=$(false)", got[0])
	}
}

func TestEnvResolver_OnlyEffectiveVarsAreEvaluated(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")

	// Lower-priority definition (config level) is shadowed by a higher
	// priority literal (task level), so its command must never run.
	configVar := EnvVar{
		Name:    "VAR",
		Value:   "$(" + bumpCmd(counter) + ")",
		Context: "/home/test/mani.yaml",
		Line:    2,
	}
	taskVar := EnvVar{Name: "VAR", Value: "literal", Context: "/home/test/mani.yaml", Line: 9}

	r := NewEnvResolver()
	got, err := r.Resolve(MergeEnvs([]EnvVar{taskVar}, []EnvVar{configVar}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got[0] != "VAR=literal" {
		t.Errorf("expected override value, got %q", got[0])
	}

	if n := readCounter(t, counter); n != 0 {
		t.Errorf("shadowed variable must not be evaluated, counter=%d", n)
	}

	// A fresh resolver (new batch) that actually uses the config def runs it.
	r2 := NewEnvResolver()
	got, err = r2.Resolve([]EnvVar{configVar})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != "VAR=val" {
		t.Errorf("expected evaluated value, got %q", got[0])
	}
	if n := readCounter(t, counter); n != 1 {
		t.Errorf("expected used variable to run once, counter=%d", n)
	}
}

func TestEnvResolver_FailureLocatesOriginalDefinition(t *testing.T) {
	r := NewEnvResolver()
	v := EnvVar{
		Name:    "BROKEN",
		Value:   "$(sh -c 'echo boom >&2; exit 1')",
		Context: "/home/test/mani.yaml",
		Line:    42,
	}

	_, err := r.Resolve([]EnvVar{v})
	if err == nil {
		t.Fatal("expected evaluation error, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "BROKEN") || !strings.Contains(msg, "/home/test/mani.yaml:42") {
		t.Errorf("error should name variable and original file:line, got:\n%s", msg)
	}
	if !strings.Contains(msg, "boom") {
		t.Errorf("error should contain command output, got:\n%s", msg)
	}

	var envErr *core.ConfigEnvFailed
	if !asConfigErr(err, &envErr) {
		t.Fatalf("expected *core.ConfigEnvFailed, got %T", err)
	}
}

func asConfigErr(err error, target **core.ConfigEnvFailed) bool {
	for err != nil {
		if e, ok := err.(*core.ConfigEnvFailed); ok {
			*target = e
			return true
		}
		break
	}
	return false
}

func TestEnvResolver_UserArgsAreNeverExecuted(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")

	user := ParseUserEnv([]string{"X=$(" + bumpCmd(counter) + ")", "Y=plain"})
	r := NewEnvResolver()
	got, err := r.Resolve(user)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got[0] != "X=$("+bumpCmd(counter)+")" {
		t.Errorf("user value should stay literal, got %q", got[0])
	}
	if got[1] != "Y=plain" {
		t.Errorf("expected Y=plain, got %q", got[1])
	}
	if n := readCounter(t, counter); n != 0 {
		t.Errorf("CLI-provided values must never execute, counter=%d", n)
	}
}

func TestParseTaskEnv_PrecedenceWithoutSideEffects(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")
	dyn := "$(" + bumpCmd(counter) + ")"

	var doc yaml.Node
	err := yaml.Unmarshal([]byte("cmdvar: "+dyn+"\nshared: cmd\n"), &doc)
	if err != nil {
		t.Fatal(err)
	}
	node := *doc.Content[0] // mapping node

	userEnv := []EnvVar{{Name: "shared", Value: "user", literal: true}}
	parentEnv := []EnvVar{
		{Name: "shared", Value: "task", Context: "mani.yaml", Line: 5},
		{Name: "taskvar", Value: "taskval", Context: "mani.yaml", Line: 6},
	}
	configEnv := []EnvVar{
		{Name: "shared", Value: "config", Context: "mani.yaml", Line: 1},
		{Name: "cfgvar", Value: "cfgval", Context: "mani.yaml", Line: 2},
	}

	got := ParseTaskEnv(node, "mani.yaml", userEnv, parentEnv, configEnv)

	want := []EnvVar{
		{Name: "shared", Value: "user", literal: true}, // user wins
		{Name: "cmdvar", Value: dyn, Context: "mani.yaml", Line: 1},
		{Name: "taskvar", Value: "taskval", Context: "mani.yaml", Line: 6},
		{Name: "cfgvar", Value: "cfgval", Context: "mani.yaml", Line: 2},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("expected %v, got %v", want, got)
	}

	if n := readCounter(t, counter); n != 0 {
		t.Errorf("parsing must not execute substitutions, counter=%d", n)
	}
}

// Config parsing (used by list, check, describe, TUI browsing) must keep
// project env expressions raw without running any of them.
func TestProjectList_EnvNotEvaluatedAtParse(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")
	dyn := "$(" + bumpCmd(counter) + ")"

	configFile := filepath.Join(t.TempDir(), "mani.yaml")
	configYaml := fmt.Sprintf(`
projects:
  selected:
    path: .
    env:
      project_dyn: %s
      static: hello
`, dyn)

	var c Config
	if err := yaml.Unmarshal([]byte(configYaml), &c); err != nil {
		t.Fatal(err)
	}
	c.Path = configFile
	c.Dir = filepath.Dir(configFile)

	projects, projectErrors := c.GetProjectList()
	if len(projectErrors) != 0 {
		t.Fatalf("unexpected project errors: %v", projectErrors)
	}

	if n := readCounter(t, counter); n != 0 {
		t.Fatalf("parsing projects must not execute env commands, counter=%d", n)
	}

	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}

	got := projects[0].EnvList
	if got[0].Name != "project_dyn" || got[0].Value != dyn {
		t.Errorf("expected raw expression, got %v", got[0])
	}
	if got[0].Context != configFile {
		t.Errorf("expected origin %q, got %q", configFile, got[0].Context)
	}
	if got[0].Line <= 0 {
		t.Errorf("expected definition line to be recorded, got %d", got[0].Line)
	}
	if got[1].Value != "hello" {
		t.Errorf("expected static value, got %q", got[1].Value)
	}
}

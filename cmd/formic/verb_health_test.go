package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/testfixture"
)

// writeHealthCheckedSource writes a source with one tool, `named`, whose launch.command is a
// trivial no-op and whose health.check runs the fixture tool with checkArguments (single-quoted
// in the manifest YAML, since the fixture tool's own path may contain a Windows backslash — a
// plain double-quoted YAML scalar would treat that as an escape sequence).
func writeHealthCheckedSource(t *testing.T, named string, checkArguments ...string) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Every element is single-quoted: an argument may itself be a filesystem path carrying a
	// Windows backslash, which a double-quoted YAML scalar would treat as an escape sequence.
	checkYAML := "['" + testfixture.Tool(t) + "'"
	for _, argument := range checkArguments {
		checkYAML += ", '" + argument + "'"
	}
	checkYAML += "]"
	manifestYAML := "formic: 1\nname: " + named + "\nlaunch:\n  command: " + testfixture.Tool(t) +
		"\n  args: [\"fixture:exit\", \"0\"]\nhealth:\n  check: " + checkYAML + "\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", named+".yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

// TestRunDescribeRunsTheDeclaredHealthCheckAndReportsIt proves `describe` runs a manifest's
// declared health.check and reports it, never leaving it unread the way the pre-fix code did (no
// non-generated reader of health.check existed at all).
func TestRunDescribeRunsTheDeclaredHealthCheckAndReportsIt(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeHealthCheckedSource(t, "healthy", "fixture:exit", "0")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "healthy", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, output)
	}
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode describe --json: %v\n%s", err, output)
	}
	if result.Health == nil || !result.Health.Ran || !result.Health.OK {
		t.Fatalf("result.Health = %+v, want Ran=true OK=true", result.Health)
	}
}

// TestRunDescribeReportsNoHealthWhenTheManifestDeclaresNone proves a manifest with no `health`
// block reports Health as nil/omitted, not a fabricated "not run" result.
func TestRunDescribeReportsNoHealthWhenTheManifestDeclaresNone(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeGreetSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "greet", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, output)
	}
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode describe --json: %v\n%s", err, output)
	}
	if result.Health != nil {
		t.Fatalf("result.Health = %+v, want nil for a manifest with no health block", result.Health)
	}
}

// TestRunListNeverRunsTheHealthCheck proves the other half of the ruling: "run it in describe
// and doctor, never at list time". The fixture's health.check writes a marker file
// on every run — `list` must leave the marker absent, and `describe` must create it, proving
// the two commands differ in exactly the way the ruling requires.
func TestRunListNeverRunsTheHealthCheck(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	markerPath := filepath.Join(t.TempDir(), "health-ran.marker")
	sourcePath := writeHealthCheckedSource(t, "marked", "fixture:touch", markerPath)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	if _, exitCode := captureStdout(t, func() int { return run([]string{"list", "--json"}) }); exitCode != 0 {
		t.Fatalf("list --json exit = %d, want 0", exitCode)
	}
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("the marker file exists after `list` — list must never run a declared health.check")
	}

	if _, exitCode := captureStdout(t, func() int { return run([]string{"describe", "marked", "--json"}) }); exitCode != 0 {
		t.Fatalf("describe --json exit = %d, want 0", exitCode)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("the marker file is missing after `describe` — want the health.check to have run: %v", err)
	}
}

// TestRunDoctorRunsTheDeclaredHealthCheckForEveryTool proves `doctor` also runs each tool's
// declared health.check and prints it, alongside describe.
func TestRunDoctorRunsTheDeclaredHealthCheckForEveryTool(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeHealthCheckedSource(t, "healthy", "fixture:exit", "0")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"doctor"}) })
	if exitCode != 0 {
		t.Fatalf("doctor exit = %d:\n%s", exitCode, output)
	}
	if !strings.Contains(output, "health healthy:") {
		t.Fatalf("doctor output missing the health line:\n%s", output)
	}
}

// TestRunDescribePersistsNothingForTheHealthCheck proves a health check leaves the state
// directory exactly as it was — the result is reported, never persisted (§15 N11).
func TestRunDescribePersistsNothingForTheHealthCheck(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDir := t.TempDir()
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	sourcePath := writeHealthCheckedSource(t, "healthy", "fixture:exit", "0")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	before, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, exitCode := captureStdout(t, func() int { return run([]string{"describe", "healthy", "--json"}) }); exitCode != 0 {
		t.Fatalf("describe --json exit = %d, want 0", exitCode)
	}

	after, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("state directory entries after describe = %d, before = %d — a health check must never write a run row", len(after), len(before))
	}
}

// TestRunDescribeTextPrintsTheHealthExitCodeValueNotItsPointer proves the human describe output
// renders HealthResult.ExitCode (a *int) as its value, never as a Go pointer (0xc000...).
func TestRunDescribeTextPrintsTheHealthExitCodeValueNotItsPointer(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeHealthCheckedSource(t, "healthy", "fixture:exit", "0")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "healthy"}) })
	if exitCode != 0 {
		t.Fatalf("describe exit = %d:\n%s", exitCode, output)
	}
	if strings.Contains(output, "0x") {
		t.Errorf("describe output carries a pointer address:\n%s", output)
	}
	if !strings.Contains(output, "exit code 0") {
		t.Errorf("describe output lacks %q:\n%s", "exit code 0", output)
	}
}

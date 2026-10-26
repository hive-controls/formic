//go:build windows

package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/runs"
)

// A manifest names its command the way a person types it: `pwsh`, `npm`, `docker`. On
// Windows those are pwsh.exe, npm.cmd, docker.exe, and the shell finds them through PATHEXT.
// A launcher that looks only for the literal name cannot start any of them.
func TestResolveExecutableFindsABareNameThroughTheExecutableExtensions(t *testing.T) {
	toolDirectory := t.TempDir()
	for _, fileName := range []string{"fixture-tool.exe", "fixture-shim.cmd"} {
		if err := os.WriteFile(filepath.Join(toolDirectory, fileName), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{"PATH=" + toolDirectory, "PATHEXT=.COM;.EXE;.BAT;.CMD"}

	cases := []struct{ name, wantFile string }{
		{"fixture-tool.exe", "fixture-tool.exe"}, // control: the literal name already resolves
		{"fixture-tool", "fixture-tool.exe"},
		{"fixture-shim", "fixture-shim.cmd"},
	}
	for _, testCase := range cases {
		resolved, err := resolveExecutable(testCase.name, environment)
		if err != nil {
			t.Errorf("resolveExecutable(%q) error = %v, want %s", testCase.name, err, testCase.wantFile)
			continue
		}
		if !strings.EqualFold(filepath.Base(resolved), testCase.wantFile) {
			t.Errorf("resolveExecutable(%q) = %q, want %s", testCase.name, resolved, testCase.wantFile)
		}
	}
}

// Resolving a path is not the same as being able to run it: a .cmd shim is how npm and most
// installed launchers reach a Windows PATH, so the command built for one must actually start
// and hand back the shim's own exit code.
func TestBuildCommandRunsACommandShimNamedWithoutItsExtension(t *testing.T) {
	toolDirectory := t.TempDir()
	shimPath := filepath.Join(toolDirectory, "fixture-shim.cmd")
	if err := os.WriteFile(shimPath, []byte("@exit /b 7\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	environment := []string{
		"PATH=" + toolDirectory,
		"PATHEXT=.COM;.EXE;.BAT;.CMD",
		"SystemRoot=" + os.Getenv("SystemRoot"),
	}
	command, err := buildCommand([]string{"fixture-shim"}, toolDirectory, environment)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode := ExitCode(command.Run()); exitCode != 7 {
		t.Fatalf("running the shim exit = %d, want the shim's own 7", exitCode)
	}
}

// TestNewTabCommandResolvesWindowsTerminalFromTheAppExecutionAliasWhenPathMisses reproduces the
// founder's PC exactly: %LOCALAPPDATA%\Microsoft\WindowsApps (where the wt.exe app-execution
// alias lives) is on neither the user nor the machine PATH, so wt.exe cannot be found through
// PATH alone even in a real Windows Terminal session.
func TestNewTabCommandResolvesWindowsTerminalFromTheAppExecutionAliasWhenPathMisses(t *testing.T) {
	localAppData := t.TempDir()
	aliasDirectory := filepath.Join(localAppData, "Microsoft", "WindowsApps")
	if err := os.MkdirAll(aliasDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(aliasDirectory, "wt.exe")
	if err := os.WriteFile(aliasPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("PATH", "") // wt.exe is reachable ONLY through the alias, never through PATH

	row := runs.Row{ID: "run-1", Cwd: `C:\repo`}
	command, err := NewTabCommand(TerminalWindows, row, []string{`C:\bin\formic.exe`, "__replay-row", `C:\state\run.json`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(command.Path, aliasPath) {
		t.Fatalf("command.Path = %q, want the resolved alias %q", command.Path, aliasPath)
	}
	if command.Args[0] != "wt.exe" {
		t.Fatalf("command.Args[0] = %q, want wt.exe (argv[0] convention, unaffected by the resolved Path)", command.Args[0])
	}
}

package launch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The Windows branch is exercised by name here, on every platform, because the platforms CI
// runs on are not Windows: a regression in it would otherwise be invisible until a Windows
// user hit it.
func TestExecutableFileNamesFollowsEachPlatformsLookupRule(t *testing.T) {
	cases := []struct {
		label, name, executableExtensions, goos string
		want                                    []string
	}{
		{"unix tries the name alone", "npm", ".EXE;.CMD", "linux", []string{"npm"}},
		{"windows never tries an extensionless name as given", "npm", ".EXE;.CMD", "windows", []string{"npm.EXE", "npm.CMD"}},
		{"windows tries a name that carries an extension first", "pwsh.exe", ".EXE;.CMD", "windows", []string{"pwsh.exe", "pwsh.exe.EXE", "pwsh.exe.CMD"}},
		{"windows falls back to the system default when PATHEXT is unset", "tool", "", "windows", []string{"tool.com", "tool.exe", "tool.bat", "tool.cmd"}},
		{"windows skips an empty PATHEXT segment", "tool", ".EXE;;.CMD;", "windows", []string{"tool.EXE", "tool.CMD"}},
	}
	for _, testCase := range cases {
		got := executableFileNames(testCase.name, testCase.executableExtensions, testCase.goos)
		if !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("%s: executableFileNames(%q, %q, %q) = %v, want %v", testCase.label, testCase.name, testCase.executableExtensions, testCase.goos, got, testCase.want)
		}
	}
}

func TestLookupEnvironmentIsCaseInsensitiveOnlyOnWindows(t *testing.T) {
	values := map[string]string{"Path": "mixed-case-value"}
	if got := lookupEnvironment(values, "PATH", "windows"); got != "mixed-case-value" {
		t.Errorf("windows lookup of PATH against a `Path` entry = %q, want the entry's value", got)
	}
	if got := lookupEnvironment(values, "PATH", "linux"); got != "" {
		t.Errorf("linux lookup of PATH against a `Path` entry = %q, want empty: names are case-sensitive there", got)
	}
	both := map[string]string{"Path": "mixed-case-value", "PATH": "exact-value"}
	if got := lookupEnvironment(both, "PATH", "windows"); got != "exact-value" {
		t.Errorf("windows lookup with both casings present = %q, want the exact match", got)
	}
}

// TestResolveWindowsTerminalExecutablePrefersPathThenFallsBackToTheAppExecutionAlias covers the
// founder's PC: %LOCALAPPDATA%\Microsoft\WindowsApps (where the wt.exe app-execution alias
// lives) is on neither the user nor the machine PATH there, so exec.LookPath alone never finds
// it even in a native Windows Terminal session. The lookups are injected so this is exercised
// on every platform, not only a real Windows machine.
func TestResolveWindowsTerminalExecutablePrefersPathThenFallsBackToTheAppExecutionAlias(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("not found") }
	foundOnPath := func(string) (string, error) { return `C:\PATH\wt.exe`, nil }
	aliasExists := func(string) (os.FileInfo, error) { return nil, nil }
	aliasMissing := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	if path, err := resolveWindowsTerminalExecutable(foundOnPath, "", aliasMissing); err != nil || path != `C:\PATH\wt.exe` {
		t.Fatalf("resolveWindowsTerminalExecutable() = %q, %v, want the PATH match tried first", path, err)
	}

	localAppData := `C:\Users\example\AppData\Local`
	wantAlias := filepath.Join(localAppData, "Microsoft", "WindowsApps", "wt.exe")
	if path, err := resolveWindowsTerminalExecutable(notFound, localAppData, aliasExists); err != nil || path != wantAlias {
		t.Fatalf("resolveWindowsTerminalExecutable() = %q, %v, want the app-execution alias %q", path, err, wantAlias)
	}

	if _, err := resolveWindowsTerminalExecutable(notFound, "", aliasMissing); err == nil {
		t.Fatal("resolveWindowsTerminalExecutable should fail when PATH misses and LOCALAPPDATA is unset")
	}
	if _, err := resolveWindowsTerminalExecutable(notFound, localAppData, aliasMissing); err == nil {
		t.Fatal("resolveWindowsTerminalExecutable should fail when PATH misses and the app-execution alias does not exist either")
	}
}

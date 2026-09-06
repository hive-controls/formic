package launch

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	toolspec "github.com/hive-controls/toolspec"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
)

func TestDetectTerminalUsesTheOrderedEnvironmentTable(t *testing.T) {
	cases := []struct {
		name        string
		environment []string
		want        Terminal
	}{
		{"tmux first", []string{"TMUX=/tmp/tmux", "TERM_PROGRAM=iTerm.app"}, TerminalTmux},
		{"Windows Terminal", []string{"WT_SESSION=session"}, TerminalWindows},
		{"kitty", []string{"KITTY_WINDOW_ID=1"}, TerminalKitty},
		{"WezTerm", []string{"WEZTERM_PANE=1"}, TerminalWezTerm},
		{"Ghostty by surface id", []string{"GHOSTTY_SURFACE_ID=0xabc", "TERM_PROGRAM=ghostty"}, TerminalGhostty},
		{"Ghostty by TERM_PROGRAM alone", []string{"TERM_PROGRAM=ghostty"}, TerminalGhostty},
		{"iTerm2", []string{"TERM_PROGRAM=iTerm.app"}, TerminalITerm},
		{"Terminal.app", []string{"TERM_PROGRAM=Apple_Terminal"}, TerminalApple},
		{"GNOME service", []string{"GNOME_TERMINAL_SERVICE=service"}, TerminalGNOME},
		{"GNOME fallback", []string{"TERM_PROGRAM=gnome-terminal"}, TerminalGNOME},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := DetectTerminal(testCase.environment)
			if !ok || got != testCase.want {
				t.Fatalf("DetectTerminal() = %q, %v; want %q, true", got, ok, testCase.want)
			}
		})
	}
	if terminal, ok := DetectTerminal([]string{"TERM_PROGRAM=unknown"}); ok || terminal != "" {
		t.Fatalf("unknown terminal = %q, %v; want unavailable", terminal, ok)
	}
}

// TestNewTabCommandBuildsEverySpawnForm asserts the FULL argv, cwd handling, and environment
// handling for every terminal row in §7.2 — not just the executable basename. Every row must
// inherit the ambient environment (command.Env == nil) rather than being forced to the
// child's resolved environment: the run row, not this command's own env, is how the child's
// environment travels (§ launch: new-tab environment).
func TestNewTabCommandBuildsEverySpawnForm(t *testing.T) {
	row := runs.Row{ID: "run-1", Cwd: "/repo"}
	wrapperArgv := []string{"/bin/hivedeck", "run", "/state/runs/run-1/run.json"}
	argvCases := map[Terminal][]string{
		TerminalTmux:    append([]string{"tmux", "new-window", "-c", "/repo", "--"}, wrapperArgv...),
		TerminalWindows: append([]string{"wt.exe", "-w", "0", "nt", "-d", "/repo"}, wrapperArgv...),
		TerminalKitty:   append([]string{"kitten", "@", "launch", "--type=tab", "--cwd", "/repo", "--"}, wrapperArgv...),
		TerminalWezTerm: append([]string{"wezterm", "cli", "spawn", "--cwd", "/repo", "--"}, wrapperArgv...),
		TerminalGNOME:   append([]string{"gnome-terminal", "--tab", "--working-directory=/repo", "--"}, wrapperArgv...),
	}
	for terminal, wantArgv := range argvCases {
		t.Run(string(terminal), func(t *testing.T) {
			command, err := NewTabCommand(terminal, row, wrapperArgv)
			if err != nil {
				t.Fatalf("NewTabCommand(%s) returned an error: %v", terminal, err)
			}
			if !reflect.DeepEqual(command.Args, wantArgv) {
				t.Fatalf("NewTabCommand(%s) argv = %v, want %v", terminal, command.Args, wantArgv)
			}
			if command.Env != nil {
				t.Fatalf("NewTabCommand(%s) env = %v, want nil (inherit the ambient environment, never the child's)", terminal, command.Env)
			}
		})
	}

	// The osascript routes build a shell STRING rather than argv (§7.2's one quoting
	// hazard): assert the cwd and every wrapper argument are present, correctly quoted.
	for _, terminal := range []Terminal{TerminalITerm, TerminalApple, TerminalGhostty} {
		t.Run(string(terminal), func(t *testing.T) {
			command, err := NewTabCommand(terminal, row, wrapperArgv)
			if err != nil {
				t.Fatalf("NewTabCommand(%s) returned an error: %v", terminal, err)
			}
			if !reflect.DeepEqual(command.Args[:2], []string{"osascript", "-e"}) {
				t.Fatalf("NewTabCommand(%s) argv = %v, want [osascript -e ...]", terminal, command.Args)
			}
			if command.Env != nil {
				t.Fatalf("NewTabCommand(%s) env = %v, want nil (inherit the ambient environment)", terminal, command.Env)
			}
			script := command.Args[2]
			if !strings.Contains(script, quoteShellArgument(row.Cwd)) {
				t.Fatalf("NewTabCommand(%s) script does not cd into the quoted cwd: %s", terminal, script)
			}
			for _, argument := range wrapperArgv {
				if !strings.Contains(script, quoteShellArgument(argument)) {
					t.Fatalf("NewTabCommand(%s) script is missing quoted argument %q: %s", terminal, argument, script)
				}
			}
		})
	}
}

func TestShellCommandQuotesSpacesQuotesAndNewlines(t *testing.T) {
	arguments := []string{"tool", "with space", "quote's", "line\nbreak"}
	command := shellCommand("/repo with space", arguments)
	if !strings.Contains(command, `'quote'"'"'s'`) || !strings.Contains(command, "'line\nbreak'") {
		t.Fatalf("shellCommand did not quote risky arguments: %q", command)
	}
}

func TestArtifactCommandUsesPlatformHandlerAndResolvesRelativePath(t *testing.T) {
	row := runs.Row{ID: "run-1", Cwd: "/repo", Artifact: "evidence/index.html"}
	cases := []struct {
		goos string
		want []string
	}{
		{"darwin", []string{"open", filepath.Join("/repo", "evidence", "index.html")}},
		{"linux", []string{"xdg-open", filepath.Join("/repo", "evidence", "index.html")}},
		{"windows", []string{"rundll32", "url.dll,FileProtocolHandler", filepath.Join("/repo", "evidence", "index.html")}},
	}
	for _, testCase := range cases {
		command, err := ArtifactCommand(row, testCase.goos)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(command.Args, testCase.want) {
			t.Fatalf("ArtifactCommand(%s) args = %v, want %v", testCase.goos, command.Args, testCase.want)
		}
	}
}

// TestArtifactCommandClassifiesEveryArtifactShape is the table required by the finding:
// scheme detection must be case-insensitive, file:// is a URL (opened as such, never joined
// beneath cwd), and only a target with NO scheme at all is resolved as a path against the
// run's cwd.
func TestArtifactCommandClassifiesEveryArtifactShape(t *testing.T) {
	row := runs.Row{ID: "run-1", Cwd: "/repo"}
	cases := []struct {
		name     string
		artifact string
		want     string
	}{
		{"relative path is joined beneath cwd", "evidence/index.html", filepath.Join("/repo", "evidence", "index.html")},
		{"absolute path is left alone", "/tmp/evidence/index.html", "/tmp/evidence/index.html"},
		{"http url is a URL", "http://example.com/evidence", "http://example.com/evidence"},
		{"https url is a URL", "https://example.com/evidence", "https://example.com/evidence"},
		{"uppercase scheme is still a URL", "HTTPS://example.com/evidence", "HTTPS://example.com/evidence"},
		{"file url is a URL, never joined beneath cwd", "file:///tmp/evidence/index.html", "file:///tmp/evidence/index.html"},
		{"mixed-case file scheme is still a URL", "File:///tmp/evidence/index.html", "File:///tmp/evidence/index.html"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row.Artifact = testCase.artifact
			command, err := ArtifactCommand(row, "darwin")
			if err != nil {
				t.Fatal(err)
			}
			if command.Args[1] != testCase.want {
				t.Fatalf("ArtifactCommand(%q) target = %q, want %q", testCase.artifact, command.Args[1], testCase.want)
			}
		})
	}
}

func TestForegroundLaunchWritesStatusAndRecordsEachExitCode(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		exitCode    int
		event       string
		wantOutcome string
	}{{0, "ok", "ok"}, {7, "failed", "failed"}, {3, "needs-human", "needs-human"}} {
		t.Run(testCase.event, func(t *testing.T) {
			store := runs.Store{StateDirectory: t.TempDir()}
			tool := foregroundFixtureTool(repositoryRoot, testCase.exitCode, testCase.event)
			prepared, err := Prepare(Request{
				Tool: tool, RepositoryRoot: repositoryRoot, Store: store,
				Environment: os.Environ(), StartedAt: time.Unix(100, 0),
			}, profile.ModeForeground)
			if err != nil {
				t.Fatal(err)
			}
			commandError := prepared.Command.Run()
			completed, err := Finish(store, prepared.Row.ID, commandError, time.Unix(200, 0))
			if err != nil {
				t.Fatal(err)
			}
			if completed.ExitCode == nil || *completed.ExitCode != testCase.exitCode || completed.Outcome != testCase.wantOutcome {
				t.Fatalf("completed row = %+v, want exit %d outcome %s", completed, testCase.exitCode, testCase.wantOutcome)
			}
			if completed.Artifact != "artifact.html" {
				t.Fatalf("artifact = %q, want artifact.html", completed.Artifact)
			}
		})
	}
}

func foregroundFixtureTool(repositoryRoot string, exitCode int, event string) discovery.Tool {
	repositoryToken := "{repo}"
	return discovery.Tool{
		Name: "fixture", Title: "Fixture",
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
			Command: "/bin/sh", Cwd: &repositoryToken,
			Args: []string{filepath.Join(repositoryRoot, "testdata", "foreground.sh"), strconv.Itoa(exitCode), event},
		}},
	}
}

func TestTabWrapperReceivesPersistedRowEnvironmentEvenWhenItsOwnEnvironmentIsDropped(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{Name: "fixture", Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
		Command: "/bin/sh", Args: []string{filepath.Join(repositoryRoot, "testdata", "print-env.sh"), "PROFILE_VAR"},
	}}}
	repositoryProfile := profile.File{Tools: map[string]profile.Tool{"fixture": {Env: map[string]string{"PROFILE_VAR": "from-profile"}}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Profile: repositoryProfile, Store: store,
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeTab)
	if err != nil {
		t.Fatal(err)
	}
	// A real new-tab spawn (kitty, iTerm2, Terminal.app, tmux across server restarts, ...)
	// starts the wrapper with a fresh environment that never carries hivedeck's process
	// environment or profile additions — only the run-row path is passed on argv. The
	// wrapper must recover everything it needs from the row itself.
	rowPath := store.RowPath(prepared.Row.ID)
	exitCode, err := RunWrapper(rowPath, func() time.Time { return time.Unix(200, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("wrapper exit = %d, want 0: PROFILE_VAR should reach the child from the persisted row", exitCode)
	}
}

func TestWrapperRecordsFailedOutcomeWithSignalExitCodeInsteadOfLeavingRunningForever(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("fixture", profile.ModeTab, repositoryRoot, []string{"sleep", "5"}, os.Environ(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	rowPath := store.RowPath(row.ID)
	type outcome struct {
		exitCode int
		err      error
	}
	results := make(chan outcome, 1)
	go func() {
		exitCode, err := RunWrapper(rowPath, func() time.Time { return time.Unix(200, 0) })
		results <- outcome{exitCode, err}
	}()
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.exitCode != 130 {
			t.Fatalf("wrapper exit code = %d, want 130 (128+SIGINT)", result.exitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper never returned after SIGINT — it is not forwarding/waiting on the signal")
	}
	completed, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Outcome != "failed" || completed.ExitCode == nil || *completed.ExitCode != 130 {
		t.Fatalf("completed row = %+v, want failed with exit 130, never running", completed)
	}
}

// TestWrapperKeepsSignalTrapArmedThroughTheHandbackWindow targets the gap between the child
// exiting and Finish persisting the row: a signal delivered there must never kill the wrapper
// before the row is saved. finishedAt is invoked by RunWrapper exactly once, as the argument
// to Finish — i.e. exactly in that window, after the child has already exited cleanly — so
// sending the signal from inside it hits the window deterministically.
func TestWrapperKeepsSignalTrapArmedThroughTheHandbackWindow(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("fixture", profile.ModeTab, repositoryRoot, []string{"true"}, os.Environ(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	rowPath := store.RowPath(row.ID)
	exitCode, err := RunWrapper(rowPath, func() time.Time {
		if killErr := syscall.Kill(os.Getpid(), syscall.SIGINT); killErr != nil {
			t.Fatal(killErr)
		}
		return time.Unix(200, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("wrapper exit = %d, want 0 (the child already exited cleanly before the signal in the handback window)", exitCode)
	}
	completed, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ExitCode == nil || *completed.ExitCode != 0 || completed.Outcome != "ok" {
		t.Fatalf("completed row = %+v, want the child's real exit 0/ok — a signal in the handback window must not lose the row", completed)
	}
}

func TestPrepareMakesARelativeManifestCwdAbsoluteAgainstTheRepositoryRootForForeground(t *testing.T) {
	repositoryRoot := t.TempDir()
	nestedInvocationDirectory := filepath.Join(repositoryRoot, "usecases", "nested")
	scriptsDirectory := filepath.Join(repositoryRoot, "scripts")
	for _, directory := range []string{nestedInvocationDirectory, scriptsDirectory} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	withWorkingDirectory(t, nestedInvocationDirectory)

	relativeCwd := "scripts"
	tool := discovery.Tool{Name: "fixture", Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
		Command: "/bin/pwd", Cwd: &relativeCwd,
	}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Store: runs.Store{StateDirectory: t.TempDir()},
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	wantCwd, err := filepath.EvalSymlinks(scriptsDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(prepared.Row.Cwd); evalErr != nil || resolved != wantCwd {
		t.Fatalf("Row.Cwd = %q, want the repository-rooted absolute path %q (relative cwd must never resolve against hivedeck's own invocation directory)", prepared.Row.Cwd, wantCwd)
	}
	output, err := prepared.Command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(strings.TrimSpace(string(output))); evalErr != nil || resolved != wantCwd {
		t.Fatalf("child ran in %q, want %q", strings.TrimSpace(string(output)), wantCwd)
	}
}

func TestPrepareMakesARelativeManifestCwdAbsoluteAgainstTheRepositoryRootForTheTabWrapper(t *testing.T) {
	repositoryRoot := t.TempDir()
	nestedInvocationDirectory := filepath.Join(repositoryRoot, "usecases", "nested")
	scriptsDirectory := filepath.Join(repositoryRoot, "scripts")
	for _, directory := range []string{nestedInvocationDirectory, scriptsDirectory} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	withWorkingDirectory(t, nestedInvocationDirectory)

	store := runs.Store{StateDirectory: t.TempDir()}
	relativeCwd := "scripts"
	tool := discovery.Tool{Name: "fixture", Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
		Command: "/bin/pwd", Cwd: &relativeCwd,
	}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Store: store,
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeTab)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the wrapper starting somewhere else entirely — its own process cwd must
	// never matter, since the wrapper reads an already-absolute cwd from the row.
	withWorkingDirectory(t, t.TempDir())

	statusFile := store.StatusFile(prepared.Row.ID)
	if err := os.MkdirAll(filepath.Dir(statusFile), 0o755); err != nil {
		t.Fatal(err)
	}
	exitCode, err := RunWrapper(store.RowPath(prepared.Row.ID), func() time.Time { return time.Unix(200, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("wrapper exit = %d, want 0", exitCode)
	}
	completed, err := store.Load(prepared.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantCwd, err := filepath.EvalSymlinks(scriptsDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(completed.Cwd); evalErr != nil || resolved != wantCwd {
		t.Fatalf("wrapper's row Cwd = %q, want %q", completed.Cwd, wantCwd)
	}
}

// TestWrapperFindsItsRowWhenTheStateDirWasConfiguredRelative models the real failure: a
// relative HIVEDECK_STATE_DIR left unresolved would produce a relative row path, embedded in
// the wrapper's argv; a wrapper started fresh in the TOOL's own cwd (as every real tab-route
// spawn is) would then look for that relative path under the wrong directory entirely.
func TestWrapperFindsItsRowWhenTheStateDirWasConfiguredRelative(t *testing.T) {
	invocationDirectory := t.TempDir()
	withWorkingDirectory(t, invocationDirectory)
	t.Setenv("HIVEDECK_STATE_DIR", "relative-state")

	store, err := runs.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(store.StateDirectory) {
		t.Fatalf("StateDirectory = %q, want an absolute path", store.StateDirectory)
	}

	toolCwd := t.TempDir() // somewhere else entirely — never the invocation directory
	row, err := store.Begin("fixture", profile.ModeTab, toolCwd, []string{"true"}, os.Environ(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	rowPath := store.RowPath(row.ID)
	if !filepath.IsAbs(rowPath) {
		t.Fatalf("RowPath() = %q, want an absolute path", rowPath)
	}

	// Simulate the wrapper starting in the TOOL's own cwd, exactly as a real tab-route spawn
	// does — the row path handed to it on argv must not depend on that.
	withWorkingDirectory(t, toolCwd)
	exitCode, err := RunWrapper(rowPath, func() time.Time { return time.Unix(200, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("wrapper exit = %d, want 0 — it must find its row from the absolute path regardless of its own cwd", exitCode)
	}
}

func withWorkingDirectory(t *testing.T, directory string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
}

func TestPrepareResolvesTheExecutableAgainstTheProfilePath(t *testing.T) {
	toolDirectory := t.TempDir()
	scriptPath := filepath.Join(toolDirectory, "hivedeck-fixture-tool")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tool := discovery.Tool{Name: "fixture", Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
		Command: "hivedeck-fixture-tool",
	}}}
	// The tool is reachable ONLY via the profile's PATH addition, never via the ambient
	// process PATH passed as Environment below.
	repositoryProfile := profile.File{Tools: map[string]profile.Tool{"fixture": {
		Env: map[string]string{"PATH": toolDirectory},
	}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Profile: repositoryProfile,
		Store: runs.Store{StateDirectory: t.TempDir()}, Environment: []string{"PATH=/usr/bin:/bin"}, StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Command.Run(); err != nil {
		t.Fatalf("Command.Run() = %v, want the executable resolved from the profile's PATH, not the ambient one", err)
	}
}

// TestExecutableLookupNeverFallsBackToTheAmbientPath proves the class the finding names: a
// profile PATH that cannot resolve the executable must fail outright, never silently fall
// through to whatever the CALLING process's own PATH happens to resolve — "sh" is present on
// virtually every ambient PATH, which is exactly why it is the fixture here.
func TestExecutableLookupNeverFallsBackToTheAmbientPath(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tool := discovery.Tool{Name: "fixture", Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
		Command: "sh",
	}}}
	repositoryProfile := profile.File{Tools: map[string]profile.Tool{"fixture": {
		Env: map[string]string{"PATH": "/nonexistent"},
	}}}
	_, err = Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Profile: repositoryProfile,
		Store: runs.Store{StateDirectory: t.TempDir()}, Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err == nil {
		t.Fatal("Prepare should refuse to launch when the configured PATH cannot resolve the executable, never fall back to the ambient PATH")
	}
	if !strings.Contains(err.Error(), "executable not found on the configured PATH") {
		t.Fatalf("error = %v, want a named \"executable not found on the configured PATH\" error", err)
	}
}

func TestPrepareUsesProfileSpecArgumentsAndEnvironment(t *testing.T) {
	toolDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolDirectory, "tool"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repositoryToken := "{repo}"
	tool := discovery.Tool{Name: "fixture", Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{
		Command: "tool", Cwd: &repositoryToken, Args: []string{"{spec}"},
	}}}
	repositoryProfile := profile.File{Tools: map[string]profile.Tool{"fixture": {
		Spec: "spec.yaml", Args: []string{"--flag"}, Env: map[string]string{"TARGET": "env.SOURCE"},
	}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Profile: repositoryProfile,
		Store: runs.Store{StateDirectory: t.TempDir()}, Environment: []string{"SOURCE=value", "PATH=" + toolDirectory}, StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Command.Args, []string{"tool", "spec.yaml", "--flag"}) {
		t.Fatalf("command args = %v", prepared.Command.Args)
	}
	if !strings.Contains(strings.Join(prepared.Command.Env, "\n"), "TARGET=value") {
		t.Fatalf("command environment = %v", prepared.Command.Env)
	}
}

// TestPrepareRefusesAPaneForAToolThatNeedsARealTerminal holds the manifest's one hard
// constraint at the seam every route passes through, so it cannot be lost by a caller that
// resolved the mode wrongly. The resolver's degrade is tested separately, in the profile
// package: these are two independent defences of the same rule, and both must hold.
func TestPrepareRefusesAPaneForAToolThatNeedsARealTerminal(t *testing.T) {
	interactive := true
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{Name: "psql-console", Manifest: toolspec.ToolManifest{
		Launch: toolspec.ToolManifestLaunch{Command: "/bin/sh"},
		Pane:   &toolspec.ToolManifestPane{Interactive: &interactive},
	}}
	request := Request{
		Tool: tool, RepositoryRoot: t.TempDir(), Store: store,
		// The profile asking for a pane is exactly the case the constraint outranks.
		Profile:     profile.File{Tools: map[string]profile.Tool{"psql-console": {Mode: profile.ModePane}}},
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}
	if _, err := Prepare(request, profile.ModePane); err == nil {
		t.Fatal("Prepare accepted a pane for a tool that declares it needs a real terminal")
	} else if !strings.Contains(err.Error(), "real terminal") {
		t.Fatalf("refusal says %q, want it to name the reason", err)
	}
	rows, err := store.List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("a refused launch left %d run rows (error %v) — nothing may be recorded for a route that never ran", len(rows), err)
	}

	// The same tool by any legal route still launches, and the row records that route.
	prepared, err := Prepare(request, profile.ModeForeground)
	if err != nil {
		t.Fatalf("foreground launch of an interactive tool: %v", err)
	}
	if prepared.Row.Mode != profile.ModeForeground {
		t.Fatalf("run row records mode %q, want the effective route", prepared.Row.Mode)
	}
}

// TestRunWrapperShowsAStatusStreamWhileTheChildIsStillRunning is the tab route's live half.
// The child publishes two events and then waits for a gate file the test only creates once it
// has SEEN the row move — so the assertion is that the row changed while the child was still
// alive, with no dependence on how long anything takes.
func TestRunWrapperShowsAStatusStreamWhileTheChildIsStillRunning(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	gatePath := filepath.Join(t.TempDir(), "release")
	script := filepath.Join(repositoryRoot, "testdata", "status-stream.sh")
	row, err := store.Begin("watcher", profile.ModeTab, repositoryRoot,
		[]string{"/bin/sh", script, gatePath}, os.Environ(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	// The status-file variable is what a real launch puts on the row in Prepare; the row is
	// the only place the wrapper reads the child's environment from.
	row.Env = setEnvironment(row.Env, "HIVEDECK_STATUS_FILE", store.StatusFile(row.ID))
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}

	finished := make(chan int, 1)
	go func() {
		exitCode, _ := RunWrapper(store.RowPath(row.ID), func() time.Time { return time.Unix(200, 0) })
		finished <- exitCode
	}()

	live := waitForOutcome(t, store, row.ID, "progress")
	if live.Artifact != "evidence.html" {
		t.Fatalf("live row = %+v, want the artifact reported before the run ended", live)
	}
	if live.FinishedAt != nil {
		t.Fatalf("the row was already finished when the live update landed: %+v", live)
	}
	if err := os.WriteFile(gatePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case exitCode := <-finished:
		if exitCode != 0 {
			t.Fatalf("wrapper exit = %d, want 0", exitCode)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the wrapper never returned after the child was released")
	}
	completed, err := store.Load(row.ID)
	if err != nil || completed.ExitCode == nil || *completed.ExitCode != 0 || completed.Outcome != "ok" {
		t.Fatalf("completed row = %+v, error = %v", completed, err)
	}
}

func waitForOutcome(t *testing.T, store runs.Store, runID, wanted string) runs.Row {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		row, err := store.Load(runID)
		if err == nil && row.Outcome == wanted {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s never reached outcome %q", runID, wanted)
	return runs.Row{}
}

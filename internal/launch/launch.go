package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/manifest"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
)

type Terminal string

const (
	TerminalTmux    Terminal = "tmux"
	TerminalWindows Terminal = "windows-terminal"
	TerminalKitty   Terminal = "kitty"
	TerminalWezTerm Terminal = "wezterm"
	TerminalGhostty Terminal = "ghostty"
	TerminalITerm   Terminal = "iterm2"
	TerminalApple   Terminal = "terminal-app"
	TerminalGNOME   Terminal = "gnome-terminal"
)

type Request struct {
	Tool           discovery.Tool
	RepositoryRoot string
	Profile        profile.File
	Spec           string
	Store          runs.Store
	Environment    []string
	StartedAt      time.Time
}

type Prepared struct {
	Row     runs.Row
	Command *exec.Cmd
}

// Prepare resolves one launch into a run row and a runnable command. The mode it is handed is
// the EFFECTIVE route, and it is what the run row records — a caller that degraded a mode has
// already done so by here.
func Prepare(request Request, mode profile.LaunchMode) (Prepared, error) {
	// The seam refuses, rather than silently degrading: a caller asking for a pane on a tool
	// that declares it needs a real terminal has resolved the mode wrongly, and quietly
	// running it somewhere else would hide that. The degrade belongs one level up, in the
	// resolver that knows what the profile asked for.
	if mode == profile.ModePane && manifest.RequiresRealTerminal(request.Tool.Manifest) {
		return Prepared{}, fmt.Errorf("%s declares it needs a real terminal and can never run in a captured pane", request.Tool.Name)
	}
	settings := request.Profile.Tools[request.Tool.Name]
	spec := request.Spec
	if spec == "" {
		spec = settings.Spec
	}
	if manifest.RequiresSpec(request.Tool.Manifest) && spec == "" {
		return Prepared{}, fmt.Errorf("a spec path is required")
	}
	resolved, err := manifest.ResolveLaunch(request.Tool.Manifest, request.RepositoryRoot, spec)
	if err != nil {
		return Prepared{}, err
	}
	resolved.Args = append(resolved.Args, settings.Args...)
	// Made absolute exactly ONCE, here, against the repository — never re-derived by a
	// route or the wrapper. A manifest cwd that does not use the {repo} token (a bare
	// relative string) would otherwise resolve against whatever directory happens to be
	// current at spawn time: hivedeck's own invocation directory for the foreground route
	// (wrong for a nested-subdirectory invocation), or the tab-route wrapper's own unrelated
	// starting directory.
	resolved.Cwd = absoluteAgainstRepository(request.RepositoryRoot, resolved.Cwd)
	environment, err := profile.ResolveEnvironment(settings, request.Environment)
	if err != nil {
		return Prepared{}, err
	}
	argv := append([]string{resolved.Command}, resolved.Args...)
	row, err := request.Store.Begin(request.Tool.Name, mode, resolved.Cwd, argv, environment, request.StartedAt)
	if err != nil {
		return Prepared{}, err
	}
	// The row persists the FULL resolved child environment — not just the additions below —
	// so a tab-route wrapper started fresh by a terminal that dropped every other variable
	// can still reconstruct exactly the environment this process resolved.
	row.Env = setEnvironment(row.Env, "HIVEDECK_STATUS_FILE", request.Store.StatusFile(row.ID))
	row.Env = setEnvironment(row.Env, "HIVEDECK_RUN_ID", row.ID)
	row.Env = setEnvironment(row.Env, "HIVEDECK_STATE_DIR", request.Store.StateDirectory)
	if err := request.Store.Save(row); err != nil {
		return Prepared{}, err
	}
	command, err := buildCommand(row.Argv, row.Cwd, row.Env)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Row: row, Command: command}, nil
}

func absoluteAgainstRepository(repositoryRoot, cwd string) string {
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Join(repositoryRoot, cwd)
}

// buildCommand resolves argv[0] against environment's own PATH ONLY — never the calling
// process's. exec.Command's implicit LookPath runs immediately, using os.Getenv("PATH"),
// before command.Env is ever assigned; a tool reachable only through a profile PATH addition
// would otherwise fail to resolve, or resolve to the wrong installation, in both the
// foreground route (here) and the tab-route wrapper.
//
// A lookup failure is a hard error here — there is no fallback to whatever the calling
// process's own ambient PATH happens to resolve. A profile PATH that cannot find the
// executable (PATH=/nonexistent, say) must fail loudly with a named error, never silently run
// an unrelated binary of the same name found on the wrong PATH (a bare "sh", present on
// virtually every ambient PATH, is exactly the shape of tool this could otherwise pick up).
func buildCommand(argv []string, cwd string, environment []string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("missing command")
	}
	executable, err := resolveExecutable(argv[0], environment)
	if err != nil {
		return nil, err
	}
	// Constructed from argv[0] (not the resolved path) so Args[0] keeps the name as given —
	// os/exec's own convention — then Path is overridden to the already-resolved executable.
	command := exec.Command(argv[0], argv[1:]...)
	command.Path, command.Err = executable, nil
	command.Dir, command.Env = cwd, environment
	return command, nil
}

// resolveExecutable mirrors os/exec.LookPath's unix contract (a name containing a path
// separator is used as-is; a bare name is searched across PATH, requiring the executable
// bit) but against an explicit environment slice rather than the calling process's own, and
// NEVER falls back to that calling process's PATH on failure.
func resolveExecutable(name string, environment []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil
	}
	pathValue := environmentMap(environment)["PATH"]
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable not found on the configured PATH: %q", name)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func Finish(store runs.Store, runID string, commandError error, finishedAt time.Time) (runs.Row, error) {
	return store.Complete(runID, ExitCode(commandError), finishedAt)
}

func ExitCode(commandError error) int {
	if commandError == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(commandError, &exitError) {
		if code := exitError.ExitCode(); code >= 0 {
			return code
		}
		if signaled, signalNumber := terminatingSignal(exitError); signaled {
			return 128 + signalNumber
		}
	}
	return 1
}

// terminatingSignal reports the signal that terminated a process, per the shell convention
// hivedeck's own run rows follow: a forwarded SIGINT records exit code 130, SIGTERM 143, so
// "send SIGINT to a running wrapper" produces a row with a real, conventional exit code
// rather than the generic fallback of 1.
func terminatingSignal(exitError *exec.ExitError) (bool, int) {
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return false, 0
	}
	return true, int(status.Signal())
}

func DetectTerminal(environment []string) (Terminal, bool) {
	values := environmentMap(environment)
	switch {
	case values["TMUX"] != "":
		return TerminalTmux, true
	case values["WT_SESSION"] != "":
		return TerminalWindows, true
	case values["KITTY_WINDOW_ID"] != "":
		return TerminalKitty, true
	case values["WEZTERM_PANE"] != "":
		return TerminalWezTerm, true
	// Checked before the TERM_PROGRAM cases below because Ghostty sets TERM_PROGRAM too, and
	// GHOSTTY_SURFACE_ID is the more specific signal: it is set per surface by the terminal
	// itself, where TERM_PROGRAM can be inherited by anything the surface starts.
	case values["GHOSTTY_SURFACE_ID"] != "" || values["TERM_PROGRAM"] == "ghostty":
		return TerminalGhostty, true
	case values["TERM_PROGRAM"] == "iTerm.app":
		return TerminalITerm, true
	case values["TERM_PROGRAM"] == "Apple_Terminal":
		return TerminalApple, true
	case values["GNOME_TERMINAL_SERVICE"] != "" || strings.Contains(strings.ToLower(values["TERM_PROGRAM"]), "gnome"):
		return TerminalGNOME, true
	default:
		return "", false
	}
}

func ProbeTerminal(terminal Terminal) error {
	if terminal != TerminalKitty {
		return nil
	}
	if err := exec.Command("kitten", "@", "ls").Run(); err != nil {
		return fmt.Errorf("kitty remote control unavailable: %w", err)
	}
	return nil
}

// NewTabCommand builds the command that opens a new tab/window in the host terminal and
// runs wrapperArgv inside it. wrapperArgv is expected to be exactly
// [hivedeckExecutable, "run", <run-row-path>] — no env and no tool argv is placed on this
// command line, because no terminal's new-tab spawn form reliably transfers a launching
// process's environment into the session it creates (§ launch: new-tab environment).
// command.Env is deliberately left nil so the terminal CLIENT itself (tmux/kitten/osascript/
// wt.exe/gnome-terminal) inherits hivedeck's own ambient environment, exactly as it needs to
// find its own socket/display/PATH; the child tool's environment travels via the run row,
// read directly by the wrapper once the new tab starts it.
func NewTabCommand(terminal Terminal, row runs.Row, wrapperArgv []string) (*exec.Cmd, error) {
	switch terminal {
	case TerminalTmux:
		return exec.Command("tmux", append([]string{"new-window", "-c", row.Cwd, "--"}, wrapperArgv...)...), nil
	case TerminalWindows:
		return exec.Command("wt.exe", append([]string{"-w", "0", "nt", "-d", row.Cwd}, wrapperArgv...)...), nil
	case TerminalKitty:
		return exec.Command("kitten", append([]string{"@", "launch", "--type=tab", "--cwd", row.Cwd, "--"}, wrapperArgv...)...), nil
	case TerminalWezTerm:
		return exec.Command("wezterm", append([]string{"cli", "spawn", "--cwd", row.Cwd, "--"}, wrapperArgv...)...), nil
	case TerminalGhostty:
		return exec.Command("osascript", "-e", ghosttyScript(row.Cwd, wrapperArgv)), nil
	case TerminalITerm:
		return exec.Command("osascript", "-e", iTermScript(row.Cwd, wrapperArgv)), nil
	case TerminalApple:
		return exec.Command("osascript", "-e", terminalScript(row.Cwd, wrapperArgv)), nil
	case TerminalGNOME:
		return exec.Command("gnome-terminal", append([]string{"--tab", "--working-directory=" + row.Cwd, "--"}, wrapperArgv...)...), nil
	default:
		return nil, fmt.Errorf("unsupported terminal %q", terminal)
	}
}

// urlSchemePattern recognizes any `scheme://` prefix (RFC 3986's scheme grammar), not just
// http/https: `file://` is a URL too, and every scheme letter class already covers both
// cases, so "HTTPS://" and "File://" match without a separate case-insensitive flag.
var urlSchemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

func ArtifactCommand(row runs.Row, goos string) (*exec.Cmd, error) {
	if row.Artifact == "" {
		return nil, fmt.Errorf("run %s has no artifact", row.ID)
	}
	target := row.Artifact
	// A URL of ANY scheme (http, https, file, ...) is opened as-is by the platform handler;
	// only a target with no scheme at all is a path, resolved against the run's cwd if not
	// already absolute. `file://` in particular must never be joined beneath cwd — it is not
	// a filesystem path, and joining it produces nonsense like "<cwd>/file:/actual/path".
	if !urlSchemePattern.MatchString(target) && !filepath.IsAbs(target) {
		target = filepath.Join(row.Cwd, target)
	}
	switch goos {
	case "darwin":
		return exec.Command("open", target), nil
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target), nil
	case "linux":
		return exec.Command("xdg-open", target), nil
	default:
		return nil, fmt.Errorf("artifact opening is unsupported on %s", goos)
	}
}

// ghosttyScript drives Ghostty's AppleScript surface, which is the only new-tab route Ghostty
// offers on macOS: `ghostty +new-window` reports "not supported on this platform" there, and
// `open -na Ghostty.app` opens a WINDOW, never a tab.
//
// The window branch is not defensive padding: `new tab` with no `in <window>` fails with
// AppleScript error -1708 when the application has no window to put the tab in.
//
// Ghostty's `command` property is split into argv rather than handed to a shell, so the shell
// string goes through `/bin/sh -c` as a single quoted argument — the same quoting helper the
// other osascript routes use, never a second one.
func ghosttyScript(cwd string, wrapper []string) string {
	command := "/bin/sh -c " + quoteShellArgument(shellCommand(cwd, wrapper))
	return "tell application \"Ghostty\"\n" +
		"set cfg to new surface configuration\n" +
		"set initial working directory of cfg to " + strconv.Quote(cwd) + "\n" +
		"set command of cfg to " + strconv.Quote(command) + "\n" +
		"if (count of windows) is 0 then\n" +
		"new window with configuration cfg\n" +
		"else\n" +
		"new tab in window 1 with configuration cfg\n" +
		"end if\n" +
		"end tell"
}

func iTermScript(cwd string, wrapper []string) string {
	command := shellCommand(cwd, wrapper)
	return "tell application \"iTerm\"\n" +
		"tell current window\n" +
		"set newTab to (create tab with default profile)\n" +
		"tell current session of newTab to write text " + strconv.Quote(command) + "\n" +
		"end tell\nend tell"
}

func terminalScript(cwd string, wrapper []string) string {
	return "tell application \"Terminal\" to do script " + strconv.Quote(shellCommand(cwd, wrapper))
}

func shellCommand(cwd string, arguments []string) string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, quoteShellArgument(argument))
	}
	return "cd " + quoteShellArgument(cwd) + " && " + strings.Join(quoted, " ")
}

func quoteShellArgument(argument string) string {
	return "'" + strings.ReplaceAll(argument, "'", "'\"'\"'") + "'"
}

func setEnvironment(environment []string, name, value string) []string {
	values := environmentMap(environment)
	values[name] = value
	return environmentSlice(values)
}

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

func environmentSlice(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result
}

// statusPollInterval is how often a live watcher re-reads a run's status file. It is a poll
// rather than a file-system watch on purpose: the file may not exist yet, may be written by a
// process on the other side of a container boundary, and is append-only — all of which a poll
// handles identically and a watch does not.
const statusPollInterval = 200 * time.Millisecond

// tailStatusFile watches a run's status file until the returned stop function is called, which
// it waits for the watcher to observe — so no update can land after the run has been
// completed, which would reopen a finished row.
func tailStatusFile(store runs.Store, runID string) func() {
	monitor := runs.NewMonitor(store.StatusFile(runID))
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(statusPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if update, changed := monitor.Poll(); changed {
					_, _ = store.UpdateLive(runID, update)
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-stopped
	}
}

// RunWrapper is the tab-route wrapper's entire job: given ONLY the path to a run row (never
// a run id, never argv, never an environment — see NewTabCommand), it reconstructs the
// child's command, working directory and environment entirely from that row, runs it, and
// records the outcome. This is what makes a tab launch correct even when the terminal that
// opened it started the wrapper with a dropped or unrelated environment.
func RunWrapper(rowPath string, finishedAt func() time.Time) (int, error) {
	store, row, err := runs.LoadFromPath(rowPath)
	if err != nil {
		return 1, err
	}
	if len(row.Argv) == 0 {
		return 2, fmt.Errorf("run row %s has no recorded command", row.ID)
	}
	command, err := buildCommand(row.Argv, row.Cwd, row.Env)
	if err != nil {
		return 1, err
	}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The wrapper is what makes a tab run observable while it is still going: it tails the
	// run's own status file — never the child's output, which belongs to the person watching
	// the tab — and writes what it sees onto the row hivedeck is listing.
	stopTailing := tailStatusFile(store, row.ID)
	// The trap stays armed past ForwardSignalsAndWait returning — deferred here, at the
	// RunWrapper level, so it covers the handback window through Finish persisting the row,
	// not just the window while the child is actually running.
	signals, stop := ArmSignalTrap()
	defer stop()
	commandError := ForwardSignalsAndWait(command, signals)
	stopTailing()
	if _, err := Finish(store, row.ID, commandError, finishedAt()); err != nil {
		return 1, err
	}
	return ExitCode(commandError), nil
}

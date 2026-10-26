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

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

type Terminal string

const (
	TerminalTmux    Terminal = "tmux"
	TerminalWindows Terminal = "windows-terminal"
	TerminalKitty   Terminal = "kitty"
	TerminalWezTerm Terminal = "wezterm"
	TerminalGhostty Terminal = "ghostty"
	TerminalCmux    Terminal = "cmux"
	TerminalITerm   Terminal = "iterm2"
	TerminalApple   Terminal = "terminal-app"
	TerminalGNOME   Terminal = "gnome-terminal"
)

type Request struct {
	Tool           discovery.Tool
	RepositoryRoot string
	Profile        profile.File
	Spec           string
	// Inputs holds a value per named input, already parsed by the caller (e.g. cmd/formic's
	// --input/FORMIC_INPUT_<NAME> parsing) — never raw flag/env text.
	Inputs      map[string]string
	Store       runs.Store
	Environment []string
	StartedAt   time.Time
	// Mission is the mission's name, set only by `formic mission run` — every other caller
	// leaves it empty and the run row records no mission (§9).
	Mission string
	// MissionPath is the discovered mission definition's own source file path (car 10b E0
	// restore), set only alongside Mission — empty for every other caller. Prepare copies it
	// onto the row so WriteMissionRecord's MissionFile can name it.
	MissionPath string
	// EvidenceRoot is a mission's resolved evidence folder, <evidence>/<mission-name>/ (set by
	// mission.Preflight per the orchestrator's evidence-path ruling) — empty for every other
	// caller, including plain `launch`. When set, Prepare creates <EvidenceRoot>/<run-id>/ and,
	// if the tool declares outputs.evidenceDir, wires that path into the child's environment
	// BEFORE declared-env scoping runs, so a required outputs.evidenceDir key is satisfied.
	EvidenceRoot string
	// Action names the action to run; empty for a manifest with no actions[]. Prepare refuses
	// an empty Action on a multi-action tool and an unknown one.
	Action string
	// Confirmed is the caller's yes for an action that declares confirm (the CLI's --yes, the
	// deck's y). Prepare refuses a confirming action without it, so every caller is gated alike.
	Confirmed bool
	// Detach is the CLI's --detach: it lets a lifecycle: service action be launched at all
	// (Prepare then expects a tab mode). Without it a service in the foreground/captured route
	// is refused with ErrNeedsDetach.
	Detach bool
	// Live is the caller's own pre-launch probe result (the deck probes off the UI thread and
	// hands it over); nil means not probed, and Prepare never probes itself.
	Live *ProbeResult
	// Anyway spawns even when the effective whenUp is skip and Live answers (the CLI's --anyway,
	// the deck's launch-anyway key). It never bypasses a need, a confirm, or the detach rule.
	Anyway bool
	// Services answers `kind: service` needs (CatalogServiceChecker); nil leaves them unchecked,
	// which never blocks. The CLI sets it from CatalogServiceChecker; the deck from results it
	// probed off the UI thread (ServiceCheckerFrom).
	Services equip.ServiceChecker
}

type Prepared struct {
	Row     runs.Row
	Command *exec.Cmd
}

// Prepare resolves one launch into a run row and a runnable command. The mode it is handed is
// the EFFECTIVE route, and it is what the run row records — a caller that degraded a mode has
// already done so by here.
func Prepare(request Request, mode profile.LaunchMode) (Prepared, error) {
	// The action projection comes first: everything below reads the one-action manifest, so an
	// unknown/missing action, an unconfirmed action, an already-up service and a service with
	// no --detach are all refused before the pane guard, needs, or any row.
	effective, err := manifest.ForAction(request.Tool.Manifest, request.Action)
	if err != nil {
		return Prepared{}, err
	}
	if err := checkActionGates(request, request.Tool.Manifest, mode); err != nil {
		return Prepared{}, err
	}
	request.Tool.Manifest = effective
	// The seam refuses, rather than silently degrading: a caller asking for a pane on a tool
	// that declares it needs a real terminal has resolved the mode wrongly, and quietly
	// running it somewhere else would hide that. The degrade belongs one level up, in the
	// resolver that knows what the profile asked for.
	if mode == profile.ModePane && manifest.RequiresRealTerminal(request.Tool.Manifest) {
		return Prepared{}, fmt.Errorf("%s declares it needs a real terminal and can never run in a captured pane", request.Tool.Name)
	}
	// The needs-resolution step (§13 "a resolution step before every launch") runs before
	// anything else that could write a run row or start a process: every caller of Prepare —
	// the CLI verb and the deck alike — is refused the same way on an unmet need.
	gateAssignments, err := resolveNeedsForLaunch(request.Tool, request.Environment, request.Services)
	if err != nil {
		return Prepared{}, err
	}
	// The inputs-resolution step (N10) runs right after needs, before any row is written or
	// process started, mirroring resolveNeedsForLaunch's own placement — every caller of
	// Prepare is refused the same way on a missing/unknown input.
	inputValues, err := resolveInputsForLaunch(request.Tool, request)
	if err != nil {
		return Prepared{}, err
	}
	settings := request.Profile.Tools[request.Tool.Name]
	spec := request.Spec
	if spec == "" {
		spec = settings.Spec
	}
	// {spec} sugar (N10): a manifest that declares an input literally named "spec" gets its
	// value through the inputs path instead of the bare fallback above.
	if declaredSpec, declared := inputValues["spec"]; declared {
		spec = declaredSpec
	}
	if manifest.RequiresSpec(request.Tool.Manifest) && spec == "" {
		return Prepared{}, fmt.Errorf("a spec path is required")
	}
	repositoryRoot := request.Tool.SourceRoot
	if repositoryRoot == "" {
		repositoryRoot = request.RepositoryRoot
	}
	resolved, err := manifest.ResolveLaunch(request.Tool.Manifest, repositoryRoot, spec, inputValues)
	if err != nil {
		return Prepared{}, err
	}
	resolved.Args = append(resolved.Args, settings.ArgsFor(request.Action)...)
	// Made absolute exactly ONCE, here, against the tool's catalogued source (§12: "{repo}
	// resolves to its source's path") — never re-derived by a route or the wrapper. A
	// manifest cwd that does not use the {repo} token (a bare relative string) would
	// otherwise resolve against whatever directory happens to be current at spawn time:
	// hivedeck's own invocation directory for the foreground route (wrong for a nested-
	// subdirectory invocation), or the tab-route wrapper's own unrelated starting directory.
	resolved.Cwd = absoluteAgainstRepository(repositoryRoot, resolved.Cwd)
	// A SECOND resolution, with every sensitive input's value swapped for "<redacted:NAME>"
	// before substitution, produces the argv/cwd the durable row is allowed to hold (N10
	// ruling 2). Resolving twice — rather than string-replacing the real argv after the fact —
	// means a redaction can never accidentally match a coincidental substring elsewhere in a
	// non-sensitive argument.
	redactedInputValues := RedactSensitiveInputValues(request.Tool.Manifest.Inputs, inputValues)
	redactedResolved, err := manifest.ResolveLaunch(request.Tool.Manifest, repositoryRoot, spec, redactedInputValues)
	if err != nil {
		return Prepared{}, err
	}
	redactedResolved.Args = append(redactedResolved.Args, settings.ArgsFor(request.Action)...)
	redactedResolved.Cwd = absoluteAgainstRepository(repositoryRoot, redactedResolved.Cwd)
	argv := append([]string{resolved.Command}, resolved.Args...)
	redactedArgv := append([]string{redactedResolved.Command}, redactedResolved.Args...)
	// The run id is minted here, before declared-env scoping, so a mission's evidence folder —
	// which names the run id — can be created and, when the tool declares outputs.evidenceDir,
	// wired into the environment scoping is about to see BEFORE it enforces a `required: true`
	// declared key (the launcher owns evidence: neither cmd/formic nor a future deck route
	// injects it after the fact). EnvNames below is corrected by the row's own final Save, the
	// same two-write pattern Mission/EvidenceDir already used.
	row, err := request.Store.Begin(request.Tool.Name, mode, redactedResolved.Cwd, redactedArgv, nil, request.StartedAt)
	if err != nil {
		return Prepared{}, err
	}
	// A met gate need's chosen provider is appended after the request's own environment, so
	// it wins when resolveEnvironment's own env[] scoping later keeps that declared key.
	requestEnvironment := append(append([]string(nil), request.Environment...), gateAssignments...)
	evidenceDirectory, err := prepareEvidenceDirectory(request, row.ID)
	if err != nil {
		return Prepared{}, err
	}
	if evidenceDirectory != "" && request.Tool.Manifest.Outputs != nil && request.Tool.Manifest.Outputs.EvidenceDir != nil {
		requestEnvironment = setEnvironment(requestEnvironment, *request.Tool.Manifest.Outputs.EvidenceDir, evidenceDirectory)
	}
	environment, err := resolveEnvironment(request.Tool.Manifest, settings, requestEnvironment)
	if err != nil {
		return Prepared{}, err
	}
	environment = setEnvironment(environment, "FORMIC_STATUS_FILE", request.Store.StatusFile(row.ID))
	environment = setEnvironment(environment, "FORMIC_RUN_ID", row.ID)
	environment = setEnvironment(environment, "FORMIC_STATE_DIR", request.Store.StateDirectory)
	// The durable row records the REDACTED argv/cwd (already passed to Begin above) and
	// environment NAMES only (N10) — never a child environment value, secret or not: the
	// manifest format has no way to mark a declared env[] entry non-sensitive, so this is a
	// blanket redaction (orchestrator ruling 2026-09-23, inside founder ruling 2).
	row.EnvNames = environmentNames(environment)
	row.Mission = request.Mission
	row.Action = request.Action
	if request.Mission != "" {
		row.Inputs = redactedInputValues
		row.MissionPath = request.MissionPath
	}
	if evidenceDirectory != "" {
		// A mission run's evidence folder IS the resolved evidenceDir — set directly, never
		// re-derived from environment (there may be no outputs.evidenceDir key at all; §9: "the
		// folder holds only the platform's record" in that case, and the folder still exists).
		row.EvidenceDir = evidenceDirectory
	} else {
		// outputs.evidenceDir names an env[] variable, not a secret — resolved here, once, into
		// its own non-redacted field, so the deck's evidence-directory fallback
		// (internal/ui/view.go runArtifact) keeps working without reading any value out of the
		// child's real environment.
		row.EvidenceDir = resolveEvidenceDir(request.Tool.Manifest, environment, resolved.Cwd)
	}
	if err := request.Store.Save(row); err != nil {
		return Prepared{}, err
	}
	// The ephemeral replay file is written ONLY on the tab route (N10 ruling 3): the
	// foreground and pane routes build and run Command directly, in this same process, and
	// never need a separate process to rebuild the child from anything persisted.
	if mode == profile.ModeTab || mode == profile.ModeSplit {
		if err := request.Store.WriteReplay(row.ID, argv, resolved.Cwd, environment); err != nil {
			return Prepared{}, err
		}
	}
	command, err := buildCommand(argv, resolved.Cwd, environment)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Row: row, Command: command}, nil
}

// prepareEvidenceDirectory creates <request.EvidenceRoot>/<runID>/ when request.EvidenceRoot is
// set (a mission run) and returns its path; it returns "" (and creates nothing) for every other
// caller. Called once Store.Begin has minted runID, so the folder — and, through it, the
// environment variable a manifest's outputs.evidenceDir names — exists before declared-env
// scoping runs.
func prepareEvidenceDirectory(request Request, runID string) (string, error) {
	if request.EvidenceRoot == "" {
		return "", nil
	}
	directory := filepath.Join(request.EvidenceRoot, runID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create evidence directory: %w", err)
	}
	return directory, nil
}

// environmentNames extracts each entry's NAME=VALUE name, discarding the value — what the
// durable row records for the child environment (N10).
func environmentNames(environment []string) []string {
	names := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	return names
}

// resolveEvidenceDir resolves the manifest's outputs.evidenceDir (an env[] variable NAME, not a
// literal path) to that variable's value in the resolved child environment, made absolute
// against cwd when it names a relative path. This is the same lookup
// internal/ui/view.go's runArtifact used to perform directly against the run row's environment
// before N10 redacted it — done once, here, at launch, since the durable row no longer carries
// environment values to look one up from later.
func resolveEvidenceDir(tool formats.ToolManifest, environment []string, cwd string) string {
	if tool.Outputs == nil || tool.Outputs.EvidenceDir == nil {
		return ""
	}
	value := environmentValue(environment, *tool.Outputs.EvidenceDir)
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(cwd, value)
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if value, found := strings.CutPrefix(entry, prefix); found {
			return value
		}
	}
	return ""
}

func absoluteAgainstRepository(repositoryRoot, cwd string) string {
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Join(repositoryRoot, cwd)
}

// resolveEnvironment applies the profile's env-by-name resolution first (unchanged for every
// manifest), then — only when the manifest declares `env[]` — scopes the child down to exactly
// the keys it declared, per the manifest format's v1.1 addition. A manifest with no declared
// env keeps today's full ambient-plus-profile passthrough exactly as before.
func resolveEnvironment(tool formats.ToolManifest, settings profile.Tool, processEnvironment []string) ([]string, error) {
	resolved, err := profile.ResolveEnvironment(settings, processEnvironment)
	if err != nil {
		return nil, err
	}
	if len(tool.Env) == 0 {
		return resolved, nil
	}
	return scopeDeclaredEnvironment(tool.Env, resolved, runtime.GOOS)
}

// windowsPassthroughVariables are kept in every scoped child environment, on every OS, even
// when no manifest declares a matching env[] entry: a Windows process needs these to
// function at all, and none of them can be declared instead — widening env[].name's pattern
// (^[A-Z][A-Z0-9_]*$, toolspec.schema.json) to admit SystemRoot or PATHEXT is explicitly
// ruled out. Unset on every non-Windows OS, so this list is harmless there and the same code
// path stays exercised by Linux CI.
var windowsPassthroughVariables = []string{"SystemRoot", "USERPROFILE", "LOCALAPPDATA", "PATHEXT", "TEMP", "TMP"}

// scopeDeclaredEnvironment keeps only the manifest's declared keys, plus PATH (never a
// declared key, but a hard requirement of buildCommand's own executable lookup below, which
// resolves argv[0] against THIS environment's PATH and never falls back to the ambient one)
// and the fixed Windows passthrough set above. A `required` key with no resolved value
// refuses the launch outright, naming every missing key at once, rather than letting the
// child start and fail confusingly on its own.
func scopeDeclaredEnvironment(declared []formats.ToolManifestEnvElem, resolved []string, goos string) ([]string, error) {
	values := environmentMap(resolved)
	scoped := make(map[string]string, len(declared)+1+len(windowsPassthroughVariables))
	if pathValue, ok := lookupEnvironmentEntry(values, "PATH", goos); ok {
		scoped["PATH"] = pathValue
	}
	for _, name := range windowsPassthroughVariables {
		if value, ok := lookupEnvironmentEntry(values, name, goos); ok {
			scoped[name] = value
		}
	}
	var missing []string
	for _, entry := range declared {
		if value := lookupEnvironment(values, entry.Name, goos); value != "" {
			scoped[entry.Name] = value
			continue
		}
		if entry.Required {
			missing = append(missing, entry.Name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}
	return environmentSlice(scoped), nil
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
	if looksLikePath(name) {
		return name, nil
	}
	values := environmentMap(environment)
	pathValue := lookupEnvironment(values, "PATH", runtime.GOOS)
	fileNames := executableFileNames(name, lookupEnvironment(values, "PATHEXT", runtime.GOOS), runtime.GOOS)
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" {
			directory = "."
		}
		for _, fileName := range fileNames {
			candidate := filepath.Join(directory, fileName)
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("executable not found on the configured PATH: %q", name)
}

// looksLikePath reports whether name should be used as given rather than searched on PATH: it
// contains a forward slash, the OS path separator, or a Windows drive prefix (`C:...`). A bare
// `os.PathSeparator` check alone missed a manifest command written with the other slash
// convention — `C:/tools/x.exe` on Windows (os.PathSeparator is `\`), or a backslash path on a
// non-Windows GOOS — so every form is checked regardless of the host OS.
func looksLikePath(name string) bool {
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) {
		return true
	}
	return hasWindowsDrivePrefix(name)
}

// hasWindowsDrivePrefix reports whether name starts with a drive letter followed by `:`
// (`C:...`), the one Windows path form that carries no separator at all when it names a
// location directly under a drive's root.
func hasWindowsDrivePrefix(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	letter := name[0]
	return (letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')
}

// defaultWindowsExecutableExtensions is what Windows itself assumes when PATHEXT is unset.
const defaultWindowsExecutableExtensions = ".com;.exe;.bat;.cmd"

// executableFileNames lists, in the order they are tried, the file names a bare command name
// may resolve to inside one PATH directory. Everywhere but Windows that is the name itself.
// On Windows a manifest's `pwsh` is pwsh.exe and its `npm` is npm.cmd, found through PATHEXT
// exactly as a shell finds them. A name with no extension is never tried as given there: npm
// ships an extensionless shell script beside npm.cmd, and Windows cannot execute it.
func executableFileNames(name, executableExtensions, goos string) []string {
	if goos != "windows" {
		return []string{name}
	}
	if executableExtensions == "" {
		executableExtensions = defaultWindowsExecutableExtensions
	}
	var fileNames []string
	if filepath.Ext(name) != "" {
		fileNames = append(fileNames, name)
	}
	for _, extension := range strings.Split(executableExtensions, ";") {
		if extension != "" {
			fileNames = append(fileNames, name+extension)
		}
	}
	return fileNames
}

// lookupEnvironment reads one variable from an explicit environment. Windows variable names
// are case-insensitive (a process commonly carries `Path`, not `PATH`), so an exact match is
// preferred there and any other casing accepted.
func lookupEnvironment(values map[string]string, name, goos string) string {
	value, _ := lookupEnvironmentEntry(values, name, goos)
	return value
}

// lookupEnvironmentEntry is lookupEnvironment plus whether the variable was present at all.
func lookupEnvironmentEntry(values map[string]string, name, goos string) (string, bool) {
	if value, ok := values[name]; ok || goos != "windows" {
		return value, ok
	}
	for candidateName, value := range values {
		if strings.EqualFold(candidateName, name) {
			return value, true
		}
	}
	return "", false
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

// Finish and Abandon are the ONLY two completion functions in this package (orchestrator ruling,
// car 10b E1) — every route (CLI foreground, mission CLI, deck foreground, pane, and the tab/
// split wrapper's own RunWrapper) completes a run through one of these two, so a mission record
// is written from exactly one place regardless of route.
func Finish(store runs.Store, runID string, commandError error, finishedAt time.Time) (runs.Row, error) {
	row, err := store.Complete(runID, ExitCode(commandError), finishedAt)
	if err != nil {
		return runs.Row{}, err
	}
	return row, writeMissionRecordIfMission(row)
}

// Abandon completes a run whose launch itself never started the tool (§ launch.go Abort's own
// doc comment) — the tab/split-route replay-missing case and the deck's own failed-spawn retry
// path — going through the SAME record write Finish uses, so an aborted mission run still gets
// its record, with outcome "aborted".
func Abandon(store runs.Store, runID, note string, finishedAt time.Time) (runs.Row, error) {
	row, err := store.Abort(runID, note, finishedAt)
	if err != nil {
		return runs.Row{}, err
	}
	return row, writeMissionRecordIfMission(row)
}

// writeMissionRecordIfMission writes row's mission record only when row was launched through
// `formic mission run` (Mission set) AND has an evidence folder to write it into — every other
// route's row leaves both empty and this is a no-op, per §9.
func writeMissionRecordIfMission(row runs.Row) error {
	if row.Mission == "" || row.EvidenceDir == "" {
		return nil
	}
	return runs.WriteMissionRecord(row, row.EvidenceDir)
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
	// cmux embeds Ghostty and inherits its GHOSTTY_*/TERM_PROGRAM variables, so it must be
	// recognised by its own CMUX_BUNDLE_ID before the Ghostty case below claims the session.
	case values["CMUX_BUNDLE_ID"] != "":
		return TerminalCmux, true
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
	switch terminal {
	case TerminalKitty:
		if err := exec.Command("kitten", "@", "ls").Run(); err != nil {
			return fmt.Errorf("kitty remote control unavailable: %w", err)
		}
	case TerminalWindows:
		if err := probeWindowsTerminalExecutable(); err != nil {
			return fmt.Errorf("windows terminal unavailable: %w", err)
		}
	case TerminalCmux:
		if _, err := resolveCmuxExecutable(os.Getenv, exec.LookPath); err != nil {
			return fmt.Errorf("cmux unavailable: %w", err)
		}
	}
	return nil
}

// resolveCmuxExecutable finds the cmux CLI: the path cmux exports into every session it hosts
// (CMUX_BUNDLED_CLI_PATH) first, since the bundled binary is the one that matches the running
// app, then PATH. The lookups are parameters so this is testable without cmux installed.
func resolveCmuxExecutable(getenv func(string) string, lookPath func(string) (string, error)) (string, error) {
	if bundled := getenv("CMUX_BUNDLED_CLI_PATH"); bundled != "" {
		return bundled, nil
	}
	path, err := lookPath("cmux")
	if err != nil {
		return "", fmt.Errorf("cmux CLI not found via CMUX_BUNDLED_CLI_PATH or PATH: %w", err)
	}
	return path, nil
}

// cmuxWorkspaceCommand builds `cmux workspace create`: a new workspace (cmux's sidebar tab)
// in the current window, started in cwd, running the wrapper. --focus false leaves the deck's
// own workspace selected. The cmux CLI ignores the child's env, like every other route here.
func cmuxWorkspaceCommand(cwd string, wrapperArgv []string) *exec.Cmd {
	quoted := make([]string, 0, len(wrapperArgv))
	for _, argument := range wrapperArgv {
		quoted = append(quoted, quoteShellArgument(argument))
	}
	executable, err := resolveCmuxExecutable(os.Getenv, exec.LookPath)
	if err != nil {
		executable = "cmux"
	}
	return exec.Command(executable, "workspace", "create", "--name", "formic", "--cwd", cwd,
		"--command", strings.Join(quoted, " "), "--focus", "false")
}

// probeWindowsTerminalExecutable is a seam over the real resolution below so a test can force
// success or failure deterministically, regardless of whether Windows Terminal actually happens
// to be installed on the machine running the test — the same seam-for-injection precedent as
// runs.openStatusFile.
var probeWindowsTerminalExecutable = func() error {
	_, err := resolveWindowsTerminalExecutable(exec.LookPath, os.Getenv("LOCALAPPDATA"), os.Stat)
	return err
}

// resolveWindowsTerminalExecutable finds wt.exe the way launching it must: through PATH first,
// then — because the wt.exe app-execution alias under %LOCALAPPDATA%\Microsoft\WindowsApps is
// commonly absent from both the user and machine PATH even in a native Windows Terminal session
// — a direct stat of that well-known location. The lookups are parameters, purely so this is
// testable on every platform: no goos check inside, only the values a Windows environment would
// supply.
func resolveWindowsTerminalExecutable(lookPath func(string) (string, error), localAppData string, stat func(string) (os.FileInfo, error)) (string, error) {
	if path, err := lookPath("wt.exe"); err == nil {
		return path, nil
	}
	if localAppData == "" {
		return "", fmt.Errorf("wt.exe not found on PATH and LOCALAPPDATA is not set")
	}
	candidate := filepath.Join(localAppData, "Microsoft", "WindowsApps", "wt.exe")
	if _, err := stat(candidate); err != nil {
		return "", fmt.Errorf("wt.exe not found on PATH or at %s: %w", candidate, err)
	}
	return candidate, nil
}

// NewTabCommand builds the command that opens a new tab/window in the host terminal and
// runs wrapperArgv inside it. wrapperArgv is expected to be exactly
// [formicExecutable, "__replay-row", <run-row-path>] — no env and no tool argv is placed on this
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
		command := exec.Command("wt.exe", append([]string{"-w", "0", "nt", "-d", row.Cwd}, wrapperArgv...)...)
		// Args[0] stays "wt.exe" (the os/exec convention every other route here follows too);
		// only Path is upgraded to the resolved location, the same override buildCommand uses.
		// A resolution failure is left alone rather than propagated — ProbeTerminal already
		// catches that ahead of launch, and NewTabCommand keeps its existing deferred-failure
		// behavior (command.Err, set by exec.Command's own implicit lookup) for whatever calls
		// it without probing first.
		if executable, err := resolveWindowsTerminalExecutable(exec.LookPath, os.Getenv("LOCALAPPDATA"), os.Stat); err == nil {
			command.Path, command.Err = executable, nil
		}
		return command, nil
	case TerminalKitty:
		return exec.Command("kitten", append([]string{"@", "launch", "--type=tab", "--cwd", row.Cwd, "--"}, wrapperArgv...)...), nil
	case TerminalWezTerm:
		return exec.Command("wezterm", append([]string{"cli", "spawn", "--cwd", row.Cwd, "--"}, wrapperArgv...)...), nil
	case TerminalCmux:
		return cmuxWorkspaceCommand(row.Cwd, wrapperArgv), nil
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

// HasSplitCommand reports whether SplitCommand has a case for terminal — the same "no split
// command means tabs only" check the mode chooser (configureSplitAvailable) and startLaunch's
// runtime fallback both need, without either one duplicating SplitCommand's own switch.
func HasSplitCommand(terminal Terminal) bool {
	switch terminal {
	case TerminalTmux, TerminalWindows:
		return true
	default:
		return false
	}
}

// SplitCommand builds the command that opens a new PANE beside the current one in the host
// terminal, running wrapperArgv inside it (N7: "a split route beside the tab route"). Only
// tmux and Windows Terminal have a split-pane command; every other terminal returns an error,
// and the caller falls back to the tab route ("no split command means tabs only", §11).
// wrapperArgv and the environment-handoff contract are identical to NewTabCommand's: the
// terminal's own new-pane spawn form does not reliably transfer this process's environment
// either, so the child's real environment travels through the run row's replay file, read by
// the wrapper once the pane starts it — never placed on this command line.
func SplitCommand(terminal Terminal, cwd string, wrapperArgv []string) (*exec.Cmd, error) {
	switch terminal {
	case TerminalTmux:
		return exec.Command("tmux", append([]string{"split-window", "-c", cwd, "--"}, wrapperArgv...)...), nil
	case TerminalWindows:
		command := exec.Command("wt.exe", append([]string{"-w", "0", "sp", "-d", cwd}, wrapperArgv...)...)
		// Same resolved-executable override NewTabCommand uses; ProbeTerminal already catches a
		// resolution failure ahead of launch for whatever calls this after probing first.
		if executable, err := resolveWindowsTerminalExecutable(exec.LookPath, os.Getenv("LOCALAPPDATA"), os.Stat); err == nil {
			command.Path, command.Err = executable, nil
		}
		return command, nil
	default:
		return nil, fmt.Errorf("no split command for terminal %q", terminal)
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
	// The durable row's own Argv/Cwd are redacted (N10) — the wrapper reads the real values
	// from the ephemeral replay file beside the row, once, deleting it before the child ever
	// starts. A missing replay file (already consumed, or never written — e.g. a row from a
	// non-tab route somehow handed to this wrapper) is a structured abort, never a launch off
	// the redacted row.
	argv, cwd, environment, err := store.ReadAndDeleteReplay(row.ID)
	if err != nil {
		note := "replay file missing — already consumed or never written"
		if !errors.Is(err, os.ErrNotExist) {
			note = fmt.Sprintf("replay file unreadable: %v", err)
		}
		if _, abortErr := Abandon(store, row.ID, note, finishedAt()); abortErr != nil {
			return 1, abortErr
		}
		return 2, fmt.Errorf("%s", note)
	}
	if len(argv) == 0 {
		return 2, fmt.Errorf("run row %s has no recorded command", row.ID)
	}
	command, err := buildCommand(argv, cwd, environment)
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

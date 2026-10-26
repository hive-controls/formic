package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

type launchResult struct {
	RunID    string `json:"runId"`
	Outcome  string `json:"outcome"`
	ExitCode *int   `json:"exitCode"`
	Artifact string `json:"artifact,omitempty"`
	// Mission is the mission's name, present only when this run came from
	// `formic mission run` (§9).
	Mission string `json:"mission,omitempty"`
	// Action is the action this run launched, absent for a tool with no actions[].
	Action string `json:"action,omitempty"`
	// Detached is true when the run was started in a new terminal tab (--detach) and this
	// verb returned without waiting for it.
	Detached bool `json:"detached,omitempty"`
	// Ready is present only when the launched action declares live.ready: true once its probe
	// answered inside the window, false when the window closed first. It never changes Outcome
	// or the process exit code (§22 c).
	Ready *bool `json:"ready,omitempty"`
}

const launchUsage = "usage: formic launch <tool> [<action>] [--yes] [--detach] [--anyway] [--input name=value]... [--json]"

// launchArguments is `launch`'s parsed command line.
type launchArguments struct {
	tool, action string
	jsonOutput   bool
	confirmed    bool
	detach       bool
	anyway       bool
	inputPairs   []string
}

// parseLaunchArguments reports ok=false when the positional shape is wrong (no tool, or more
// than a tool and an action). --yes, --detach and --anyway are stripped before positional parsing so
// neither can be mistaken for an action name.
func parseLaunchArguments(arguments []string) (launchArguments, bool) {
	inputPairs, remaining := extractInputFlags(arguments)
	parsed := launchArguments{inputPairs: inputPairs}
	var positional []string
	for _, argument := range remaining {
		switch argument {
		case "--json":
			parsed.jsonOutput = true
		case "--yes":
			parsed.confirmed = true
		case "--detach":
			parsed.detach = true
		case "--anyway":
			parsed.anyway = true
		default:
			positional = append(positional, argument)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		return parsed, false
	}
	parsed.tool = positional[0]
	if len(positional) == 2 {
		parsed.action = positional[1]
	}
	return parsed, true
}

// actionRefusal is `launch`'s structured refusal for a missing, unknown or misplaced action
// and for an unconfirmed one (§23): never a guessed default.
type actionRefusal struct {
	Refused bool              `json:"refused"`
	Reason  string            `json:"reason"`
	Tool    string            `json:"tool,omitempty"`
	Action  string            `json:"action,omitempty"`
	Actions []describedChoice `json:"actions,omitempty"`
	Confirm string            `json:"confirm,omitempty"`
	Hint    string            `json:"hint,omitempty"`
}

type describedChoice struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func actionChoices(tool formats.ToolManifest) []describedChoice {
	choices := make([]describedChoice, 0, len(tool.Actions))
	for _, action := range tool.Actions {
		choices = append(choices, describedChoice{
			Name: action.Name, Title: dereference(action.Title),
			Description: dereference(action.Description), Confirm: dereference(action.Confirm),
		})
	}
	return choices
}

// refuseAction renders an actionRefusal: structured under --json, one line per action in text.
func refuseAction(jsonOutput bool, refused actionRefusal) int {
	refused.Refused = true
	if jsonOutput {
		printJSON(refused)
		return 1
	}
	if refused.Reason == "action required" {
		fmt.Fprintf(os.Stderr, "formic: %s declares actions; name one:\n", refused.Tool)
	} else {
		fmt.Fprintf(os.Stderr, "formic: %s\n", refused.Reason)
	}
	for _, choice := range refused.Actions {
		fmt.Fprintf(os.Stderr, "  %s  %s\n", choice.Name, choice.Title)
	}
	if refused.Confirm != "" {
		fmt.Fprintf(os.Stderr, "  %s\n", refused.Confirm)
	}
	if refused.Hint != "" {
		fmt.Fprintf(os.Stderr, "hint: %s\n", refused.Hint)
	}
	return 1
}

// checkActionShape is the CLI's own friendly refusal for an action that is missing, unknown or
// named on an action-less tool; Prepare enforces the same rules for every other caller. It
// returns refused=true with the exit code to use.
func checkActionShape(arguments launchArguments, tool discovery.Tool) (int, bool) {
	names := manifest.ActionNames(tool.Manifest)
	switch {
	case len(names) == 0 && arguments.action != "":
		return refuseAction(arguments.jsonOutput, actionRefusal{Reason: tool.Name + " declares no actions", Tool: tool.Name, Action: arguments.action}), true
	case len(names) > 0 && arguments.action == "":
		return refuseAction(arguments.jsonOutput, actionRefusal{Reason: "action required", Tool: tool.Name, Actions: actionChoices(tool.Manifest)}), true
	case arguments.action != "":
		if _, found := manifest.ActionSpec(tool.Manifest, arguments.action); !found {
			return refuseAction(arguments.jsonOutput, actionRefusal{Reason: "unknown action", Tool: tool.Name, Action: arguments.action, Actions: actionChoices(tool.Manifest)}), true
		}
	}
	return 0, false
}

// runLaunch is `formic launch <tool> [<action>]` (§15): runs the tool to completion, reusing
// the same launch/runs packages the deck uses, and reports the run id, outcome, exit code, and
// artifact. It refuses an interactive tool under --json with no terminal — a captured
// child's stdio would otherwise collide with the JSON result on stdout. Under --json the
// launched tool's own stdout and stderr are both redirected to this process's stderr, so
// stdout carries only the JSON summary (or refusal object); without --json, nothing changes.
func runLaunch(rawArguments []string) int {
	arguments, ok := parseLaunchArguments(rawArguments)
	if !ok {
		fmt.Fprintln(os.Stderr, launchUsage)
		return 2
	}
	jsonOutput := arguments.jsonOutput
	repositoryRoot, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	tool, found := discoveredCatalog.Find(arguments.tool)
	if !found {
		return refuse(jsonOutput, fmt.Sprintf("unknown tool %q", arguments.tool))
	}
	if exitCode, refused := checkActionShape(arguments, tool); refused {
		return exitCode
	}
	effective, err := manifest.ForAction(tool.Manifest, arguments.action)
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	baseEnvironment := os.Environ()
	terminal, isTerminal := launch.DetectTerminal(baseEnvironment)
	if jsonOutput && !isTerminal && manifest.RequiresRealTerminal(effective) {
		return refuse(jsonOutput, fmt.Sprintf("%s needs a real terminal, refused under --json with no terminal attached", tool.Name))
	}
	if arguments.detach {
		if !isTerminal {
			return refuse(jsonOutput, "no host terminal to detach into")
		}
		if err := launch.ProbeTerminal(terminal); err != nil {
			return refuse(jsonOutput, err.Error())
		}
	}
	services := launch.CatalogServiceChecker(context.Background(), discoveredCatalog, repositoryRoot)
	return launchPrepared(arguments, tool, effective, repositoryRoot, baseEnvironment, terminal, services)
}

// launchPrepared builds the Request (inputs, profile, store, live probe), runs Prepare, and
// hands the prepared run to the foreground or detached path.
func launchPrepared(arguments launchArguments, tool discovery.Tool, effective formats.ToolManifest, repositoryRoot string, baseEnvironment []string, terminal launch.Terminal, services equip.ServiceChecker) int {
	jsonOutput := arguments.jsonOutput
	// CLI-parsing-time input violations (a name given twice, or a sensitive value on the
	// command line) are refused before Prepare is ever called — a command line is the one
	// thing only this verb has to leak onto, never the deck.
	inputValues, inputsRefused := parseInputArguments(tool.Name, tool.Manifest.Inputs, arguments.inputPairs, baseEnvironment)
	if inputsRefused != nil {
		return refuseInputs(jsonOutput, inputsRefused)
	}
	repositoryProfile, profileProblem := profile.Load(repositoryRoot)
	if profileProblem != nil {
		return refuse(jsonOutput, profileProblem.Error())
	}
	store, err := runs.DefaultStore()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	settings := repositoryProfile.Tools[tool.Name]
	request := launch.Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Profile: repositoryProfile,
		Spec: settings.Spec, Inputs: inputValues, Store: store, Environment: baseEnvironment, StartedAt: time.Now(),
		Action: arguments.action, Confirmed: arguments.confirmed, Detach: arguments.detach,
		Services: services, Anyway: arguments.anyway,
	}
	// --anyway spawns past an answering probe, so the pre-launch probe cannot change the outcome.
	if tool.Manifest.Live != nil && !arguments.anyway {
		// Prepare never probes (the deck hands it its own result); the CLI has no UI thread to
		// protect, so it probes here, once, only for a tool that declares a live probe.
		probed := launch.Probe(context.Background(), tool, repositoryRoot)
		request.Live = &probed
	}
	mode := profile.ModeForeground
	if arguments.detach {
		mode = profile.ModeTab
	}
	// The needs-resolution step (§13) and the inputs-resolution step (§14 N10) both run INSIDE
	// launch.Prepare, so every caller — this verb and the deck — is refused the same way
	// before any run row is written or process started.
	prepared, err := launch.Prepare(request, mode)
	if err != nil {
		return refusePrepare(arguments, tool, err)
	}
	awaiting := readyWait{tool: tool, repositoryRoot: repositoryRoot, effective: effective}
	if arguments.detach {
		return startDetached(arguments, store, prepared, terminal, awaiting)
	}
	return runForeground(arguments, store, prepared, awaiting)
}

// readyWait is what a launch needs to honour live.ready: the tool whose probe to ask and the
// effective (one-action) manifest that declares the window.
type readyWait struct {
	tool           discovery.Tool
	repositoryRoot string
	effective      formats.ToolManifest
}

// await polls the probe through the ready window and reports whether it answered. window is
// zero when the launch declares none. Ctrl-C ends the wait early (reported as not ready); the
// wait never touches the run row or the exit code (§22 c).
func (waiting readyWait) await() (ready *bool, window time.Duration) {
	within, declared := launch.ReadyWindow(waiting.effective)
	if !declared {
		return nil, 0
	}
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	probed := waiting.tool
	probed.Manifest = waiting.effective
	_, answered := launch.WaitReady(interrupted, probed, waiting.repositoryRoot, within, nil)
	return &answered, within
}

// readySuffix is the text-mode tail for a launch that waited: " · ready" or " · not ready within 2m0s".
func readySuffix(ready *bool, window time.Duration) string {
	switch {
	case ready == nil:
		return ""
	case *ready:
		return " · ready"
	}
	return " · not ready within " + window.String()
}

// refusePrepare maps every typed Prepare refusal to its structured rendering.
func refusePrepare(arguments launchArguments, tool discovery.Tool, err error) int {
	jsonOutput := arguments.jsonOutput
	var needsRefused *launch.NeedsRefused
	var inputsRefused *launch.InputsRefused
	var confirmation *launch.ConfirmationRequired
	switch {
	case errors.Is(err, launch.ErrAlreadyRunning):
		if jsonOutput {
			printJSON(map[string]any{"launched": false, "outcome": "already-running", "tool": tool.Name, "action": arguments.action})
		} else {
			fmt.Printf("%s: already running; not launched\n", launchLabel(tool.Name, arguments.action))
		}
		return 0
	case errors.As(err, &needsRefused):
		return refuseNeeds(jsonOutput, needsRefused)
	case errors.As(err, &inputsRefused):
		return refuseInputs(jsonOutput, inputsRefused)
	case errors.As(err, &confirmation):
		return refuseAction(jsonOutput, actionRefusal{
			Reason: "confirmation required", Tool: tool.Name, Action: confirmation.Action,
			Confirm: confirmation.Sentence, Hint: "pass --yes to confirm",
		})
	}
	return refuse(jsonOutput, err.Error())
}

func launchLabel(tool, action string) string {
	if action == "" {
		return tool
	}
	return tool + " · " + action
}

func runForeground(arguments launchArguments, store runs.Store, prepared launch.Prepared, awaiting readyWait) int {
	jsonOutput := arguments.jsonOutput
	prepared.Command.Stdin, prepared.Command.Stdout, prepared.Command.Stderr = os.Stdin, childStdout(jsonOutput), os.Stderr
	commandError := prepared.Command.Run()
	row, err := launch.Finish(store, prepared.Row.ID, commandError, time.Now())
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	result := launchResult{RunID: row.ID, Outcome: row.Outcome, ExitCode: row.ExitCode, Artifact: row.Artifact, Action: arguments.action}
	var window time.Duration
	if row.ExitCode != nil && *row.ExitCode == 0 {
		result.Ready, window = awaiting.await()
	}
	suffix := readySuffix(result.Ready, window)
	switch {
	case jsonOutput:
		printJSON(result)
	case arguments.action != "":
		fmt.Printf("run %s: %s · %s: %s (exit code %s) %s%s\n", result.RunID, row.Tool, arguments.action, result.Outcome, formatExitCode(result.ExitCode), result.Artifact, suffix)
	default:
		fmt.Printf("run %s: %s (exit code %s) %s%s\n", result.RunID, result.Outcome, formatExitCode(result.ExitCode), result.Artifact, suffix)
	}
	if row.ExitCode != nil {
		return *row.ExitCode
	}
	return 0
}

// startDetached opens the prepared run in a new tab of the host terminal and returns at once.
// It never falls back to the foreground (that would block on a service); a failed spawn
// abandons the row it just wrote.
func startDetached(arguments launchArguments, store runs.Store, prepared launch.Prepared, terminal launch.Terminal, awaiting readyWait) int {
	jsonOutput := arguments.jsonOutput
	executable, err := os.Executable()
	if err != nil {
		return abandonDetached(jsonOutput, store, prepared, err)
	}
	command, err := launch.NewTabCommand(terminal, prepared.Row, []string{executable, "__replay-row", store.RowPath(prepared.Row.ID)})
	if err != nil {
		return abandonDetached(jsonOutput, store, prepared, err)
	}
	if err := command.Run(); err != nil {
		return abandonDetached(jsonOutput, store, prepared, err)
	}
	result := launchResult{RunID: prepared.Row.ID, Outcome: "running", Action: arguments.action, Detached: true}
	var window time.Duration
	if effectiveIsService(awaiting.effective) {
		result.Ready, window = awaiting.await()
	}
	if jsonOutput {
		printJSON(result)
	} else {
		fmt.Printf("run %s: %s started in a new %s tab%s\n", result.RunID, launchLabel(prepared.Row.Tool, arguments.action), terminal, readySuffix(result.Ready, window))
	}
	return 0
}

// effectiveIsService reports a lifecycle: service projection: only a service's spawn is followed
// by a ready wait, since a detached task's exit is not observed here.
func effectiveIsService(effective formats.ToolManifest) bool {
	return effective.Lifecycle != nil && *effective.Lifecycle == formats.ToolManifestLifecycleService
}

func abandonDetached(jsonOutput bool, store runs.Store, prepared launch.Prepared, cause error) int {
	_, _ = launch.Abandon(store, prepared.Row.ID, "detach failed: "+cause.Error(), time.Now())
	return refuse(jsonOutput, "could not start a terminal tab: "+cause.Error())
}

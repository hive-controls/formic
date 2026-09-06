package ui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/launch"
	"github.com/hive-controls/hivedeck/internal/manifest"
	"github.com/hive-controls/hivedeck/internal/profile"
)

type foregroundFinishedMsg struct {
	runID          string
	commandError   error
	tabFallback    bool
	stopSignalTrap func()
}

type tabStartedMsg struct {
	runID    string
	terminal launch.Terminal
	err      error
}

type artifactOpenedMsg struct {
	artifact string
	err      error
}

func (model Model) beginLaunch() (tea.Model, tea.Cmd) {
	tool, ok := model.currentTool()
	if !ok || tool.Problem != "" {
		return model, nil
	}
	if model.profileProblem != "" {
		model.notice = "Fix profile problems before launching: " + model.profileProblem
		return model, nil
	}
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, toolInteractive(tool))
	settings := model.repositoryProfile.Tools[tool.Name]
	if manifest.RequiresSpec(tool.Manifest) && settings.Spec == "" {
		return model.beginSpecPrompt(tool)
	}
	return model.startLaunch(tool, settings.Spec, mode)
}

func (model Model) beginSpecPrompt(tool discovery.Tool) (tea.Model, tea.Cmd) {
	model.promptingSpec = true
	model.pendingTool = tool
	model.specInput = textInput("Spec: ", "path to the input this tool reads")
	model.specInput.Focus()
	model.notice = ""
	return model, textinput.Blink
}

func (model Model) updateSpecKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		model.promptingSpec = false
		return model, nil
	}
	if key.String() == "enter" {
		spec := strings.TrimSpace(model.specInput.Value())
		if spec == "" {
			model.notice = "A spec path is required."
			return model, nil
		}
		model.promptingSpec = false
		mode := profile.ResolveMode(model.repositoryProfile, model.pendingTool.Name, toolInteractive(model.pendingTool))
		return model.startLaunch(model.pendingTool, spec, mode)
	}
	updated, command := model.specInput.Update(key)
	model.specInput = updated
	return model, command
}

func (model Model) startLaunch(tool discovery.Tool, spec string, mode profile.LaunchMode) (tea.Model, tea.Cmd) {
	terminal, tabAvailable := launch.DetectTerminal(model.environment)
	tabFallback := mode == profile.ModeTab && (!tabAvailable || launch.ProbeTerminal(terminal) != nil)
	if tabFallback {
		mode = profile.ModeForeground
		model.notice = "New tab unavailable here — running in the foreground."
	}
	prepared, err := launch.Prepare(model.launchRequest(tool, spec), mode)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	if mode == profile.ModePane {
		return model.startPane(prepared, tool.Title)
	}
	if mode == profile.ModeTab {
		return model.startTab(prepared, terminal)
	}
	// The trap is armed here and stopped only in handleForegroundFinished, AFTER Finish has
	// persisted the row — not merely once the child exits or once this Run call returns.
	// Bubble Tea's own terminal-restore and async message dispatch run in between; the trap
	// must stay live through all of it so a second signal in that window cannot kill this
	// process before the run's outcome is ever recorded.
	signals, stop := launch.ArmSignalTrap()
	callback := func(commandError error) tea.Msg {
		return foregroundFinishedMsg{runID: prepared.Row.ID, commandError: commandError, tabFallback: tabFallback, stopSignalTrap: stop}
	}
	command := &launch.SignalForwardingCommand{Cmd: prepared.Command, Signals: signals}
	return model, tea.Exec(command, callback)
}

func (model Model) launchRequest(tool discovery.Tool, spec string) launch.Request {
	return launch.Request{
		Tool: tool, RepositoryRoot: model.repositoryRoot, Profile: model.repositoryProfile,
		Spec: spec, Store: model.runStore, Environment: model.environment, StartedAt: model.clock(),
	}
}

func (model Model) startTab(prepared launch.Prepared, terminal launch.Terminal) (tea.Model, tea.Cmd) {
	wrapperArgv := []string{model.executable, "run", model.runStore.RowPath(prepared.Row.ID)}
	command, err := launch.NewTabCommand(terminal, prepared.Row, wrapperArgv)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	return model, func() tea.Msg {
		return tabStartedMsg{runID: prepared.Row.ID, terminal: terminal, err: command.Run()}
	}
}

func (model Model) handleForegroundFinished(message foregroundFinishedMsg) Model {
	row, err := launch.Finish(model.runStore, message.runID, message.commandError, model.clock())
	// Stop the trap only AFTER Finish has persisted the row — never before — so a signal
	// arriving in the handback window (child already exited, row not yet saved) still finds
	// the trap armed instead of hitting the default disposition.
	if message.stopSignalTrap != nil {
		message.stopSignalTrap()
	}
	if err != nil {
		model.notice = err.Error()
		return model
	}
	model.refreshRuns()
	model.showRuns, model.detail = true, false
	prefix := ""
	if message.tabFallback {
		prefix = "New tab unavailable; foreground "
	}
	model.notice = fmt.Sprintf("%srun finished: exit %d · %s", prefix, *row.ExitCode, row.Outcome)
	return model
}

func (model Model) handleTabStarted(message tabStartedMsg) Model {
	if message.err != nil {
		_, _ = launch.Finish(model.runStore, message.runID, message.err, model.clock())
		model.notice = fmt.Sprintf("Could not open %s tab: %v", message.terminal, message.err)
	} else {
		model.notice = fmt.Sprintf("Run opened in %s.", message.terminal)
	}
	model.refreshRuns()
	model.showRuns, model.detail = true, false
	return model
}

func (model *Model) refreshRuns() {
	rows, err := model.runStore.List()
	if err != nil {
		model.notice = err.Error()
		return
	}
	model.runRows = rows
	model.runCursor = clampCursor(model.runCursor, len(rows))
}

func (model Model) openRunList() (tea.Model, tea.Cmd) {
	model.refreshRuns()
	model.showRuns, model.detail, model.showPanes = true, false, false
	// The run list is live while it is open: a tab run's wrapper is updating its row from
	// another process, and this is what makes that visible without a keypress.
	return model.ensureTicking()
}

func (model Model) updateRunKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q":
		return model, tea.Quit
	case "esc", "r", "left", "backspace":
		model.showRuns = false
	case "up":
		model.runCursor = wrapCursor(model.runCursor-1, len(model.runRows))
	case "down":
		model.runCursor = wrapCursor(model.runCursor+1, len(model.runRows))
	case "enter":
		return model.openSelectedArtifact()
	}
	return model, nil
}

func (model Model) openSelectedArtifact() (tea.Model, tea.Cmd) {
	if len(model.runRows) == 0 {
		return model, nil
	}
	row := model.runRows[clampCursor(model.runCursor, len(model.runRows))]
	// row.Artifact is swapped for the resolved evidence directory (toolspec v1.1
	// outputs.evidenceDir) when the run's own status stream reported no artifact — reusing
	// ArtifactCommand's existing path/URL handling rather than duplicating it.
	row.Artifact = model.runArtifact(row)
	command, err := launch.ArtifactCommand(row, model.goos)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	return model, runCommand(command, row.Artifact)
}

func runCommand(command *exec.Cmd, artifact string) tea.Cmd {
	return func() tea.Msg {
		return artifactOpenedMsg{artifact: artifact, err: command.Run()}
	}
}

func (model Model) handleArtifactOpened(message artifactOpenedMsg) Model {
	if message.err != nil {
		model.notice = fmt.Sprintf("Could not open artifact: %v", message.err)
	} else {
		model.notice = "Opened " + message.artifact
	}
	return model
}

func textInput(prompt, placeholder string) textinput.Model {
	input := textinput.New()
	input.Prompt, input.Placeholder = prompt, placeholder
	return input
}

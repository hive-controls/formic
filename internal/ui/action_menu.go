package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

const actionRunningNotice = "an action is running — wait for it, or ctrl+c to leave it running"

// actionResult is the menu's last-result block: one line, then the tail of the captured output.
type actionResult struct {
	line string
	tail []string
}

func toolHasActions(tool discovery.Tool) bool { return len(tool.Manifest.Actions) > 0 }

func actionTitle(action formats.Action) string {
	if action.Title != nil && *action.Title != "" {
		return *action.Title
	}
	return action.Name
}

// highlightedAction is the action under the menu cursor.
func (model Model) highlightedAction(tool discovery.Tool) (formats.Action, bool) {
	actions := tool.Manifest.Actions
	if len(actions) == 0 {
		return formats.Action{}, false
	}
	return actions[clampCursor(model.actionCursor, len(actions))], true
}

func (model Model) highlightedActionName(tool discovery.Tool) string {
	action, _ := model.highlightedAction(tool)
	return action.Name
}

func (model Model) inFlightAction(tool discovery.Tool) formats.Action {
	action, _ := manifest.ActionSpec(tool.Manifest, model.actionInFlight)
	return action
}

func (model Model) actionIsService(tool discovery.Tool) bool {
	action := model.inFlightAction(tool)
	return action.Lifecycle != nil && *action.Lifecycle == formats.ActionLifecycleService
}

// actionInteractive reports whether the named action needs a real terminal, through the same
// projection a launch of it sees.
func (model Model) actionInteractive(tool discovery.Tool, name string) bool {
	effective, err := manifest.ForAction(tool.Manifest, name)
	return err == nil && manifest.RequiresRealTerminal(effective)
}

// exitMeaningFor looks a code up in the action's merged exit table, falling back to the tool's
// own when the action cannot be projected (an action since removed from the manifest).
func exitMeaningFor(tool discovery.Tool, action string, code int) string {
	table := tool.Manifest.Exit
	if action != "" {
		if effective, err := manifest.ForAction(tool.Manifest, action); err == nil {
			table = effective.Exit
		}
	}
	text, _ := table[strconv.Itoa(code)].(string)
	return text
}

// actionResultLine is `<title>: exit <n> — <meaning>`; with no declared meaning the run's
// outcome word stands in.
func (model Model) actionResultLine(tool discovery.Tool, action string, exitCode *int, outcome string) string {
	spec, found := manifest.ActionSpec(tool.Manifest, action)
	title := action
	if found {
		title = actionTitle(spec)
	}
	if exitCode == nil {
		return fmt.Sprintf("%s: %s", title, outcome)
	}
	meaning := exitMeaningFor(tool, action, *exitCode)
	if meaning == "" {
		meaning = outcome
	}
	return fmt.Sprintf("%s: exit %d — %s", title, *exitCode, meaning)
}

func (model Model) updateActionMenuKey(key tea.KeyMsg, tool discovery.Tool) (tea.Model, tea.Cmd) {
	count := len(tool.Manifest.Actions)
	switch key.String() {
	case "up":
		model.actionCursor = wrapCursor(model.actionCursor-1, count)
	case "down":
		model.actionCursor = wrapCursor(model.actionCursor+1, count)
	case "enter":
		return model.beginActionLaunch(tool, model.highlightedActionName(tool))
	case "c":
		return model.beginConfigure()
	case "esc", "left", "backspace":
		model.detail = false
		model.actionCursor = 0
		model.actionResult = actionResult{}
	}
	return model, nil
}

// beginActionLaunch starts one action. A confirming action asks first, before any equip or
// input prompt, and launch.Prepare enforces the same Confirmed flag for every caller.
func (model Model) beginActionLaunch(tool discovery.Tool, name string) (tea.Model, tea.Cmd) {
	if tool.Problem != "" {
		return model, nil
	}
	if model.profileProblem != "" {
		model.notice = "Fix profile problems before launching: " + model.profileProblem
		return model, nil
	}
	if model.capturedRunID != "" {
		model.notice = "an action is already running — wait for it to finish"
		return model, nil
	}
	model.missionInFlight, model.missionEvidenceRoot, model.noticeProblems = nil, "", nil
	model.clearReadyOutcome(tool.Name)
	model.actionInFlight, model.actionConfirmed, model.actionResult = name, false, actionResult{}
	if action, _ := manifest.ActionSpec(tool.Manifest, name); action.Confirm != nil && *action.Confirm != "" {
		model.confirmingAction = true
		model.notice = ""
		return model, nil
	}
	return model.continueActionLaunch(tool)
}

func (model Model) continueActionLaunch(tool discovery.Tool) (tea.Model, tea.Cmd) {
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, model.actionInteractive(tool, model.actionInFlight))
	return model.beginEquipCheck(tool, mode, model.repositoryProfile.Tools[tool.Name].Spec)
}

func (model Model) updateConfirmKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	tool, ok := model.currentTool()
	model.confirmingAction = false
	if !ok {
		return model, nil
	}
	if key.String() == "y" || key.String() == "Y" {
		model.actionConfirmed = true
		return model.continueActionLaunch(tool)
	}
	model.actionResult = actionResult{line: actionTitle(model.inFlightAction(tool)) + ": cancelled"}
	model.actionInFlight = ""
	return model, nil
}

// quitRefused reports whether `q` must be refused: a captured action is running and the key is
// not being typed into a prompt or the list filter.
func (model Model) quitRefused() bool {
	if model.capturedRunID == "" {
		return false
	}
	if model.configuring || model.promptingEquip || model.promptingInputs || model.promptingSpec {
		return false
	}
	onList := !model.detail && !model.showRuns && !model.showPanes && !model.showMissions && !model.showEquipView
	return !(onList && (model.filter.Value() != "" || model.searching))
}

// applyConfiguredArguments stores the customize screen's argument rows: tool-level for an
// action-less manifest, per named action otherwise.
func applyConfiguredArguments(settings profile.Tool, action string, args []string) profile.Tool {
	if action == "" {
		settings.Args = args
		return settings
	}
	return settings.WithActionArgs(action, args)
}

func configureTitle(tool discovery.Tool, action string) string {
	if action == "" {
		return "Configure " + tool.Title
	}
	return "Configure " + tool.Name + " · " + action
}

// actionMenuView is the detail view of a tool that declares actions.
func (model Model) actionMenuView(tool discovery.Tool) string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate(tool.Title, width)) + "\n")
	content.WriteString(mutedStyle.Render(truncate(tool.Name+" · "+string(tool.Source.Kind), width)) + "\n\n")
	var body strings.Builder
	if tool.Description != "" {
		body.WriteString(wrap(tool.Description, width) + "\n\n")
	}
	stateWord := liveStateLabel(model, tool.Name)
	body.WriteString(field("State: ", liveStateStyle(stateWord).Render(stateWord)))
	selected := clampCursor(model.actionCursor, len(tool.Manifest.Actions))
	for index, action := range tool.Manifest.Actions {
		body.WriteString(actionLine(action, index == selected, width) + "\n")
	}
	model.appendActionPreview(&body, tool, width)
	model.appendActionResult(&body, tool, width)
	capacity := listCapacity(model.layoutHeight(), 5+model.noticeLineCount())
	lines := clipLines(strings.Split(strings.TrimRight(body.String(), "\n"), "\n"), capacity)
	content.WriteString(strings.Join(lines, "\n") + "\n\n")
	model.renderNotice(&content, width)
	content.WriteString(mutedStyle.Render(truncate(model.helpText(), width)))
	return content.String()
}

func actionLine(action formats.Action, selected bool, width int) string {
	text := actionTitle(action)
	if action.Confirm != nil && *action.Confirm != "" {
		text += " (asks first)"
	}
	if action.Description != nil && *action.Description != "" {
		text += " — " + *action.Description
	}
	if selected {
		return selectedStyle.Render(truncate("> "+text, width))
	}
	return "  " + truncate(text, width-2)
}

func (model Model) appendActionPreview(body *strings.Builder, tool discovery.Tool, width int) {
	action, ok := model.highlightedAction(tool)
	if !ok {
		return
	}
	body.WriteString("\n")
	effective, err := manifest.ForAction(tool.Manifest, action.Name)
	if err != nil {
		body.WriteString(problemStyle.Render(wrap("Action: "+err.Error(), width)) + "\n")
		return
	}
	projected := tool
	projected.Manifest = effective
	appendLaunch(body, projected, model.repositoryRoot, model.repositoryProfile.Tools[tool.Name].ArgsFor(action.Name), width)
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, manifest.RequiresRealTerminal(effective))
	body.WriteString(field("Launch mode: ", string(mode)))
}

func (model Model) appendActionResult(body *strings.Builder, tool discovery.Tool, width int) {
	switch {
	case model.confirmingAction:
		body.WriteString("\n" + collisionStyle.Render(wrap(*model.inFlightAction(tool).Confirm, width)) + "\n")
		body.WriteString("[y/N]\n")
	case model.capturedRunID != "":
		body.WriteString("\n" + model.capturedRunningLine(tool) + "\n")
	case model.actionResult.line != "":
		body.WriteString("\n" + truncate(model.actionResult.line+model.readySuffix(tool.Name), width) + "\n")
		for _, line := range model.actionResult.tail {
			body.WriteString(mutedStyle.Render(truncate("  "+line, width)) + "\n")
		}
	}
}

// finishedAction is the action a finished foreground run belongs to: the one the deck carried
// through the launch, else the one the run row recorded.
func finishedAction(carried string, row runs.Row) string {
	if carried != "" {
		return carried
	}
	return row.Action
}

// finishActionRun returns an interactive action's finished run to the menu, with a result line
// in place of the run list an action-less foreground run lands on.
func (model Model) finishActionRun(row runs.Row, action string) Model {
	model.refreshRuns()
	model.showRuns, model.detail = false, true
	model.notice = ""
	tool, _ := model.toolByName(row.Tool)
	model.actionResult = actionResult{line: model.actionResultLine(tool, action, row.ExitCode, row.Outcome)}
	model.actionInFlight = ""
	return model
}

// finishActionTab keeps a tab- or split-routed action in the menu once its terminal opened.
func (model Model) finishActionTab(message tabStartedMsg) Model {
	model.refreshRuns()
	model.showRuns, model.detail = false, true
	model.notice = ""
	title := message.action
	if spec, found := manifest.ActionSpec(message.tool.Manifest, message.action); found {
		title = actionTitle(spec)
	}
	model.actionResult = actionResult{line: fmt.Sprintf("%s: opened in %s", title, message.terminal)}
	model.actionInFlight = ""
	return model
}

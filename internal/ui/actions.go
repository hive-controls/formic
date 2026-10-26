package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
)

type foregroundFinishedMsg struct {
	runID          string
	action         string
	commandError   error
	tabFallback    bool
	stopSignalTrap func()
}

type tabStartedMsg struct {
	runID    string
	action   string
	terminal launch.Terminal
	// tool, spec, and inputs are what the tab route was launching, carried on the completion
	// message so a spawn failure (the terminal client itself, not the tool) can retry the SAME
	// tool in the foreground without the caller having to remember its own request across the
	// async gap.
	tool   discovery.Tool
	spec   string
	inputs map[string]string
	err    error
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
	// An ordinary tool launch never carries mission context, nor a stale refusal panel from a
	// previous mission attempt — cleared here so neither can leak into this one (E2b).
	model.missionInFlight = nil
	model.missionEvidenceRoot = ""
	model.noticeProblems = nil
	model.actionInFlight, model.actionConfirmed = "", false
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, toolInteractive(tool))
	settings := model.repositoryProfile.Tools[tool.Name]
	return model.beginEquipCheck(tool, mode, settings.Spec)
}

// beginMissionLaunch starts E2b's mission launch: resolves the mission's step tool from the
// catalog, then routes through the SAME equip check every tool launch uses (C1) — mirroring the
// CLI's own order (equip resolves before pre-flight so pre-flight sees the equipped state),
// since the CLI has no equip-prompt step at all and pre-flight alone would otherwise refuse on
// any unmet need the deck could have prompted for.
func (model Model) beginMissionLaunch(m mission.Mission) (tea.Model, tea.Cmd) {
	if model.profileProblem != "" {
		model.notice = "Fix profile problems before launching: " + model.profileProblem
		return model, nil
	}
	step := m.Definition.Steps[0]
	tool, found := model.catalog.Find(step.Tool)
	if !found {
		model.notice = fmt.Sprintf("mission %s: tool %q not found in the catalog", m.Definition.Name, step.Tool)
		return model, nil
	}
	model.missionInFlight = &m
	model.actionInFlight, model.actionConfirmed = "", false
	model.missionEvidenceRoot = ""
	model.noticeProblems = nil
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, toolInteractive(tool))
	return model.beginEquipCheck(tool, mode, "")
}

// beginEquipCheck resolves the tool's needs BEFORE anything else in the launch path (C1,
// ruling 2, §13 "a resolution step before every launch"): an unmet need that can never be
// prompted for (kind runtime/network) refuses the launch outright, showing its own status
// reason; every unmet promptable need (secret/gate) starts the equip prompt. A need already
// met is never entered into that prompt at all — "ask once."
func (model Model) beginEquipCheck(tool discovery.Tool, mode profile.LaunchMode, spec string) (tea.Model, tea.Cmd) {
	statuses, err := launch.ResolveNeedStatuses(tool, model.environment)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	if blocking := firstUnpromptableUnmetNeed(statuses); blocking != nil {
		model.notice = blocking.Reason
		return model, nil
	}
	unmet := unmetPromptableNeeds(statuses)
	if len(unmet) == 0 {
		return model.afterEquipResolved(tool, mode, spec)
	}
	return model.beginEquipPrompt(tool, mode, spec, unmet)
}

// afterEquipResolved continues the launch path beginEquipCheck interrupted (or never needed to
// interrupt): N10's generalized sequential input prompt for a tool that declares inputs[], the
// legacy {spec} sugar prompt, or straight through to startLaunch — exactly what beginLaunch did
// before C1 existed.
func (model Model) afterEquipResolved(tool discovery.Tool, mode profile.LaunchMode, spec string) (tea.Model, tea.Cmd) {
	// A mission launch (E2b) never uses the tool's own spec/input prompts here — its inputs are
	// the mission's declared inputs[], collected by beginMissionInputPrompt, then bound through
	// mission.Preflight instead of Prepare's own input resolution.
	if model.missionInFlight != nil {
		return model.beginMissionInputPrompt(tool, mode)
	}
	if len(tool.Manifest.Inputs) > 0 {
		return model.beginInputPrompt(tool, mode, spec)
	}
	if manifest.RequiresSpec(tool.Manifest) && spec == "" {
		return model.beginSpecPrompt(tool)
	}
	return model.startLaunch(tool, spec, mode, nil)
}

// unmetPromptableNeeds returns every unmet need C1 can prompt for (secret or gate), in
// resolution order — a met need is filtered out here, which is what makes "ask once" hold.
func unmetPromptableNeeds(statuses []equip.NeedStatus) []equip.NeedStatus {
	var promptable []equip.NeedStatus
	for _, status := range statuses {
		if !status.Blocks() {
			continue
		}
		if _, ok := equip.SecretDetails(status.Need); ok {
			promptable = append(promptable, status)
			continue
		}
		if _, _, _, ok := equip.GateDetails(status.Need); ok {
			promptable = append(promptable, status)
		}
	}
	return promptable
}

// firstUnpromptableUnmetNeed returns the first unmet need C1 cannot prompt for (runtime or
// network — neither has an answer a person can type or choose here), or nil when every unmet
// need is promptable.
func firstUnpromptableUnmetNeed(statuses []equip.NeedStatus) *equip.NeedStatus {
	for index := range statuses {
		status := statuses[index]
		if !status.Blocks() {
			continue
		}
		if _, ok := equip.SecretDetails(status.Need); ok {
			continue
		}
		if _, _, _, ok := equip.GateDetails(status.Need); ok {
			continue
		}
		return &status
	}
	return nil
}

// beginEquipPrompt starts C1's launch-time equip prompt over needs (every unmet, promptable
// need, in order). Esc at any step cancels the whole launch (updateEquipKey), never partially
// applying what was recorded so far.
func (model Model) beginEquipPrompt(tool discovery.Tool, mode profile.LaunchMode, spec string, needs []equip.NeedStatus) (tea.Model, tea.Cmd) {
	model.promptingEquip = true
	model.pendingTool = tool
	model.pendingMode = mode
	model.pendingSpec = spec
	model.equipNeeds = needs
	model.equipStep = 0
	model.notice = ""
	model.focusCurrentEquipStep()
	return model, textinput.Blink
}

// focusCurrentEquipStep builds the input for the current equip step: a text field pre-filled
// with env.NAME for a secret need, or nothing (the gate branch renders a cursor list directly
// off model.equipNeeds/equipProviderCursor — there is no textinput.Model for it).
func (model *Model) focusCurrentEquipStep() {
	model.equipProviderCursor = 0
	status := model.equipNeeds[model.equipStep]
	name, ok := equip.SecretDetails(status.Need)
	if !ok {
		return
	}
	field := textinput.New()
	field.Prompt = "Reference for " + name + ": "
	field.SetValue("env." + name)
	field.CursorEnd()
	field.Focus()
	model.equipField = field
}

// equipPath resolves the machine's equip file path the same way launch.ResolveNeedStatuses
// does internally, rather than depending on WithCatalogContext's userConfigRoot having been
// set — a Model built directly by a test takes this path too.
func equipPath() (string, error) {
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return "", err
	}
	return equip.DefaultPath(userConfigRoot), nil
}

func (model Model) updateEquipKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		model.promptingEquip = false
		model.equipNeeds = nil
		model.missionInFlight = nil
		return model, nil
	}
	status := model.equipNeeds[model.equipStep]
	if name, ok := equip.SecretDetails(status.Need); ok {
		return model.updateEquipSecretKey(key, name)
	}
	return model.updateEquipGateKey(key, status)
}

// updateEquipSecretKey records name's reference (§13, C1): a literal is refused with the exact
// text `formic equip secret` itself refuses with (equip.ErrLiteralValue), and the prompt stays
// open so it can be answered again.
func (model Model) updateEquipSecretKey(key tea.KeyMsg, name string) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		reference := strings.TrimSpace(model.equipField.Value())
		path, err := equipPath()
		if err != nil {
			model.notice = err.Error()
			return model, nil
		}
		if _, err := equip.SetSecret(path, name, reference); err != nil {
			if errors.Is(err, equip.ErrLiteralValue) {
				model.notice = "refused: " + equip.ErrLiteralValue.Error()
			} else {
				model.notice = err.Error()
			}
			return model, nil
		}
		model.notice = ""
		return model.advanceEquipStep()
	}
	updated, command := model.equipField.Update(key)
	model.equipField = updated
	return model, command
}

// updateEquipGateKey drives the gate branch's cursor list, populated from the need's OWN
// declared providers — the list never offers a choice `formic equip provider` would refuse.
func (model Model) updateEquipGateKey(key tea.KeyMsg, status equip.NeedStatus) (tea.Model, tea.Cmd) {
	class, _, providers, ok := equip.GateDetails(status.Need)
	if !ok || len(providers) == 0 {
		model.notice = "no providers listed for this need"
		return model, nil
	}
	switch key.String() {
	case "up":
		model.equipProviderCursor = wrapCursor(model.equipProviderCursor-1, len(providers))
	case "down":
		model.equipProviderCursor = wrapCursor(model.equipProviderCursor+1, len(providers))
	case "enter":
		provider := providers[clampCursor(model.equipProviderCursor, len(providers))]
		path, err := equipPath()
		if err != nil {
			model.notice = err.Error()
			return model, nil
		}
		if _, err := equip.SetProvider(path, "gate."+class, provider); err != nil {
			model.notice = err.Error()
			return model, nil
		}
		model.notice = ""
		return model.advanceEquipStep()
	}
	return model, nil
}

func (model Model) advanceEquipStep() (tea.Model, tea.Cmd) {
	model.equipStep++
	if model.equipStep >= len(model.equipNeeds) {
		return model.finishEquipPrompt()
	}
	model.focusCurrentEquipStep()
	return model, textinput.Blink
}

// finishEquipPrompt re-resolves every one of the tool's needs against what was just recorded —
// not only the previously-unmet ones, since a gate need's own resolution also checks the
// manifest's env[] declaration. Still unmet (a recorded secret reference whose environment
// variable is not actually set, for instance) refuses with that need's own reason; otherwise
// the launch continues exactly where beginEquipCheck would have taken it had every need
// already been met.
func (model Model) finishEquipPrompt() (tea.Model, tea.Cmd) {
	model.promptingEquip = false
	tool, mode, spec := model.pendingTool, model.pendingMode, model.pendingSpec
	model.equipNeeds = nil
	statuses, err := launch.ResolveNeedStatuses(tool, model.environment)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	for _, status := range statuses {
		if status.Blocks() {
			model.notice = status.Reason
			return model, nil
		}
	}
	return model.afterEquipResolved(tool, mode, spec)
}

// openEquipView opens C2's read-only equip view (key `e`): every need across the whole armory,
// grouped by identity, computed once here via the same equip.GroupNeedStatuses computation
// `formic equip`'s own no-argument form shares.
func (model Model) openEquipView() (tea.Model, tea.Cmd) {
	path, err := equipPath()
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	equipFile, err := equip.Load(path)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	model.equipViewNeeds = equip.GroupNeedStatuses(model.catalog.Tools, equipFile, model.environment)
	model.showEquipView = true
	model.detail, model.showRuns, model.showPanes, model.showMissions = false, false, false, false
	return model, nil
}

// updateEquipViewKey is C2's "no editing keys" contract: esc/left/backspace close the view,
// nothing else does anything — it is read-only, never writes to the equip file.
func (model Model) updateEquipViewKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q":
		return model, tea.Quit
	case "esc", "left", "backspace":
		model.showEquipView = false
	}
	return model, nil
}

// beginInputPrompt starts the sequential input prompt (N10) over tool's declared inputs[],
// required first then optional (orderedInputs). Esc at any step cancels the whole launch
// (updateInputKey), never partially applying collected answers.
func (model Model) beginInputPrompt(tool discovery.Tool, mode profile.LaunchMode, spec string) (tea.Model, tea.Cmd) {
	return model.beginInputPromptWithSchema(tool, mode, spec, orderedInputs(tool.Manifest.Inputs))
}

// beginInputPromptWithSchema is beginInputPrompt's schema-parametrized core, shared with E2b's
// mission input prompt (beginMissionInputPrompt), which walks the mission's own declared
// inputs[] rather than the tool's full inputs[].
func (model Model) beginInputPromptWithSchema(tool discovery.Tool, mode profile.LaunchMode, spec string, schema []formats.Input) (tea.Model, tea.Cmd) {
	model.promptingInputs = true
	model.pendingTool = tool
	model.pendingMode = mode
	model.pendingSpec = spec
	model.inputSchema = schema
	model.inputAnswers = make(map[string]string, len(model.inputSchema))
	model.inputStep = 0
	model.notice = ""
	model.focusCurrentInputField()
	return model, textinput.Blink
}

// orderedInputs returns declared in the order the sequential prompt walks it: every required
// input first (in declared order), then every optional one (in declared order) — §N10's "required
// first, then optional."
func orderedInputs(declared []formats.Input) []formats.Input {
	ordered := make([]formats.Input, 0, len(declared))
	for _, input := range declared {
		if input.Required {
			ordered = append(ordered, input)
		}
	}
	for _, input := range declared {
		if !input.Required {
			ordered = append(ordered, input)
		}
	}
	return ordered
}

// focusCurrentInputField builds the textinput.Model for the current step: label from the
// manifest (`label`, else the input's name), masked echo for a `sensitive: true` input so the
// typed value is never shown on screen — the value itself is still kept, only in
// model.inputAnswers, never written to the profile or any file.
func (model *Model) focusCurrentInputField() {
	entry := model.inputSchema[model.inputStep]
	field := textinput.New()
	field.Prompt = inputPromptLabel(entry) + ": "
	if entry.Sensitive {
		field.EchoMode = textinput.EchoPassword
	}
	field.Focus()
	model.inputField = field
}

func inputPromptLabel(input formats.Input) string {
	if input.Label != nil && *input.Label != "" {
		return *input.Label
	}
	return input.Name
}

func (model Model) updateInputKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		model.promptingInputs = false
		model.inputAnswers = nil
		model.missionInFlight = nil
		return model, nil
	}
	if key.String() == "enter" {
		entry := model.inputSchema[model.inputStep]
		value := strings.TrimSpace(model.inputField.Value())
		if value == "" && entry.Required {
			model.notice = fmt.Sprintf("%s is required.", inputPromptLabel(entry))
			return model, nil
		}
		// A blank OPTIONAL input is left absent — the same "blank optional = absent" contract
		// resolveInputsForLaunch already applies to a value that was never offered at all.
		if value != "" {
			model.inputAnswers[entry.Name] = value
		}
		model.inputStep++
		if model.inputStep >= len(model.inputSchema) {
			return model.finishInputPrompt()
		}
		model.focusCurrentInputField()
		return model, textinput.Blink
	}
	updated, command := model.inputField.Update(key)
	model.inputField = updated
	return model, command
}

// finishInputPrompt resumes the launch the prompt interrupted. A mission launch (E2b) hands its
// answers to mission.Preflight instead of going straight to startLaunch — the tool's own inputs
// still need binding, substitution, and evidence-root resolution first.
func (model Model) finishInputPrompt() (tea.Model, tea.Cmd) {
	model.promptingInputs = false
	answers := model.inputAnswers
	model.inputAnswers = nil
	if model.missionInFlight != nil {
		return model.finishMissionInputPrompt(answers)
	}
	return model.startLaunch(model.pendingTool, model.pendingSpec, model.pendingMode, answers)
}

// beginMissionInputPrompt starts the sequential prompt over the mission's own declared inputs[]
// (E2b, founder ruling 4 — "mission inputs use the same prompt component"). A mission that
// declares no inputs skips straight to pre-flight with no answers to collect.
func (model Model) beginMissionInputPrompt(tool discovery.Tool, mode profile.LaunchMode) (tea.Model, tea.Cmd) {
	schema := missionInputSchema(model.missionInFlight.Definition.Inputs, tool.Manifest.Inputs)
	if len(schema) == 0 {
		return model.finishMissionInputPrompt(nil)
	}
	return model.beginInputPromptWithSchema(tool, mode, "", schema)
}

// missionInputSchema builds one formats.Input per mission-level input NAME (mission.Preflight
// refuses the launch if any of these is left unbound, so every one is effectively required here)
// — sensitive-ness, label, and kind come from the step tool's OWN declared input of the same
// name when one exists (brief: "sensitive-ness comes from the TOOL's declared input"); a mission
// input with no matching declared tool input still prompts, as a bare required string.
func missionInputSchema(names []string, declared []formats.Input) []formats.Input {
	declaredByName := make(map[string]formats.Input, len(declared))
	for _, input := range declared {
		declaredByName[input.Name] = input
	}
	schema := make([]formats.Input, 0, len(names))
	for _, name := range names {
		if input, ok := declaredByName[name]; ok {
			input.Required = true
			schema = append(schema, input)
			continue
		}
		schema = append(schema, formats.Input{Name: name, Required: true})
	}
	return schema
}

// finishMissionInputPrompt runs mission.Preflight over answers (E2b, ruling 3): a refusal
// renders EVERY problem in the multi-line notice panel (appendProblemNotice); success hands the
// resolved plan's tool and merged inputs to the SAME mode resolution every tool launch uses
// (startLaunch), with Request.Mission/MissionPath/EvidenceRoot populated by launchRequest from
// model.missionInFlight/missionEvidenceRoot, set here from plan.EvidenceRoot.
func (model Model) finishMissionInputPrompt(answers map[string]string) (tea.Model, tea.Cmd) {
	m := *model.missionInFlight
	workingDirectory, err := os.Getwd()
	if err != nil {
		model.notice = err.Error()
		model.missionInFlight = nil
		return model, nil
	}
	plan, refused := mission.Preflight(m, model.catalog, workingDirectory, missionValues(answers), model.environment, false)
	if refused != nil {
		model.missionInFlight = nil
		model.notice = ""
		model.noticeProblems = renderProblems(refused.Problems)
		return model, nil
	}
	model.missionEvidenceRoot = plan.EvidenceRoot
	return model.startLaunch(plan.Tool, "", model.pendingMode, plan.Inputs)
}

// missionValues converts the sequential prompt's collected answers into mission.Preflight's own
// "--input name=value" pair shape.
func missionValues(answers map[string]string) mission.Values {
	values := make(mission.Values, 0, len(answers))
	for name, value := range answers {
		values = append(values, name+"="+value)
	}
	return values
}

// renderProblems is E2b's "a refusal is shown as the FULL problem list" (ruling 3): one
// "kind: detail" line per problem, never truncated to the first.
func renderProblems(problems []mission.Problem) []string {
	lines := make([]string, 0, len(problems))
	for _, problem := range problems {
		lines = append(lines, problem.Kind+": "+problem.Detail)
	}
	return lines
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
		return model.startLaunch(model.pendingTool, spec, mode, nil)
	}
	updated, command := model.specInput.Update(key)
	model.specInput = updated
	return model, command
}

func (model Model) startLaunch(tool discovery.Tool, spec string, mode profile.LaunchMode, inputs map[string]string) (tea.Model, tea.Cmd) {
	requestedMode := mode
	mode = model.routeActionBeforeFallback(tool, mode)
	terminal, tabAvailable := launch.DetectTerminal(model.environment)
	// "No split command means tabs only" (§11): a split degrades to the tab route, not straight
	// to foreground — the SAME degrade the tab-route fallback below then applies on top of it if
	// the detected terminal cannot even open a tab either.
	if mode == profile.ModeSplit && (!tabAvailable || !launch.HasSplitCommand(terminal)) {
		mode = profile.ModeTab
		model.notice = "Split unavailable here — opening a tab instead."
	}
	tabFallback := mode == profile.ModeTab && (!tabAvailable || launch.ProbeTerminal(terminal) != nil)
	if tabFallback && model.actionInFlight != "" && model.actionIsService(tool) {
		// A service never returns: running it in the deck's own terminal would block the deck.
		model.notice = "Could not open a terminal tab for " + actionTitle(model.inFlightAction(tool)) + "."
		return model, nil
	}
	if tabFallback {
		mode = profile.ModeForeground
		model.notice = "New tab unavailable here — running in the foreground."
	}
	mode = model.routeActionAfterFallback(tool, mode)
	if !model.preLaunchProbed && model.needsPreLaunchHold(tool) {
		model.notice = "Checking what " + tool.Title + " needs..."
		if len(model.serviceNeedTools(tool)) == 0 {
			model.notice = "Checking whether " + tool.Title + " is already running..."
		}
		held := model.preLaunchProbeCmd(tool, spec, mode, inputs)
		model.launchAnyway = false
		return model, held
	}
	prepared, err := launch.Prepare(model.launchRequest(tool, spec, inputs), mode)
	if errors.Is(err, launch.ErrAlreadyRunning) {
		return model.noteAlreadyRunning(tool, spec, requestedMode, inputs), nil
	}
	if err != nil {
		return model.noteLaunchRefusal(tool, err), nil
	}
	if mode == profile.ModeCaptured {
		return model.startCaptured(prepared)
	}
	if mode == profile.ModePane {
		return model.startPane(prepared, tool.Title)
	}
	if mode == profile.ModeSplit {
		return model.startSplit(tool, spec, inputs, prepared, terminal)
	}
	if mode == profile.ModeTab {
		return model.startTab(tool, spec, inputs, prepared, terminal)
	}
	return model.runForeground(prepared, tabFallback)
}

// runForeground starts an already-prepared foreground command. The trap is armed here and
// stopped only in handleForegroundFinished, AFTER Finish has persisted the row — not merely
// once the child exits or once this Run call returns. Bubble Tea's own terminal-restore and
// async message dispatch run in between; the trap must stay live through all of it so a second
// signal in that window cannot kill this process before the run's outcome is ever recorded.
func (model Model) runForeground(prepared launch.Prepared, tabFallback bool) (tea.Model, tea.Cmd) {
	signals, stop := launch.ArmSignalTrap()
	action := model.actionInFlight
	callback := func(commandError error) tea.Msg {
		return foregroundFinishedMsg{runID: prepared.Row.ID, action: action, commandError: commandError, tabFallback: tabFallback, stopSignalTrap: stop}
	}
	command := &launch.SignalForwardingCommand{Cmd: prepared.Command, Signals: signals}
	return model, tea.Exec(command, callback)
}

func (model Model) launchRequest(tool discovery.Tool, spec string, inputs map[string]string) launch.Request {
	request := launch.Request{
		Tool: tool, RepositoryRoot: model.repositoryRoot, Profile: model.repositoryProfile,
		Spec: spec, Inputs: inputs, Store: model.runStore, Environment: model.environment, StartedAt: model.clock(),
		Live: model.launchLive, Anyway: model.launchAnyway,
		Action: model.actionInFlight, Confirmed: model.actionConfirmed,
	}
	// A mission launch (E2b) carries Request.Mission/MissionPath/EvidenceRoot exactly as
	// launch.Prepare needs to write the row's own mission fields — nil for an ordinary tool
	// launch, so every other request is unaffected.
	if model.missionInFlight != nil {
		request.Mission = model.missionInFlight.Definition.Name
		request.MissionPath = model.missionInFlight.Path
		request.EvidenceRoot = model.missionEvidenceRoot
	}
	if model.launchServices != nil {
		request.Services = launch.ServiceCheckerFrom(model.launchServices)
	}
	return request
}

func (model Model) startTab(tool discovery.Tool, spec string, inputs map[string]string, prepared launch.Prepared, terminal launch.Terminal) (tea.Model, tea.Cmd) {
	wrapperArgv := []string{model.executable, "__replay-row", model.runStore.RowPath(prepared.Row.ID)}
	command, err := launch.NewTabCommand(terminal, prepared.Row, wrapperArgv)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	return model, func() tea.Msg {
		return tabStartedMsg{runID: prepared.Row.ID, action: model.actionInFlight, terminal: terminal, tool: tool, spec: spec, inputs: inputs, err: command.Run()}
	}
}

// startSplit mirrors startTab exactly (N7: "the wrapper/replay contract is identical to the tab
// route") — the only difference is which command opens the host terminal. It reuses tabStartedMsg
// and handleTabStarted/retryTabFailureInForeground unchanged: both routes report the same
// runID/terminal/tool/spec/inputs/err shape, and a failed spawn of either one retries the same
// tool in the foreground the same way.
func (model Model) startSplit(tool discovery.Tool, spec string, inputs map[string]string, prepared launch.Prepared, terminal launch.Terminal) (tea.Model, tea.Cmd) {
	wrapperArgv := []string{model.executable, "__replay-row", model.runStore.RowPath(prepared.Row.ID)}
	command, err := launch.SplitCommand(terminal, prepared.Row.Cwd, wrapperArgv)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	return model, func() tea.Msg {
		return tabStartedMsg{runID: prepared.Row.ID, action: model.actionInFlight, terminal: terminal, tool: tool, spec: spec, inputs: inputs, err: command.Run()}
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
	model.openReadyWindowAfterExit(row)
	if action := finishedAction(message.action, row); action != "" {
		return model.finishActionRun(row, action)
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

func (model Model) handleTabStarted(message tabStartedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		return model.retryTabFailureInForeground(message)
	}
	model.openReadyWindowAfterSpawn(message.tool, message.action)
	if message.action != "" {
		return model.finishActionTab(message), model.probeReadyWindows()
	}
	model.notice = fmt.Sprintf("Run opened in %s.", message.terminal)
	model.refreshRuns()
	model.showRuns, model.detail = true, false
	return model, model.probeReadyWindows()
}

// retryTabFailureInForeground handles a tab-route spawn that never started the tool at all —
// the terminal client itself (wt.exe/tmux/osascript) failed, not the tool. The tab row is
// closed out via Abort, which records why without claiming the tool failed, and the SAME tool
// is launched again in the foreground as its own new row, mirroring the wording of the
// existing unknown-terminal fallback in startLaunch.
func (model Model) retryTabFailureInForeground(message tabStartedMsg) (tea.Model, tea.Cmd) {
	note := fmt.Sprintf("could not open %s tab: %v", message.terminal, message.err)
	_, _ = launch.Abandon(model.runStore, message.runID, note, model.clock())
	model.actionInFlight, model.actionConfirmed = message.action, message.action != ""
	if message.action != "" && model.actionIsService(message.tool) {
		// A service never returns: retrying it in the deck's own terminal would block the deck.
		model.notice = fmt.Sprintf("Could not open a terminal tab for %s (%v).", actionTitle(model.inFlightAction(message.tool)), message.err)
		model.actionInFlight, model.actionConfirmed = "", false
		return model, nil
	}
	model.notice = fmt.Sprintf("New tab unavailable (%s); running in the foreground.", message.terminal)
	// The held service results were cleared after the resumed launch, so this re-Prepare leaves
	// service needs unchecked; acceptable, it only runs after a Prepare that already passed.
	prepared, err := launch.Prepare(model.launchRequest(message.tool, message.spec, message.inputs), profile.ModeForeground)
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	return model.runForeground(prepared, true)
}

// openMissionsView opens E2a's missions section (key `m`): every mission mission.Discover finds
// across the SAME user-config root and catalog sources the deck's own tool catalog uses —
// personal missions first, then each catalogued toolkit's missions, in precedence order. An
// invalid mission file is excluded here (§9: `doctor`'s to report, never runnable from the deck).
func (model Model) openMissionsView() (tea.Model, tea.Cmd) {
	discovered, err := discoverMissions()
	if err != nil {
		model.notice = err.Error()
		return model, nil
	}
	model.missionRows = discovered.Missions
	model.missionCursor = clampCursor(model.missionCursor, len(model.missionRows))
	model.showMissions = true
	model.detail, model.showRuns, model.showPanes, model.showEquipView = false, false, false, false
	return model, nil
}

// discoverMissions resolves the user's config root and catalog sources independently, mirroring
// equipPath()'s own precedent — it does not depend on WithCatalogContext's userConfigRoot having
// been set, so a Model built directly by a test (or one launched before the add-offer context
// resolves) still discovers missions correctly.
func discoverMissions() (mission.Set, error) {
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return mission.Set{}, err
	}
	sourcePaths, err := catalog.SourcePaths(userConfigRoot)
	if err != nil {
		return mission.Set{}, err
	}
	sources := make([]mission.Source, 0, len(sourcePaths))
	for _, sourcePath := range sourcePaths {
		sources = append(sources, mission.Source{Path: sourcePath})
	}
	return mission.Discover(userConfigRoot, sources)
}

// updateMissionsKey drives E2a's missions section: up/down move, enter begins E2b's mission
// launch (beginMissionLaunch), esc/m/left/backspace close it, q quits — mirroring the run list's
// and equip view's own back-key conventions.
func (model Model) updateMissionsKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q":
		return model, tea.Quit
	case "esc", "m", "left", "backspace":
		model.showMissions = false
		return model, nil
	case "up":
		model.missionCursor = wrapCursor(model.missionCursor-1, len(model.missionRows))
	case "down":
		model.missionCursor = wrapCursor(model.missionCursor+1, len(model.missionRows))
	case "enter":
		if len(model.missionRows) == 0 {
			return model, nil
		}
		selected := model.missionRows[clampCursor(model.missionCursor, len(model.missionRows))]
		model.showMissions = false
		return model.beginMissionLaunch(selected)
	}
	return model, nil
}

// refreshRuns is the explicit refresh: it recovers abandoned foreground rows, then reloads the
// run list. The live-status tick calls reloadRuns instead, so recovery never runs per tick.
func (model *Model) refreshRuns() {
	// A foreground row left "running" by a session that died mid-run — no completion message
	// ever arrives to end it — is recovered here, before the rows a person actually sees are read.
	model.recoverAbandonedRuns()
	model.reloadRuns()
}

func (model *Model) recoverAbandonedRuns() {
	_, _ = model.runStore.RecoverAbandonedForegroundRuns(model.clock())
}

func (model *Model) reloadRuns() {
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
	model.showRuns, model.detail, model.showPanes, model.showEquipView, model.showMissions = true, false, false, false, false
	// The run list is live while it is open: a tab run's wrapper is updating its row from
	// another process, and this is what makes that visible without a keypress.
	model, tickCommand := model.ensureTicking()
	return model, tea.Batch(tickCommand, model.probeVisible())
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
	// row.Artifact is swapped for the resolved evidence directory (manifest v1.1
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

// acceptUncataloguedOffer is the deck-side equivalent of `formic add <path>` (N8 point 3): same
// validation, same refusal text on an invalid descriptor. On success the catalog is
// re-discovered and the offer disappears; on refusal the offer stays exactly as it was, so the
// person can fix the descriptor and press "a" again.
func (model Model) acceptUncataloguedOffer() Model {
	if _, err := catalog.Add(catalog.DefaultPath(model.userConfigRoot), model.uncatalogued.Path); err != nil {
		model.notice = err.Error()
		return model
	}
	sourcePaths, err := catalog.SourcePaths(model.userConfigRoot)
	if err != nil {
		model.notice = err.Error()
		return model
	}
	discoveredCatalog, err := discovery.Discover(sourcePaths)
	if err != nil {
		model.notice = err.Error()
		return model
	}
	model.catalog = discoveredCatalog
	model.cursor = clampCursor(model.cursor, len(model.visibleTools()))
	model.uncatalogued = nil
	model.notice = "Added the repo's toolkit to the catalog."
	return model
}

func textInput(prompt, placeholder string) textinput.Model {
	input := textinput.New()
	input.Prompt, input.Placeholder = prompt, placeholder
	return input
}

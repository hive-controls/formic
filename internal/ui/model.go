package ui

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/pane"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

type Model struct {
	catalog           discovery.Catalog
	repositoryRoot    string
	filter            textinput.Model
	searching         bool
	cursor            int
	detail            bool
	showHelp          bool
	width             int
	height            int
	repositoryProfile profile.File
	profileProblem    string
	configuring       bool
	configureStep     int
	configureMode     profile.LaunchMode
	configureArgs     textinput.Model
	configureEnv      textinput.Model
	// configureEnvEntries drives a one-row-per-declared-key env form when the manifest declares
	// the manifest format's v1.1 `env[]`; empty for a manifest that does not, which keeps the
	// free-text configureEnv field above exactly as it worked before v1.1.
	configureEnvEntries []formats.ToolManifestEnvElem
	configureEnvIndex   int
	configureEnvValues  map[string]string
	argumentRows        argumentRows
	envChooser          envChooser
	notice              string
	// noticeProblems carries E2b's mission pre-flight refusal (ruling 3: "a refusal is shown as
	// the FULL problem list") as one line per problem — rendered alongside notice, never
	// truncated to a single wrapped line the way a plain notice string would be. Set only by
	// finishMissionInputPrompt's refusal path; nil otherwise.
	noticeProblems []string
	runStore       runs.Store
	runRows        []runs.Row
	runCursor      int
	showRuns       bool
	promptingSpec  bool
	panes          []*pane.Pane
	paneFocus      int
	showPanes      bool
	runMonitors    map[string]*runs.Monitor
	ticking        bool
	paneTicker     func(time.Duration) tea.Cmd
	specInput      textinput.Model
	pendingTool    discovery.Tool
	// promptingInputs, inputSchema/inputStep/inputAnswers/inputField, and pendingMode/pendingSpec
	// drive the generalized sequential input prompt (N10): one screen per declared input, in
	// inputSchema order (required first, then optional — see orderedInputs), collecting answers
	// into inputAnswers until every step is done, then resuming the launch that was interrupted
	// to ask (pendingTool/pendingMode/pendingSpec, mirroring promptingSpec's own pendingTool use).
	// inputAnswers is never written to model.repositoryProfile or any file — it lives only in
	// this in-memory map, handed to launch.Request.Inputs once the last step is answered.
	promptingInputs bool
	inputSchema     []formats.Input
	inputStep       int
	inputAnswers    map[string]string
	inputField      textinput.Model
	pendingMode     profile.LaunchMode
	pendingSpec     string
	// promptingEquip, equipNeeds/equipStep, equipField, and equipProviderCursor drive C1's
	// launch-time equip prompt — resolved and, if needed, prompted for BEFORE the sequential
	// input prompt above, so a launch that needs both an equipped secret and a declared input
	// asks for the secret first. equipNeeds holds only the UNMET, PROMPTABLE (secret/gate)
	// needs, in resolution order — a met need is never entered here at all (§13 "ask once"),
	// and an unmet runtime/network need refuses the launch before promptingEquip is ever set.
	promptingEquip      bool
	equipNeeds          []equip.NeedStatus
	equipStep           int
	equipField          textinput.Model
	equipProviderCursor int
	// showEquipView and equipViewNeeds drive C2's read-only equip view (key `e`): every need
	// across the whole armory, grouped by identity, with which tools declare it and whether it
	// is met — computed once when the view opens, the same equip.GroupNeedStatuses computation
	// `formic equip`'s own no-argument form shares.
	showEquipView  bool
	equipViewNeeds []equip.ArmoryNeedStatus
	// showMissions and missionRows/missionCursor drive E2a's missions section (key `m`): every
	// discovered mission (mission.Discover, the same user-config root and catalog sources the
	// deck's own tool catalog uses), personal missions and toolkit missions together, in
	// discovery order — a shadowed one still shows, carrying its own ShadowedBy marker; an
	// invalid mission file is never listed here (§9: that is `doctor`'s to report).
	showMissions  bool
	missionRows   []mission.Mission
	missionCursor int
	// missionInFlight and missionEvidenceRoot carry E2b's mission-launch context across the
	// equip prompt, the mission's own sequential input prompt, and pre-flight: set only by
	// beginMissionLaunch, read by launchRequest to populate Request.Mission/MissionPath/
	// EvidenceRoot — nil/"" for an ordinary tool launch, and always cleared at the top of
	// beginLaunch so mission context never leaks into a later, unrelated tool launch.
	missionInFlight     *mission.Mission
	missionEvidenceRoot string
	environment         []string
	executable          string
	clock               func() time.Time
	goos                string
	// userConfigRoot and uncatalogued carry N8 point 3's add-this-repo offer context: where the
	// user's catalog.yaml lives (needed to accept the offer) and the offer itself, computed by
	// the caller (cmd/formic's runBrowser) via WithCatalogContext from the exact source list
	// `formic list --json`'s own uncatalogued field shares. Neither is set by New/newModel
	// directly — an untouched Model makes no catalog-admission filesystem calls of its own.
	userConfigRoot string
	uncatalogued   *catalog.Uncatalogued
	// live is the shared in-memory live-state store (live.go); launchLive carries the pre-launch
	// probe result to launchRequest for exactly one resumed launch.
	live       *liveState
	launchLive *launch.ProbeResult
	// launchServices holds the pre-launch service-need probe results for exactly one resumed launch;
	// preLaunchProbed marks that resumed startLaunch so it does not hold again (launchLive stays nil
	// when only services were probed, so it cannot be that marker).
	launchServices  map[string]launch.ProbeResult
	preLaunchProbed bool
	// serviceOffer is the pending offer to run an unmet service need's action (service_offer.go);
	// its line is the notice, and any key answers it.
	serviceOffer *serviceOffer
	// anywayOffer is the pending hint to launch past an already-running skip (anyway_offer.go);
	// launchAnyway carries Request.Anyway for exactly one resumed launch, cleared like launchLive.
	anywayOffer  *anywayOffer
	launchAnyway bool

	// Action menu state (spec §23): the highlighted action, the one a launch is carrying through
	// the confirm/equip/input chain, its y/N prompt, the captured run in flight (blocks `q`), the
	// last result block, and the spinner shown while a captured run is going.
	actionCursor     int
	actionInFlight   string
	actionConfirmed  bool
	confirmingAction bool
	capturedRunID    string
	actionResult     actionResult
	actionSpinner    spinner.Model
	// configureAction is the action the customize screen is editing; "" for an action-less tool.
	configureAction string
}

func New(catalog discovery.Catalog, repositoryRoot string) Model {
	store, storeErr := runs.DefaultStore()
	model := newModel(catalog, repositoryRoot, store)
	if storeErr != nil {
		model.notice = storeErr.Error()
	} else {
		model.recoverAbandonedRuns()
	}
	return model
}

func newModel(catalog discovery.Catalog, repositoryRoot string, store runs.Store) Model {
	filter := textinput.New()
	filter.Prompt = "Filter: "
	filter.Placeholder = "type to search"
	filter.CharLimit = 120
	filter.Focus()
	repositoryProfile, err := profile.Load(repositoryRoot)
	model := Model{
		catalog: catalog, repositoryRoot: repositoryRoot, filter: filter,
		repositoryProfile: repositoryProfile,
		runStore:          store, environment: os.Environ(), clock: time.Now, goos: runtime.GOOS,
	}
	model.executable, _ = os.Executable()
	if err != nil {
		model.profileProblem = err.Error()
	}
	model.live = newLiveState(repositoryRoot)
	return model
}

// WithCatalogContext attaches the catalog-admission context the add-this-repo offer needs
// (N8 point 3): userConfigRoot, so accepting the offer can reach the user's catalog.yaml, and
// the offer itself, computed by the caller from the same source list `formic list --json`'s
// uncatalogued field shares (catalog.UncataloguedAt). Never called by New/newModel — a Model
// nobody attaches this to simply never shows the offer.
func (model Model) WithCatalogContext(userConfigRoot string, uncatalogued *catalog.Uncatalogued) Model {
	model.userConfigRoot, model.uncatalogued = userConfigRoot, uncatalogued
	return model
}

func (model Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, model.probeVisible(), model.startLiveTicking())
}

// quit stops every captured child before leaving. A pane's child would otherwise be left
// writing into a pseudo-terminal nobody reads, blocking on a full buffer for as long as it
// lives — so quitting is the one moment the two-stage kill runs without being asked for.
func (model Model) quit() (tea.Model, tea.Cmd) {
	model.cancelLive()
	if len(model.panes) == 0 {
		return model, tea.Quit
	}
	stopped := model
	return model, tea.Sequence(func() tea.Msg {
		stopped.terminateAllPanes()
		return nil
	}, tea.Quit)
}

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := message.(type) {
	case tea.WindowSizeMsg:
		model.width, model.height = typed.Width, typed.Height
		if len(model.panes) == 0 {
			return model, nil
		}
		// Every pane's child is told the new geometry, which is what raises SIGWINCH in it.
		return model, model.resizePanes()
	case tea.KeyMsg:
		return model.updateKey(typed)
	case foregroundFinishedMsg:
		finished := model.handleForegroundFinished(typed)
		return finished, finished.probeReadyWindows()
	case tabStartedMsg:
		return model.handleTabStarted(typed)
	case artifactOpenedMsg:
		return model.handleArtifactOpened(typed), nil
	case paneTickMsg:
		return model.handlePaneTick()
	case paneFinishedMsg:
		finished := model.handlePaneFinished(typed)
		return finished, finished.probeReadyWindows()
	case liveTickMsg:
		return model.handleLiveTick()
	case liveResultMsg:
		return model.handleLiveResult(typed)
	case preLaunchProbedMsg:
		return model.handlePreLaunchProbed(typed)
	case capturedFinishedMsg:
		finished := model.handleCapturedFinished(typed)
		return finished, finished.probeReadyWindows()
	case spinner.TickMsg:
		return model.handleSpinnerTick(typed)
	}
	return model, nil
}

func (model Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyName := key.String()
	if keyName == "ctrl+c" {
		return model.quit()
	}
	if model.confirmingAction {
		return model.updateConfirmKey(key)
	}
	if model.serviceOfferShowing() {
		return model.updateServiceOfferKey(key)
	}
	if keyName == "a" && model.anywayOfferShowing() {
		return model.launchAnywayNow()
	}
	model = model.dropPendingOffers()
	if keyName == "q" && model.quitRefused() {
		model.notice = actionRunningNotice
		return model, nil
	}
	if model.configuring {
		return model.updateConfigureKey(key)
	}
	if model.promptingEquip {
		return model.updateEquipKey(key)
	}
	if model.promptingInputs {
		return model.updateInputKey(key)
	}
	if model.promptingSpec {
		return model.updateSpecKey(key)
	}
	if model.showEquipView {
		return model.updateEquipViewKey(key)
	}
	if model.showMissions {
		return model.updateMissionsKey(key)
	}
	if model.showPanes {
		return model.updatePaneKey(key)
	}
	if model.showRuns {
		return model.updateRunKey(key)
	}
	switch keyName {
	case "q":
		// `q` quits only when there is nothing to lose it in: the detail view has no
		// filter to type into, and on the list an empty filter means the user isn't
		// mid-search. A non-empty filter falls through to updateListKey below, where it
		// is typed into the filter like any other rune (Esc clears the filter first).
		if model.detail || (model.filter.Value() == "" && !model.searching) {
			return model.quit()
		}
	case "?":
		model.showHelp = !model.showHelp
		return model, nil
	}
	if model.detail {
		if tool, ok := model.currentTool(); ok && toolHasActions(tool) {
			return model.updateActionMenuKey(key, tool)
		}
		if keyName == "enter" {
			return model.beginLaunch()
		}
		if keyName == "c" {
			return model.beginConfigure()
		}
		if keyName == "esc" || keyName == "left" || keyName == "backspace" {
			model.detail = false
		}
		return model, nil
	}
	if keyName == "/" && model.filter.Value() == "" {
		model.searching = true
		return model, nil
	}
	if keyName == "r" && model.filter.Value() == "" && !model.searching {
		return model.openRunList()
	}
	// "a" = accept the add-this-repo offer (N8 point 3). Any other key does nothing — the offer
	// stays until the deck exits, with no "never ask again" state.
	if keyName == "a" && model.uncatalogued != nil && model.filter.Value() == "" && !model.searching {
		return model.acceptUncataloguedOffer(), nil
	}
	if keyName == "p" && len(model.panes) > 0 && model.filter.Value() == "" && !model.searching {
		model.showPanes, model.showRuns, model.showMissions = true, false, false
		return model, model.paneTick()
	}
	if keyName == "e" && model.filter.Value() == "" && !model.searching {
		return model.openEquipView()
	}
	if keyName == "m" && model.filter.Value() == "" && !model.searching {
		return model.openMissionsView()
	}
	return model.updateListKey(key)
}

func (model Model) beginConfigure() (tea.Model, tea.Cmd) {
	tool, ok := model.currentTool()
	if !ok {
		return model, nil
	}
	if model.profileProblem != "" {
		model.notice = "Fix profile problems before configuring: " + model.profileProblem
		return model, nil
	}
	model.configuring = true
	model.configureStep = 0
	model.configureMode = profile.ResolveMode(model.repositoryProfile, tool.Name, toolInteractive(tool))
	model.configureArgs = textinput.New()
	model.configureArgs.Prompt = "Arguments: "
	model.configureArgs.Placeholder = "--flag, value"
	model.configureAction = model.highlightedActionName(tool)
	storedArgs := model.repositoryProfile.Tools[tool.Name].ArgsFor(model.configureAction)
	model.configureArgs.SetValue(strings.Join(storedArgs, ", "))
	model = model.withArgumentRows(declaredArguments(tool, model.configureAction), storedArgs)
	model.configureEnvEntries = tool.Manifest.Env
	model.configureEnvValues = make(map[string]string)
	existingEnv := model.repositoryProfile.Tools[tool.Name].Env
	model.envChooser = envChooser{}
	if len(model.configureEnvEntries) > 0 {
		model.configureEnvIndex = 0
		model.configureEnv = envRowInput(model.configureEnvEntries[0], existingEnv)
		model.envChooser = newEnvChooser(model.configureEnvEntries[0], existingEnv)
	} else {
		model.configureEnv = textinput.New()
		model.configureEnv.Prompt = "Environment: "
		model.configureEnv.Placeholder = "NAME=value, SECRET=env.SOURCE"
		model.configureEnv.SetValue(formatEnvironment(existingEnv))
	}
	model.notice = ""
	return model, nil
}

// envRowInput builds the input for one declared env[] entry (manifest v1.1): a secret entry is
// masked and never asks for the secret's value at all — only the name of the process
// environment variable it should be read from at launch, per the existing env.NAME reference
// convention profile.ResolveEnvironment already resolves against. A non-secret entry is
// prefilled from the tool's existing saved value, falling back to the manifest's declared
// `default`.
func envRowInput(entry formats.ToolManifestEnvElem, existingEnv map[string]string) textinput.Model {
	input := textinput.New()
	input.Prompt = envRowPrompt(entry)
	if entry.Secret {
		input.EchoMode = textinput.EchoPassword
		input.SetValue(secretSourceName(entry, existingEnv))
	} else {
		input.SetValue(envRowValue(entry, existingEnv))
	}
	input.Focus()
	return input
}

func envRowPrompt(entry formats.ToolManifestEnvElem) string {
	if entry.Required {
		return entry.Name + " (required): "
	}
	return entry.Name + ": "
}

func envRowValue(entry formats.ToolManifestEnvElem, existingEnv map[string]string) string {
	if value, ok := existingEnv[entry.Name]; ok {
		return value
	}
	if entry.Default != nil {
		return *entry.Default
	}
	return ""
}

// secretSourceName never returns the secret's actual value — only the name of the environment
// variable a previously saved `env.NAME` reference already points at, so reopening the form
// does not lose a prior choice. A row with no prior choice starts BLANK, deliberately never
// guessed at (e.g. from the declared entry's own name): SetValue leaves the cursor at the end
// of whatever it prefilled, so a guessed name a person types over — rather than clears first —
// would silently concatenate onto it instead of replacing it.
func secretSourceName(entry formats.ToolManifestEnvElem, existingEnv map[string]string) string {
	if value, ok := existingEnv[entry.Name]; ok {
		if source, isReference := strings.CutPrefix(value, "env."); isReference {
			return source
		}
	}
	return ""
}

func (model Model) updateConfigureKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		model.configuring = false
		model.configureEnv.Blur()
		return model, nil
	}
	if model.configureStep == 0 {
		return model.updateConfigureMode(key), nil
	}
	if model.onArgumentRow() {
		return model.updateArgumentRowKey(key)
	}
	if model.onEnvChooserRow() && key.String() != "enter" {
		return model.updateEnvChooserKey(key), nil
	}
	if model.configureStep == 1 && key.String() == "enter" {
		model.configureStep = 2
		model.configureArgs.Blur()
		model.configureEnv.Focus()
		return model, nil
	}
	if model.configureStep == 2 && key.String() == "enter" {
		if len(model.configureEnvEntries) > 0 {
			return model.advanceEnvRow()
		}
		return model.saveConfiguration(), nil
	}
	if model.configureStep == 1 {
		updated, command := model.configureArgs.Update(key)
		model.configureArgs = updated
		return model, command
	}
	updated, command := model.configureEnv.Update(key)
	model.configureEnv = updated
	return model, command
}

// advanceEnvRow records the current declared env[] row's typed value, then either moves on to
// the next declared row or — on the last one — saves, exactly like the free-text form's single
// Enter-to-save step 2 above.
func (model Model) advanceEnvRow() (tea.Model, tea.Cmd) {
	tool, ok := model.currentTool()
	if !ok {
		return model, nil
	}
	entry := model.configureEnvEntries[model.configureEnvIndex]
	model.storeEnvRowValue(entry)
	if model.configureEnvIndex+1 < len(model.configureEnvEntries) {
		model.configureEnvIndex++
		nextEntry := model.configureEnvEntries[model.configureEnvIndex]
		model.configureEnv = envRowInput(nextEntry, model.repositoryProfile.Tools[tool.Name].Env)
		model.envChooser = newEnvChooser(nextEntry, model.repositoryProfile.Tools[tool.Name].Env)
		return model, textinput.Blink
	}
	return model.saveConfiguration(), nil
}

// storeEnvRowValue never writes a secret's typed value verbatim: for a `secret: true` entry the
// typed text is the SOURCE variable name, and only "env."+that name — the existing
// env.NAME-reference convention profile.ResolveEnvironment already resolves at launch — is
// stored. A blank row unsets any previously saved value for that key.
func (model Model) storeEnvRowValue(entry formats.ToolManifestEnvElem) {
	typed := strings.TrimSpace(model.configureEnv.Value())
	if model.envChooser.active {
		typed = model.envChooser.selectedValue()
	}
	if typed == "" {
		delete(model.configureEnvValues, entry.Name)
		return
	}
	if entry.Secret {
		model.configureEnvValues[entry.Name] = "env." + typed
		return
	}
	model.configureEnvValues[entry.Name] = typed
}

func (model Model) updateConfigureMode(key tea.KeyMsg) Model {
	paneAvailable := model.configurePaneAvailable()
	splitAvailable := model.configureSplitAvailable()
	switch key.String() {
	case "left":
		model.configureMode = previousMode(model.configureMode, splitAvailable, paneAvailable)
	case "right":
		model.configureMode = nextMode(model.configureMode, splitAvailable, paneAvailable)
	case "enter":
		model.configureStep = 1
		model.configureArgs.Focus()
	}
	return model
}

// configurePaneAvailable reports whether pane can be offered for the tool currently being
// configured — see paneUnavailableReason.
func (model Model) configurePaneAvailable() bool {
	tool, ok := model.currentTool()
	return ok && paneUnavailableReason(tool) == ""
}

// configureSplitAvailable reports whether split can be offered: N7's "the mode chooser... offers
// it only when the detected terminal has a split command" — unlike pane, split has no per-tool
// restriction (it drives a real host terminal, so an interactive tool is exactly what it is for).
func (model Model) configureSplitAvailable() bool {
	terminal, detected := launch.DetectTerminal(model.environment)
	return detected && launch.HasSplitCommand(terminal)
}

func (model Model) saveConfiguration() Model {
	tool, ok := model.currentTool()
	if !ok {
		return model
	}
	environment, err := model.resolvedConfigureEnvironment()
	if err != nil {
		model.notice = err.Error()
		return model
	}
	// Validate and save on a CLONE, never the live profile directly: a rejected edit
	// (invalid settings, or a write failure) must leave model.repositoryProfile exactly as
	// it was, so neither Esc nor a subsequent launch ever sees a draft that was never
	// actually saved.
	candidate := model.repositoryProfile.Clone()
	settings := candidate.Tools[tool.Name]
	settings.Mode, settings.Env = model.configureMode, environment
	settings = applyConfiguredArguments(settings, model.configureAction, model.serializedConfigureArguments())
	candidate.SetTool(tool.Name, settings)
	if err := candidate.Save(model.repositoryRoot); err != nil {
		model.notice = err.Error()
		return model
	}
	model.repositoryProfile = candidate
	model.configuring = false
	model.notice = fmt.Sprintf("Saved %s in .formic/%s", tool.Name, profile.FileName)
	return model
}

// resolvedConfigureEnvironment returns what the free-text field parses when the manifest
// declares no env[] (unchanged), or a defensive copy of the per-row values collected by the
// declared-env[] form otherwise — never the working map itself, so a later reconfigure session
// building a fresh map can never alias into a profile already saved from a previous one.
func (model Model) resolvedConfigureEnvironment() (map[string]string, error) {
	if len(model.configureEnvEntries) == 0 {
		return parseEnvironment(model.configureEnv.Value())
	}
	if len(model.configureEnvValues) == 0 {
		return nil, nil
	}
	environment := make(map[string]string, len(model.configureEnvValues))
	for name, value := range model.configureEnvValues {
		environment[name] = value
	}
	return environment, nil
}

func (model Model) currentTool() (discovery.Tool, bool) {
	tools := model.visibleTools()
	if len(tools) == 0 {
		return discovery.Tool{}, false
	}
	return tools[clampCursor(model.cursor, len(tools))], true
}

// toolByName looks a tool up by its manifest name — the identifier a run row carries — rather
// than by filter/cursor position, so a run from a tool no longer visible under the current
// filter can still be resolved against its manifest in the run list.
func (model Model) toolByName(name string) (discovery.Tool, bool) {
	for _, tool := range model.catalog.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return discovery.Tool{}, false
}

func toolInteractive(tool discovery.Tool) bool {
	return manifest.RequiresRealTerminal(tool.Manifest)
}

// paneUnavailableReason names why `pane` cannot be offered for this tool on this platform, or
// "" when it can. It is the single source both the chooser (to leave the option out) and
// nextMode/previousMode (to skip over it) consult, so they can never disagree about which tools
// are affected.
func paneUnavailableReason(tool discovery.Tool) string {
	if manifest.RequiresRealTerminal(tool.Manifest) {
		return "pane: not available — this tool needs a real terminal"
	}
	if !pane.CanHostPane() {
		return "pane: not available on Windows"
	}
	return ""
}

func nextMode(mode profile.LaunchMode, splitAvailable, paneAvailable bool) profile.LaunchMode {
	switch mode {
	case profile.ModeForeground:
		return profile.ModeTab
	case profile.ModeTab:
		if splitAvailable {
			return profile.ModeSplit
		}
		if paneAvailable {
			return profile.ModePane
		}
		return profile.ModeForeground
	case profile.ModeSplit:
		if paneAvailable {
			return profile.ModePane
		}
		return profile.ModeForeground
	default:
		return profile.ModeForeground
	}
}

func previousMode(mode profile.LaunchMode, splitAvailable, paneAvailable bool) profile.LaunchMode {
	switch mode {
	case profile.ModeForeground:
		if paneAvailable {
			return profile.ModePane
		}
		if splitAvailable {
			return profile.ModeSplit
		}
		return profile.ModeTab
	case profile.ModePane:
		if splitAvailable {
			return profile.ModeSplit
		}
		return profile.ModeTab
	case profile.ModeSplit:
		return profile.ModeTab
	default:
		return profile.ModeForeground
	}
}

func parseEnvironment(value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	environment := make(map[string]string)
	for _, assignment := range strings.Split(value, ",") {
		name, setting, found := strings.Cut(strings.TrimSpace(assignment), "=")
		if !found || strings.TrimSpace(name) == "" || setting == "" {
			return nil, fmt.Errorf("environment entries must use NAME=value")
		}
		environment[strings.TrimSpace(name)] = setting
	}
	return environment, nil
}

func parseArguments(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var arguments []string
	for _, argument := range strings.Split(value, ",") {
		arguments = append(arguments, strings.TrimSpace(argument))
	}
	return arguments
}

func formatEnvironment(environment map[string]string) string {
	keys := make([]string, 0, len(environment))
	for name := range environment {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	assignments := make([]string, 0, len(keys))
	for _, name := range keys {
		assignments = append(assignments, name+"="+environment[name])
	}
	return strings.Join(assignments, ", ")
}

func (model Model) updateListKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	visibleTools := model.visibleTools()
	switch key.String() {
	case "enter", "right":
		if len(visibleTools) > 0 {
			model.detail = true
			return model, model.probeVisible()
		}
		return model, nil
	case "up":
		model.cursor = wrapCursor(model.cursor-1, len(visibleTools))
		return model, nil
	case "down":
		model.cursor = wrapCursor(model.cursor+1, len(visibleTools))
		return model, nil
	case "esc":
		model.filter.SetValue("")
		model.searching = false
		model.cursor = 0
		return model, nil
	}
	if key.Type == tea.KeyRunes {
		model.searching = true
	}
	updatedFilter, command := model.filter.Update(key)
	model.filter = updatedFilter
	model.cursor = clampCursor(model.cursor, len(model.visibleTools()))
	return model, command
}

func (model Model) visibleTools() []discovery.Tool {
	query := strings.ToLower(strings.TrimSpace(model.filter.Value()))
	if query == "" {
		return model.catalog.Tools
	}
	tools := make([]discovery.Tool, 0, len(model.catalog.Tools))
	for _, tool := range model.catalog.Tools {
		haystack := strings.ToLower(strings.Join([]string{tool.Name, tool.Title, tool.Description}, " "))
		if strings.Contains(haystack, query) {
			tools = append(tools, tool)
		}
	}
	return tools
}

func wrapCursor(cursor, count int) int {
	if count == 0 {
		return 0
	}
	if cursor < 0 {
		return count - 1
	}
	if cursor >= count {
		return 0
	}
	return cursor
}

func clampCursor(cursor, count int) int {
	if count == 0 || cursor < 0 {
		return 0
	}
	if cursor >= count {
		return count - 1
	}
	return cursor
}

package ui

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/manifest"
	"github.com/hive-controls/hivedeck/internal/pane"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
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
	notice            string
	runStore          runs.Store
	runRows           []runs.Row
	runCursor         int
	showRuns          bool
	promptingSpec     bool
	panes             []*pane.Pane
	paneFocus         int
	showPanes         bool
	runMonitors       map[string]*runs.Monitor
	ticking           bool
	paneTicker        func(time.Duration) tea.Cmd
	specInput         textinput.Model
	pendingTool       discovery.Tool
	environment       []string
	executable        string
	clock             func() time.Time
	goos              string
}

func New(catalog discovery.Catalog, repositoryRoot string) Model {
	store, storeErr := runs.DefaultStore()
	model := newModel(catalog, repositoryRoot, store)
	if storeErr != nil {
		model.notice = storeErr.Error()
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
	return model
}

func (model Model) Init() tea.Cmd {
	return textinput.Blink
}

// quit stops every captured child before leaving. A pane's child would otherwise be left
// writing into a pseudo-terminal nobody reads, blocking on a full buffer for as long as it
// lives — so quitting is the one moment the two-stage kill runs without being asked for.
func (model Model) quit() (tea.Model, tea.Cmd) {
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
		return model.handleForegroundFinished(typed), nil
	case tabStartedMsg:
		return model.handleTabStarted(typed), nil
	case artifactOpenedMsg:
		return model.handleArtifactOpened(typed), nil
	case paneTickMsg:
		return model.handlePaneTick()
	case paneFinishedMsg:
		return model.handlePaneFinished(typed), nil
	}
	return model, nil
}

func (model Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyName := key.String()
	if keyName == "ctrl+c" {
		return model.quit()
	}
	if model.configuring {
		return model.updateConfigureKey(key)
	}
	if model.promptingSpec {
		return model.updateSpecKey(key)
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
	if keyName == "p" && len(model.panes) > 0 && model.filter.Value() == "" && !model.searching {
		model.showPanes, model.showRuns = true, false
		return model, model.paneTick()
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
	model.configureArgs.SetValue(strings.Join(model.repositoryProfile.Tools[tool.Name].Args, ", "))
	model.configureEnv = textinput.New()
	model.configureEnv.Prompt = "Environment: "
	model.configureEnv.Placeholder = "NAME=value, SECRET=env.SOURCE"
	model.configureEnv.SetValue(formatEnvironment(model.repositoryProfile.Tools[tool.Name].Env))
	model.notice = ""
	return model, nil
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
	if model.configureStep == 1 && key.String() == "enter" {
		model.configureStep = 2
		model.configureArgs.Blur()
		model.configureEnv.Focus()
		return model, nil
	}
	if model.configureStep == 2 && key.String() == "enter" {
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

func (model Model) updateConfigureMode(key tea.KeyMsg) Model {
	switch key.String() {
	case "left":
		model.configureMode = previousMode(model.configureMode)
	case "right":
		model.configureMode = nextMode(model.configureMode)
	case "enter":
		model.configureStep = 1
		model.configureArgs.Focus()
	}
	return model
}

func (model Model) saveConfiguration() Model {
	tool, ok := model.currentTool()
	if !ok {
		return model
	}
	environment, err := parseEnvironment(model.configureEnv.Value())
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
	settings.Mode, settings.Args, settings.Env = model.configureMode, parseArguments(model.configureArgs.Value()), environment
	candidate.SetTool(tool.Name, settings)
	if err := candidate.Save(model.repositoryRoot); err != nil {
		model.notice = err.Error()
		return model
	}
	model.repositoryProfile = candidate
	model.configuring = false
	model.notice = fmt.Sprintf("Saved %s in .hivedeck/%s", tool.Name, profile.FileName)
	return model
}

func (model Model) currentTool() (discovery.Tool, bool) {
	tools := model.visibleTools()
	if len(tools) == 0 {
		return discovery.Tool{}, false
	}
	return tools[clampCursor(model.cursor, len(tools))], true
}

func toolInteractive(tool discovery.Tool) bool {
	return manifest.RequiresRealTerminal(tool.Manifest)
}

func nextMode(mode profile.LaunchMode) profile.LaunchMode {
	switch mode {
	case profile.ModeForeground:
		return profile.ModeTab
	case profile.ModeTab:
		return profile.ModePane
	default:
		return profile.ModeForeground
	}
}

func previousMode(mode profile.LaunchMode) profile.LaunchMode {
	switch mode {
	case profile.ModeForeground:
		return profile.ModePane
	case profile.ModePane:
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

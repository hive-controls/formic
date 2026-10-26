package ui

import (
	"context"
	"errors"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

// liveInterval is how often the deck re-probes the rows on screen (§22 b: probes run only while
// a deck is open and only for what the person can see).
const liveInterval = 5 * time.Second

// liveConcurrencyCap bounds how many probes run at once.
const liveConcurrencyCap = 4

type liveProber func(ctx context.Context, tool discovery.Tool) launch.ProbeResult

type liveTickMsg time.Time

type liveResultMsg struct {
	tool   string
	result launch.ProbeResult
}

// preLaunchProbedMsg resumes a launch that was held for its pre-launch probe (R4: the probe runs
// off the UI thread, then hands its result to launch.Prepare through Request.Live). probedLive is
// false when only the tool's service needs were probed, so result is then meaningless; services
// holds one result per service need's target tool.
type preLaunchProbedMsg struct {
	tool       discovery.Tool
	spec       string
	mode       profile.LaunchMode
	inputs     map[string]string
	result     launch.ProbeResult
	probedLive bool
	services   map[string]launch.ProbeResult
	// anyway is the launch-anyway request the held launch carries to its resumed startLaunch.
	anyway bool
}

// liveState is the deck's in-memory live store. It is held by pointer so every Model copy shares
// it; its maps are mutated only from Update, never from a command goroutine. Nothing here is
// ever written to a file or a run row.
type liveState struct {
	states   map[string]launch.ProbeResult
	checked  map[string]time.Time
	inFlight map[string]bool
	// readyUntil, readyTools and readyOutcome hold the ready windows this deck opened (§22 c),
	// keyed by tool name: the deadline, the window itself, and the closed window's menu suffix.
	readyUntil   map[string]time.Time
	readyTools   map[string]readyWindow
	readyOutcome map[string]string
	ticking      bool
	ticker       func(time.Duration) tea.Cmd
	prober       liveProber
	context      context.Context
	cancel       context.CancelFunc
	slots        chan struct{}
}

func newLiveState(repositoryRoot string) *liveState {
	ctx, cancel := context.WithCancel(context.Background())
	return &liveState{
		states: map[string]launch.ProbeResult{}, checked: map[string]time.Time{}, inFlight: map[string]bool{},
		readyUntil: map[string]time.Time{}, readyTools: map[string]readyWindow{}, readyOutcome: map[string]string{},
		ticker: func(interval time.Duration) tea.Cmd {
			return tea.Tick(interval, func(at time.Time) tea.Msg { return liveTickMsg(at) })
		},
		prober: func(probeContext context.Context, tool discovery.Tool) launch.ProbeResult {
			return launch.Probe(probeContext, tool, repositoryRoot)
		},
		context: ctx, cancel: cancel, slots: make(chan struct{}, liveConcurrencyCap),
	}
}

// declaresProbe reports whether the tool carries a live.probe at all; only such tools are ever
// probed, and the rest keep an empty live cell.
func declaresProbe(tool discovery.Tool) bool {
	return tool.Problem == "" && tool.Manifest.Live != nil && tool.Manifest.Live.Probe != nil
}

// skipsWhenUp reports whether a launch of this (already effective, single-action) manifest is
// skipped when its probe says up. Services default to skip, tasks to launch.
func skipsWhenUp(tool discovery.Tool) bool {
	if !declaresProbe(tool) {
		return false
	}
	if whenUp := tool.Manifest.Live.WhenUp; whenUp != nil {
		return *whenUp == formats.LiveWhenUpSkip
	}
	return tool.Manifest.Lifecycle != nil && *tool.Manifest.Lifecycle == formats.ToolManifestLifecycleService
}

// probeCmd runs one probe off the UI thread, inside the concurrency cap and the deck's context.
func (live *liveState) probeCmd(tool discovery.Tool) tea.Cmd {
	return func() tea.Msg {
		return liveResultMsg{tool: tool.Name, result: live.runProbe(tool)}
	}
}

func (live *liveState) runProbe(tool discovery.Tool) launch.ProbeResult {
	select {
	case live.slots <- struct{}{}:
		defer func() { <-live.slots }()
	case <-live.context.Done():
		return launch.ProbeResult{State: launch.ProbeUnknown, Detail: "deck closed"}
	}
	return live.prober(live.context, tool)
}

// startLiveTicking returns the first tick exactly once; asking again returns nil, so there is
// never a second ticker.
func (model Model) startLiveTicking() tea.Cmd {
	live := model.live
	if live == nil || live.ticking {
		return nil
	}
	live.ticking = true
	return live.ticker(liveInterval)
}

func (model Model) cancelLive() {
	if model.live != nil {
		model.live.cancel()
	}
}

// probeTargets lists the tools worth probing right now: the screen's own (see screenProbeTargets)
// plus every tool with an open ready window, on screen or not.
func (model Model) probeTargets() []discovery.Tool {
	return model.withReadyWindowTargets(model.screenProbeTargets())
}

// screenProbeTargets lists the rows in the armory window, the run rows on screen, or the one
// open item; every other screen probes nothing.
func (model Model) screenProbeTargets() []discovery.Tool {
	if model.configuring || model.promptingEquip || model.promptingInputs || model.promptingSpec ||
		model.showEquipView || model.showMissions || model.showPanes {
		return nil
	}
	if model.showRuns {
		return model.runListProbeTargets()
	}
	if model.detail {
		if tool, ok := model.currentTool(); ok && declaresProbe(tool) {
			return []discovery.Tool{tool}
		}
		return nil
	}
	var targets []discovery.Tool
	for _, tool := range model.listWindow() {
		if declaresProbe(tool) {
			targets = append(targets, tool)
		}
	}
	return targets
}

// runListProbeTargets is the distinct catalog tools of the run rows on screen that declare a
// probe; a row whose tool left the catalog is never probed.
func (model Model) runListProbeTargets() []discovery.Tool {
	var targets []discovery.Tool
	seen := map[string]bool{}
	for _, row := range model.runListWindow() {
		if seen[row.Tool] {
			continue
		}
		seen[row.Tool] = true
		if tool, ok := model.toolByName(row.Tool); ok && declaresProbe(tool) {
			targets = append(targets, tool)
		}
	}
	return targets
}

// probeNow starts a probe for every target not already in flight.
func (model Model) probeNow(targets []discovery.Tool) tea.Cmd {
	live := model.live
	if live == nil {
		return nil
	}
	var commands []tea.Cmd
	for _, tool := range targets {
		if live.inFlight[tool.Name] {
			continue
		}
		live.inFlight[tool.Name] = true
		commands = append(commands, live.probeCmd(tool))
	}
	return tea.Batch(commands...)
}

func (model Model) probeVisible() tea.Cmd { return model.probeNow(model.probeTargets()) }

func (model Model) handleLiveTick() (tea.Model, tea.Cmd) {
	if model.live == nil || model.live.context.Err() != nil {
		return model, nil
	}
	model.expireReadyWindows()
	return model, tea.Batch(model.probeVisible(), model.live.ticker(model.liveTickInterval()))
}

func (model Model) handleLiveResult(message liveResultMsg) (tea.Model, tea.Cmd) {
	live := model.live
	if live == nil || live.context.Err() != nil {
		return model, nil
	}
	delete(live.inFlight, message.tool)
	live.states[message.tool] = message.result
	live.checked[message.tool] = model.clock()
	model.settleReadyWindow(message.tool, message.result)
	return model, nil
}

// needsPreLaunchProbe is true when launching would be skipped by an up probe. whenUp and
// lifecycle live on the action, so a multi-action tool is judged through the same ForAction
// projection the launch itself sees.
func (model Model) needsPreLaunchProbe(tool discovery.Tool) bool {
	if model.live == nil || model.launchAnyway {
		return false
	}
	if model.actionInFlight != "" {
		effective, err := manifest.ForAction(tool.Manifest, model.actionInFlight)
		if err != nil {
			return false
		}
		tool.Manifest = effective
	}
	return skipsWhenUp(tool)
}

// serviceNeedTools lists the distinct tools the tool's service needs name, in declaration order.
func (model Model) serviceNeedTools(tool discovery.Tool) []string {
	var names []string
	seen := map[string]bool{}
	for _, need := range tool.Manifest.Needs {
		name, _, ok := equip.ServiceDetails(need)
		if ok && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// needsPreLaunchHold is true when a launch must wait for an off-thread probe: its own live probe
// (whenUp skip) or the state of a service it needs.
func (model Model) needsPreLaunchHold(tool discovery.Tool) bool {
	return model.live != nil && (model.needsPreLaunchProbe(tool) || len(model.serviceNeedTools(tool)) > 0)
}

// preLaunchProbeCmd probes, off the UI thread and inside the concurrency cap, whatever the launch
// is held for: the tool's own probe only when it would be skipped by an up answer, and each
// service need's target tool. Prepare itself never probes; it consumes these results.
func (model Model) preLaunchProbeCmd(tool discovery.Tool, spec string, mode profile.LaunchMode, inputs map[string]string) tea.Cmd {
	live := model.live
	probeOwn := model.needsPreLaunchProbe(tool)
	serviceNames := model.serviceNeedTools(tool)
	catalog := model.catalog
	anyway := model.launchAnyway
	return func() tea.Msg {
		message := preLaunchProbedMsg{tool: tool, spec: spec, mode: mode, inputs: inputs, probedLive: probeOwn, anyway: anyway}
		if probeOwn {
			message.result = live.runProbe(tool)
		}
		message.services = probeServices(live, catalog, serviceNames)
		return message
	}
}

// probeServices probes each named service tool concurrently; a name the catalog does not hold is
// unknown, so the need it satisfies stays unmet.
func probeServices(live *liveState, catalog discovery.Catalog, names []string) map[string]launch.ProbeResult {
	results := make(map[string]launch.ProbeResult, len(names))
	var mutex sync.Mutex
	var group sync.WaitGroup
	for _, name := range names {
		group.Add(1)
		go func(name string) {
			defer group.Done()
			result := launch.ProbeResult{State: launch.ProbeUnknown, Detail: "not in the catalog"}
			if target, found := catalog.Find(name); found {
				result = live.runProbe(target)
			}
			mutex.Lock()
			results[name] = result
			mutex.Unlock()
		}(name)
	}
	group.Wait()
	return results
}

// handlePreLaunchProbed resumes the held launch with the probe results in hand. The held values
// are set for exactly this one startLaunch and cleared right after it.
func (model Model) handlePreLaunchProbed(message preLaunchProbedMsg) (tea.Model, tea.Cmd) {
	if model.live == nil || model.live.context.Err() != nil {
		return model, nil
	}
	if message.probedLive {
		model.live.states[message.tool.Name] = message.result
		model.launchLive = &message.result
	}
	for name, result := range message.services {
		model.live.states[name] = result
	}
	model.launchServices = message.services
	model.preLaunchProbed, model.launchAnyway = true, message.anyway
	resumed, command := model.startLaunch(message.tool, message.spec, message.mode, message.inputs)
	if typed, ok := resumed.(Model); ok {
		typed.launchLive, typed.launchServices, typed.preLaunchProbed, typed.launchAnyway = nil, nil, false, false
		return typed, command
	}
	return resumed, command
}

// describeLaunchError turns the launch seam's refusals into the line the deck shows.
func describeLaunchError(tool discovery.Tool, err error) string {
	if errors.Is(err, launch.ErrAlreadyRunning) {
		return tool.Title + " is already running."
	}
	var refused *launch.NeedsRefused
	if errors.As(err, &refused) && len(refused.Needs) > 0 {
		if _, _, isService := equip.ServiceDetails(refused.Needs[0].Need); isService && refused.Needs[0].Reason != "" {
			return tool.Title + " cannot start: " + refused.Needs[0].Reason
		}
	}
	return err.Error()
}

// listWindow is the armory rows on screen — the one computation both listView and the poll use.
func (model Model) listWindow() []discovery.Tool {
	tools := model.visibleTools()
	start, end := model.listWindowBounds(len(tools))
	return tools[start:end]
}

func (model Model) listWindowBounds(count int) (int, int) {
	capacity := listCapacity(model.layoutHeight(), 8+model.noticeLineCount()+uncataloguedOfferRows(model.uncatalogued))
	return visibleWindow(model.cursor, count, capacity)
}

// runListWindow is the run rows on screen — the one computation both runListView and the poll use.
func (model Model) runListWindow() []runs.Row {
	start, end := model.runListWindowBounds()
	return model.runRows[start:end]
}

func (model Model) runListWindowBounds() (int, int) {
	// title, blank, rows, hint, blank, notice, help.
	capacity := listCapacity(model.layoutHeight(), 6+model.noticeLineCount())
	return visibleWindow(model.runCursor, len(model.runRows), capacity)
}

// liveCell is the armory's live column text for one tool: "–" for a tool with no probe, the
// last state word otherwise ("unknown" until the first result lands).
func (model Model) liveCell(tool discovery.Tool) string {
	if !declaresProbe(tool) {
		return "–"
	}
	return liveStateLabel(model, tool.Name)
}

// runLiveCell is the run list's live column for one row: the row's tool's live state, "–" when
// the tool declares no probe or has left the catalog. It is its own column, so the run's outcome
// word is never replaced by it.
func (model Model) runLiveCell(row runs.Row, cell func(lipgloss.Style, string) string) string {
	text := "–"
	if tool, ok := model.toolByName(row.Tool); ok {
		text = model.liveCell(tool)
	}
	return cell(liveStateStyle(text), pad(text, liveColumnWidth))
}

// liveCellStyled pads the live cell to its column and colors it through liveStateStyle. A styled
// (selected or invalid) row keeps its own styling.
func (model Model) liveCellStyled(tool discovery.Tool, cell func(lipgloss.Style, string) string) string {
	text := model.liveCell(tool)
	return cell(liveStateStyle(text), pad(text, liveColumnWidth))
}

var liveUpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

func (model Model) showLiveColumn() bool {
	return model.layoutWidth() >= toolRowFixedWidth+liveColumnExtraWidth+minimumTitleWidth
}

// liveColumnSpend is the width the live column takes from the title column (0 when dropped).
func (model Model) liveColumnSpend() int {
	if model.showLiveColumn() {
		return liveColumnExtraWidth
	}
	return 0
}

func (model Model) liveSuffix(tool discovery.Tool, cell func(lipgloss.Style, string) string) string {
	if !model.showLiveColumn() {
		return ""
	}
	return "  " + model.liveCellStyled(tool, cell)
}

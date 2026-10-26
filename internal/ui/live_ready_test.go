package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/testfixture"
)

// scriptedProber answers every probe with whatever state the test last set, and records the
// names it was asked about.
type scriptedProber struct {
	mutex sync.Mutex
	state launch.ProbeState
	names []string
}

func (prober *scriptedProber) set(state launch.ProbeState) {
	prober.mutex.Lock()
	defer prober.mutex.Unlock()
	prober.state = state
}

func (prober *scriptedProber) probe(_ context.Context, tool discovery.Tool) launch.ProbeResult {
	prober.mutex.Lock()
	defer prober.mutex.Unlock()
	prober.names = append(prober.names, tool.Name)
	return launch.ProbeResult{State: prober.state}
}

func (prober *scriptedProber) countOf(name string) int {
	prober.mutex.Lock()
	defer prober.mutex.Unlock()
	count := 0
	for _, probed := range prober.names {
		if probed == name {
			count++
		}
	}
	return count
}

// readyStackTool declares four actions over one http probe, each with the given ready window:
// start (a task that exits 0), fail (a task that exits 1), serve (a service) and once (a task
// that is skipped while the probe says up).
func readyStackTool(t *testing.T, name, within string) discovery.Tool {
	t.Helper()
	ready := &formats.Ready{Within: formats.Duration(within)}
	skip := formats.ActionWhenUpSkip
	return discovery.Tool{
		Name: name, Title: name, Source: discovery.Source{Kind: discovery.SourceCatalog},
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Live:   &formats.Live{Probe: map[string]any{"http": "http://127.0.0.1:1/health"}},
			Actions: []formats.Action{
				{Name: "start", Title: strPtr("Start"), Args: []string{"fixture:exit", "0"}, Ready: ready},
				{Name: "fail", Title: strPtr("Fail"), Args: []string{"fixture:exit", "1"}, Ready: ready},
				{Name: "serve", Title: strPtr("Serve"), Args: []string{"fixture:exit", "0"}, Ready: ready,
					Lifecycle: actionLifecyclePtr(formats.ActionLifecycleService)},
				{Name: "once", Title: strPtr("Once"), Args: []string{"fixture:exit", "0"}, Ready: ready, WhenUp: &skip},
			},
		},
	}
}

// readyModel is a live-state deck over tools with a clock the test moves by hand.
func readyModel(t *testing.T, tools []discovery.Tool, prober *scriptedProber) (Model, *time.Time) {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	model := newModel(discovery.Catalog{Tools: tools}, t.TempDir(), testStore(t))
	model.environment = []string{"TERM_PROGRAM=unknown"}
	model.live.prober = prober.probe
	model.live.ticker = func(time.Duration) tea.Cmd { return func() tea.Msg { return liveTickMsg(time.Time{}) } }
	now := time.Unix(1000, 0)
	model.clock = func() time.Time { return now }
	return updateModel(t, model, tea.WindowSizeMsg{Width: 120, Height: 40}), &now
}

// runCapturedAction launches one task action of tool through the captured route and delivers its
// completion, returning the model and the command the completion produced.
func runCapturedAction(t *testing.T, model Model, tool discovery.Tool, action string) (Model, tea.Cmd) {
	t.Helper()
	updated, command := model.beginActionLaunch(tool, action)
	model = requireModel(t, updated)
	if command == nil || model.capturedRunID == "" {
		t.Fatalf("captured route did not start: command nil=%v notice=%q", command == nil, model.notice)
	}
	updated, followUp := model.Update(runCapturedCommand(t, command))
	return requireModel(t, updated), followUp
}

// deliver runs a command and feeds every message it yields back through Update, returning the
// model and the commands those updates produced.
func deliver(t *testing.T, model Model, command tea.Cmd) (Model, []tea.Cmd) {
	t.Helper()
	var followUps []tea.Cmd
	for _, message := range runBatch(command) {
		updated, next := model.Update(message)
		model = requireModel(t, updated)
		followUps = append(followUps, next)
	}
	return model, followUps
}

func stateLine(model Model) string {
	for _, line := range strings.Split(model.View(), "\n") {
		if strings.Contains(line, "State:") {
			return line
		}
	}
	return ""
}

func TestCapturedStartOpensAReadyWindowShowingStarting(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model, followUp := runCapturedAction(t, model, tool, "start")
	if label := liveStateLabel(model, "stack"); label != "starting" {
		t.Fatalf("label after a captured exit 0 = %q, want starting", label)
	}
	if !strings.Contains(stateLine(model), "starting") {
		t.Fatalf("menu State line = %q, want starting", stateLine(model))
	}
	if cell := model.liveCell(tool); cell != "starting" {
		t.Fatalf("armory cell = %q, want starting", cell)
	}
	model, _ = deliver(t, model, followUp)
	if label := liveStateLabel(model, "stack"); label != "starting" {
		t.Fatalf("a down probe inside the window must keep the word starting, got %q", label)
	}
	if prober.countOf("stack") == 0 {
		t.Fatal("opening a window probed nothing")
	}
	model.detail = false
	model = pressRuneKey(t, model, 'r')
	if !strings.Contains(model.View(), "starting") {
		t.Fatalf("runs list shows no starting cell:\n%s", model.View())
	}
}

func pressRuneKey(t *testing.T, model Model, letter rune) Model {
	t.Helper()
	return updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{letter}})
}

func TestServiceTabStartOpensAReadyWindow(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model.environment = []string{"TMUX=/tmp/tmux"}
	installFakeExecutable(t, "tmux")
	updated, command := model.beginActionLaunch(tool, "serve")
	model = requireModel(t, updated)
	held := runBatch(command)
	if len(held) != 1 {
		t.Fatalf("a service launch should hold for its pre-launch probe, got %d messages", len(held))
	}
	updated, command = model.Update(held[0])
	model = requireModel(t, updated)
	if command == nil {
		t.Fatalf("the held service launch did not resume; notice=%q", model.notice)
	}
	updated, followUp := model.Update(command())
	model = requireModel(t, updated)
	if rows := rowsOf(t, model.runStore); len(rows) != 1 || rows[0].Mode != profile.ModeTab {
		t.Fatalf("rows = %+v, want one tab row", rows)
	}
	if label := liveStateLabel(model, "stack"); label != "starting" {
		t.Fatalf("label after the service tab opened = %q, want starting", label)
	}
	before := prober.countOf("stack")
	deliver(t, model, followUp)
	if prober.countOf("stack") <= before {
		t.Fatal("the service window did not probe the tool")
	}
}

func windowThatCloses(t *testing.T, answer launch.ProbeState) (Model, *scriptedProber) {
	t.Helper()
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model, followUp := runCapturedAction(t, model, tool, "start")
	if liveStateLabel(model, "stack") != "starting" {
		t.Fatal("precondition: the window is not open")
	}
	model, _ = deliver(t, model, followUp)
	prober.set(answer)
	model, _ = deliver(t, model, model.probeReadyWindows())
	return model, prober
}

func TestReadyWindowClosesOnUp(t *testing.T) {
	model, _ := windowThatCloses(t, launch.ProbeUp)
	if label := liveStateLabel(model, "stack"); label != "up" {
		t.Fatalf("label after an up answer = %q, want up", label)
	}
	if !strings.Contains(model.View(), "Start: exit 0 — ok · up") {
		t.Fatalf("result line lacks the up suffix:\n%s", model.View())
	}
	before := len(model.probeTargets())
	if before != 1 {
		// the detail tool itself still probes on screen; a closed window adds nothing more
		t.Fatalf("probe targets after the window closed = %d, want only the open item", before)
	}
}

func TestReadyWindowClosesOnIdle(t *testing.T) {
	model, _ := windowThatCloses(t, launch.ProbeIdle)
	if label := liveStateLabel(model, "stack"); label != "idle" {
		t.Fatalf("label after an idle answer = %q, want idle", label)
	}
	if !strings.Contains(model.View(), "Start: exit 0 — ok · up") {
		t.Fatalf("an idle answer should close the window with the up suffix:\n%s", model.View())
	}
}

func TestReadyWindowExpiresWithoutChangingTheRun(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, now := readyModel(t, []discovery.Tool{tool}, prober)
	model, followUp := runCapturedAction(t, model, tool, "start")
	before := rowsOf(t, model.runStore)
	model, _ = deliver(t, model, followUp)
	if label := liveStateLabel(model, "stack"); label != "starting" {
		t.Fatalf("inside the window the word is %q, want starting", label)
	}
	*now = now.Add(3 * time.Minute)
	model, _ = deliver(t, model, model.probeReadyWindows())
	model = updateModel(t, model, liveTickMsg(*now))
	if label := liveStateLabel(model, "stack"); label != "down" {
		t.Fatalf("after the window expired the word is %q, want the real state down", label)
	}
	if !strings.Contains(model.View(), "Start: exit 0 — ok · not up within 2m0s") {
		t.Fatalf("result line lacks the expiry suffix:\n%s", model.View())
	}
	after := rowsOf(t, model.runStore)
	if len(before) != 1 || len(after) != 1 || before[0].Outcome != after[0].Outcome ||
		before[0].ExitCode == nil || after[0].ExitCode == nil || *before[0].ExitCode != *after[0].ExitCode || *after[0].ExitCode != 0 {
		t.Fatalf("the window changed the run: before=%+v after=%+v", before, after)
	}
}

func TestReadyWindowProbesEvenWhenTheRowIsOffScreen(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	var tools []discovery.Tool
	for index := 0; index < 30; index++ {
		tools = append(tools, probedTool("filler-"+string(rune('a'+index%26))+string(rune('a'+index/26)), false))
	}
	target := readyStackTool(t, "zz-stack", "2m")
	tools = append(tools, target)
	model, _ := readyModel(t, tools, prober)
	model.height = 14
	model, followUp := runCapturedAction(t, model, target, "start")
	model, _ = deliver(t, model, followUp)
	model.detail, model.showRuns = false, false
	for _, onScreen := range model.listWindow() {
		if onScreen.Name == target.Name {
			t.Fatal("test needs the target row off screen")
		}
	}
	before := prober.countOf(target.Name)
	updated, tick := model.Update(liveTickMsg(time.Time{}))
	model = requireModel(t, updated)
	runBatch(tick)
	if prober.countOf(target.Name) <= before {
		t.Fatal("a tick during an open window did not probe the off-screen tool")
	}
	if label := liveStateLabel(model, target.Name); label != "starting" {
		t.Fatalf("label = %q, want starting", label)
	}
}

func TestFailedRunOpensNoReadyWindow(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model, followUp := runCapturedAction(t, model, tool, "fail")
	if label := liveStateLabel(model, "stack"); label == "starting" {
		t.Fatal("a non-zero exit opened a ready window")
	}
	deliver(t, model, followUp)
	if prober.countOf("stack") != 0 {
		t.Fatalf("a failed run probed %d times, want none", prober.countOf("stack"))
	}
}

func TestAlreadyRunningSkipOpensNoReadyWindow(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	updated, command := model.beginActionLaunch(tool, "once")
	model = requireModel(t, updated)
	model, _ = deliver(t, model, command)
	if !strings.Contains(model.notice, "already running") {
		t.Fatalf("notice = %q, want the already-running refusal", model.notice)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("a skipped launch wrote %d rows", len(rows))
	}
	delete(model.live.states, "stack")
	if label := liveStateLabel(model, "stack"); label == "starting" {
		t.Fatal("an already-running skip opened a ready window")
	}
}

func TestReadyWindowTicksAtTheReadyPollInterval(t *testing.T) {
	previous := launch.ReadyPollInterval
	launch.ReadyPollInterval = 7 * time.Millisecond
	t.Cleanup(func() { launch.ReadyPollInterval = previous })
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	var intervals []time.Duration
	model.live.ticker = func(interval time.Duration) tea.Cmd {
		intervals = append(intervals, interval)
		return nil
	}
	updateModel(t, model, liveTickMsg(time.Time{}))
	model, _ = runCapturedAction(t, model, tool, "start")
	updateModel(t, model, liveTickMsg(time.Time{}))
	if len(intervals) != 2 || intervals[0] != liveInterval || intervals[1] != 7*time.Millisecond {
		t.Fatalf("tick intervals = %v, want [%v 7ms]", intervals, liveInterval)
	}
}

func TestAcceptedServiceOfferGetsAReadyWindow(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	needy := offerNeedyTool(t, serviceNeed("stack", "start"))
	target := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{needy, target}, prober)
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	if command == nil {
		t.Fatal("no pre-launch probe command for a tool with a service need")
	}
	model = updateModel(t, model, runBatch(command)[0])
	if model.serviceOffer == nil {
		t.Fatalf("no offer after the refusal; notice=%q", model.notice)
	}
	model, command = pressRune(t, model, 'y')
	if command == nil || model.capturedRunID == "" {
		t.Fatalf("y did not start the offered action: notice=%q", model.notice)
	}
	updated, _ := model.Update(runCapturedCommand(t, command))
	model = requireModel(t, updated)
	if label := liveStateLabel(model, "stack"); label != "starting" {
		t.Fatalf("label after the offered start = %q, want starting", label)
	}
	for _, row := range rowsOf(t, model.runStore) {
		if row.Tool == "needy" {
			t.Fatalf("the original launch resumed: %+v", row)
		}
	}
}

func TestLiveColumnFitsTheWordStarting(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeDown}
	tool := readyStackTool(t, "stack", "2m")
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model, _ = runCapturedAction(t, model, tool, "start")
	model.detail = false
	if view := model.View(); !strings.Contains(view, "starting") {
		t.Fatalf("tool list truncates the live word:\n%s", view)
	}
	model = pressRuneKey(t, model, 'r')
	if view := model.View(); !strings.Contains(view, "starting") {
		t.Fatalf("runs list truncates the live word:\n%s", view)
	}
}

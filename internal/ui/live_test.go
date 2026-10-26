package ui

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/testfixture"
)

func probedTool(name string, probed bool) discovery.Tool {
	tool := discovery.Tool{Name: name, Title: name, Source: discovery.Source{Kind: discovery.SourceCatalog}}
	if probed {
		tool.Manifest.Live = &formats.Live{Probe: map[string]any{"http": "http://127.0.0.1:1/health"}}
	}
	return tool
}

// recordingProber is a fake prober that records every call and the peak concurrency.
type recordingProber struct {
	mutex      sync.Mutex
	names      []string
	active     int32
	peak       int32
	state      launch.ProbeState
	blockUntil <-chan struct{}
}

func (prober *recordingProber) probe(ctx context.Context, tool discovery.Tool) launch.ProbeResult {
	current := atomic.AddInt32(&prober.active, 1)
	defer atomic.AddInt32(&prober.active, -1)
	for {
		peak := atomic.LoadInt32(&prober.peak)
		if current <= peak || atomic.CompareAndSwapInt32(&prober.peak, peak, current) {
			break
		}
	}
	prober.mutex.Lock()
	prober.names = append(prober.names, tool.Name)
	prober.mutex.Unlock()
	if prober.blockUntil != nil {
		select {
		case <-prober.blockUntil:
		case <-ctx.Done():
			return launch.ProbeResult{State: launch.ProbeUnknown, Detail: "cancelled"}
		}
	}
	state := prober.state
	if state == "" {
		state = launch.ProbeUp
	}
	return launch.ProbeResult{State: state}
}

func (prober *recordingProber) called() []string {
	prober.mutex.Lock()
	defer prober.mutex.Unlock()
	return append([]string(nil), prober.names...)
}

func liveModel(t *testing.T, tools []discovery.Tool, prober *recordingProber) Model {
	t.Helper()
	model := newModel(discovery.Catalog{Tools: tools}, t.TempDir(), testStore(t))
	model.live.prober = prober.probe
	model.live.ticker = func(time.Duration) tea.Cmd { return func() tea.Msg { return liveTickMsg(time.Time{}) } }
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 100, Height: 40})
	return model
}

// runBatch executes a command (flattening a tea.Batch) and returns every message it yields.
func runBatch(command tea.Cmd) []tea.Msg {
	if command == nil {
		return nil
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		var messages []tea.Msg
		for _, inner := range batch {
			messages = append(messages, runBatch(inner)...)
		}
		return messages
	}
	if message == nil {
		return nil
	}
	return []tea.Msg{message}
}

func resultMessages(messages []tea.Msg) []liveResultMsg {
	var results []liveResultMsg
	for _, message := range messages {
		if result, ok := message.(liveResultMsg); ok {
			results = append(results, result)
		}
	}
	return results
}

func TestLiveTickProbesOnlyVisibleRowsWithProbes(t *testing.T) {
	var tools []discovery.Tool
	for index := 0; index < 30; index++ {
		tools = append(tools, probedTool("tool-"+string(rune('a'+index%26))+string(rune('a'+index/26)), index%2 == 0))
	}
	prober := &recordingProber{}
	model := liveModel(t, tools, prober)
	model.height = 14
	window := model.listWindow()
	if len(window) >= len(tools) {
		t.Fatalf("test needs a window smaller than the catalog, got %d of %d", len(window), len(tools))
	}
	want := 0
	for _, tool := range window {
		if declaresProbe(tool) {
			want++
		}
	}
	runBatch(model.probeVisible())
	got := prober.called()
	if len(got) != want || want == 0 {
		t.Fatalf("probed %v, want %d visible probed rows only", got, want)
	}
	inWindow := map[string]bool{}
	for _, tool := range window {
		inWindow[tool.Name] = true
	}
	for _, name := range got {
		if !inWindow[name] {
			t.Fatalf("probed %s which is not on screen", name)
		}
	}
}

func TestLiveTickSkipsRowsAlreadyInFlight(t *testing.T) {
	prober := &recordingProber{}
	model := liveModel(t, []discovery.Tool{probedTool("alpha", true)}, prober)
	first := model.probeVisible()
	second := model.probeVisible()
	if second != nil && len(runBatch(second)) != 0 {
		t.Fatal("a row already in flight was probed a second time")
	}
	runBatch(first)
	if len(prober.called()) != 1 {
		t.Fatalf("probe calls = %v, want exactly one", prober.called())
	}
}

func TestLiveTickNeverStartsASecondTicker(t *testing.T) {
	model := liveModel(t, []discovery.Tool{probedTool("alpha", true)}, &recordingProber{})
	if model.startLiveTicking() == nil {
		t.Fatal("first startLiveTicking returned no tick")
	}
	if model.startLiveTicking() != nil {
		t.Fatal("second startLiveTicking returned a second ticker")
	}
}

func TestLiveResultUpdatesColumn(t *testing.T) {
	prober := &recordingProber{state: launch.ProbeUp}
	model := liveModel(t, []discovery.Tool{probedTool("alpha", true)}, prober)
	for _, message := range runBatch(model.probeVisible()) {
		model = updateModel(t, model, message)
	}
	if view := model.View(); !strings.Contains(view, "up") {
		t.Fatalf("view lacks the live state:\n%s", view)
	}
	if label := liveStateLabel(model, "alpha"); label != "up" {
		t.Fatalf("liveStateLabel = %q, want up", label)
	}
}

func TestUndeclaredToolShowsNoLiveStateAndSendsNoProbe(t *testing.T) {
	prober := &recordingProber{}
	model := liveModel(t, []discovery.Tool{probedTool("plain", false)}, prober)
	if cell := model.liveCell(model.catalog.Tools[0]); cell != "–" {
		t.Fatalf("live cell = %q, want –", cell)
	}
	runBatch(model.probeVisible())
	if len(prober.called()) != 0 {
		t.Fatalf("a tool without live.probe was probed: %v", prober.called())
	}
	if label := liveStateLabel(model, "plain"); label != "unknown" {
		t.Fatalf("label = %q, want unknown", label)
	}
}

func TestQuitCancelsInFlightProbes(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	prober := &recordingProber{blockUntil: release}
	model := liveModel(t, []discovery.Tool{probedTool("alpha", true)}, prober)
	command := model.probeVisible()
	model.quit()
	done := make(chan []tea.Msg, 1)
	go func() { done <- runBatch(command) }()
	select {
	case messages := <-done:
		results := resultMessages(messages)
		if len(results) != 1 || results[0].result.State != launch.ProbeUnknown {
			t.Fatalf("results = %+v, want one unknown", results)
		}
		updated, _ := model.handleLiveResult(results[0])
		if _, stored := updated.(Model).live.states["alpha"]; stored {
			t.Fatal("a result arriving after quit was stored")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("in-flight probe did not stop within 200ms of quit")
	}
}

func TestLiveConcurrencyCapIsFour(t *testing.T) {
	release := make(chan struct{})
	prober := &recordingProber{blockUntil: release}
	var tools []discovery.Tool
	for index := 0; index < 8; index++ {
		tools = append(tools, probedTool("tool-"+string(rune('a'+index)), true))
	}
	model := liveModel(t, tools, prober)
	command := model.probeNow(tools)
	batch := command().(tea.BatchMsg)
	var group sync.WaitGroup
	for _, inner := range batch {
		group.Add(1)
		go func() { defer group.Done(); inner() }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&prober.active) < liveConcurrencyCap && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	group.Wait()
	if peak := atomic.LoadInt32(&prober.peak); peak != liveConcurrencyCap {
		t.Fatalf("peak concurrent probes = %d, want exactly %d", peak, liveConcurrencyCap)
	}
	if len(prober.called()) != 8 {
		t.Fatalf("probed %d tools, want 8", len(prober.called()))
	}
}

func TestOpeningTheItemTriggersAProbe(t *testing.T) {
	prober := &recordingProber{}
	model := liveModel(t, []discovery.Tool{probedTool("alpha", true)}, prober)
	updated, command := model.updateListKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.(Model).detail {
		t.Fatal("enter did not open the item")
	}
	runBatch(command)
	if got := prober.called(); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("probed %v on open, want [alpha]", got)
	}
}

func TestListWindowMatchesRenderedRows(t *testing.T) {
	var tools []discovery.Tool
	for index := 0; index < 25; index++ {
		tools = append(tools, probedTool("row-"+string(rune('a'+index)), true))
	}
	model := liveModel(t, tools, &recordingProber{})
	model.height = 12
	for cursor := 0; cursor < len(tools); cursor++ {
		model.cursor = cursor
		view := model.View()
		window := map[string]bool{}
		for _, tool := range model.listWindow() {
			window[tool.Name] = true
		}
		for _, tool := range tools {
			if rendered := strings.Contains(view, tool.Name); rendered != window[tool.Name] {
				t.Fatalf("cursor %d: %s rendered=%v inWindow=%v", cursor, tool.Name, rendered, window[tool.Name])
			}
		}
	}
}

func serviceTool(name string) discovery.Tool {
	tool := probedTool(name, true)
	service := formats.ToolManifestLifecycleService
	tool.Manifest.Lifecycle = &service
	return tool
}

func TestPreLaunchProbeRunsOffTheUpdateThread(t *testing.T) {
	prober := &recordingProber{state: launch.ProbeUp}
	tool := serviceTool("svc")
	model := liveModel(t, []discovery.Tool{tool}, prober)
	updated, command := model.startLaunch(tool, "", profile.ModeForeground, nil)
	if len(prober.called()) != 0 {
		t.Fatal("the pre-launch probe ran inside Update")
	}
	if command == nil {
		t.Fatal("no probe command returned")
	}
	messages := runBatch(command)
	if len(prober.called()) != 1 || len(messages) != 1 {
		t.Fatalf("probe calls=%v messages=%d", prober.called(), len(messages))
	}
	if _, ok := messages[0].(preLaunchProbedMsg); !ok {
		t.Fatalf("message = %T, want preLaunchProbedMsg", messages[0])
	}
	_ = updated
}

func TestLaunchRequestCarriesPreLaunchResult(t *testing.T) {
	tool := serviceTool("svc")
	model := liveModel(t, []discovery.Tool{tool}, &recordingProber{})
	result := launch.ProbeResult{State: launch.ProbeDown}
	model.launchLive = &result
	if request := model.launchRequest(tool, "", nil); request.Live != &result {
		t.Fatal("Request.Live did not carry the pre-launch probe result")
	}
}

func TestAlreadyRunningShowsNotice(t *testing.T) {
	tool := serviceTool("svc")
	tool.Title = "Service"
	if got := describeLaunchError(tool, launch.ErrAlreadyRunning); got != "Service is already running." {
		t.Fatalf("notice = %q", got)
	}
	if got := describeLaunchError(tool, errors.New("boom")); got != "boom" {
		t.Fatalf("notice = %q", got)
	}
}

func TestTaskWithoutWhenUpSkipDoesNotProbeBeforeLaunch(t *testing.T) {
	model := liveModel(t, []discovery.Tool{probedTool("task", true)}, &recordingProber{})
	if model.needsPreLaunchProbe(probedTool("task", true)) {
		t.Fatal("a task defaults to whenUp launch and must not pre-probe")
	}
}

// needyTask is a plain task (no probe, no whenUp skip) that needs the named service tool.
func needyTask(t *testing.T, name, serviceName string) discovery.Tool {
	tool := discovery.Tool{Name: name, Title: "Needy", Source: discovery.Source{Kind: discovery.SourceCatalog}}
	tool.Manifest.Launch = formats.ToolManifestLaunch{Command: testfixture.Tool(t)}
	tool.Manifest.Needs = []formats.Need{map[string]any{"kind": "service", "tool": serviceName, "action": "start"}}
	return tool
}

// serviceNeedModel is a deck holding the needy task and the service it needs, with an isolated
// equip file so Prepare resolves needs against nothing but the held results.
func serviceNeedModel(t *testing.T, state launch.ProbeState) (Model, *recordingProber, discovery.Tool) {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	prober := &recordingProber{state: state}
	needy := needyTask(t, "needy", "svc")
	service := probedTool("svc", true)
	service.Title = "Service"
	return liveModel(t, []discovery.Tool{needy, service}, prober), prober, needy
}

func TestLaunchWithAServiceNeedProbesTheServiceOffTheUpdateThread(t *testing.T) {
	model, prober, needy := serviceNeedModel(t, launch.ProbeDown)
	updated, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	if len(prober.called()) != 0 {
		t.Fatal("the service probe ran inside Update")
	}
	if command == nil {
		t.Fatal("no probe command returned for a tool with a service need")
	}
	if entries, _ := os.ReadDir(model.runStore.StateDirectory); len(entries) != 0 {
		t.Fatalf("a run row was written before the probe message arrived: %v", entries)
	}
	if !strings.Contains(updated.(Model).notice, "Checking what Needy needs") {
		t.Fatalf("notice = %q", updated.(Model).notice)
	}
	messages := runBatch(command)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	probed, ok := messages[0].(preLaunchProbedMsg)
	if !ok || probed.probedLive || probed.services["svc"].State != launch.ProbeDown {
		t.Fatalf("message = %#v, want a services-only preLaunchProbedMsg with svc down", messages[0])
	}
}

func TestDeckRefusesWhenTheNeededServiceIsDown(t *testing.T) {
	model, _, needy := serviceNeedModel(t, launch.ProbeDown)
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	resumed, _ := model.Update(runBatch(command)[0])
	notice := resumed.(Model).notice
	if !strings.Contains(notice, "Needy cannot start: service svc is down") {
		t.Fatalf("notice = %q, want the service named with its state", notice)
	}
	if rows, _ := model.runStore.List(); len(rows) != 0 {
		t.Fatalf("a refused launch wrote %d rows", len(rows))
	}
	if model.live.states["svc"].State != launch.ProbeDown {
		t.Fatalf("armory state for svc = %+v, want the probed down", model.live.states["svc"])
	}
}

func TestDeckLaunchesWhenTheNeededServiceIsUp(t *testing.T) {
	model, _, needy := serviceNeedModel(t, launch.ProbeUp)
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	resumed, resumedCommand := model.Update(runBatch(command)[0])
	if strings.Contains(resumed.(Model).notice, "cannot start") {
		t.Fatalf("notice = %q, want no refusal", resumed.(Model).notice)
	}
	if resumedCommand == nil {
		t.Fatal("an up service must let the launch start (an exec command)")
	}
	if rows, _ := model.runStore.List(); len(rows) != 1 {
		t.Fatalf("rows = %d, want the launch's one row", len(rows))
	}
}

func TestServiceOnlyHoldDoesNotSetLaunchLive(t *testing.T) {
	model, _, needy := serviceNeedModel(t, launch.ProbeUp)
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	message := runBatch(command)[0].(preLaunchProbedMsg)
	model.launchLive = nil
	resumedModel, _ := model.handlePreLaunchProbed(message)
	seen := resumedModel.(Model)
	if message.probedLive || seen.launchLive != nil {
		t.Fatalf("probedLive=%v launchLive=%v, want a services-only hold to leave Request.Live unset", message.probedLive, seen.launchLive)
	}
}

func TestHeldServiceResultsAreClearedAfterTheLaunch(t *testing.T) {
	model, _, needy := serviceNeedModel(t, launch.ProbeUp)
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	resumed, _ := model.Update(runBatch(command)[0])
	typed := resumed.(Model)
	if typed.launchServices != nil || typed.launchLive != nil || typed.preLaunchProbed {
		t.Fatalf("held state survived the resumed launch: services=%v live=%v probed=%v", typed.launchServices, typed.launchLive, typed.preLaunchProbed)
	}
}

func TestServiceNeedTargetMissingFromTheCatalogIsUnknown(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	needy := needyTask(t, "needy", "ghost")
	model := liveModel(t, []discovery.Tool{needy}, &recordingProber{})
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	probed := runBatch(command)[0].(preLaunchProbedMsg)
	result := probed.services["ghost"]
	if result.State != launch.ProbeUnknown || !strings.Contains(result.Detail, "not in the catalog") {
		t.Fatalf("ghost = %+v, want unknown / not in the catalog", result)
	}
}

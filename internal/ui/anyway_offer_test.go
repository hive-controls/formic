package ui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/testfixture"
)

const alreadyRunningHint = "Stack is already running. Press a to launch anyway."

// anywayTool is a probed tool with two actions that are skipped while the probe says up: plain,
// and gated, which asks first. needsService adds a service need on the tool named "svc".
func anywayTool(t *testing.T, needsService bool) discovery.Tool {
	t.Helper()
	skip := formats.ActionWhenUpSkip
	tool := discovery.Tool{
		Name: "stack", Title: "Stack", Source: discovery.Source{Kind: discovery.SourceCatalog},
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Live:   &formats.Live{Probe: map[string]any{"http": "http://127.0.0.1:1/health"}},
			Actions: []formats.Action{
				{Name: "plain", Title: strPtr("Plain"), Args: []string{"fixture:exit", "0"}, WhenUp: &skip},
				{Name: "gated", Title: strPtr("Gated"), Args: []string{"fixture:exit", "0"}, WhenUp: &skip,
					Confirm: strPtr("Really?")},
				{Name: "other", Title: strPtr("Other"), Args: []string{"fixture:exit", "0"}},
			},
		},
	}
	if needsService {
		tool.Manifest.Needs = []formats.Need{map[string]any{"kind": "service", "tool": "svc"}}
	}
	return tool
}

// skippedModel launches one action of a probed tool whose probe says up and returns the deck as it
// stands after the already-running skip, in the tool's detail view.
func skippedModel(t *testing.T, tools []discovery.Tool, prober *scriptedProber, action string) Model {
	t.Helper()
	model, _ := readyModel(t, tools, prober)
	model.detail = true
	updated, command := model.beginActionLaunch(tools[0], action)
	model = requireModel(t, updated)
	if model.confirmingAction {
		model, command = pressRune(t, model, 'y')
	}
	model, _ = deliver(t, model, command)
	return model
}

func TestAlreadyRunningNoticeOffersLaunchAnyway(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	model := skippedModel(t, []discovery.Tool{anywayTool(t, false)}, prober, "plain")
	if model.notice != alreadyRunningHint {
		t.Fatalf("notice = %q, want %q", model.notice, alreadyRunningHint)
	}
	offer := model.anywayOffer
	if offer == nil || offer.tool.Name != "stack" || offer.action != "plain" {
		t.Fatalf("offer = %+v, want stack · plain", offer)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("a skipped launch wrote %d rows", len(rows))
	}
}

func TestPressingAAfterTheSkipLaunchesWithAnyway(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tool := anywayTool(t, false)
	model := skippedModel(t, []discovery.Tool{tool}, prober, "plain")
	model, command := pressRune(t, model, 'a')
	if command == nil || model.capturedRunID == "" {
		t.Fatalf("pressing a started nothing; notice = %q", model.notice)
	}
	rows := rowsOf(t, model.runStore)
	if len(rows) != 1 || rows[0].Tool != "stack" || rows[0].Action != "plain" {
		t.Fatalf("rows = %+v, want one stack · plain row", rows)
	}
	if model.launchAnyway || model.anywayOffer != nil {
		t.Fatalf("launchAnyway=%v offer=%+v, want both cleared after the launch", model.launchAnyway, model.anywayOffer)
	}

	// The request itself carries Anyway, and with it Prepare spawns past an up probe.
	carrying := model
	carrying.launchAnyway, carrying.actionInFlight = true, "plain"
	up := launch.ProbeResult{State: launch.ProbeUp}
	carrying.launchLive = &up
	request := carrying.launchRequest(tool, "", nil)
	if !request.Anyway {
		t.Fatal("launchRequest dropped Anyway")
	}
	if _, err := launch.Prepare(request, profile.ModeCaptured); err != nil {
		t.Fatalf("Prepare with Anyway against an up probe = %v, want a launch", err)
	}
}

func TestLaunchAnywayStillRefusesAnUnmetServiceNeed(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tools := []discovery.Tool{anywayTool(t, true), probedTool("svc", true)}
	model := skippedModel(t, tools, prober, "plain")
	if model.anywayOffer == nil {
		t.Fatalf("no anyway offer while everything is up; notice = %q", model.notice)
	}
	prober.set(launch.ProbeDown)
	model, command := pressRune(t, model, 'a')
	if command == nil {
		t.Fatal("no held probe command: the service need was not probed")
	}
	model, _ = deliver(t, model, command)
	if !strings.Contains(model.notice, "cannot start") || !strings.Contains(model.notice, "svc") {
		t.Fatalf("notice = %q, want the unmet service refusal", model.notice)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("launch anyway spawned past an unmet need: %+v", rows)
	}
	if model.launchAnyway {
		t.Fatal("launchAnyway outlived the refused launch")
	}
}

func TestLaunchAnywayDoesNotAskAConfirmedActionAgain(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	model := skippedModel(t, []discovery.Tool{anywayTool(t, false)}, prober, "gated")
	if model.anywayOffer == nil {
		t.Fatalf("no offer after a confirmed, skipped action; notice = %q", model.notice)
	}
	// Another action finishing while the notice shows clears the in-flight state; the offer must
	// still carry what this launch had already answered.
	model.actionInFlight, model.actionConfirmed = "", false
	model, _ = pressRune(t, model, 'a')
	if model.confirmingAction {
		t.Fatal("launch anyway asked the confirm a second time")
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 1 || rows[0].Action != "gated" {
		t.Fatalf("rows = %+v, want one gated row", rows)
	}
}

// A skip is only reachable after the confirm was answered (Prepare checks confirm first), so an
// unanswered confirm cannot reach the offer; this pins the other half: Anyway never stands in for
// the answer.
func TestLaunchAnywayNeverSkipsAConfirmThatWasNotAnswered(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tool := anywayTool(t, false)
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model.launchAnyway, model.actionInFlight, model.actionConfirmed = true, "gated", false
	_, err := launch.Prepare(model.launchRequest(tool, "", nil), profile.ModeCaptured)
	var required *launch.ConfirmationRequired
	if !errors.As(err, &required) {
		t.Fatalf("Prepare = %v, want ConfirmationRequired", err)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("an unconfirmed launch wrote rows: %+v", rows)
	}
}

func TestOtherKeysDropTheAnywayOfferAndAreHandledNormally(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	model := skippedModel(t, []discovery.Tool{anywayTool(t, false)}, prober, "plain")
	cursorBefore := model.actionCursor
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.actionCursor == cursorBefore {
		t.Fatal("down was swallowed instead of moving the action cursor")
	}
	if model.anywayOffer != nil {
		t.Fatal("another key left the offer pending")
	}
	model, _ = pressRune(t, model, 'a')
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("a launched after the offer was dropped: %+v", rows)
	}
}

func TestAnywayOfferDoesNotAnswerWhenItsNoticeIsNotShowing(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tools := []discovery.Tool{anywayTool(t, false)}
	replaced := skippedModel(t, tools, prober, "plain")
	replaced.notice = "Something else happened."
	replaced, command := pressRune(t, replaced, 'a')
	if command != nil || len(rowsOf(t, replaced.runStore)) != 0 {
		t.Fatal("a launched from an offer whose notice had been replaced")
	}
	typing := skippedModel(t, tools, prober, "plain")
	typing.promptingSpec = true
	typing, _ = pressRune(t, typing, 'a')
	if len(rowsOf(t, typing.runStore)) != 0 {
		t.Fatal("a launched while a typed-input prompt was active")
	}
}

func TestNoAnywayOfferForAMissionLaunch(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tool := anywayTool(t, false)
	model, _ := readyModel(t, []discovery.Tool{tool}, prober)
	model.missionInFlight = &mission.Mission{}
	model = model.noteAlreadyRunning(tool, "", profile.ModeForeground, nil)
	if model.anywayOffer != nil || model.notice != "Stack is already running." {
		t.Fatalf("offer=%+v notice=%q, want the plain notice for a mission", model.anywayOffer, model.notice)
	}
}

func TestAOnTheToolListStillOpensTheAddOffer(t *testing.T) {
	userConfigRoot, repositoryRoot := t.TempDir(), t.TempDir()
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "toolkit.yaml"), "toolkit: 1\nname: offer-source\n")
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "tools", "offered.yaml"),
		"formic: 1\nname: offered\ntitle: Offered\nlaunch:\n  command: \"true\"\n")
	sourcePaths, err := catalog.SourcePaths(userConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	prober := &scriptedProber{state: launch.ProbeUp}
	model := skippedModel(t, []discovery.Tool{anywayTool(t, false)}, prober, "plain")
	model = model.WithCatalogContext(userConfigRoot, catalog.UncataloguedAt(repositoryRoot, sourcePaths))
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.detail || model.anywayOffer != nil {
		t.Fatalf("esc left detail=%v offer=%+v", model.detail, model.anywayOffer)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if model.uncatalogued != nil || model.notice != "Added the repo's toolkit to the catalog." {
		t.Fatalf("a on the list did not accept the add offer: uncatalogued=%+v notice=%q", model.uncatalogued, model.notice)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("a on the list launched something: %+v", rows)
	}
}

func TestDroppingTheAnywayOfferRevertsTheNoticeToThePlainSentence(t *testing.T) {
	prober := &scriptedProber{state: launch.ProbeUp}
	tools := []discovery.Tool{anywayTool(t, false)}
	model := skippedModel(t, tools, prober, "plain")
	cursorBefore := model.actionCursor
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.actionCursor == cursorBefore || model.notice != "Stack is already running." {
		t.Fatalf("cursor %d->%d notice = %q, want the cursor moved and the plain sentence", cursorBefore, model.actionCursor, model.notice)
	}

	userConfigRoot, repositoryRoot := t.TempDir(), t.TempDir()
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "toolkit.yaml"), "toolkit: 1\nname: offer-source\n")
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "tools", "offered.yaml"),
		"formic: 1\nname: offered\ntitle: Offered\nlaunch:\n  command: \"true\"\n")
	sourcePaths, err := catalog.SourcePaths(userConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	model = skippedModel(t, tools, prober, "plain")
	model = model.WithCatalogContext(userConfigRoot, catalog.UncataloguedAt(repositoryRoot, sourcePaths))
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(model.notice, "launch anyway") {
		t.Fatalf("notice after esc = %q, still promises a key", model.notice)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if model.uncatalogued != nil || strings.Contains(model.notice, "launch anyway") {
		t.Fatalf("a on the list: uncatalogued=%+v notice=%q", model.uncatalogued, model.notice)
	}
}

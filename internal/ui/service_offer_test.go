package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/testfixture"
)

// offerNeedyTool is a plain task that needs docker-desktop; need is the service need's body, so a
// case can name an action, name none, or name a tool that is not in the catalog.
func offerNeedyTool(t *testing.T, need map[string]any) discovery.Tool {
	tool := discovery.Tool{Name: "needy", Title: "Needy", Source: discovery.Source{Kind: discovery.SourceCatalog}}
	tool.Manifest.Launch = formats.ToolManifestLaunch{Command: testfixture.Tool(t)}
	tool.Manifest.Needs = []formats.Need{need}
	return tool
}

// offerTarget is the docker-desktop-shaped action tool, with a probe so the deck can ask whether
// it is up.
func offerTarget(t *testing.T) discovery.Tool {
	target := actionsTool(t)
	target.Source = discovery.Source{Kind: discovery.SourceCatalog}
	target.Manifest.Live = &formats.Live{Probe: map[string]any{"http": "http://127.0.0.1:1/health"}}
	return target
}

func serviceNeed(tool, action string) map[string]any {
	need := map[string]any{"kind": "service", "tool": tool}
	if action != "" {
		need["action"] = action
	}
	return need
}

// refusedModel holds needy (first, so accepting moves the cursor) and the target, launches needy
// while the target is down, and returns the model as it stands after the refusal.
func refusedModel(t *testing.T, need map[string]any, includeTarget bool) Model {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	needy := offerNeedyTool(t, need)
	tools := []discovery.Tool{needy}
	if includeTarget {
		tools = append(tools, offerTarget(t))
	}
	model := liveModel(t, tools, &recordingProber{state: launch.ProbeDown})
	_, command := model.startLaunch(needy, "", profile.ModeForeground, nil)
	if command == nil {
		t.Fatal("no pre-launch probe command for a tool with a service need")
	}
	return updateModel(t, model, runBatch(command)[0])
}

func pressRune(t *testing.T, model Model, letter rune) (Model, tea.Cmd) {
	t.Helper()
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{letter}})
	return requireModel(t, updated), command
}

func TestUnmetServiceNeedOffersTheNamedAction(t *testing.T) {
	model := refusedModel(t, serviceNeed("docker-desktop", "start"), true)
	offer := model.serviceOffer
	if offer == nil {
		t.Fatalf("no offer after the refusal; notice = %q", model.notice)
	}
	if offer.forTool != "needy" || offer.target.Name != "docker-desktop" || offer.action != "start" {
		t.Fatalf("offer = %+v, want needy -> docker-desktop · start", offer)
	}
	if !strings.HasPrefix(model.notice, "Needy cannot start: service docker-desktop is down") ||
		!strings.HasSuffix(model.notice, ". Run Docker Desktop · Start? [y/N]") {
		t.Fatalf("notice = %q", model.notice)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("an offer wrote %d rows; nothing runs before the person says y", len(rows))
	}
}

func TestAcceptingTheOfferRunsTheTargetActionAndDoesNotResume(t *testing.T) {
	model := refusedModel(t, serviceNeed("docker-desktop", "start"), true)
	model, command := pressRune(t, model, 'y')
	if command == nil || model.capturedRunID == "" {
		t.Fatalf("y did not start the action: command nil=%v notice=%q", command == nil, model.notice)
	}
	if current, _ := model.currentTool(); current.Name != "docker-desktop" || !model.detail {
		t.Fatalf("cursor on %q detail=%v, want the target's menu", current.Name, model.detail)
	}
	finished := runCapturedCommand(t, command)
	updated, followUp := model.Update(finished)
	model = requireModel(t, updated)
	if followUp != nil {
		t.Fatal("finishing the offered action returned a command; nothing may resume the original launch (R8)")
	}
	rows := rowsOf(t, model.runStore)
	if len(rows) != 1 || rows[0].Tool != "docker-desktop" || rows[0].Action != "start" {
		t.Fatalf("rows = %+v, want exactly docker-desktop · start and no row for needy", rows)
	}
	if model.serviceOffer != nil {
		t.Fatal("the offer survived being accepted")
	}
}

func TestAcceptingAConfirmingTargetStillAsks(t *testing.T) {
	model := refusedModel(t, serviceNeed("docker-desktop", "stop"), true)
	model, command := pressRune(t, model, 'y')
	if command != nil || !model.confirmingAction || len(rowsOf(t, model.runStore)) != 0 {
		t.Fatalf("accepting must reach the action's own confirm: command nil=%v confirming=%v", command == nil, model.confirmingAction)
	}
	if !strings.Contains(model.View(), "Stops EVERY container. Continue?") {
		t.Fatalf("the confirm prompt is not showing:\n%s", model.View())
	}
	model, command = pressRune(t, model, 'y')
	if command == nil || len(rowsOf(t, model.runStore)) != 1 {
		t.Fatalf("the second y should run the confirmed action: rows=%d", len(rowsOf(t, model.runStore)))
	}
}

func TestDecliningTheOfferChangesNothing(t *testing.T) {
	refused := refusedModel(t, serviceNeed("docker-desktop", "start"), true)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'n'}}, {Type: tea.KeyEnter}, {Type: tea.KeyEsc}} {
		updated, command := refused.Update(key)
		model := requireModel(t, updated)
		if command != nil || model.serviceOffer != nil || model.detail != refused.detail || model.cursor != refused.cursor ||
			len(rowsOf(t, model.runStore)) != 0 {
			t.Fatalf("key %q changed more than the offer: command nil=%v offer=%v detail=%v cursor=%d", key.String(), command == nil, model.serviceOffer, model.detail, model.cursor)
		}
		if strings.Contains(model.notice, "[y/N]") || !strings.HasPrefix(model.notice, "Needy cannot start: service docker-desktop is down") {
			t.Fatalf("notice after %q = %q, want the plain refusal", key.String(), model.notice)
		}
	}
}

func TestOfferForAServiceNeedWithoutActionOpensTheTargetsMenu(t *testing.T) {
	model := refusedModel(t, serviceNeed("docker-desktop", ""), true)
	if model.serviceOffer == nil || model.serviceOffer.action != "" || !strings.Contains(model.notice, "Open Docker Desktop") {
		t.Fatalf("offer=%+v notice=%q, want an offer to open the menu", model.serviceOffer, model.notice)
	}
	model, command := pressRune(t, model, 'y')
	current, _ := model.currentTool()
	if command != nil || current.Name != "docker-desktop" || !model.detail || model.actionInFlight != "" || model.notice != "" {
		t.Fatalf("y should only open the menu: command nil=%v tool=%q detail=%v inFlight=%q notice=%q", command == nil, current.Name, model.detail, model.actionInFlight, model.notice)
	}
	if rows := rowsOf(t, model.runStore); len(rows) != 0 {
		t.Fatalf("opening the menu wrote %d rows; the person picks the action", len(rows))
	}
}

func TestNoOfferWhenTheTargetIsNotInTheCatalog(t *testing.T) {
	for _, action := range []string{"start", ""} {
		model := refusedModel(t, serviceNeed("docker-desktop", action), false)
		if model.serviceOffer != nil || strings.Contains(model.notice, "[y/N]") {
			t.Fatalf("action %q: offer=%+v notice=%q, want the plain refusal", action, model.serviceOffer, model.notice)
		}
		if !strings.HasPrefix(model.notice, "Needy cannot start: service docker-desktop is unknown") {
			t.Fatalf("action %q: notice = %q", action, model.notice)
		}
	}
}

func TestAStaleOfferDoesNotSwallowKeys(t *testing.T) {
	model := refusedModel(t, serviceNeed("docker-desktop", "start"), true)
	model.notice = "something else replaced the offer line"
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	moved := requireModel(t, updated)
	if moved.serviceOffer != nil || moved.cursor != 1 {
		t.Fatalf("offer=%v cursor=%d, want the key to navigate and the stale offer gone", moved.serviceOffer, moved.cursor)
	}
}

func TestOfferInAViewThatDoesNotShowItDoesNotSwallowKeys(t *testing.T) {
	for name, enter := range map[string]func(*Model){
		"equip view": func(model *Model) { model.showEquipView = true },
		"input form": func(model *Model) { model.promptingSpec = true },
	} {
		model := refusedModel(t, serviceNeed("docker-desktop", "start"), true)
		enter(&model)
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		after := requireModel(t, updated)
		if after.serviceOffer != nil || len(rowsOf(t, after.runStore)) != 0 {
			t.Fatalf("%s: y answered an offer the person cannot see: offer=%v rows=%d", name, after.serviceOffer, len(rowsOf(t, after.runStore)))
		}
	}
}

func TestDroppedServiceOfferRevertsItsLingeringLineToThePlainRefusal(t *testing.T) {
	model := refusedModel(t, serviceNeed("docker-desktop", "start"), true)
	model.showEquipView = true
	after := updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if strings.Contains(after.notice, "[y/N]") || !strings.HasPrefix(after.notice, "Needy cannot start: service docker-desktop is down") {
		t.Fatalf("notice = %q, want the plain refusal once the offer stopped answering", after.notice)
	}
}

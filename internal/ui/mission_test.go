package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

// writeMissionSource mirrors cmd/formic/mission_test.go's own writeHealerSource fixture shape —
// a catalogued toolkit source carrying one tool manifest and, optionally, mission files under
// .formic/missions/.
func writeMissionSource(t *testing.T) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "missions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture-toolkit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

func writeMissionFile(t *testing.T, directory, fileName, contents string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, fileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMissionsSectionListsPersonalAndToolkitMissionsWithShadowMarkerAndExcludesInvalid is E2a's
// listing proof: personal missions and a catalogued toolkit's own missions both appear, a
// same-named personal definition shows the toolkit one as shadowed, and an invalid mission file
// (doctor's to report, never `mission list`'s or the deck's) is excluded entirely.
func TestMissionsSectionListsPersonalAndToolkitMissionsWithShadowMarkerAndExcludesInvalid(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())

	sourcePath := writeMissionSource(t)
	if _, err := catalog.Add(catalog.DefaultPath(configDir), sourcePath); err != nil {
		t.Fatal(err)
	}

	sharedYAML := "mission: 1\nname: shared\nsteps:\n  - tool: healer\n    with: { spec: value }\nevidence: evidence/\n"
	toolkitOnlyYAML := "mission: 1\nname: toolkit-only\nsteps:\n  - tool: healer\n    with: { spec: value }\nevidence: evidence/\n"
	invalidYAML := "mission: 1\nname: broken\nsteps: []\nevidence: e/\n"
	writeMissionFile(t, filepath.Join(configDir, "formic", "missions"), "shared.yaml", sharedYAML)
	writeMissionFile(t, filepath.Join(sourcePath, ".formic", "missions"), "shared.yaml", sharedYAML)
	writeMissionFile(t, filepath.Join(sourcePath, ".formic", "missions"), "toolkit-only.yaml", toolkitOnlyYAML)
	writeMissionFile(t, filepath.Join(sourcePath, ".formic", "missions"), "broken.yaml", invalidYAML)

	emptyCatalog, repositoryRoot := discovery.Catalog{}, t.TempDir()
	model := New(emptyCatalog, repositoryRoot)
	model.width, model.height = 120, 40
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !model.showMissions {
		t.Fatal("m should open the missions section")
	}
	if len(model.missionRows) != 2 {
		t.Fatalf("missionRows = %+v, want exactly 2 (shared winner + toolkit-only, broken excluded)", model.missionRows)
	}
	view := model.View()
	if !strings.Contains(view, "shared") || !strings.Contains(view, "toolkit-only") {
		t.Fatalf("missions view does not list both winners:\n%s", view)
	}
	if strings.Contains(view, "broken") {
		t.Fatalf("missions view lists the invalid mission file, which only doctor should report:\n%s", view)
	}
	if !strings.Contains(view, "shadowed") {
		t.Fatalf("missions view does not mark the personal winner's toolkit shadow:\n%s", view)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.showMissions {
		t.Fatal("esc should close the missions section")
	}
}

// missionInputTool declares exactly one required, non-sensitive input, "target" — the fixture a
// mission whose own inputs[] fully cover the tool's declared inputs can launch straight through
// the deck's own equip+input+pre-flight pipeline.
func missionInputTool(t *testing.T) discovery.Tool {
	return discovery.Tool{
		Name: "target-tool", Title: "Target Tool",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"{target}"}},
			Inputs: []formats.Input{{Name: "target", Required: true}},
		},
	}
}

// missionTargetingTool builds a mission.Mission naming missionInputTool as its one step, with
// evidenceRoot as an ABSOLUTE evidence path (never relative — this keeps the test hermetic,
// since mission.ResolveEvidenceRoot would otherwise resolve a relative personal path against the
// real process cwd).
func missionTargetingTool(evidenceRoot string) mission.Mission {
	return mission.Mission{
		Definition: formats.Mission{
			Mission: 1, Name: "demo", Evidence: evidenceRoot, Inputs: []string{"target"},
			Steps: []formats.MissionStepsElem{{Tool: "target-tool", With: formats.MissionStepsElemWith{"target": "{target}"}}},
		},
		Path: "/fake/.formic/missions/demo.yaml", Personal: true,
	}
}

// TestMissionWithAnUnboundInputPromptsThenLaunches is E2b's core proof: a mission whose own
// inputs[] declares "target" — unbound at launch time — prompts for it (the sequential input
// prompt, section E), then launches with the typed value threaded into the resolved argv.
func TestMissionWithAnUnboundInputPromptsThenLaunches(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, missionInputTool(t))
	evidenceRoot := t.TempDir()
	m := missionTargetingTool(evidenceRoot)

	updated, _ := model.beginMissionLaunch(m)
	model = requireModel(t, updated)
	if !model.promptingInputs {
		t.Fatal("a mission with an unbound declared input should prompt for it")
	}
	if !strings.Contains(model.View(), "target") {
		t.Fatalf("mission input prompt does not show the input's label:\n%s", model.View())
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bound-value")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if model.promptingInputs || command == nil {
		t.Fatalf("submitting the mission's last input should leave the prompt and launch: notice=%q", model.notice)
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one launched run", rows)
	}
	row := rows[0]
	if row.Argv[len(row.Argv)-1] != "bound-value" {
		t.Fatalf("resolved argv does not carry the typed mission input value: %+v", row.Argv)
	}
	if row.Mission != "demo" || row.MissionPath != "/fake/.formic/missions/demo.yaml" {
		t.Fatalf("row = %+v, want mission=demo missionPath=/fake/.formic/missions/demo.yaml", row)
	}
}

// TestLaunchedMissionRunWritesMissionRecordAfterForegroundCompletion proves the deck's own
// mission-launch path reaches E1's shared record write: after the deck's foreground handoff
// completes, mission-run.json exists in the mission's evidence folder — not just the CLI path
// E1's own tests already cover.
func TestLaunchedMissionRunWritesMissionRecordAfterForegroundCompletion(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, missionInputTool(t))
	evidenceRoot := t.TempDir()
	m := missionTargetingTool(evidenceRoot)

	updated, _ := model.beginMissionLaunch(m)
	model = requireModel(t, updated)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bound-value")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("submitting the mission input should prepare the foreground launch")
	}
	rows, err := model.runStore.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
	runID := rows[0].ID
	model = requireModel(t, model.handleForegroundFinished(foregroundFinishedMsg{runID: runID}))
	recordPath := filepath.Join(evidenceRoot, "demo", runID, "mission-run.json")
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("mission-run.json missing after the deck's own foreground completion: %v", err)
	}
}

// TestRunListShowsMissionName is E2c's run-list proof: a launched mission run's row shows
// Row.Mission in the run list.
func TestRunListShowsMissionName(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, missionInputTool(t))
	m := missionTargetingTool(t.TempDir())

	updated, _ := model.beginMissionLaunch(m)
	model = requireModel(t, updated)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bound-value")})
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows, err := model.runStore.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
	model = requireModel(t, model.handleForegroundFinished(foregroundFinishedMsg{runID: rows[0].ID}))
	if !model.showRuns {
		t.Fatal("expected the run list to be showing after completion")
	}
	if !strings.Contains(model.View(), "mission: demo") {
		t.Fatalf("run list does not show the mission name:\n%s", model.View())
	}
}

// TestMissionRefusalRendersEveryProblem is E2b's ruling-3 proof: a mission whose step tool
// declares MORE required inputs than the mission itself binds refuses pre-flight, and the
// refusal renders EVERY problem, not just the first.
func TestMissionRefusalRendersEveryProblem(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{
		Name: "under-bound-tool", Title: "Under-bound Tool",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Inputs: []formats.Input{
				{Name: "target", Required: true},
				{Name: "apiKey", Required: true},
				{Name: "region", Required: true},
			},
		},
	}
	model := launchModel(t, store, tool)
	m := mission.Mission{
		Definition: formats.Mission{
			Mission: 1, Name: "under-bound", Evidence: t.TempDir(), Inputs: []string{"target"},
			Steps: []formats.MissionStepsElem{{Tool: "under-bound-tool", With: formats.MissionStepsElemWith{"target": "{target}"}}},
		},
		Path: "/fake/.formic/missions/under-bound.yaml", Personal: true,
	}

	updated, _ := model.beginMissionLaunch(m)
	model = requireModel(t, updated)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bound-value")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command != nil {
		t.Fatal("a pre-flight refusal must not launch anything")
	}
	if len(model.noticeProblems) < 2 {
		t.Fatalf("noticeProblems = %+v, want at least 2 (apiKey AND region unbound)", model.noticeProblems)
	}
	view := model.View()
	if !strings.Contains(view, "apiKey") || !strings.Contains(view, "region") {
		t.Fatalf("refusal panel does not render every problem:\n%s", view)
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused mission pre-flight must start no run: %+v", rows)
	}
}

// TestEquipUnmetMissionToolTriggersEquipPromptBeforePreflight proves the equip prompt (C) still
// fires for a mission's tool: an unmet secret need is prompted for BEFORE mission.Preflight ever
// runs (which would otherwise refuse the whole launch outright on the same need, per §13).
func TestEquipUnmetMissionToolTriggersEquipPromptBeforePreflight(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"secret","name":"FAKE_KEY"}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDir}
	tool := discovery.Tool{
		Name: "needy-mission-tool", Title: "Needy Mission Tool",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
		},
	}
	model := launchModel(t, store, tool)
	m := mission.Mission{
		Definition: formats.Mission{Mission: 1, Name: "needy", Evidence: t.TempDir(), Steps: []formats.MissionStepsElem{{Tool: "needy-mission-tool"}}},
		Path:       "/fake/.formic/missions/needy.yaml", Personal: true,
	}

	updated, _ := model.beginMissionLaunch(m)
	model = requireModel(t, updated)
	if !model.promptingEquip {
		t.Fatal("a mission tool's unmet secret need should trigger the equip prompt before pre-flight")
	}
	if model.promptingInputs {
		t.Fatal("the equip prompt must resolve before the mission's own input prompt begins")
	}
}

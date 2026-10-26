package ui

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

func TestModelFiltersAndOpensDetail(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if len(model.visibleTools()) != 1 || model.visibleTools()[0].Name != "user-only" {
		t.Fatalf("filter did not narrow to user-only: %+v", model.visibleTools())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	view := model.View()
	// Label and value are asserted separately: the detail view pads every label to one width
	// so the values line up in a single column, so "Status:" and its value are not adjacent.
	if !strings.Contains(view, "Launch argv:") || !strings.Contains(view, "Status:") ||
		!strings.Contains(view, "exit-code (fallback)") {
		t.Fatalf("detail view does not show launch and status declarations:\n%s", view)
	}
}

func TestModelShowsCollisionBadgeAndShadowPaths(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	listView := model.View()
	if !strings.Contains(listView, "↑") {
		t.Fatalf("list view does not show collision badge:\n%s", listView)
	}

	// The admission rule (§12) keeps an invalid manifest out of the armory, so the shadowed
	// "shared" tool is the first item now — no navigation needed to reach it.
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	detailView := model.View()
	if !strings.Contains(detailView, "Shadowed definitions") || !strings.Contains(detailView, "shared.yaml") {
		t.Fatalf("detail view does not name shadowed definitions:\n%s", detailView)
	}
}

func TestListAndDetailNameAnUnsupportedManifestVersion(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "future-tool", Title: "Future Tool",
		Source:  discovery.Source{Kind: discovery.SourceCatalog, Path: "/repo/.formic/tools/future-tool.yaml"},
		Problem: manifest.UnsupportedManifestVersionPrefix + ": 2",
	}}}
	model := New(catalog, "/repo")
	listView := model.View()
	if !strings.Contains(listView, "unsupported") {
		t.Fatalf("list view does not show the unsupported state:\n%s", listView)
	}
	if strings.Contains(listView, "invalid") {
		t.Fatalf("list view should not use the generic invalid badge for an unsupported version:\n%s", listView)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	detailView := model.View()
	if !strings.Contains(detailView, manifest.UnsupportedManifestVersionPrefix) {
		t.Fatalf("detail view does not name the unsupported manifest version:\n%s", detailView)
	}
}

func TestQKeyTypesIntoANonEmptyFilterInsteadOfQuitting(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = requireModel(t, updated)
	if isQuitCmd(cmd) {
		t.Fatal("q should not quit while the filter is non-empty")
	}
	if model.filter.Value() != "xq" {
		t.Fatalf("filter value = %q, want %q (q typed into the filter)", model.filter.Value(), "xq")
	}
}

func TestQKeyQuitsWhenTheFilterIsEmpty(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !isQuitCmd(cmd) {
		t.Fatal("q should quit when the filter is empty")
	}
}

func TestEscClearsTheFilterThenQQuits(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.filter.Value() != "" {
		t.Fatalf("esc should clear the filter, got %q", model.filter.Value())
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !isQuitCmd(cmd) {
		t.Fatal("q should quit once the filter has been cleared")
	}
}

func TestQKeyQuitsFromDetailViewRegardlessOfTheFilter(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.detail {
		t.Fatal("expected the model to have opened the detail view")
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !isQuitCmd(cmd) {
		t.Fatal("q should quit from the detail view, which has no filter to type into")
	}
}

func requireModel(t *testing.T, value tea.Model) Model {
	t.Helper()
	model, ok := value.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", value)
	}
	return model
}

func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestQuestionMarkExpandsHelp(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !strings.Contains(model.View(), "backspace: edit") {
		t.Fatalf("expanded help is missing:\n%s", model.View())
	}
}

func TestConfigureUpdatesModeAndEnvironmentInRepositoryProfile(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "true"}},
	}}}
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if !model.configuring || model.configureMode != profile.ModeForeground {
		t.Fatalf("configure state = %v mode %q, want foreground form", model.configuring, model.configureMode)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRight})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("FORMIC_GATE=local")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.configuring {
		t.Fatal("configure form should close after a successful save")
	}
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if written.Tools["checks"].Mode != profile.ModeTab || written.Tools["checks"].Env["FORMIC_GATE"] != "local" {
		t.Fatalf("saved settings = %+v, want tab and FORMIC_GATE=local", written.Tools["checks"])
	}
}

// TestModeChooserWithholdsPaneForAToolThatNeedsARealTerminal covers the manifest-declared half
// of the withholding rule: a tool declaring `pane.interactive: true` can never be captured, on
// any platform, so the chooser must never offer pane for it — and cycling with left/right must
// skip straight over the withheld option rather than landing on it.
func TestModeChooserWithholdsPaneForAToolThatNeedsARealTerminal(t *testing.T) {
	repositoryRoot := t.TempDir()
	interactive := true
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "psql-console", Title: "psql console",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "true"},
			Pane:   &formats.ToolManifestPane{Interactive: &interactive},
		},
	}}}
	model := New(catalog, repositoryRoot)
	// Deterministic regardless of the terminal actually running this test: split must stay out
	// of the cycle here for the same reason New()'s real os.Environ() must not leak in — this
	// test is about the withheld-pane cycle, not split availability, which model_test.go's other
	// cases cover explicitly.
	model.environment = []string{"TERM_PROGRAM=unknown"}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	view := model.View()
	if strings.Contains(view, " pane ") {
		t.Fatalf("mode chooser offers pane for a tool that needs a real terminal:\n%s", view)
	}
	if !strings.Contains(view, "not available") {
		t.Fatalf("mode chooser does not explain why pane is withheld:\n%s", view)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRight})
	if model.configureMode != profile.ModeTab {
		t.Fatalf("configureMode after one right = %q, want tab (pane skipped)", model.configureMode)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRight})
	if model.configureMode != profile.ModeForeground {
		t.Fatalf("configureMode after two rights = %q, want foreground (wrapped past the withheld pane)", model.configureMode)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.configureMode != profile.ModeTab {
		t.Fatalf("configureMode after wrapping left = %q, want tab (pane skipped going backward too)", model.configureMode)
	}
}

// declaredEnvTool builds a fixture manifest exercising the manifest format's v1.1 env[]: a required
// non-secret entry with a default, and a secret entry with none — the two shapes the config
// form must render and store differently.
func declaredEnvTool() discovery.Tool {
	defaultMode := "replay"
	return discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "true"},
			Env: []formats.ToolManifestEnvElem{
				{Name: "FIXTURE_MODE", Description: strPtr("Which mode the fixture runs in."), Required: true, Default: &defaultMode},
				{Name: "FIXTURE_API_KEY", Description: strPtr("Bearer for the fixture's backend."), Secret: true},
			},
		},
	}
}

func strPtr(value string) *string { return &value }

// TestConfigureFormIsDrivenByDeclaredEnvEntries is deliverable 1 of the manifest format's v1.1
// consumer contract: when a manifest declares env[], the env step becomes one row per declared key — the
// required, non-secret row shows its description and marks itself required, and a secret row
// never asks for (or stores) the secret's own value, only the process variable name it should
// be read from at launch.
func TestConfigureFormIsDrivenByDeclaredEnvEntries(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{declaredEnvTool()}}
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // mode: keep foreground
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // args: leave blank

	view := model.View()
	if !strings.Contains(view, "FIXTURE_MODE (required)") || !strings.Contains(view, "Which mode the fixture runs in.") {
		t.Fatalf("configure form does not show the required, described first row:\n%s", view)
	}
	if model.configureEnv.Value() != "replay" {
		t.Fatalf("first row value = %q, want the manifest's declared default %q", model.configureEnv.Value(), "replay")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // accept the prefilled default, advance

	secretView := model.View()
	if model.configureEnv.EchoMode != textinput.EchoPassword {
		t.Fatal("a secret row must mask its input")
	}
	if !strings.Contains(secretView, "FIXTURE_API_KEY") || strings.Contains(secretView, "literal-secret-value") {
		t.Fatalf("secret row prompt missing or a secret value leaked into the view:\n%s", secretView)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("MY_OWN_SECRET_VAR")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // last row: save

	if model.configuring {
		t.Fatal("configure form should close after saving the last declared row")
	}
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	settings := written.Tools["checks"]
	if settings.Env["FIXTURE_MODE"] != "replay" {
		t.Fatalf("saved FIXTURE_MODE = %q, want the accepted default %q", settings.Env["FIXTURE_MODE"], "replay")
	}
	if settings.Env["FIXTURE_API_KEY"] != "env.MY_OWN_SECRET_VAR" {
		t.Fatalf("saved FIXTURE_API_KEY = %q, want the env.NAME reference, never a literal secret", settings.Env["FIXTURE_API_KEY"])
	}
}

// TestConfigureFormPrefillsASecretRowsSourceNameFromAPreviouslySavedReference proves reopening
// the form never regresses to asking for (or showing) the secret's actual value: only the
// source variable name embedded in a previously saved env.NAME reference is ever prefilled.
func TestConfigureFormPrefillsASecretRowsSourceNameFromAPreviouslySavedReference(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{declaredEnvTool()}}
	model := New(catalog, repositoryRoot)
	model.repositoryProfile.Tools["checks"] = profile.Tool{
		Env: map[string]string{"FIXTURE_MODE": "replay", "FIXTURE_API_KEY": "env.PREVIOUSLY_CHOSEN_VAR"},
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // mode
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // args
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // accept row 1, advance to the secret row
	if model.configureEnv.Value() != "PREVIOUSLY_CHOSEN_VAR" {
		t.Fatalf("secret row prefill = %q, want the source name from the saved env.NAME reference, never the raw stored value", model.configureEnv.Value())
	}
}

// TestConfigureFormWithoutDeclaredEnvBehavesExactlyAsBefore is the explicit non-regression
// check for deliverable 1's "manifests without env[] behave exactly as today": the free-text
// field, its placeholder, and the save format must be untouched.
func TestConfigureFormWithoutDeclaredEnvBehavesExactlyAsBefore(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "true"}},
	}}}
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.configureEnv.Placeholder != "NAME=value, SECRET=env.SOURCE" || model.configureEnv.EchoMode != textinput.EchoNormal {
		t.Fatalf("free-text env form regressed: placeholder=%q echoMode=%v", model.configureEnv.Placeholder, model.configureEnv.EchoMode)
	}
}

func TestRejectedConfigurationLeavesThePreviousSettingsInPlace(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "true"}},
	}}}
	model := New(catalog, repositoryRoot)
	model.repositoryProfile.Tools["checks"] = profile.Tool{Mode: profile.ModeForeground, Args: []string{"--kept"}}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRight}) // foreground -> tab
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	// A literal secret in a free-text env entry is rejected by profile validation — a
	// realistic way for the configure form to submit a save that Save() then refuses.
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("API_TOKEN=literal-secret")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.configuring {
		t.Fatal("a rejected save should keep the configure form open")
	}
	if model.notice == "" {
		t.Fatal("a rejected save should show why it was rejected")
	}
	if model.repositoryProfile.Tools["checks"].Mode != profile.ModeForeground ||
		len(model.repositoryProfile.Tools["checks"].Args) != 1 || model.repositoryProfile.Tools["checks"].Args[0] != "--kept" {
		t.Fatalf("rejected settings leaked into the live profile: %+v", model.repositoryProfile.Tools["checks"])
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.configuring {
		t.Fatal("esc should close the configure form")
	}
	if model.repositoryProfile.Tools["checks"].Mode != profile.ModeForeground {
		t.Fatalf("esc after a rejected edit changed the previous settings: %+v", model.repositoryProfile.Tools["checks"])
	}
}

func TestProfileProblemsRefuseLaunch(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}},
	}}}
	model := newModel(catalog, repositoryRoot, runs.Store{StateDirectory: t.TempDir()})
	model.profileProblem = "profile file is invalid:\n  - tools \"checks\": unknown key \"bogus\""
	model.environment = []string{"TERM_PROGRAM=unknown"}
	model.executable = "/bin/hivedeck"
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command != nil {
		t.Fatal("launch should be refused while the profile has unresolved problems")
	}
	if !strings.Contains(model.notice, "profile") && !strings.Contains(model.notice, model.profileProblem) {
		t.Fatalf("refusal notice does not name the profile problem: %q", model.notice)
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("no run should have started while the profile has problems: %+v", rows)
	}
}

// TestUnmetRuntimeNeedRefusesLaunchAndStartsNoProcess proves an unmet need that cannot be
// prompted for (kind: runtime — C1, §13's four kinds; ruling 2) still refuses the launch
// outright, showing the need's own status reason, exactly as `formic launch`'s
// launch.Prepare refusal does — and never enters the equip prompt (promptingEquip stays
// false). A secret/gate need, by contrast, is now promptable (see
// TestUnmetSecretNeedPromptsForAReferenceThenLaunches below) — this premise moved from
// "every unmet need refuses" (car 10's own floor) to "only an unpromptable one does."
func TestUnmetRuntimeNeedRefusesLaunchAndStartsNoProcess(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"runtime","name":"totally-nonexistent-runtime-xyz"}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "needy", Title: "Needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command != nil {
		t.Fatal("launch should be refused while a runtime need is unmet")
	}
	if model.promptingEquip {
		t.Fatal("an unpromptable need must refuse outright, never enter the equip prompt")
	}
	if model.notice == "" || !strings.Contains(model.notice, "totally-nonexistent-runtime-xyz") {
		t.Fatalf("refusal notice does not name the missing runtime: %q", model.notice)
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("no run should have started while a need is unmet: %+v", rows)
	}
}

// TestUnmetSecretNeedPromptsForAReferenceThenLaunches proves C1's core path: a tool declaring
// an unmet secret need is no longer refused outright — the deck prompts "Reference for NAME:",
// pre-filled with env.NAME, and a typed reference is recorded via equip.SetSecret; once the
// referenced environment variable is actually present, the need resolves met and the launch
// proceeds.
func TestUnmetSecretNeedPromptsForAReferenceThenLaunches(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"secret","name":"FAKE_KEY"}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDir}
	model := launchModel(t, store, discovery.Tool{
		Name: "needy", Title: "Needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
		},
	})
	model.environment = append(model.environment, "FAKE_KEY=present-but-value-never-read")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // detail
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // launch -> equip prompt
	if !model.promptingEquip {
		t.Fatal("launch should prompt for an unmet secret need's reference")
	}
	if !strings.Contains(model.View(), "FAKE_KEY") || !strings.Contains(model.equipField.Value(), "env.FAKE_KEY") {
		t.Fatalf("secret prompt does not pre-fill env.FAKE_KEY:\n%s", model.View())
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if model.promptingEquip || command == nil {
		t.Fatalf("accepting the pre-filled reference should leave the prompt and launch: notice=%q", model.notice)
	}
	equipPath := equip.DefaultPath(configDir)
	data, err := os.ReadFile(equipPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "env.FAKE_KEY") {
		t.Fatalf("equip file does not record the reference: %s", data)
	}
	if strings.Contains(string(data), "present-but-value-never-read") {
		t.Fatalf("equip file must hold a reference, never the secret's value: %s", data)
	}
}

// TestSecretPromptRefusesALiteralValue proves the deck's secret prompt applies the SAME
// literal-value refusal as `formic equip secret` (§13, §15): typing a literal instead of a
// reference is refused with equip.ErrLiteralValue's exact text, the prompt stays open (ask
// again), and the equip file is never written.
func TestSecretPromptRefusesALiteralValue(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"secret","name":"FAKE_KEY"}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDir}
	model := launchModel(t, store, discovery.Tool{
		Name: "needy", Title: "Needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingEquip {
		t.Fatal("expected the equip prompt to be showing")
	}
	model.equipField.SetValue("")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a-literal-secret-value")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingEquip {
		t.Fatal("a refused literal must leave the prompt open, not cancel or launch")
	}
	if !strings.Contains(model.notice, "refused:") || !strings.Contains(model.notice, "a reference such as env.NAME") {
		t.Fatalf("refusal notice does not match the CLI's own text: %q", model.notice)
	}
	equipPath := equip.DefaultPath(configDir)
	if _, err := os.Stat(equipPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused literal must never be written to the equip file: stat err = %v", err)
	}
}

// TestAskOnceAMetNeedIsNeverPrompted proves §13's "ask once": a need already met against the
// equip file at launch time never shows the equip prompt at all — pressing enter on the detail
// view goes straight through to the launch.
func TestAskOnceAMetNeedIsNeverPrompted(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	if _, err := equip.SetSecret(equip.DefaultPath(configDir), "ALREADY_EQUIPPED", "env.ALREADY_EQUIPPED"); err != nil {
		t.Fatal(err)
	}
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"secret","name":"ALREADY_EQUIPPED"}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDir}
	model := launchModel(t, store, discovery.Tool{
		Name: "needy", Title: "Needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
		},
	})
	model.environment = append(model.environment, "ALREADY_EQUIPPED=present-but-value-never-read")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if model.promptingEquip {
		t.Fatal("a need already met must never be prompted (ask once)")
	}
	if command == nil {
		t.Fatal("a met need should let the launch proceed")
	}
}

// TestGateNeedPromptsCursorListSelectingAProvider proves C1's gate branch: a cursor list shows
// exactly the tool's own declared providers, and selecting one records it via equip.SetProvider
// — the need then resolves met (the manifest also declares the env[] entry that receives it)
// and the launch proceeds.
func TestGateNeedPromptsCursorListSelectingAProvider(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"gate","class":"browser","env":"GATE_ENV","providers":["solari","local"]}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDir}
	model := launchModel(t, store, discovery.Tool{
		Name: "needs-gate", Title: "Needs Gate",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
			Env:    []formats.ToolManifestEnvElem{{Name: "GATE_ENV"}},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingEquip {
		t.Fatal("launch should prompt for an unmet gate need's provider")
	}
	view := model.View()
	if !strings.Contains(view, "solari") || !strings.Contains(view, "local") {
		t.Fatalf("gate prompt does not list the tool's own declared providers:\n%s", view)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown}) // cursor -> local
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if model.promptingEquip || command == nil {
		t.Fatalf("selecting a listed provider should leave the prompt and launch: notice=%q", model.notice)
	}
	equipPath := equip.DefaultPath(configDir)
	data, err := os.ReadFile(equipPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "local") {
		t.Fatalf("equip file does not record the chosen provider: %s", data)
	}
}

// TestEscCancelsEquipPromptEntirely proves Esc at the equip prompt cancels the whole launch —
// no run row is written, mirroring the generalized input prompt's own Esc contract (N10).
func TestEscCancelsEquipPromptEntirely(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"secret","name":"FAKE_KEY"}`), &need); err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "needy", Title: "Needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Needs:  []formats.Need{need},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingEquip {
		t.Fatal("expected the equip prompt to be showing")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.promptingEquip {
		t.Fatal("esc should leave the equip prompt")
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("esc should cancel the whole launch, started none: %+v", rows)
	}
}

// TestEquipViewShowsNeedsGroupedAcrossTheArmory proves C2's read-only equip view (key `e`):
// two tools declaring the SAME need share one grouped row naming both tools, the same
// computation `formic equip`'s own grouping shares (equip.GroupNeedStatuses, hoisted for
// exactly this reuse).
func TestEquipViewShowsNeedsGroupedAcrossTheArmory(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	var need formats.Need
	if err := json.Unmarshal([]byte(`{"kind":"secret","name":"SHARED_KEY"}`), &need); err != nil {
		t.Fatal(err)
	}
	toolCatalog := discovery.Catalog{Tools: []discovery.Tool{
		{Name: "tool-one", Title: "Tool One", Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}, Needs: []formats.Need{need},
		}},
		{Name: "tool-two", Title: "Tool Two", Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}, Needs: []formats.Need{need},
		}},
	}}
	model := New(toolCatalog, t.TempDir())
	model.width, model.height = 120, 40
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !model.showEquipView {
		t.Fatal("e should open the read-only equip view")
	}
	view := model.View()
	if !strings.Contains(view, "SHARED_KEY") || !strings.Contains(view, "tool-one") || !strings.Contains(view, "tool-two") {
		t.Fatalf("equip view does not group the shared need across both declaring tools:\n%s", view)
	}
}

// TestEquipViewIsReadOnly proves C2's "no editing keys" contract: an arbitrary keypress inside
// the equip view neither closes it nor mutates any state — only esc/left/backspace close it,
// exactly like the run list's own back keys.
func TestEquipViewIsReadOnly(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !model.showEquipView {
		t.Fatal("expected the equip view to be open")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !model.showEquipView {
		t.Fatal("an arbitrary key should not close the read-only equip view")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.showEquipView {
		t.Fatal("esc should close the equip view")
	}
}

// TestDeclaredInputPromptsSequentiallyThenLaunches proves N10's generalized input prompt: a tool
// declaring a required, non-spec input is no longer refused outright (superseding car 8's floor,
// § car 10 D) — the deck prompts for it, one screen per declared input, then launches with the
// typed value threaded into the resolved argv.
func TestDeclaredInputPromptsSequentiallyThenLaunches(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "needs-input", Title: "Needs Input",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"{apiKey}"}},
			Inputs: []formats.Input{{Name: "apiKey", Required: true}},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // detail
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // launch -> prompt
	if !model.promptingInputs {
		t.Fatal("launch should prompt for a missing required declared input")
	}
	if !strings.Contains(model.View(), "apiKey") {
		t.Fatalf("input prompt does not show the input's label:\n%s", model.View())
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("secret-123")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if model.promptingInputs || command == nil {
		t.Fatal("submitting the last input should leave the prompt and prepare the launch")
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Argv) == 0 || rows[0].Argv[len(rows[0].Argv)-1] != "secret-123" {
		t.Fatalf("typed input did not reach the resolved argv: %+v", rows)
	}
}

// TestSensitiveInputMaskedAndNeverWrittenToDisk proves N10's masking guarantee: a
// `sensitive: true` input is echoed as a password field on screen, and its real value never
// lands in the state dir, the config dir, or the repository's own .formic/profile.yaml — only
// the redacted marker does.
func TestSensitiveInputMaskedAndNeverWrittenToDisk(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	store := runs.Store{StateDirectory: stateDir}
	model := launchModel(t, store, discovery.Tool{
		Name: "needs-secret", Title: "Needs Secret",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"{token}"}},
			Inputs: []formats.Input{{Name: "token", Required: true, Sensitive: true}},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingInputs || model.inputField.EchoMode != textinput.EchoPassword {
		t.Fatal("a sensitive input should be masked on screen")
	}
	const secretValue = "VERY-SECRET-TOKEN-VALUE"
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(secretValue)})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("submitting the sensitive input should prepare the launch")
	}
	rows, err := model.runStore.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
	for _, argument := range rows[0].Argv {
		if strings.Contains(argument, secretValue) {
			t.Fatalf("the redacted run row's argv carries the real secret: %+v", rows[0].Argv)
		}
	}
	assertNoFileUnderContainsBytes(t, stateDir, secretValue)
	assertNoFileUnderContainsBytes(t, configDir, secretValue)
	assertNoFileUnderContainsBytes(t, model.repositoryRoot, secretValue)
}

// TestEscCancelsInputPromptEntirely proves Esc at any step of the sequential prompt cancels the
// whole launch (N10) — no run row is written, and the deck returns to the list/detail view.
func TestEscCancelsInputPromptEntirely(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "needs-input", Title: "Needs Input",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
			Inputs: []formats.Input{{Name: "apiKey", Required: true}},
		},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingInputs {
		t.Fatal("expected the input prompt to be showing")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("partial-value")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.promptingInputs {
		t.Fatal("esc should leave the input prompt")
	}
	rows, err := model.runStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("esc should cancel the whole launch, started none: %+v", rows)
	}
}

// assertNoFileUnderContainsBytes fails the test if any regular file under root contains needle —
// the byte-level proof that a sensitive value never reached disk anywhere under the given root.
func assertNoFileUnderContainsBytes(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(data), needle) {
			t.Fatalf("%s contains the sensitive value", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnterLaunchesForegroundAndShowsCompletedRun(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("detail Enter should return a foreground handoff command")
	}
	rows, err := store.List()
	if err != nil || len(rows) != 1 || rows[0].Outcome != "running" {
		t.Fatalf("prepared rows = %+v, %v", rows, err)
	}
	updated, _ = model.Update(foregroundFinishedMsg{runID: rows[0].ID})
	model = requireModel(t, updated)
	if !model.showRuns || !strings.Contains(model.View(), "exit=0") || !strings.Contains(model.View(), "ok") {
		t.Fatalf("finished foreground run is not visible:\n%s", model.View())
	}
}

// TestHandleForegroundFinishedStopsTheSignalTrapOnlyAfterFinishPersists proves the ordering
// half of "the trap stays armed until Finish has persisted the row": a spy stop function
// observes, at the instant it is called, whether the row already carries an exit code on
// disk. Calling stop before Finish reopens exactly the handback-window gap the fix closes.
func TestHandleForegroundFinishedStopsTheSignalTrapOnlyAfterFinishPersists(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	model := launchModel(t, store, discovery.Tool{Name: "checks"})

	stoppedBeforePersist := true
	stop := func() {
		completed, loadErr := store.Load(row.ID)
		stoppedBeforePersist = loadErr != nil || completed.ExitCode == nil
	}
	model.handleForegroundFinished(foregroundFinishedMsg{runID: row.ID, stopSignalTrap: stop})
	if stoppedBeforePersist {
		t.Fatal("the signal trap was stopped BEFORE Finish persisted the row's exit code")
	}
}

// TestForegroundHandoffExecutesTheRealWrapperAgainstForegroundFixture drives a real, headless
// Bubble Tea Program (no fabricated completion message) through the foreground handoff, so
// reverting SignalForwardingCommand back to a bare *exec.Cmd, or stopping the signal trap
// before Finish, is exercised by the actual code path rather than skipped by a hand-crafted
// foregroundFinishedMsg.
func TestForegroundHandoffExecutesTheRealWrapperAgainstForegroundFixture(t *testing.T) {
	hivedeckRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{Name: "checks", Title: "Checks", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:status", "0", "ok"}},
	}}
	model := newModel(discovery.Catalog{Tools: []discovery.Tool{tool}}, hivedeckRoot, store)
	model.environment = []string{"TERM_PROGRAM=unknown"}
	model.executable = "/bin/hivedeck"
	model.clock = time.Now
	model.goos = "darwin"

	program := tea.NewProgram(model, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	finished := make(chan tea.Model, 1)
	go func() {
		final, _ := program.Run()
		finished <- final
	}()

	program.Send(tea.KeyMsg{Type: tea.KeyEnter}) // open detail
	program.Send(tea.KeyMsg{Type: tea.KeyEnter}) // launch — real foreground handoff

	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, listErr := store.List()
		if listErr == nil && len(rows) == 1 && rows[0].ExitCode != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the real foreground handoff never completed the run on disk")
		}
		time.Sleep(20 * time.Millisecond)
	}

	program.Quit()
	final := <-finished
	finalModel, ok := final.(Model)
	if !ok {
		t.Fatalf("program.Run() returned %T, want ui.Model", final)
	}
	if !finalModel.showRuns || !strings.Contains(finalModel.View(), "ok") {
		t.Fatalf("finished foreground run is not visible:\n%s", finalModel.View())
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Outcome != "ok" || rows[0].ExitCode == nil || *rows[0].ExitCode != 0 || rows[0].Artifact != "artifact.html" {
		t.Fatalf("row = %+v, want outcome ok, exit 0, artifact.html — from the real fixture tool", rows[0])
	}
}

func TestSpecTokenPromptsBeforeLaunch(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "doctor", Title: "Doctor",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"{spec}"}}},
	})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.promptingSpec {
		t.Fatal("launch should prompt for a missing {spec} value")
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("specs/sample.yaml")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if model.promptingSpec || command == nil {
		t.Fatal("submitting a spec should leave the prompt and prepare the launch")
	}
}

func TestUnknownTerminalFallsBackToForeground(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}},
	})
	model.repositoryProfile.Tools = map[string]profile.Tool{"checks": {Mode: profile.ModeTab}}
	model.environment = []string{"TERM_PROGRAM=unknown"}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows, err := store.List()
	if err != nil || command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeForeground {
		t.Fatalf("fallback rows = %+v, command nil = %v, error = %v", rows, command == nil, err)
	}
	if !strings.Contains(model.notice, "New tab unavailable") {
		t.Fatalf("fallback notice = %q", model.notice)
	}
}

func TestKnownTerminalPreparesTabAndShowsRunningRow(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}},
	})
	model.repositoryProfile.Tools = map[string]profile.Tool{"checks": {Mode: profile.ModeTab}}
	model.environment = []string{"TMUX=/tmp/tmux"}
	// A fake "tmux" on PATH so the returned command can actually be EXECUTED, rather than
	// injecting a hand-crafted tabStartedMsg — signal handling and the actual handoff stay
	// covered only when the real code path runs.
	installFakeExecutable(t, "tmux")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows, err := store.List()
	if err != nil || command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeTab {
		t.Fatalf("tab rows = %+v, command nil = %v, error = %v", rows, command == nil, err)
	}
	updated, _ = model.Update(command())
	model = requireModel(t, updated)
	if !model.showRuns || !strings.Contains(model.View(), "running") {
		t.Fatalf("started tab row is not visible:\n%s", model.View())
	}
}

// TestKnownSplitTerminalPreparesSplitAndShowsRunningRow mirrors
// TestKnownTerminalPreparesTabAndShowsRunningRow for the split route (N7): tmux is one of the two
// terminals HasSplitCommand recognizes, so a tool configured for split actually opens a pane
// instead of degrading anywhere.
func TestKnownSplitTerminalPreparesSplitAndShowsRunningRow(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}},
	})
	model.repositoryProfile.Tools = map[string]profile.Tool{"checks": {Mode: profile.ModeSplit}}
	model.environment = []string{"TMUX=/tmp/tmux"}
	installFakeExecutable(t, "tmux")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows, err := store.List()
	if err != nil || command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeSplit {
		t.Fatalf("split rows = %+v, command nil = %v, error = %v", rows, command == nil, err)
	}
	updated, _ = model.Update(command())
	model = requireModel(t, updated)
	if !model.showRuns || !strings.Contains(model.View(), "running") {
		t.Fatalf("started split row is not visible:\n%s", model.View())
	}
}

// TestSplitFallsBackToTabWhenTerminalHasNoSplitCommand is the mutation proof the brief names
// ("split fallback to tab"): §11's "no split command means tabs only" — a tool configured for
// split, on a KNOWN terminal that DetectTerminal recognizes but HasSplitCommand does not (kitty,
// here), degrades to the tab route, not straight to foreground, and the row records that.
func TestSplitFallsBackToTabWhenTerminalHasNoSplitCommand(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}},
	})
	model.repositoryProfile.Tools = map[string]profile.Tool{"checks": {Mode: profile.ModeSplit}}
	model.environment = []string{"KITTY_WINDOW_ID=1"}
	installFakeExecutable(t, "kitten")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows, err := store.List()
	if err != nil || command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeTab {
		t.Fatalf("split-fallback rows = %+v, command nil = %v, error = %v", rows, command == nil, err)
	}
	if !strings.Contains(model.notice, "Split unavailable") {
		t.Fatalf("fallback notice = %q, want it to name split as unavailable", model.notice)
	}
}

// TestModeChooserOffersSplitOnlyWhenTheDetectedTerminalHasASplitCommand is the mode-chooser half
// of N7's "offers it only when the detected terminal has a split command": split must appear in
// the cycle for tmux and be entirely absent for a terminal DetectTerminal cannot even recognize.
// TestUncataloguedOfferShowsThenAcceptingAddsTheRepoAndRefreshesTheCatalog is N8 point 3's
// core walk-through: the offer shows for a valid, not-yet-catalogued toolkit; pressing "a" adds
// it (the deck-side equivalent of `formic add`, same validation) and the newly catalogued
// tool is now visible, with the offer gone.
func TestUncataloguedOfferShowsThenAcceptingAddsTheRepoAndRefreshesTheCatalog(t *testing.T) {
	userConfigRoot := t.TempDir()
	repositoryRoot := t.TempDir()
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "toolkit.yaml"), "toolkit: 1\nname: offer-source\n")
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "tools", "offered.yaml"),
		"formic: 1\nname: offered\ntitle: Offered\nlaunch:\n  command: \"true\"\n")

	model := New(discovery.Catalog{}, repositoryRoot)
	sourcePaths, err := catalog.SourcePaths(userConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	model = model.WithCatalogContext(userConfigRoot, catalog.UncataloguedAt(repositoryRoot, sourcePaths))
	if model.uncatalogued == nil || model.uncatalogued.Name != "offer-source" {
		t.Fatalf("model.uncatalogued = %+v, want the offer-source toolkit", model.uncatalogued)
	}
	if !strings.Contains(model.View(), "add it? press a") {
		t.Fatalf("list view does not show the add-this-repo offer:\n%s", model.View())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if model.uncatalogued != nil {
		t.Fatalf("model.uncatalogued = %+v after accepting, want nil", model.uncatalogued)
	}
	if len(model.catalog.Tools) != 1 || model.catalog.Tools[0].Name != "offered" {
		t.Fatalf("model.catalog.Tools = %+v, want the newly catalogued offered tool", model.catalog.Tools)
	}
	reloaded, err := catalog.SourcePaths(userConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 1 || filepath.Clean(reloaded[0]) != filepath.Clean(repositoryRoot) {
		t.Fatalf("catalog.SourcePaths after accepting = %v, want [%s]", reloaded, repositoryRoot)
	}
}

// TestUncataloguedOfferRefusesAnInvalidDescriptorAndLeavesTheOfferShowing is the mutation-proof
// half of N8 point 3: an invalid `.formic/toolkit.yaml` refuses exactly the way `formic add`
// would, and the offer keeps showing rather than silently disappearing on a refusal.
func TestUncataloguedOfferRefusesAnInvalidDescriptorAndLeavesTheOfferShowing(t *testing.T) {
	userConfigRoot := t.TempDir()
	repositoryRoot := t.TempDir()
	writeFileForTest(t, filepath.Join(repositoryRoot, ".formic", "toolkit.yaml"), "toolkit: 1\nunknownField: true\n")

	model := New(discovery.Catalog{}, repositoryRoot)
	// The offer field is set directly here (rather than through catalog.UncataloguedAt, which
	// would itself refuse to report an invalid descriptor) to exercise the same refusal path
	// catalog.Add hits if a descriptor is edited to become invalid between the offer being
	// computed and "a" being pressed.
	model = model.WithCatalogContext(userConfigRoot, &catalog.Uncatalogued{Name: "broken", Path: repositoryRoot})

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if model.uncatalogued == nil {
		t.Fatal("a refused add should leave the offer showing")
	}
	if model.notice == "" {
		t.Fatal("a refused add should explain why")
	}
	sourcePaths, err := catalog.SourcePaths(userConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourcePaths) != 0 {
		t.Fatalf("catalog.SourcePaths after a refused add = %v, want none", sourcePaths)
	}
}

func writeFileForTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModeChooserOffersSplitOnlyWhenTheDetectedTerminalHasASplitCommand(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "true"}},
	}}}
	model := New(catalog, repositoryRoot)
	model.environment = []string{"TMUX=/tmp/tmux"}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if !strings.Contains(model.View(), " split ") {
		t.Fatalf("mode chooser does not offer split for tmux:\n%s", model.View())
	}

	model.environment = []string{"TERM_PROGRAM=unknown"}
	if strings.Contains(model.View(), " split ") {
		t.Fatalf("mode chooser offers split with no detected terminal:\n%s", model.View())
	}
}

// TestFailedTabSpawnRetriesInForegroundWithoutClaimingTheToolFailed reproduces the founder's
// three stuck rows: the tab route's own spawn command (wt.exe/tmux/osascript itself) fails
// before the tool ever starts. The tab row must not read as though the TOOL failed — it never
// ran — and the tool must still get a real attempt, this time in the foreground, as its own row.
func TestFailedTabSpawnRetriesInForegroundWithoutClaimingTheToolFailed(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{
			Command: testfixture.Tool(t), Args: []string{"fixture:status", "0", "ok"},
		}},
	}
	model := launchModel(t, store, tool)
	row, err := store.Begin(tool.Name, profile.ModeTab, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	message := tabStartedMsg{
		runID: row.ID, terminal: launch.TerminalTmux, tool: tool,
		err: errors.New(`exec: "tmux": executable file not found in $PATH`),
	}
	updated, command := model.handleTabStarted(message)
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("a failed tab spawn should retry the tool in the foreground")
	}

	aborted, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aborted.ExitCode != nil {
		t.Fatalf("aborted tab row = %+v, must not invent an exit code the tool never produced", aborted)
	}
	if aborted.Outcome == "failed" || aborted.Outcome == "ok" {
		t.Fatalf("aborted tab row outcome = %q, must not claim the tool itself finished", aborted.Outcome)
	}
	if aborted.Note == "" {
		t.Fatal("aborted tab row should record why in Note")
	}

	rows, err := store.List()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v, error = %v, want the aborted tab row plus its own foreground retry row", rows, err)
	}
	if !strings.Contains(model.notice, "New tab unavailable") {
		t.Fatalf("notice = %q, want wording mirroring the unknown-terminal fallback", model.notice)
	}
}

// installFakeExecutable puts an executable named `name` that exits 0 first on PATH for the
// duration of the test, so a returned tea.Cmd that shells out to it (tmux, open, ...) can be
// safely EXECUTED for real instead of only ever being injected as a hand-crafted completion
// message.
func installFakeExecutable(t *testing.T, name string) {
	t.Helper()
	directory := t.TempDir()
	testfixture.InstallAs(t, directory, name)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestOpeningTheRunListRecoversAForegroundRowAbandonedByADeadSupervisor reproduces the
// founder's stuck `phone-lookup` row: a foreground row left "running" because the process that
// recorded it died before it could ever complete. Opening the run list — where rows are loaded
// for display — must recover it rather than showing "running" forever.
func TestOpeningTheRunListRecoversAForegroundRowAbandonedByADeadSupervisor(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("phone-lookup", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.SupervisorPID = 999999 // a pid this process is not; recovery must treat it as gone
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}

	model := launchModel(t, store, discovery.Tool{Name: "phone-lookup", Title: "Phone Lookup"})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	recovered, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Outcome == "running" {
		t.Fatalf("row = %+v, want it recovered rather than stuck running forever", recovered)
	}
	if recovered.ExitCode != nil {
		t.Fatalf("recovered row ExitCode = %v, want nil — the tool never told anyone it finished", recovered.ExitCode)
	}
}

func TestRunListOpensAnArtifact(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.StatusFile(row.ID), []byte(`{"event":"ok","artifact":"https://example.com/report"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(row.ID, 0, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	installFakeExecutable(t, "open")
	model := launchModel(t, store, discovery.Tool{Name: "checks", Title: "Checks"})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !model.showRuns || !strings.Contains(model.View(), "https://example.com/report") {
		t.Fatalf("run list is not visible:\n%s", model.View())
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("Enter on an artifact row should return an open command")
	}
	updated, _ = model.Update(command())
	model = requireModel(t, updated)
	if !strings.Contains(model.notice, "Opened") {
		t.Fatalf("model.notice = %q, want an Opened confirmation from actually running the open command", model.notice)
	}
}

// TestRunListFallsBackToTheResolvedEvidenceDirectoryWhenNoArtifactWasReported is the manifest
// format's v1.1 outputs.evidenceDir contract: a run whose status stream never reported an artifact
// still shows (and can open, via the existing "enter" affordance) the evidence directory the
// manifest declared, resolved once at launch time and stored on the row directly (N10: the
// durable row's environment is redacted to names only, so this can no longer be resolved back
// out of it at display time).
func TestRunListFallsBackToTheResolvedEvidenceDirectoryWhenNoArtifactWasReported(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	evidenceDirectory := "/repo/evidence/run-1"
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.EvidenceDir = evidenceDirectory
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}
	// No status file is written, so Complete falls back to exit-code outcome with Artifact
	// left empty — the case outputs.evidenceDir exists to cover.
	if _, err := store.Complete(row.ID, 0, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	evidenceDirectoryKey := "FIXTURE_EVIDENCE_DIR"
	tool := discovery.Tool{Name: "checks", Title: "Checks", Manifest: formats.ToolManifest{
		Launch:  formats.ToolManifestLaunch{Command: "true"},
		Outputs: &formats.ToolManifestOutputs{EvidenceDir: &evidenceDirectoryKey},
	}}
	installFakeExecutable(t, "open")
	model := launchModel(t, store, tool)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !strings.Contains(model.View(), evidenceDirectory) {
		t.Fatalf("run list does not show the resolved evidence directory:\n%s", model.View())
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("enter on a row with a resolved evidence directory should return an open command")
	}
	updated, _ = model.Update(command())
	model = requireModel(t, updated)
	if !strings.Contains(model.notice, "Opened "+evidenceDirectory) {
		t.Fatalf("model.notice = %q, want it to confirm opening the resolved evidence directory", model.notice)
	}
}

// TestRunListRendersTheDeclaredExitMeaningNextToAFinishedRunsCode is the manifest format's v1.1 `exit`
// contract: a finished run's code is looked up against its tool's declared exit table and
// rendered right beside the code, not left for a person to go find in the manifest.
func TestRunListRendersTheDeclaredExitMeaningNextToAFinishedRunsCode(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.StatusFile(row.ID), []byte(`{"event":"needs-human"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(row.ID, 7, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	tool := discovery.Tool{Name: "checks", Title: "Checks", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: "true"},
		Exit:   formats.ToolManifestExit{"7": "an assertion proposal awaits review"},
	}}
	model := launchModel(t, store, tool)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !strings.Contains(model.View(), "exit=7 (an assertion proposal awaits review)") {
		t.Fatalf("run list does not show the declared exit meaning next to the code:\n%s", model.View())
	}
}

func launchModel(t *testing.T, store runs.Store, tool discovery.Tool) Model {
	t.Helper()
	model := newModel(discovery.Catalog{Tools: []discovery.Tool{tool}}, t.TempDir(), store)
	model.environment = []string{"TERM_PROGRAM=unknown"}
	model.executable = "/bin/hivedeck"
	model.clock = func() time.Time { return time.Unix(100, 0) }
	model.goos = "darwin"
	// Every view is laid out against the reported terminal size, so a model that never
	// received a WindowSizeMsg renders against the 80x24 fallback and elides long run rows.
	// bubbletea sends this on startup; the tests say so explicitly rather than depending on
	// whatever the fallback happens to be.
	model.width, model.height = 120, 40
	return model
}

func fixtureCatalog(t *testing.T) (discovery.Catalog, string) {
	t.Helper()
	sourceOne := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-one"))
	sourceTwo := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-two"))
	catalog, err := discovery.Discover([]string{sourceOne, sourceTwo})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	return catalog, sourceOne
}

func updateModel(t *testing.T, model Model, message tea.Msg) Model {
	t.Helper()
	updated, _ := model.Update(message)
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", updated)
	}
	return updatedModel
}

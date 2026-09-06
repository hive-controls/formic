package ui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	toolspec "github.com/hive-controls/toolspec"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/manifest"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
)

func TestModelFiltersAndOpensDetail(t *testing.T) {
	catalog, repositoryRoot := fixtureCatalog(t)
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if len(model.visibleTools()) != 1 || model.visibleTools()[0].Name != "root-task" {
		t.Fatalf("filter did not narrow to root-task: %+v", model.visibleTools())
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

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	detailView := model.View()
	if !strings.Contains(detailView, "Shadowed definitions") || !strings.Contains(detailView, "package.json") {
		t.Fatalf("detail view does not name shadowed definitions:\n%s", detailView)
	}
}

func TestListAndDetailNameAnUnsupportedManifestVersion(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "future-tool", Title: "Future Tool",
		Source:  discovery.Source{Kind: discovery.SourceLocal, Path: "/repo/.hivedeck/tools/future-tool.yaml"},
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
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "true"}},
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

// declaredEnvTool builds a fixture manifest exercising toolspec v1.1's env[]: a required
// non-secret entry with a default, and a secret entry with none — the two shapes the config
// form must render and store differently.
func declaredEnvTool() discovery.Tool {
	defaultMode := "replay"
	return discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: toolspec.ToolManifest{
			Launch: toolspec.ToolManifestLaunch{Command: "true"},
			Env: []toolspec.ToolManifestEnvElem{
				{Name: "FIXTURE_MODE", Description: strPtr("Which mode the fixture runs in."), Required: true, Default: &defaultMode},
				{Name: "FIXTURE_API_KEY", Description: strPtr("Bearer for the fixture's backend."), Secret: true},
			},
		},
	}
}

func strPtr(value string) *string { return &value }

// TestConfigureFormIsDrivenByDeclaredEnvEntries is deliverable 1 of the toolspec v1.1 consumer
// contract: when a manifest declares env[], the env step becomes one row per declared key — the
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
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "true"}},
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
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "true"}},
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
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "/bin/true"}},
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

func TestEnterLaunchesForegroundAndShowsCompletedRun(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "checks", Title: "Checks",
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "/bin/true"}},
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
	scriptPath := filepath.Join(hivedeckRoot, "testdata", "foreground.sh")
	tool := discovery.Tool{Name: "checks", Title: "Checks", Manifest: toolspec.ToolManifest{
		Launch: toolspec.ToolManifestLaunch{Command: "/bin/sh", Args: []string{scriptPath, "0", "ok"}},
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
		t.Fatalf("row = %+v, want outcome ok, exit 0, artifact.html — from the real foreground.sh fixture", rows[0])
	}
}

func TestSpecTokenPromptsBeforeLaunch(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{
		Name: "doctor", Title: "Doctor",
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "/bin/echo", Args: []string{"{spec}"}}},
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
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "/bin/true"}},
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
		Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "/bin/true"}},
	})
	model.repositoryProfile.Tools = map[string]profile.Tool{"checks": {Mode: profile.ModeTab}}
	model.environment = []string{"TMUX=/tmp/tmux"}
	// A fake "tmux" on PATH so the returned command can actually be EXECUTED, rather than
	// injecting a hand-crafted tabStartedMsg — signal handling and the actual handoff stay
	// covered only when the real code path runs.
	installFakeExecutable(t, "tmux", "#!/bin/sh\nexit 0\n")
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

// installFakeExecutable puts a script named `name` first on PATH for the duration of the
// test, so a returned tea.Cmd that shells out to it (tmux, open, ...) can be safely EXECUTED
// for real instead of only ever being injected as a hand-crafted completion message.
func installFakeExecutable(t *testing.T, name, script string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	installFakeExecutable(t, "open", "#!/bin/sh\nexit 0\n")
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

// TestRunListFallsBackToTheResolvedEvidenceDirectoryWhenNoArtifactWasReported is toolspec
// v1.1's outputs.evidenceDir contract: a run whose status stream never reported an artifact
// still shows (and can open, via the existing "enter" affordance) the evidence directory the
// manifest declared, resolved out of that run's own persisted environment.
func TestRunListFallsBackToTheResolvedEvidenceDirectoryWhenNoArtifactWasReported(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	evidenceDirectory := "/repo/evidence/run-1"
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil,
		[]string{"FIXTURE_EVIDENCE_DIR=" + evidenceDirectory}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	// No status file is written, so Complete falls back to exit-code outcome with Artifact
	// left empty — the case outputs.evidenceDir exists to cover.
	if _, err := store.Complete(row.ID, 0, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	evidenceDirectoryKey := "FIXTURE_EVIDENCE_DIR"
	tool := discovery.Tool{Name: "checks", Title: "Checks", Manifest: toolspec.ToolManifest{
		Launch:  toolspec.ToolManifestLaunch{Command: "true"},
		Outputs: &toolspec.ToolManifestOutputs{EvidenceDir: &evidenceDirectoryKey},
	}}
	installFakeExecutable(t, "open", "#!/bin/sh\nexit 0\n")
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

// TestRunListRendersTheDeclaredExitMeaningNextToAFinishedRunsCode is toolspec v1.1's `exit`
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
	tool := discovery.Tool{Name: "checks", Title: "Checks", Manifest: toolspec.ToolManifest{
		Launch: toolspec.ToolManifestLaunch{Command: "true"},
		Exit:   toolspec.ToolManifestExit{"7": "an assertion proposal awaits review"},
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
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "repository"))
	userConfigRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "user-config"))
	catalog, err := discovery.Discover(repositoryRoot, userConfigRoot)
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	return catalog, repositoryRoot
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

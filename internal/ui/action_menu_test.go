package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

func boolPtr(value bool) *bool { return &value }

func actionLifecyclePtr(value formats.ActionLifecycle) *formats.ActionLifecycle { return &value }

// actionsTool is a docker-desktop-shaped item: five actions over one launch whose child is the
// fixture tool, so a test that runs an action runs a real, harmless process.
func actionsTool(t *testing.T) discovery.Tool {
	t.Helper()
	return discovery.Tool{
		Name: "docker-desktop", Title: "Docker Desktop",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:exit", "1"}},
			Exit:   formats.ToolManifestExit{"1": "tool-level one"},
			Actions: []formats.Action{
				{Name: "start", Title: strPtr("Start"), Description: strPtr("Start the daemon."),
					Exit: formats.ActionExit{"1": "daemon did not come up"}},
				{Name: "stop", Title: strPtr("Stop"), Confirm: strPtr("Stops EVERY container. Continue?")},
				{Name: "restart", Title: strPtr("Restart"), Arguments: []formats.Argument{
					{Flag: "--timeout", Kind: formats.ArgumentKindValue, Description: strPtr("Seconds to wait.")}}},
				{Name: "tui", Title: strPtr("Dashboard"), Interactive: boolPtr(true)},
				{Name: "serve", Title: strPtr("Serve"), Lifecycle: actionLifecyclePtr(formats.ActionLifecycleService)},
			},
		},
	}
}

func openMenu(t *testing.T, tool discovery.Tool) (Model, runs.Store) {
	t.Helper()
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, tool)
	return updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}), store
}

func moveActionCursor(t *testing.T, model Model, steps int) Model {
	t.Helper()
	for range steps {
		model = pressKey(t, model, tea.KeyDown)
	}
	return model
}

func TestEnterOnAMultiActionToolShowsTheMenuWithItsStateLine(t *testing.T) {
	model, _ := openMenu(t, actionsTool(t))
	view := model.View()
	for _, want := range []string{"State:", "unknown", "Start", "Stop", "Restart", "Dashboard", "(asks first)", "Start the daemon."} {
		if !strings.Contains(view, want) {
			t.Fatalf("menu is missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(model.helpText(), "enter run") {
		t.Fatalf("menu help text = %q", model.helpText())
	}
}

func TestActionCursorWrapsAndEscReturnsToTheList(t *testing.T) {
	model, _ := openMenu(t, actionsTool(t))
	model = pressKey(t, model, tea.KeyUp)
	if model.actionCursor != 4 {
		t.Fatalf("up from the first action = %d, want it to wrap to 4", model.actionCursor)
	}
	model = pressKey(t, model, tea.KeyDown)
	if model.actionCursor != 0 {
		t.Fatalf("down from the last action = %d, want it to wrap to 0", model.actionCursor)
	}
	model = pressKey(t, model, tea.KeyEsc)
	if model.detail {
		t.Fatal("esc in the menu should return to the list")
	}
}

func TestConfigureOnAnActionStoresItsArgumentsPerActionLeavingToolArgsUntouched(t *testing.T) {
	model, _ := openMenu(t, actionsTool(t))
	model.repositoryProfile.Tools = map[string]profile.Tool{"docker-desktop": {Args: []string{"--global"}}}
	model = moveActionCursor(t, model, 2) // restart
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if !strings.Contains(model.View(), "docker-desktop · restart") {
		t.Fatalf("configure screen does not name the action:\n%s", model.View())
	}
	model = pressKey(t, model, tea.KeyEnter) // mode
	if !strings.Contains(model.View(), "Argument 1/1") {
		t.Fatalf("the action's own argument is not a row:\n%s", model.View())
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("300")})
	model = pressKey(t, model, tea.KeyEnter) // Extra row
	model = pressKey(t, model, tea.KeyEnter) // env step
	model = pressKey(t, model, tea.KeyEnter) // save
	saved := model.repositoryProfile.Tools["docker-desktop"]
	if got := strings.Join(saved.ArgsFor("restart"), " "); got != "--timeout 300" {
		t.Fatalf("restart args = %q, want %q (notice: %s)", got, "--timeout 300", model.notice)
	}
	if got := strings.Join(saved.Args, " "); got != "--global" {
		t.Fatalf("tool-level args changed to %q", got)
	}
}

func TestRunListNamesTheActionAndReadsItsExitMeaning(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	row, err := store.Begin("docker-desktop", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.Action = "start"
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(row.ID, 1, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	model = pressKey(t, model, tea.KeyEsc)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	view := model.View()
	if !strings.Contains(view, "docker-desktop · start") || !strings.Contains(view, "exit=1 (daemon did not come up)") {
		t.Fatalf("run list does not show the action and its own exit meaning:\n%s", view)
	}
}

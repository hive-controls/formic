package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

func optionsEnvTool() discovery.Tool {
	defaultGate := "local"
	return discovery.Tool{
		Name: "gated", Title: "Gated",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "true"},
			Env: []formats.ToolManifestEnvElem{
				{Name: "FIXTURE_GATE", Description: strPtr("Which gate runs."), Options: []formats.Option{{Value: "solari"}, {Value: "local"}, {Value: "cdp"}}},
				{Name: "FIXTURE_HOST", Options: []formats.Option{{Value: "sandbox"}, {Value: "local"}}, Default: &defaultGate},
				{Name: "FIXTURE_TOKEN", Secret: true, Options: []formats.Option{{Value: "never-shown-option"}}},
				{Name: "FIXTURE_FREE"},
			},
		},
	}
}

// openConfigureAtEnv opens the customize screen on the first env row of optionsEnvTool.
func openConfigureAtEnv(t *testing.T, stored map[string]string) (Model, string) {
	t.Helper()
	repositoryRoot := t.TempDir()
	model := New(discovery.Catalog{Tools: []discovery.Tool{optionsEnvTool()}}, repositoryRoot)
	if stored != nil {
		model.repositoryProfile.Tools["gated"] = profile.Tool{Env: stored}
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // mode
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // args
	return model, repositoryRoot
}

func pressKeyTimes(t *testing.T, model Model, name string, times int) Model {
	t.Helper()
	keys := map[string]tea.KeyMsg{
		"left": {Type: tea.KeyLeft}, "right": {Type: tea.KeyRight}, "enter": {Type: tea.KeyEnter},
	}
	for range times {
		model = updateModel(t, model, keys[name])
	}
	return model
}

// saveThroughRows presses enter through every remaining env row (the free-text row last).
func saveThroughRows(t *testing.T, model Model, repositoryRoot string) map[string]string {
	t.Helper()
	model = pressKeyTimes(t, model, "enter", 4)
	if model.configuring {
		t.Fatal("configure form should have saved and closed")
	}
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	return written.Tools["gated"].Env
}

func TestConfigureEnvRowWithOptionsIsAChooser(t *testing.T) {
	model, repositoryRoot := openConfigureAtEnv(t, nil)
	view := model.View()
	for _, label := range []string{"(not set)", "solari", "local", "cdp"} {
		if !strings.Contains(view, label) {
			t.Fatalf("chooser view missing %q:\n%s", label, view)
		}
	}
	model = pressKeyTimes(t, model, "right", 3) // not set -> solari -> local -> cdp
	model = pressKeyTimes(t, model, "left", 1)  // back to local
	environment := saveThroughRows(t, model, repositoryRoot)
	if environment["FIXTURE_GATE"] != "local" {
		t.Fatalf("saved FIXTURE_GATE = %q, want the chosen option %q", environment["FIXTURE_GATE"], "local")
	}
}

func TestConfigureEnvChooserNotSetRemovesTheKey(t *testing.T) {
	model, repositoryRoot := openConfigureAtEnv(t, map[string]string{"FIXTURE_GATE": "cdp"})
	model = pressKeyTimes(t, model, "right", 1) // cdp -> wraps to (not set)
	if _, set := saveThroughRows(t, model, repositoryRoot)["FIXTURE_GATE"]; set {
		t.Fatal("choosing (not set) must remove the key from the saved profile")
	}
}

func TestConfigureEnvChooserKeepsAStoredValueOutsideItsOptions(t *testing.T) {
	model, repositoryRoot := openConfigureAtEnv(t, map[string]string{"FIXTURE_GATE": "retired-gate"})
	if !strings.Contains(model.View(), "retired-gate (not among the options)") {
		t.Fatalf("a stored value outside the options must be shown and marked:\n%s", model.View())
	}
	if got := saveThroughRows(t, model, repositoryRoot)["FIXTURE_GATE"]; got != "retired-gate" {
		t.Fatalf("saving without touching dropped the stored value: got %q", got)
	}
}

func TestConfigureEnvChooserPrefillsTheDeclaredDefault(t *testing.T) {
	model, repositoryRoot := openConfigureAtEnv(t, nil)
	model = pressKeyTimes(t, model, "enter", 1) // leave FIXTURE_GATE unset, reach FIXTURE_HOST
	environment := func() map[string]string {
		model = pressKeyTimes(t, model, "enter", 3)
		written, err := profile.Load(repositoryRoot)
		if err != nil {
			t.Fatal(err)
		}
		return written.Tools["gated"].Env
	}()
	if environment["FIXTURE_HOST"] != "local" {
		t.Fatalf("saved FIXTURE_HOST = %q, want the declared default %q accepted untouched", environment["FIXTURE_HOST"], "local")
	}
}

// TestConfigureEnvRequiredChooserDoesNotOfferNotSet pins that a required row cannot be saved
// unset, which the launch would refuse, and that it prefills the first option.
func TestConfigureEnvRequiredChooserDoesNotOfferNotSet(t *testing.T) {
	entry := formats.ToolManifestEnvElem{Name: "REQ", Required: true, Options: []formats.Option{{Value: "a"}, {Value: "b"}}}
	chooser := newEnvChooser(entry, nil)
	if got := strings.Join(chooser.labels(), "|"); got != "a|b" {
		t.Fatalf("required chooser labels = %q, want a|b", got)
	}
	if chooser.selectedValue() != "a" {
		t.Fatalf("required chooser with no value must prefill the first option, got %q", chooser.selectedValue())
	}
	stored := newEnvChooser(entry, map[string]string{"REQ": "gone"})
	if got := strings.Join(stored.labels(), "|"); got != "a|b|gone"+chooserOutsideSuffix || stored.selectedValue() != "gone" {
		t.Fatalf("a kept stored value must stay in a required cycle: %q selected %q", got, stored.selectedValue())
	}
	optional := newEnvChooser(formats.ToolManifestEnvElem{Name: "OPT", Options: entry.Options}, nil)
	if optional.labels()[0] != chooserNotSetLabel || optional.selectedValue() != "" {
		t.Fatalf("an optional chooser still starts at (not set): %v", optional.labels())
	}
}

func TestConfigureEnvSecretRowIgnoresDeclaredOptions(t *testing.T) {
	model, repositoryRoot := openConfigureAtEnv(t, nil)
	model = pressKeyTimes(t, model, "enter", 2) // reach FIXTURE_TOKEN
	if model.configureEnv.EchoMode != textinput.EchoPassword {
		t.Fatal("a secret row must stay masked even when it declares options")
	}
	if strings.Contains(model.View(), "never-shown-option") {
		t.Fatalf("a secret row must not render its options:\n%s", model.View())
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("MY_SOURCE")})
	model = pressKeyTimes(t, model, "enter", 2)
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := written.Tools["gated"].Env["FIXTURE_TOKEN"]; got != "env.MY_SOURCE" {
		t.Fatalf("saved FIXTURE_TOKEN = %q, want env.MY_SOURCE", got)
	}
}

func TestConfigureEnvRowWithoutOptionsKeepsItsTextInput(t *testing.T) {
	model, _ := openConfigureAtEnv(t, nil)
	model = pressKeyTimes(t, model, "enter", 3) // reach FIXTURE_FREE
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("typed")})
	if model.configureEnv.Value() != "typed" {
		t.Fatalf("a row without options must stay free text, got %q", model.configureEnv.Value())
	}
}

// TestDetailLaunchArgvIncludesProfileArguments pins the launch screen's "what will run"
// preview to the argv the launch path really builds, profile arguments included.
func TestDetailLaunchArgvIncludesProfileArguments(t *testing.T) {
	tool := discovery.Tool{
		Name: "runner", Title: "Runner",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: existingExecutable(t), Args: []string{"base"}}},
	}
	repositoryRoot := t.TempDir()
	model := New(discovery.Catalog{Tools: []discovery.Tool{tool}}, repositoryRoot)
	model.repositoryProfile.Tools["runner"] = profile.Tool{Args: []string{"--write", "--app", "web"}}
	prepared, err := launch.Prepare(launch.Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Profile: model.repositoryProfile,
		Store: runs.Store{StateDirectory: t.TempDir()}, StartedAt: time.Now(),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	want := "Launch argv: " + encodeArgv(prepared.Row.Argv)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model.width = 500
	flattened := strings.Join(strings.Fields(model.View()), " ")
	if !strings.Contains(flattened, strings.Join(strings.Fields(want), " ")) {
		t.Fatalf("detail view does not show the real argv %s:\n%s", want, model.View())
	}
}

func TestSpecPromptArgvIncludesProfileArguments(t *testing.T) {
	tool := discovery.Tool{
		Name: "runner", Title: "Runner",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: existingExecutable(t), Args: []string{"run", "{spec}"}}},
	}
	repositoryRoot := t.TempDir()
	model := New(discovery.Catalog{Tools: []discovery.Tool{tool}}, repositoryRoot)
	model.repositoryProfile.Tools["runner"] = profile.Tool{Args: []string{"--write"}}
	model.pendingTool = tool
	model.specInput.SetValue("a.spec")
	prepared, err := launch.Prepare(launch.Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Profile: model.repositoryProfile, Spec: "a.spec",
		Store: runs.Store{StateDirectory: t.TempDir()}, StartedAt: time.Now(),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	preview := model.specArgvPreview(200)
	if !strings.Contains(preview, encodeArgv(prepared.Row.Argv)) {
		t.Fatalf("spec prompt preview %q does not show the real argv %s", preview, encodeArgv(prepared.Row.Argv))
	}
}

func existingExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

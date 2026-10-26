package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
)

func declaredArgumentsTool() discovery.Tool {
	return discovery.Tool{
		Name: "healer", Title: "Healer",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "true"},
			Arguments: []formats.Argument{
				{Flag: "--healer", Kind: formats.ArgumentKindChoice, Description: strPtr("Which healer repairs the spec."),
					Options: []formats.Option{{Value: "openai-compatible"}, {Value: "agent:claude"}}},
				{Flag: "--write", Kind: formats.ArgumentKindFlag, Description: strPtr("Write the repaired spec back.")},
				{Flag: "--app", Kind: formats.ArgumentKindValue, Description: strPtr("Directory of the app.")},
			},
		},
	}
}

// openConfigureAtArguments opens the customize screen and passes the mode step, leaving the
// model on the first argument row.
func openConfigureAtArguments(t *testing.T, tool discovery.Tool, stored []string) (Model, string) {
	t.Helper()
	repositoryRoot := t.TempDir()
	model := New(discovery.Catalog{Tools: []discovery.Tool{tool}}, repositoryRoot)
	if stored != nil {
		model.repositoryProfile.Tools[tool.Name] = profile.Tool{Args: stored}
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // mode
	return model, repositoryRoot
}

func pressKey(t *testing.T, model Model, keyType tea.KeyType) Model {
	t.Helper()
	return updateModel(t, model, tea.KeyMsg{Type: keyType})
}

func TestConfigureFormIsDrivenByDeclaredArguments(t *testing.T) {
	model, repositoryRoot := openConfigureAtArguments(t, declaredArgumentsTool(), nil)
	view := model.View()
	if !strings.Contains(view, "Argument 1/3") || !strings.Contains(view, "Which healer repairs the spec.") {
		t.Fatalf("first row not rendered as a described argument row:\n%s", view)
	}
	model = pressKey(t, model, tea.KeyRight)                                            // (not set) -> openai-compatible
	model = pressKey(t, model, tea.KeyRight)                                            // -> agent:claude
	model = pressKey(t, model, tea.KeyEnter)                                            // next row
	model = pressKey(t, model, tea.KeyRight)                                            // --write on
	model = pressKey(t, model, tea.KeyEnter)                                            // next row
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("web")}) // --app value
	model = pressKey(t, model, tea.KeyEnter)                                            // Extra row
	if !strings.Contains(model.View(), "Extra arguments") {
		t.Fatalf("no Extra arguments row after the declared rows:\n%s", model.View())
	}
	model = pressKey(t, model, tea.KeyEnter) // env step (free text)
	model = pressKey(t, model, tea.KeyEnter) // save
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--healer", "agent:claude", "--write", "--app", "web"}
	if !reflect.DeepEqual(written.Tools["healer"].Args, want) {
		t.Fatalf("saved args = %v, want %v", written.Tools["healer"].Args, want)
	}
}

func TestConfigureFormPrefillsDeclaredArgumentsFromTheSavedProfile(t *testing.T) {
	stored := []string{"--healer", "openai-compatible", "--unknown", "x"}
	model, repositoryRoot := openConfigureAtArguments(t, declaredArgumentsTool(), stored)
	if model.argumentRows.choiceIndex != 1 {
		t.Fatalf("choice preselection = %d, want 1 (openai-compatible)", model.argumentRows.choiceIndex)
	}
	model = pressKey(t, model, tea.KeyEnter) // --healer
	model = pressKey(t, model, tea.KeyEnter) // --write
	model = pressKey(t, model, tea.KeyEnter) // --app
	if !strings.Contains(model.View(), "--unknown, x") {
		t.Fatalf("Extra row does not show the unmatched tokens:\n%s", model.View())
	}
	model = pressKey(t, model, tea.KeyEnter)
	model = pressKey(t, model, tea.KeyEnter)
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written.Tools["healer"].Args, stored) {
		t.Fatalf("unchanged save = %v, want %v", written.Tools["healer"].Args, stored)
	}
}

func TestConfigureChoiceKeepsAStoredValueOutsideItsOptionsAndMarksIt(t *testing.T) {
	stored := []string{"--healer", "legacy-healer"}
	model, repositoryRoot := openConfigureAtArguments(t, declaredArgumentsTool(), stored)
	if !strings.Contains(model.View(), "legacy-healer (not among the options)") {
		t.Fatalf("outside value not shown and marked:\n%s", model.View())
	}
	for range 5 {
		model = pressKey(t, model, tea.KeyEnter)
	}
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written.Tools["healer"].Args, stored) {
		t.Fatalf("outside value dropped: saved %v, want %v", written.Tools["healer"].Args, stored)
	}
}

func TestConfigureArgumentDefaultPrefillsOnlyRowsWithoutAStoredValue(t *testing.T) {
	tool := declaredArgumentsTool()
	defaultValue := "agent:claude"
	tool.Manifest.Arguments[0].Default = &defaultValue
	flagDefault := "x"
	tool.Manifest.Arguments[1].Default = &flagDefault
	model, _ := openConfigureAtArguments(t, tool, nil)
	if model.argumentRows.choiceIndex != 2 {
		t.Fatalf("default not preselected: index %d, want 2", model.argumentRows.choiceIndex)
	}
	model = pressKey(t, model, tea.KeyEnter)
	if model.argumentRows.choiceIndex != 0 {
		t.Fatalf("a default must be ignored for kind flag; chooser index %d, want 0 (off)", model.argumentRows.choiceIndex)
	}
	stored, _ := openConfigureAtArguments(t, tool, []string{"--healer", "openai-compatible"})
	if stored.argumentRows.choiceIndex != 1 {
		t.Fatalf("stored value must win over default: index %d, want 1", stored.argumentRows.choiceIndex)
	}
}

func TestConfigureFormWithoutDeclaredArgumentsBehavesExactlyAsBefore(t *testing.T) {
	tool := discovery.Tool{Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "true"}}}
	model, repositoryRoot := openConfigureAtArguments(t, tool, []string{"--a", "b"})
	if model.configureArgs.Prompt != "Arguments: " || model.configureArgs.Placeholder != "--flag, value" {
		t.Fatalf("free-text args field changed: prompt %q placeholder %q", model.configureArgs.Prompt, model.configureArgs.Placeholder)
	}
	if !strings.Contains(model.View(), "Comma-separated argv · enter continue · esc cancel") ||
		strings.Contains(model.View(), "Argument 1/") {
		t.Fatalf("free-text args step view changed:\n%s", model.View())
	}
	model = pressKey(t, model, tea.KeyEnter)
	model = pressKey(t, model, tea.KeyEnter)
	written, err := profile.Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written.Tools["checks"].Args, []string{"--a", "b"}) {
		t.Fatalf("free-text round trip = %v", written.Tools["checks"].Args)
	}
}

//go:build windows

package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
)

// TestModeChooserWithholdsPaneOnWindowsForAnOrdinaryTool covers the platform half of the
// withholding rule (TestModeChooserWithholdsPaneForAToolThatNeedsARealTerminal, in model_test.go,
// covers the manifest-declared half): an ordinary tool with no pane.interactive constraint
// still cannot be offered `pane` on Windows, where hivedeck has never exercised a captured pane.
func TestModeChooserWithholdsPaneOnWindowsForAnOrdinaryTool(t *testing.T) {
	repositoryRoot := t.TempDir()
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "checks", Title: "Checks",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "true"}},
	}}}
	model := New(catalog, repositoryRoot)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	view := model.View()
	if strings.Contains(view, " pane ") {
		t.Fatalf("mode chooser offers pane on Windows, where it is unverified:\n%s", view)
	}
	if !strings.Contains(view, "not available on Windows") {
		t.Fatalf("mode chooser does not explain the platform reason:\n%s", view)
	}
}

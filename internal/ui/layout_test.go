package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/manifest"
	"github.com/hive-controls/hivedeck/internal/runs"
)

func TestTruncateNeverExceedsTheLimitAndMarksTheLoss(t *testing.T) {
	long := "End-to-end doctor — diagnose a failing suite against a live target"
	for _, limit := range []int{0, 1, 2, 5, 20, 66, 67, 200} {
		got := truncate(long, limit)
		if lipgloss.Width(got) > limit {
			t.Fatalf("truncate(limit=%d) = %q, width %d exceeds the limit", limit, got, lipgloss.Width(got))
		}
		if limit > 0 && limit < lipgloss.Width(long) && !strings.HasSuffix(got, ellipsis) {
			t.Fatalf("truncate(limit=%d) = %q, want a trailing ellipsis marking the loss", limit, got)
		}
	}
	if got := truncate(long, 200); got != long {
		t.Fatalf("truncate past the text length = %q, want the text unchanged", got)
	}
}

// Display width, not rune count: a two-column rune must count as two, or a row of CJK titles
// overflows the column it was measured into.
func TestTruncateMeasuresDisplayWidthNotRunes(t *testing.T) {
	wide := "日本語のツール名"
	got := truncate(wide, 7)
	if lipgloss.Width(got) > 7 {
		t.Fatalf("truncate(%q, 7) = %q, width %d", wide, got, lipgloss.Width(got))
	}
	if lipgloss.Width(got) < 6 {
		t.Fatalf("truncate(%q, 7) = %q, width %d — too conservative, it dropped a column it could keep", wide, got, lipgloss.Width(got))
	}
}

func TestTruncatePathKeepsTheTail(t *testing.T) {
	value := "/home/example/.local/state/hivedeck/runs/20260905T090412/evidence.html"
	got := truncatePath(value, 30)
	if lipgloss.Width(got) > 30 {
		t.Fatalf("truncatePath = %q, width %d exceeds 30", got, lipgloss.Width(got))
	}
	if !strings.HasPrefix(got, ellipsis) {
		t.Fatalf("truncatePath = %q, want a LEADING ellipsis", got)
	}
	if !strings.HasSuffix(got, "evidence.html") {
		t.Fatalf("truncatePath = %q, want the filename kept — the tail is what identifies a path", got)
	}
}

func TestPadFillsExactlyTheColumnWidth(t *testing.T) {
	for _, testCase := range []struct {
		text  string
		width int
	}{
		{"repo", 7}, {"product", 7}, {"a very long tool title indeed", 7}, {"", 5},
	} {
		got := pad(testCase.text, testCase.width)
		if lipgloss.Width(got) != testCase.width {
			t.Fatalf("pad(%q, %d) = %q, width %d", testCase.text, testCase.width, got, lipgloss.Width(got))
		}
	}
}

func TestVisibleWindowKeepsTheCursorOnScreen(t *testing.T) {
	const count, capacity = 40, 9
	for cursor := 0; cursor < count; cursor++ {
		start, end := visibleWindow(cursor, count, capacity)
		if end-start != capacity {
			t.Fatalf("cursor %d: window [%d,%d) holds %d rows, want %d", cursor, start, end, end-start, capacity)
		}
		if cursor < start || cursor >= end {
			t.Fatalf("cursor %d falls outside its own window [%d,%d)", cursor, start, end)
		}
		if start < 0 || end > count {
			t.Fatalf("cursor %d: window [%d,%d) runs outside [0,%d)", cursor, start, end, count)
		}
	}
}

func TestVisibleWindowShowsEverythingWhenItFits(t *testing.T) {
	if start, end := visibleWindow(3, 5, 20); start != 0 || end != 5 {
		t.Fatalf("visibleWindow = [%d,%d), want the whole list [0,5)", start, end)
	}
	if start, end := visibleWindow(0, 0, 20); start != 0 || end != 0 {
		t.Fatalf("visibleWindow over an empty list = [%d,%d), want [0,0)", start, end)
	}
}

func TestListCapacityNeverCollapsesToZero(t *testing.T) {
	if got := listCapacity(24, 8); got != 16 {
		t.Fatalf("listCapacity(24, 8) = %d, want 16", got)
	}
	if got := listCapacity(4, 8); got != minimumListRows {
		t.Fatalf("listCapacity on a terminal shorter than its own chrome = %d, want %d", got, minimumListRows)
	}
}

func TestScrollHintNamesTheHiddenRows(t *testing.T) {
	if got := scrollHint(0, 10, 10); got != "" {
		t.Fatalf("scrollHint with nothing hidden = %q, want empty", got)
	}
	if got := scrollHint(0, 9, 40); got != "↓ 31 more" {
		t.Fatalf("scrollHint below = %q", got)
	}
	if got := scrollHint(31, 40, 40); got != "↑ 31 more" {
		t.Fatalf("scrollHint above = %q", got)
	}
	if got := scrollHint(5, 14, 40); got != "↑ 5 more · ↓ 26 more" {
		t.Fatalf("scrollHint both = %q", got)
	}
}

// The regression that gates every render: a WindowSizeMsg was captured into the model and read
// by nothing, so no view ever changed shape. Every line of every view must now fit the reported
// width, and the list must not emit more rows than the reported height can hold.
func TestEveryViewFitsTheReportedTerminalSize(t *testing.T) {
	catalog := manyTools(40)
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}, {Width: 40, Height: 12}} {
		model := newModel(catalog, "/home/example/Documents/repositories/example-workspace", testStore(t))
		model = updateModel(t, model, size)
		for _, stage := range viewStages(t, model) {
			assertFits(t, stage.name, stage.view, size)
		}
	}
}

type namedView struct {
	name string
	view string
}

func viewStages(t *testing.T, model Model) []namedView {
	t.Helper()
	list := model.View()
	deep := updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	deep = updateModel(t, deep, tea.KeyMsg{Type: tea.KeyDown})
	scrolled := deep.View()
	detail := updateModel(t, deep, tea.KeyMsg{Type: tea.KeyEnter})
	runList := updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	return []namedView{
		{"list", list},
		{"list scrolled", scrolled},
		{"detail", detail.View()},
		{"runs", runList.View()},
	}
}

func assertFits(t *testing.T, name, view string, size tea.WindowSizeMsg) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > size.Height {
		t.Fatalf("%s at %dx%d emitted %d lines, more than the terminal holds:\n%s",
			name, size.Width, size.Height, len(lines), view)
	}
	for index, line := range lines {
		if lipgloss.Width(line) > size.Width {
			t.Fatalf("%s at %dx%d: line %d is %d columns wide and will wrap:\n%q",
				name, size.Width, size.Height, index+1, lipgloss.Width(line), line)
		}
	}
}

// A selected row is ONE visual object. lipgloss closes an inner style with a full SGR reset,
// which also closes the enclosing selection, so a cell that carries its own colour cuts the
// highlight short at that column — visible as a selection bar that stops a third of the way
// across the row. The invariant: a selected row emits exactly one reset, at its end.
// A selected row is ONE visual object. lipgloss closes an inner style with a full SGR reset,
// which also closes the enclosing selection, so a cell carrying its own style cuts the
// highlight short at that column — a selection bar that stops a third of the way across.
//
// The assertion uses a WIDTH style rather than a colour one on purpose: `go test` has no TTY,
// so lipgloss renders under the Ascii profile and strips every colour, which would make a
// colour-based assertion pass whatever the code did.
func TestSelectedRowCellsCarryNoStyleOfTheirOwn(t *testing.T) {
	observable := lipgloss.NewStyle().Width(10)

	if got := cellStyler(true)(observable, "cell"); got != "cell" {
		t.Fatalf("cellStyler(true) returned %q, want %q — a styled row's cells must stay plain", got, "cell")
	}
	if got := cellStyler(false)(observable, "cell"); got != observable.Render("cell") {
		t.Fatalf("cellStyler(false) returned %q, want the style applied", got)
	}
}

func testStore(t *testing.T) runs.Store {
	t.Helper()
	return runs.Store{StateDirectory: t.TempDir()}
}

func manyTools(count int) discovery.Catalog {
	tools := make([]discovery.Tool, 0, count)
	for index := 0; index < count; index++ {
		tool := discovery.Tool{
			Name:  "tool-" + strings.Repeat("x", index%7) + string(rune('a'+index%26)),
			Title: "A deliberately long tool title that no eighty-column terminal can hold, number " + string(rune('a'+index%26)),
			Description: "A description long enough to need reflowing across several lines rather than " +
				"being hard-wrapped mid-word by the terminal itself.",
			Source: discovery.Source{
				Kind: discovery.SourceLocal,
				Path: "/home/example/Documents/repositories/example-workspace/.hivedeck/tools/very-long-name.yaml",
			},
		}
		if index%5 == 0 {
			tool.Problem = manifest.UnsupportedManifestVersionPrefix + ": 2"
		}
		if index%7 == 0 {
			tool.Shadowed = []discovery.Definition{{Source: discovery.Source{
				Kind: discovery.SourceUser,
				Path: "/home/example/.config/hivedeck/tools/very-long-name.yaml",
			}}}
		}
		tools = append(tools, tool)
	}
	return discovery.Catalog{Tools: tools}
}

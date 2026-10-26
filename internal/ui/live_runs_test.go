package ui

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

func runRowFor(id, toolName, outcome string) runs.Row {
	return runs.Row{ID: id, Tool: toolName, Mode: profile.ModeForeground, Outcome: outcome, StartedAt: time.Unix(1700000000, 0)}
}

// runListModel is a wide deck showing the runs list over the given rows.
func runListModel(t *testing.T, tools []discovery.Tool, prober *recordingProber, rows []runs.Row, height int) Model {
	t.Helper()
	model := liveModel(t, tools, prober)
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 200, Height: height})
	model.showRuns = true
	model.runRows = rows
	return model
}

func TestRunListShowsTheToolsLiveStateBesideItsRows(t *testing.T) {
	prober := &recordingProber{state: launch.ProbeUp}
	model := runListModel(t, []discovery.Tool{probedTool("alpha", true)}, prober,
		[]runs.Row{runRowFor("run-1", "alpha", "ok")}, 40)
	for _, message := range runBatch(model.probeVisible()) {
		model = updateModel(t, model, message)
	}
	var line string
	for _, candidate := range strings.Split(model.View(), "\n") {
		if strings.Contains(candidate, "run-1") {
			line = candidate
		}
	}
	liveAt, modeAt, outcomeAt := strings.Index(line, "up"), strings.Index(line, "foreground"), strings.Index(line, "ok")
	if liveAt < 0 || modeAt < 0 || outcomeAt < 0 || !(liveAt < modeAt && modeAt < outcomeAt) {
		t.Fatalf("row should read tool, live state, mode, ... outcome:\n%s", line)
	}
}

func TestRunListProbesOnlyToolsOfVisibleRows(t *testing.T) {
	prober := &recordingProber{}
	tools := []discovery.Tool{probedTool("alpha", true), probedTool("beta", true), probedTool("gone-from-view", true)}
	rows := []runs.Row{
		runRowFor("run-1", "alpha", "ok"),
		runRowFor("run-2", "alpha", "failed"),
		runRowFor("run-3", "beta", "ok"),
	}
	for index := 0; index < 10; index++ {
		rows = append(rows, runRowFor(fmt.Sprintf("run-gone-%02d", index), "no-longer-in-catalog", "ok"))
	}
	for index := 0; index < 30; index++ {
		rows = append(rows, runRowFor(fmt.Sprintf("run-far-%02d", index), "gone-from-view", "ok"))
	}
	model := runListModel(t, tools, prober, rows, 12)
	runBatch(model.probeVisible())
	called := prober.called()
	sort.Strings(called)
	if strings.Join(called, ",") != "alpha,beta" {
		t.Fatalf("probed %v, want exactly alpha and beta once each", called)
	}
}

func TestRunListRowOfToolWithoutProbeShowsDash(t *testing.T) {
	prober := &recordingProber{}
	model := runListModel(t, []discovery.Tool{probedTool("alpha", true), probedTool("plain", false)}, prober,
		[]runs.Row{runRowFor("run-1", "plain", "ok"), runRowFor("run-2", "removed-tool", "ok")}, 40)
	model.runRows = append(model.runRows, runRowFor("run-3", "alpha", "ok"))
	if len(model.probeTargets()) != 1 {
		t.Fatalf("only alpha should be targeted: %v", model.probeTargets())
	}
	for _, id := range []string{"run-1", "run-2"} {
		for _, line := range strings.Split(model.View(), "\n") {
			if strings.Contains(line, id) && !strings.Contains(line, "–") {
				t.Fatalf("row %s shows no dash:\n%s", id, line)
			}
		}
	}
}

func TestRunListMixedWindowShowsDashForProbeLessRows(t *testing.T) {
	prober := &recordingProber{state: launch.ProbeUp}
	model := runListModel(t, []discovery.Tool{probedTool("alpha", true), probedTool("plain", false)}, prober,
		[]runs.Row{runRowFor("run-1", "alpha", "ok"), runRowFor("run-2", "plain", "ok")}, 40)
	for _, message := range runBatch(model.probeVisible()) {
		model = updateModel(t, model, message)
	}
	columnOf := map[string]int{}
	for _, line := range strings.Split(model.View(), "\n") {
		for _, id := range []string{"run-1", "run-2"} {
			if strings.Contains(line, id) {
				columnOf[id] = lipglossWidthBefore(line, "foreground")
				if (id == "run-1") != strings.Contains(line, "up") || (id == "run-2") != strings.Contains(line, "–") {
					t.Fatalf("row %s has the wrong live cell: %q", id, line)
				}
			}
		}
	}
	if columnOf["run-1"] == 0 || columnOf["run-1"] != columnOf["run-2"] {
		t.Fatalf("rows are not aligned: %v", columnOf)
	}
}

func lipglossWidthBefore(line, marker string) int {
	return lipgloss.Width(line[:strings.Index(line, marker)])
}

func TestRunListLiveColumnStaysVisibleAtANormalWidthWithLongArtifacts(t *testing.T) {
	prober := &recordingProber{state: launch.ProbeDown}
	row := runRowFor("20260101T000000.000000000Z-1234-1", "alpha", "needs-human")
	row.Artifact = "C:/devprojects/some-repository/.formic/evidence/2026-10-01/very-long-artifact-directory-name/report.html"
	model := runListModel(t, []discovery.Tool{probedTool("alpha", true)}, prober, []runs.Row{row}, 40)
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 110, Height: 40})
	for _, message := range runBatch(model.probeVisible()) {
		model = updateModel(t, model, message)
	}
	var line string
	for _, candidate := range strings.Split(model.View(), "\n") {
		if strings.Contains(candidate, "20260101T") {
			line = candidate
		}
	}
	liveAt, modeAt := strings.Index(line, "down"), strings.Index(line, "foreground")
	if liveAt < 0 || modeAt < 0 || liveAt > modeAt {
		t.Fatalf("live word missing or after mode at width 110:\n%s", line)
	}
}

func TestRunListWithoutProbedToolsRendersNoLiveColumn(t *testing.T) {
	prober := &recordingProber{}
	model := runListModel(t, []discovery.Tool{probedTool("plain", false)}, prober,
		[]runs.Row{runRowFor("run-1", "plain", "ok")}, 40)
	row := model.runRows[0]
	columns := model.runColumnWidths(model.runRows)
	want := pad(row.ID, columns.id) + "  " + pad(runToolLabel(row), columns.tool) + "  " +
		pad(string(row.Mode), columns.mode) + "  " + row.StartedAt.Local().Format("2006-01-02 15:04:05") + "  " +
		pad(model.runExitCode(row), columns.exit) + "  " + pad(row.Outcome, columns.outcome) + "  —"
	if got := model.runRow(row, columns, 198, false); got != want {
		t.Fatalf("probe-less row changed:\n got %q\nwant %q", got, want)
	}
}

func TestRunListMatchesRenderedRowsWindow(t *testing.T) {
	prober := &recordingProber{}
	var rows []runs.Row
	for index := 0; index < 40; index++ {
		rows = append(rows, runRowFor(fmt.Sprintf("run-%03d", index), "alpha", "ok"))
	}
	model := runListModel(t, []discovery.Tool{probedTool("alpha", true)}, prober, rows, 14)
	model.runCursor = 25
	window := model.runListWindow()
	if len(window) == 0 || len(window) >= len(rows) {
		t.Fatalf("window size %d does not exercise scrolling", len(window))
	}
	inWindow := map[string]bool{}
	for _, row := range window {
		inWindow[row.ID] = true
	}
	view := model.View()
	for _, row := range rows {
		if rendered := strings.Contains(view, row.ID); rendered != inWindow[row.ID] {
			t.Fatalf("row %s rendered=%v but inWindow=%v", row.ID, rendered, inWindow[row.ID])
		}
	}
}

package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/hivedeck/internal/launch"
	"github.com/hive-controls/hivedeck/internal/pane"
	"github.com/hive-controls/hivedeck/internal/runs"
)

// paneRenderInterval is the pane host's whole rate limit. Child output is read continuously
// into each pane's bounded buffer and produces NO message of any kind; the only thing that
// repaints the screen is this tick. A child writing a million lines a second therefore costs
// the UI loop exactly one frame per interval, the same as a child writing one line a minute.
const paneRenderInterval = 100 * time.Millisecond

// maximumPanes is the fixed split's capacity. Anything beyond it would need a layout engine,
// and a layout engine is not what a person opening two tools side by side is asking for.
const maximumPanes = 4

// paneSeparator sits between two columns of the split.
const paneSeparator = " │ "

// paneChromeRows is what a pane spends on something other than the child's output: its header
// and the help line under the whole split.
const paneChromeRows = 3

type paneTickMsg time.Time

type paneFinishedMsg struct {
	runID string
}

func defaultPaneTicker(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(now time.Time) tea.Msg { return paneTickMsg(now) })
}

// startPane is the pane launch route: the child is captured on its own pseudo-terminal, and
// the run row records `pane` as the route it actually took.
func (model Model) startPane(prepared launch.Prepared, title string) (tea.Model, tea.Cmd) {
	if len(model.panes) >= maximumPanes {
		model.notice = fmt.Sprintf("The split already holds %d panes — close one first.", maximumPanes)
		return model, nil
	}
	opened, err := pane.Open(prepared.Row.ID, title, prepared.Command, model.paneSize(len(model.panes)+1), pane.Options{})
	if err != nil {
		model.notice = err.Error()
		_, _ = launch.Finish(model.runStore, prepared.Row.ID, err, model.clock())
		return model, nil
	}
	model.watchRun(prepared.Row.ID)
	model.panes = append(model.panes, opened)
	model.paneFocus = len(model.panes) - 1
	model.showPanes, model.detail, model.showRuns = true, false, false
	model.notice = ""
	model, tick := model.ensureTicking()
	return model, tea.Batch(tick, awaitPaneExit(opened), model.resizePanes())
}

func (model Model) paneTick() tea.Cmd {
	ticker := model.paneTicker
	if ticker == nil {
		ticker = defaultPaneTicker
	}
	return ticker(paneRenderInterval)
}

func awaitPaneExit(watched *pane.Pane) tea.Cmd {
	return func() tea.Msg {
		<-watched.Done()
		return paneFinishedMsg{runID: watched.RunID}
	}
}

// watchRun starts tailing a run's status file. Only a run this process is hosting is watched
// here: a tab run is watched by its own wrapper, in the tab, and a foreground run is read
// after it exits because the child owns the screen while it runs.
func (model *Model) watchRun(runID string) {
	if model.runMonitors == nil {
		model.runMonitors = make(map[string]*runs.Monitor)
	}
	model.runMonitors[runID] = runs.NewMonitor(model.runStore.StatusFile(runID))
}

// ensureTicking starts the one ticker, if it is not already running. Two tickers would double
// every frame and every status poll for the rest of the session.
func (model Model) ensureTicking() (Model, tea.Cmd) {
	if model.ticking {
		return model, nil
	}
	model.ticking = true
	return model, model.paneTick()
}

// handlePaneTick is the one place a pane frame is produced from, and the one place live status
// is polled. It reschedules itself only while there is something to watch, so a session that
// never opens a pane or a run list never pays for the ticker.
func (model Model) handlePaneTick() (Model, tea.Cmd) {
	model = model.pollLiveRuns()
	if len(model.panes) == 0 && !model.showRuns {
		model.ticking = false
		return model, nil
	}
	return model, model.paneTick()
}

// pollLiveRuns reads each watched run's status file and writes what it says onto that run's
// row — state and the last artifact pointer — so the run list shows a run while it is still
// going. The exit code still decides the FINAL outcome; nothing here can pre-empt it.
func (model Model) pollLiveRuns() Model {
	changed := false
	for runID, monitor := range model.runMonitors {
		update, moved := monitor.Poll()
		if !moved {
			continue
		}
		if _, err := model.runStore.UpdateLive(runID, update); err == nil {
			changed = true
		}
	}
	if changed || model.showRuns {
		model.refreshRuns()
	}
	return model
}

func stoppingNotice(title string) string {
	return "Stopping " + title + "…"
}

func (model Model) handlePaneFinished(message paneFinishedMsg) Model {
	finished := model.paneByRunID(message.runID)
	if finished == nil {
		return model
	}
	// The pane's own header now says what happened to it, so the in-progress notice this
	// stop raised has nothing left to report and would otherwise sit there for the session.
	if model.notice == stoppingNotice(finished.Title) {
		model.notice = ""
	}
	if _, err := launch.Finish(model.runStore, message.runID, finished.WaitError(), model.clock()); err != nil {
		model.notice = err.Error()
	}
	model.refreshRuns()
	return model
}

func (model Model) paneByRunID(runID string) *pane.Pane {
	for _, candidate := range model.panes {
		if candidate.RunID == runID {
			return candidate
		}
	}
	return nil
}

// resizePanes propagates the split's current geometry to every child, which is what raises
// SIGWINCH in each of them.
func (model Model) resizePanes() tea.Cmd {
	panes, size := model.panes, model.paneSize(len(model.panes))
	return func() tea.Msg {
		for _, resized := range panes {
			_ = resized.Resize(size)
		}
		return nil
	}
}

func (model Model) paneSize(count int) pane.Size {
	if count < 1 {
		count = 1
	}
	columns := (model.layoutWidth() - (count-1)*lipgloss.Width(paneSeparator)) / count
	if columns < 20 {
		columns = 20
	}
	rows := model.layoutHeight() - paneChromeRows
	if rows < 1 {
		rows = 1
	}
	return pane.Size{Rows: uint16(rows), Columns: uint16(columns)}
}

func (model Model) updatePaneKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "left", "backspace":
		model.showPanes = false
		return model, nil
	case "tab", "right":
		model.paneFocus = wrapCursor(model.paneFocus+1, len(model.panes))
		return model, nil
	case "shift+tab":
		model.paneFocus = wrapCursor(model.paneFocus-1, len(model.panes))
		return model, nil
	case "x":
		return model.terminateFocusedPane()
	case "w":
		return model.closeFinishedPanes(), nil
	case "q":
		return model.quit()
	}
	return model, nil
}

// terminateFocusedPane is the ONLY thing that kills a pane's child: explicit, and never on a
// timer. A long silent run is legal, so nothing here watches the clock.
func (model Model) terminateFocusedPane() (tea.Model, tea.Cmd) {
	if len(model.panes) == 0 {
		return model, nil
	}
	target := model.panes[clampCursor(model.paneFocus, len(model.panes))]
	model.notice = stoppingNotice(target.Title)
	return model, func() tea.Msg {
		_ = target.Terminate()
		return nil
	}
}

// closeFinishedPanes removes the panes whose children have already exited. A finished pane is
// kept until it is closed on purpose, so a run that ended while the user was elsewhere is
// still there to read.
func (model Model) closeFinishedPanes() Model {
	kept := make([]*pane.Pane, 0, len(model.panes))
	for _, candidate := range model.panes {
		if candidate.Snapshot().Running {
			kept = append(kept, candidate)
			continue
		}
		_ = candidate.Close()
	}
	model.panes = kept
	model.paneFocus = clampCursor(model.paneFocus, len(model.panes))
	if len(model.panes) == 0 {
		model.showPanes = false
	}
	return model
}

// terminateAllPanes runs on quit. Leaving a captured child alive with nothing reading its
// pseudo-terminal would block it on a full buffer forever, so quitting is the one moment every
// pane is stopped — explicitly, by the same two-stage kill.
func (model Model) terminateAllPanes() {
	for _, running := range model.panes {
		_ = running.Terminate()
		_ = running.Close()
	}
}

// paneView renders ONE frame for every pane: the split is composed here, in a single string,
// and bubbletea diffs that against the previous frame. N panes cost one frame, never N.
func (model Model) paneView() string {
	width, height := model.layoutWidth(), model.layoutHeight()
	snapshots := make([]pane.Snapshot, 0, len(model.panes))
	for _, visible := range model.panes {
		snapshots = append(snapshots, visible.Snapshot())
	}
	columnWidth := (width - (len(snapshots)-1)*lipgloss.Width(paneSeparator)) / max(len(snapshots), 1)
	bodyRows := height - paneChromeRows
	columns := make([][]string, 0, len(snapshots))
	for index, snapshot := range snapshots {
		columns = append(columns, paneColumn(snapshot, columnWidth, bodyRows, index == model.paneFocus))
	}
	var content strings.Builder
	content.WriteString(strings.Join(joinColumns(columns, columnWidth), "\n"))
	content.WriteString("\n")
	appendNotice(&content, model.notice, width)
	content.WriteString(mutedStyle.Render(truncate("tab focus · x stop · w close finished · esc back · q quit", width)))
	return content.String()
}

// paneColumn is one pane's header plus the tail of its output, padded to exactly bodyRows so
// two panes of unequal length still line up beside each other.
func paneColumn(snapshot pane.Snapshot, width, bodyRows int, focused bool) []string {
	header := paneHeader(snapshot, width)
	if focused {
		header = selectedStyle.Render(header)
	} else {
		header = mutedStyle.Render(header)
	}
	rows := make([]string, 0, bodyRows+1)
	rows = append(rows, header)
	start := max(len(snapshot.Lines)-bodyRows, 0)
	for _, line := range snapshot.Lines[start:] {
		rows = append(rows, pad(line, width))
	}
	for len(rows) < bodyRows+1 {
		rows = append(rows, strings.Repeat(" ", max(width, 0)))
	}
	return rows
}

// paneHeader spends the column on the pane's STATE first and its title last. A long tool title
// would otherwise push the truncation marker and the decode-reset count off the end — and those
// two are the whole reason the header exists, where the title merely repeats what was launched.
func paneHeader(snapshot pane.Snapshot, width int) string {
	suffix := " · " + paneState(snapshot)
	if snapshot.Marker != "" {
		suffix += " · " + snapshot.Marker
	}
	if snapshot.Resets > 0 {
		suffix += fmt.Sprintf(" · %d decode reset(s)", snapshot.Resets)
	}
	title := truncate(snapshot.Title, width-lipgloss.Width(suffix))
	return pad(title+suffix, width)
}

// paneState is what became of the child. A pane someone stopped on purpose is reported as
// stopped, never as failed: the signal that ends it looks exactly like the one a crash reports.
func paneState(snapshot pane.Snapshot) string {
	switch {
	case snapshot.Running:
		return "running"
	case snapshot.Stopped:
		return "stopped"
	case snapshot.Failure != "":
		return "failed"
	default:
		return "exited"
	}
}

func joinColumns(columns [][]string, columnWidth int) []string {
	if len(columns) == 0 {
		return nil
	}
	rowCount := 0
	for _, column := range columns {
		rowCount = max(rowCount, len(column))
	}
	joined := make([]string, 0, rowCount)
	for row := 0; row < rowCount; row++ {
		cells := make([]string, 0, len(columns))
		for _, column := range columns {
			if row < len(column) {
				cells = append(cells, column[row])
				continue
			}
			cells = append(cells, strings.Repeat(" ", max(columnWidth, 0)))
		}
		joined = append(joined, strings.Join(cells, paneSeparator))
	}
	return joined
}

package ui

import (
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	toolspec "github.com/hive-controls/toolspec"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/pane"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
)

func TestSplitRendersEveryPaneInOneFrame(t *testing.T) {
	model := paneModel(t, feedPane("run-a", "Builder", "compiling\nlinking"), feedPane("run-b", "Watcher", "watching"))
	view := model.View()
	if !strings.Contains(view, "Builder") || !strings.Contains(view, "Watcher") {
		t.Fatalf("the split does not name both panes:\n%s", view)
	}
	if !strings.Contains(view, "linking") || !strings.Contains(view, "watching") {
		t.Fatalf("the split does not show both children's output:\n%s", view)
	}
	// Both headers on ONE line is the property that matters: the frame is composed here, as a
	// single string bubbletea diffs once, rather than each pane painting itself.
	headerLine := strings.SplitN(view, "\n", 2)[0]
	if !strings.Contains(headerLine, "Builder") || !strings.Contains(headerLine, "Watcher") {
		t.Fatalf("panes are not composed side by side into one frame; first line:\n%s", headerLine)
	}
}

func TestPaneHeaderNamesTruncationAndDecodeResets(t *testing.T) {
	noisy := pane.New("run-c", "Noisy", pane.Options{ScrollbackLines: 2, ScrollbackBytes: 4096, Columns: 60})
	noisy.Consume(strings.NewReader("one\ntwo\nthree\nfour\n\x80five\n"))
	model := paneModel(t, noisy)
	view := model.View()
	if !strings.Contains(view, "earlier lines dropped") {
		t.Fatalf("a truncated pane does not say so:\n%s", view)
	}
	if !strings.Contains(view, "decode reset") {
		t.Fatalf("a pane that recovered from a decode error does not say so:\n%s", view)
	}
}

// TestAFloodingChildCostsOneFrameParTick is the rate limit, asserted without a single sleep:
// the ticker is replaced with one the TEST fires, so the only frames that exist are the ones
// asked for here. A child writing megabytes throughout still costs exactly three.
func TestAFloodingChildCostsOneFrameParTick(t *testing.T) {
	flooding := pane.New("run-flood", "Flooder", pane.Options{ScrollbackLines: 20, ScrollbackBytes: 8192, Columns: 40})
	endless := &endlessReader{}
	t.Cleanup(endless.stop)
	go flooding.Consume(endless)

	model := paneModel(t, flooding)
	model.paneTicker = func(time.Duration) tea.Cmd {
		return func() tea.Msg { return paneTickMsg(time.Time{}) }
	}
	frames := 0
	for tick := 0; tick < 3; tick++ {
		updated, command := model.Update(paneTickMsg(time.Time{}))
		model = requireModel(t, updated)
		if command == nil {
			t.Fatalf("the pane tick stopped rescheduling itself on tick %d — a flooding child starved the UI loop", tick)
		}
		_ = model.View()
		frames++
	}
	waitForBytes(t, flooding, 1<<20)

	if frames != 3 {
		t.Fatalf("rendered %d frames for 3 ticks", frames)
	}
	snapshot := flooding.Snapshot()
	if snapshot.Bytes < 1<<20 {
		t.Fatalf("the flooding child only wrote %d bytes — the test proved nothing", snapshot.Bytes)
	}
	if len(snapshot.Lines) > 21 {
		t.Fatalf("the pane kept %d lines of a %d-byte flood; the cap is 20 plus the partial row", len(snapshot.Lines), snapshot.Bytes)
	}
}

func TestWindowResizeAsksEveryPaneToResize(t *testing.T) {
	model := paneModel(t, feedPane("run-d", "Builder", "output"))
	updated, command := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("a resize with panes open returned no command — no child would ever be told")
	}
	if size := model.paneSize(1); size.Rows != 27 || size.Columns != 100 {
		t.Fatalf("pane size after the resize is %+v, want 27x100", size)
	}
}

func TestPaneKeysMoveFocusAndLeaveTheSplit(t *testing.T) {
	model := paneModel(t, feedPane("run-e", "First", "a"), feedPane("run-f", "Second", "b"))
	model.paneFocus = 0
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	if model.paneFocus != 1 {
		t.Fatalf("focus is %d, want 1", model.paneFocus)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.showPanes {
		t.Fatal("Esc did not leave the split")
	}
	if !strings.Contains(model.View(), "hivedeck") {
		t.Fatalf("leaving the split did not return to the tool list:\n%s", model.View())
	}
}

func feedPane(runID, title, output string) *pane.Pane {
	fed := pane.New(runID, title, pane.Options{ScrollbackLines: 100, ScrollbackBytes: 8192, Columns: 40})
	fed.Consume(strings.NewReader(output))
	return fed
}

func paneModel(t *testing.T, panes ...*pane.Pane) Model {
	t.Helper()
	model := launchModel(t, runs.Store{StateDirectory: t.TempDir()}, discovery.Tool{Name: "checks", Title: "Checks"})
	model.panes = panes
	model.showPanes = true
	return model
}

// endlessReader writes as fast as it is read, which is what a child flooding its pane looks
// like from the reader's side.
type endlessReader struct {
	stopped atomic.Bool
}

func (endless *endlessReader) Read(buffer []byte) (int, error) {
	if endless.stopped.Load() {
		return 0, io.EOF
	}
	for index := range buffer {
		buffer[index] = 'x'
		if index%40 == 39 {
			buffer[index] = '\n'
		}
	}
	return len(buffer), nil
}

func (endless *endlessReader) stop() {
	endless.stopped.Store(true)
}

func waitForBytes(t *testing.T, watched *pane.Pane, wanted int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if watched.Snapshot().Bytes >= wanted {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("the flooding child only produced %d bytes, wanted %d", watched.Snapshot().Bytes, wanted)
}

// TestAnInteractiveToolDegradesOutOfAPaneEvenWhenTheProfileAsksForOne is the user-visible half
// of the same rule: the profile says pane, the manifest says this tool needs a real terminal,
// and what actually happens is a foreground run recorded as foreground — not a pane, and not a
// refusal the user has to resolve by hand.
func TestAnInteractiveToolDegradesOutOfAPaneEvenWhenTheProfileAsksForOne(t *testing.T) {
	interactive := true
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{Name: "psql-console", Title: "psql console", Manifest: toolspec.ToolManifest{
		Launch: toolspec.ToolManifestLaunch{Command: "/bin/true"},
		Pane:   &toolspec.ToolManifestPane{Interactive: &interactive},
	}}
	model := launchModel(t, store, tool)
	model.repositoryProfile.Tools = map[string]profile.Tool{"psql-console": {Mode: profile.ModePane}}

	if mode := profile.ResolveMode(model.repositoryProfile, tool.Name, toolInteractive(tool)); mode != profile.ModeForeground {
		t.Fatalf("resolved mode is %q, want foreground", mode)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("launching an interactive tool returned no command")
	}
	if len(model.panes) != 0 {
		t.Fatalf("a tool that needs a real terminal was captured into %d pane(s)", len(model.panes))
	}
	rows, err := store.List()
	if err != nil || len(rows) != 1 || rows[0].Mode != profile.ModeForeground {
		t.Fatalf("run rows = %+v, error = %v — the row must record the route actually taken", rows, err)
	}
}

// TestARunRowMovesWhileTheRunIsStillGoing is the live monitor as a user meets it: a status
// file that grows while the run is going turns into a run row that changes state and gains an
// artifact, on the host's own tick, without a keypress.
func TestARunRowMovesWhileTheRunIsStillGoing(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, discovery.Tool{Name: "checks", Title: "Checks"})
	row, err := store.Begin("checks", profile.ModePane, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	model.watchRun(row.ID)
	model.showRuns = true
	model.refreshRuns()
	if strings.Contains(model.View(), "evidence.html") {
		t.Fatalf("the run list showed an artifact before the tool reported one:\n%s", model.View())
	}

	if err := os.WriteFile(store.StatusFile(row.ID), []byte(`{"event":"progress","artifact":"evidence.html"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, paneTickMsg(time.Time{}))

	view := model.View()
	if !strings.Contains(view, "progress") || !strings.Contains(view, "evidence.html") {
		t.Fatalf("the run list did not move with the status stream:\n%s", view)
	}
	live, err := store.Load(row.ID)
	if err != nil || live.Outcome != "progress" || live.Artifact != "evidence.html" || live.FinishedAt != nil {
		t.Fatalf("row on disk = %+v, error = %v — a live update must move an unfinished row", live, err)
	}
}

// TestALongToolTitleNeverPushesTheHeaderStateOffTheEnd holds the header's priority: a pane
// whose title fills the column must still report what became of it and what it dropped.
func TestALongToolTitleNeverPushesTheHeaderStateOffTheEnd(t *testing.T) {
	long := pane.New("run-long", strings.Repeat("a very long tool title ", 5),
		pane.Options{ScrollbackLines: 2, ScrollbackBytes: 4096, Columns: 40})
	long.Consume(strings.NewReader("one\ntwo\nthree\nfour\n"))
	header := paneHeader(long.Snapshot(), 60)
	if !strings.Contains(header, "running") || !strings.Contains(header, "earlier lines dropped") {
		t.Fatalf("header lost its state or its marker to the title:\n%s", header)
	}
	if lipgloss.Width(header) != 60 {
		t.Fatalf("header is %d columns wide, want exactly 60", lipgloss.Width(header))
	}
}

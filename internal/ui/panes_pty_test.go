//go:build !windows

// These tests capture a real child on a real pseudo-terminal. Whether that works on a
// platform this module implements no pseudo-terminal for is UNVERIFIED, so the file is not
// built there rather than skipped — a skip would read as a claim it might.

package ui

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/pane"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

func TestPaneModeCapturesTheChildAndRecordsThePaneRoute(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := paneLaunchModel(t, store, "/bin/sh", "-c", "echo captured output")
	t.Cleanup(func() { model.terminateAllPanes() })
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(model.panes) != 1 {
		t.Logf("notice: %s", model.notice)
		t.Fatalf("pane mode opened %d panes, want 1", len(model.panes))
	}
	rows, err := store.List()
	if err != nil || len(rows) != 1 || rows[0].Mode != profile.ModePane {
		t.Fatalf("run rows = %+v, error = %v — the row must record the route it actually took", rows, err)
	}
	waitForPaneExit(t, model.panes[0])
	if !strings.Contains(model.View(), "captured output") {
		t.Fatalf("the pane frame does not show the child's output:\n%s", model.View())
	}

	model = updateModel(t, model, paneFinishedMsg{runID: rows[0].ID})
	finished, err := store.Load(rows[0].ID)
	if err != nil || finished.ExitCode == nil || *finished.ExitCode != 0 || finished.Outcome != "ok" {
		t.Fatalf("finished row = %+v, error = %v — a pane run must complete its row like every other route", finished, err)
	}
}

func TestStoppingAFocusedPaneKillsItsChild(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := paneLaunchModel(t, store, "/bin/sh", "-c", "echo ready; while :; do sleep 0.05; done")
	t.Cleanup(func() { model.terminateAllPanes() })
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	model = requireModel(t, updated)
	if command == nil {
		t.Fatal("x on a focused pane returned no command — nothing would ever stop the child")
	}
	command()
	waitForPaneExit(t, model.panes[0])
	if model.panes[0].Snapshot().Running {
		t.Fatal("the pane still reports its child as running after the explicit stop")
	}
}

func TestClosingFinishedPanesKeepsTheRunningOnes(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	finished, err := pane.Open("run-finished", "Finished", exec.Command("/bin/sh", "-c", "echo done"), pane.Size{Rows: 10, Columns: 40}, pane.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	waitForPaneExit(t, finished)
	model := paneModel(t, feedPane("run-running", "Running", "still going"), finished)
	model.runStore = store
	t.Cleanup(func() { model.terminateAllPanes() })
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if len(model.panes) != 1 || model.panes[0].RunID != "run-running" {
		t.Fatalf("panes after closing the finished one: %+v", model.panes)
	}
}

func paneLaunchModel(t *testing.T, store runs.Store, command string, arguments ...string) Model {
	t.Helper()
	tool := discovery.Tool{
		Name: "watcher", Title: "Watcher",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: command, Args: arguments}},
	}
	model := launchModel(t, store, tool)
	model.repositoryProfile.Tools = map[string]profile.Tool{"watcher": {Mode: profile.ModePane}}
	return model
}

func waitForPaneExit(t *testing.T, watched *pane.Pane) {
	t.Helper()
	select {
	case <-watched.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("pane never finished")
	}
}

// TestAStoppedPaneIsReportedAsStoppedNotFailed holds the difference between a tool that broke
// and a tool a person stopped: the signal that ends the child is the same either way, and only
// the pane knows the stop was asked for.
func TestAStoppedPaneIsReportedAsStoppedNotFailed(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := paneLaunchModel(t, store, "/bin/sh", "-c", "echo ready; while :; do sleep 0.05; done")
	t.Cleanup(func() { model.terminateAllPanes() })
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	model = requireModel(t, updated)
	if !strings.Contains(model.notice, "Stopping") {
		t.Fatalf("notice = %q, want it to say the stop is under way", model.notice)
	}
	command()
	waitForPaneExit(t, model.panes[0])

	model = updateModel(t, model, paneFinishedMsg{runID: model.panes[0].RunID})
	view := model.View()
	if !strings.Contains(view, "stopped") {
		t.Fatalf("a pane the user stopped is not reported as stopped:\n%s", view)
	}
	if strings.Contains(view, "failed") {
		t.Fatalf("a pane the user stopped is reported as failed:\n%s", view)
	}
	if model.notice != "" {
		t.Fatalf("notice = %q, want the in-progress stop notice cleared once the pane reported", model.notice)
	}
}

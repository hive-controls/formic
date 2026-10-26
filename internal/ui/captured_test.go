package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

func rowsOf(t *testing.T, store runs.Store) []runs.Row {
	t.Helper()
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// runCapturedCommand drives the returned command for real and hands back the completion message
// the captured route produced. A foreground tea.Exec would not be a BatchMsg of plain commands.
func runCapturedCommand(t *testing.T, command tea.Cmd) capturedFinishedMsg {
	t.Helper()
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("the captured route should return a batch (spinner + run), not an exec handoff")
	}
	for _, inner := range batch {
		if finished, ok := inner().(capturedFinishedMsg); ok {
			return finished
		}
	}
	t.Fatal("no capturedFinishedMsg came out of the batch")
	return capturedFinishedMsg{}
}

func TestConfirmingActionAsksFirstAndNoCancelsWithoutARow(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	model = moveActionCursor(t, model, 1) // stop
	model = pressKey(t, model, tea.KeyEnter)
	view := model.View()
	if !strings.Contains(view, "Stops EVERY container. Continue?") || !strings.Contains(view, "[y/N]") {
		t.Fatalf("no confirmation prompt:\n%s", view)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if model.confirmingAction || !model.detail || len(rowsOf(t, store)) != 0 {
		t.Fatalf("cancel must return to the menu with no row: confirming=%v detail=%v", model.confirmingAction, model.detail)
	}
	if !strings.Contains(model.View(), "Stop: cancelled") {
		t.Fatalf("no cancelled result line:\n%s", model.View())
	}
}

func TestConfirmedActionProceedsToARow(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	model = moveActionCursor(t, model, 1) // stop
	model = pressKey(t, model, tea.KeyEnter)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = requireModel(t, updated)
	rows := rowsOf(t, store)
	if command == nil || len(rows) != 1 || model.confirmingAction {
		t.Fatalf("y should proceed: command nil=%v rows=%d confirming=%v notice=%q", command == nil, len(rows), model.confirmingAction, model.notice)
	}
}

func TestNonInteractiveActionRunsCapturedAndReportsItsOwnExitMeaning(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter}) // start
	model = requireModel(t, updated)
	if command == nil || model.capturedRunID == "" {
		t.Fatalf("captured route did not start: command nil=%v notice=%q", command == nil, model.notice)
	}
	finished := runCapturedCommand(t, command)
	model = updateModel(t, model, finished)
	rows := rowsOf(t, store)
	if len(rows) != 1 || rows[0].Mode != profile.ModeCaptured || rows[0].ExitCode == nil || *rows[0].ExitCode != 1 {
		t.Fatalf("captured row = %+v", rows)
	}
	if model.capturedRunID != "" || !model.detail || model.showRuns {
		t.Fatalf("after the run the deck should sit in the menu: captured=%q detail=%v runs=%v", model.capturedRunID, model.detail, model.showRuns)
	}
	if !strings.Contains(model.View(), "Start: exit 1") || !strings.Contains(model.View(), "daemon did not come up") {
		t.Fatalf("no result line with the action's exit meaning:\n%s", model.View())
	}
}

func TestQuitIsRefusedWhileACapturedActionRuns(t *testing.T) {
	model, _ := openMenu(t, actionsTool(t))
	model = requireModel(t, func() tea.Model { u, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter}); return u }())
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = requireModel(t, updated)
	if isQuitCmd(command) || !strings.Contains(model.notice, "an action is running") {
		t.Fatalf("q must be refused while a captured action runs; notice=%q", model.notice)
	}
	_, command = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuitCmd(command) {
		t.Fatal("ctrl+c must still leave the deck")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = requireModel(t, updated)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil || !strings.Contains(requireModel(t, updated).notice, "already running") {
		t.Fatalf("a second action must be refused while one runs; notice=%q", requireModel(t, updated).notice)
	}
}

func TestInteractiveActionHandsOffTheTerminalAndReturnsToTheMenu(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	model = moveActionCursor(t, model, 3) // tui
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows := rowsOf(t, store)
	if command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeForeground || model.capturedRunID != "" {
		t.Fatalf("interactive action should be a foreground exec: rows=%+v captured=%q", rows, model.capturedRunID)
	}
	if _, isBatch := command().(tea.BatchMsg); isBatch {
		t.Fatal("interactive action must not take the captured route")
	}
	updated, _ = model.Update(foregroundFinishedMsg{runID: rows[0].ID, action: "tui"})
	model = requireModel(t, updated)
	if !model.detail || model.showRuns || !strings.Contains(model.View(), "Dashboard: exit") {
		t.Fatalf("interactive finish should return to the menu with a result line:\n%s", model.View())
	}
}

func TestServiceActionRoutesToATabNeverCaptured(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	model.environment = []string{"TMUX=/tmp/tmux"}
	installFakeExecutable(t, "tmux")
	model = moveActionCursor(t, model, 4) // serve
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows := rowsOf(t, store)
	if command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeTab || model.capturedRunID != "" {
		t.Fatalf("service action should open a tab: rows=%+v captured=%q notice=%q", rows, model.capturedRunID, model.notice)
	}
	updated, _ = model.Update(command())
	model = requireModel(t, updated)
	if !model.detail || model.showRuns || !strings.Contains(model.View(), "Serve: opened in") {
		t.Fatalf("tab start should stay in the menu with a result line:\n%s", model.View())
	}
}

func TestTailBufferKeepsOnlyTheLastLines(t *testing.T) {
	buffer := &tailBuffer{}
	for index := 1; index <= 300; index++ {
		_, _ = buffer.Write([]byte("line " + strings.Repeat("x", 3) + "\n"))
	}
	_, _ = buffer.Write([]byte("partial"))
	lines := buffer.Lines()
	if len(lines) > tailMaximumLines+1 || lines[len(lines)-1] != "partial" {
		t.Fatalf("tail kept %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestCapturedRouteNeverWritesOutputToTheStateDirectory(t *testing.T) {
	model, store := openMenu(t, actionsTool(t))
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = requireModel(t, updated)
	runCapturedCommand(t, command)
	err := filepath.WalkDir(store.StateDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && strings.Contains(filepath.Base(path), "output") {
			t.Errorf("unexpected output file %s", path)
		}
		return walkErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

func itemLevelTool(t *testing.T, lifecycle *formats.ToolManifestLifecycle) discovery.Tool {
	t.Helper()
	return discovery.Tool{
		Name: "plain", Title: "Plain",
		Manifest: formats.ToolManifest{
			Launch:    formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:exit", "0"}},
			Lifecycle: lifecycle,
		},
	}
}

func TestItemLevelServiceToolRoutesToATabNotTheDetachHint(t *testing.T) {
	service := formats.ToolManifestLifecycleService
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, itemLevelTool(t, &service))
	model.environment = []string{"TMUX=/tmp/tmux"}
	installFakeExecutable(t, "tmux")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows := rowsOf(t, store)
	if strings.Contains(model.notice, "--detach") || command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeTab {
		t.Fatalf("item-level service should open a tab: rows=%+v notice=%q", rows, model.notice)
	}
}

func TestPlainTaskToolStillRunsInTheForeground(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	model := launchModel(t, store, itemLevelTool(t, nil))
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = requireModel(t, updated)
	rows := rowsOf(t, store)
	if command == nil || len(rows) != 1 || rows[0].Mode != profile.ModeForeground || strings.Contains(model.notice, "--detach") {
		t.Fatalf("plain task should run foreground: rows=%+v notice=%q", rows, model.notice)
	}
}

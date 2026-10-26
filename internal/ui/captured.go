package ui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/profile"
)

const (
	tailMaximumLines     = 200
	tailMaximumBytes     = 64 * 1024
	tailMaximumLineBytes = 1024
	resultTailLines      = 5
)

// capturedFinishedMsg reports a captured action's child exiting. tail is the last of its output,
// kept in memory only: a run row has no output field, and output can carry secrets.
type capturedFinishedMsg struct {
	runID  string
	action string
	err    error
	tail   []string
}

// tailBuffer is a bounded in-memory sink for a captured child's stdout and stderr: the last
// tailMaximumLines lines, at most tailMaximumBytes in all.
type tailBuffer struct {
	mutex   sync.Mutex
	lines   []string
	partial string
	bytes   int
}

func (buffer *tailBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	buffer.partial += string(data)
	for {
		newline := strings.IndexByte(buffer.partial, '\n')
		if newline < 0 {
			break
		}
		buffer.appendLine(strings.TrimRight(buffer.partial[:newline], "\r"))
		buffer.partial = buffer.partial[newline+1:]
	}
	if len(buffer.partial) > tailMaximumLineBytes {
		buffer.partial = buffer.partial[len(buffer.partial)-tailMaximumLineBytes:]
	}
	return len(data), nil
}

func (buffer *tailBuffer) appendLine(line string) {
	if len(line) > tailMaximumLineBytes {
		line = line[:tailMaximumLineBytes]
	}
	buffer.lines = append(buffer.lines, line)
	buffer.bytes += len(line)
	for len(buffer.lines) > tailMaximumLines || (buffer.bytes > tailMaximumBytes && len(buffer.lines) > 1) {
		buffer.bytes -= len(buffer.lines[0])
		buffer.lines = buffer.lines[1:]
	}
}

// Lines returns the kept lines, with any unterminated final line last.
func (buffer *tailBuffer) Lines() []string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	lines := append([]string(nil), buffer.lines...)
	if buffer.partial != "" {
		lines = append(lines, buffer.partial)
	}
	return lines
}

// startCaptured runs an already-prepared action with its output captured instead of handed the
// terminal: stdin is the null device, stdout and stderr go to one bounded in-memory tail.
func (model Model) startCaptured(prepared launch.Prepared) (tea.Model, tea.Cmd) {
	tail := &tailBuffer{}
	command := prepared.Command
	command.Stdin, command.Stdout, command.Stderr = nil, tail, tail
	runID, action := prepared.Row.ID, model.actionInFlight
	model.capturedRunID = runID
	model.actionSpinner = spinner.New(spinner.WithSpinner(spinner.Line))
	run := func() tea.Msg {
		err := command.Run()
		return capturedFinishedMsg{runID: runID, action: action, err: err, tail: tail.Lines()}
	}
	return model, tea.Batch(model.actionSpinner.Tick, run)
}

func (model Model) handleCapturedFinished(message capturedFinishedMsg) Model {
	row, err := launch.Finish(model.runStore, message.runID, message.err, model.clock())
	model.capturedRunID = ""
	if err != nil {
		model.notice = err.Error()
		return model
	}
	model.refreshRuns()
	model.showRuns, model.detail = false, true
	model.notice = ""
	model.openReadyWindowAfterExit(row)
	tool, _ := model.toolByName(row.Tool)
	model.actionResult = actionResult{line: model.actionResultLine(tool, message.action, row.ExitCode, row.Outcome), tail: lastLines(message.tail, resultTailLines)}
	return model
}

func lastLines(lines []string, count int) []string {
	if len(lines) <= count {
		return lines
	}
	return lines[len(lines)-count:]
}

func (model Model) handleSpinnerTick(message spinner.TickMsg) (tea.Model, tea.Cmd) {
	if model.capturedRunID == "" {
		return model, nil
	}
	updated, command := model.actionSpinner.Update(message)
	model.actionSpinner = updated
	return model, command
}

// capturedRunningLine is the menu's progress line while a captured action runs.
func (model Model) capturedRunningLine(tool discovery.Tool) string {
	return fmt.Sprintf("%s running %s…", model.actionSpinner.View(), actionTitle(model.inFlightAction(tool)))
}

// routeActionBeforeFallback sends a service action to a tab (a service never returns, so the
// captured route would hang); every other action keeps the resolved mode.
func (model Model) routeActionBeforeFallback(tool discovery.Tool, mode profile.LaunchMode) profile.LaunchMode {
	if model.launchIsService(tool) && (mode == profile.ModeForeground || mode == profile.ModePane) {
		return profile.ModeTab
	}
	return mode
}

// launchIsService reports whether the launch in progress never returns: the in-flight action
// declares lifecycle: service, or no action is in flight and the item itself does.
func (model Model) launchIsService(tool discovery.Tool) bool {
	if model.actionInFlight != "" {
		return model.actionIsService(tool)
	}
	return tool.Manifest.Lifecycle != nil && *tool.Manifest.Lifecycle == formats.ToolManifestLifecycleService
}

// routeActionAfterFallback captures a non-interactive, non-service action that would otherwise
// run in the foreground or a pane; an interactive action keeps the real terminal.
func (model Model) routeActionAfterFallback(tool discovery.Tool, mode profile.LaunchMode) profile.LaunchMode {
	if model.actionInFlight == "" || model.actionIsService(tool) || model.actionInteractive(tool, model.actionInFlight) {
		return mode
	}
	if mode == profile.ModeForeground || mode == profile.ModePane {
		return profile.ModeCaptured
	}
	return mode
}

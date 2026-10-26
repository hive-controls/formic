package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/runs"
)

// readyWindow is one ready window this deck opened (§22 c): the effective tool to probe and the
// length of the window. It lives in memory only and dies with the deck; it never touches a run
// row, an outcome word or an exit code (R5).
type readyWindow struct {
	tool   discovery.Tool
	within time.Duration
}

// effectiveReadyWindow is the window the launch of tool's action would open: the action's own
// projection (an item-level ready beside actions[] is refused by doctor), and only for a tool
// that declares a probe.
func effectiveReadyWindow(tool discovery.Tool, action string) (readyWindow, bool) {
	effective, err := manifest.ForAction(tool.Manifest, action)
	if err != nil {
		return readyWindow{}, false
	}
	probed := tool
	probed.Manifest = effective
	within, ok := launch.ReadyWindow(effective)
	if !ok || !declaresProbe(probed) {
		return readyWindow{}, false
	}
	return readyWindow{tool: probed, within: within}, true
}

func isServiceManifest(effective formats.ToolManifest) bool {
	return effective.Lifecycle != nil && *effective.Lifecycle == formats.ToolManifestLifecycleService
}

// openReadyWindowAfterExit opens a window for a task run this deck watched exit 0 (the captured,
// foreground and pane routes); a non-zero exit, a service and an unwatched run open none.
func (model Model) openReadyWindowAfterExit(row runs.Row) {
	if row.ExitCode == nil || *row.ExitCode != 0 {
		return
	}
	tool, found := model.toolByName(row.Tool)
	if !found {
		return
	}
	window, ok := effectiveReadyWindow(tool, row.Action)
	if !ok || isServiceManifest(window.tool.Manifest) {
		return
	}
	model.openReadyWindow(window)
}

// openReadyWindowAfterSpawn opens a window once a service's terminal opened. A task sent to a
// tab is not observed, so it opens none.
func (model Model) openReadyWindowAfterSpawn(tool discovery.Tool, action string) {
	window, ok := effectiveReadyWindow(tool, action)
	if !ok || !isServiceManifest(window.tool.Manifest) {
		return
	}
	model.openReadyWindow(window)
}

func (model Model) openReadyWindow(window readyWindow) {
	live := model.live
	if live == nil {
		return
	}
	live.readyUntil[window.tool.Name] = model.clock().Add(window.within)
	live.readyTools[window.tool.Name] = window
	delete(live.readyOutcome, window.tool.Name)
}

// readyWindowOpen reports whether a window opened by this deck is still running for the tool.
func (model Model) readyWindowOpen(toolName string) bool {
	if model.live == nil {
		return false
	}
	deadline, open := model.live.readyUntil[toolName]
	return open && model.clock().Before(deadline)
}

func (model Model) closeReadyWindow(toolName, outcome string) {
	delete(model.live.readyUntil, toolName)
	delete(model.live.readyTools, toolName)
	model.live.readyOutcome[toolName] = outcome
}

// settleReadyWindow closes the tool's window on the first answering result, or once its
// deadline has passed; the word then falls back to the real last state.
func (model Model) settleReadyWindow(toolName string, result launch.ProbeResult) {
	window, open := model.live.readyTools[toolName]
	if !open {
		return
	}
	if result.State.Answering() {
		model.closeReadyWindow(toolName, "up")
	} else if !model.clock().Before(model.live.readyUntil[toolName]) {
		model.closeReadyWindow(toolName, "not up within "+window.within.String())
	}
}

// expireReadyWindows closes every window whose deadline has passed without an answer.
func (model Model) expireReadyWindows() {
	for toolName, window := range model.live.readyTools {
		if !model.clock().Before(model.live.readyUntil[toolName]) {
			model.closeReadyWindow(toolName, "not up within "+window.within.String())
		}
	}
}

// readyWindowTools are the effective tools of every open window, probed even when their rows are
// off screen: §22 c's launcher poll, not §22 b's visible-row poll.
func (model Model) readyWindowTools() []discovery.Tool {
	var tools []discovery.Tool
	for toolName, window := range model.live.readyTools {
		if model.readyWindowOpen(toolName) {
			tools = append(tools, window.tool)
		}
	}
	return tools
}

// withReadyWindowTargets adds the open windows' tools to the on-screen targets, once each.
func (model Model) withReadyWindowTargets(targets []discovery.Tool) []discovery.Tool {
	if model.live == nil {
		return targets
	}
	present := map[string]bool{}
	for _, tool := range targets {
		present[tool.Name] = true
	}
	for _, tool := range model.readyWindowTools() {
		if !present[tool.Name] {
			targets = append(targets, tool)
		}
	}
	return targets
}

func (model Model) probeReadyWindows() tea.Cmd {
	if model.live == nil {
		return nil
	}
	return model.probeNow(model.readyWindowTools())
}

// liveTickInterval is the one ticker's next gap: the ready poll interval while any window is
// open, the on-screen interval otherwise.
func (model Model) liveTickInterval() time.Duration {
	for toolName := range model.live.readyTools {
		if model.readyWindowOpen(toolName) && launch.ReadyPollInterval < liveInterval {
			return launch.ReadyPollInterval
		}
	}
	return liveInterval
}

// clearReadyOutcome forgets a closed window's suffix when a new launch of the tool begins, so it
// never trails a result line it does not belong to.
func (model Model) clearReadyOutcome(toolName string) {
	if model.live != nil {
		delete(model.live.readyOutcome, toolName)
	}
}

// readySuffix is the action menu's result-line suffix once a window has closed.
func (model Model) readySuffix(toolName string) string {
	if model.live == nil || model.live.readyOutcome[toolName] == "" {
		return ""
	}
	return " · " + model.live.readyOutcome[toolName]
}

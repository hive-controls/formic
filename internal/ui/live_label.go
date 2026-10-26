package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/formic/internal/launch"
)

// liveStateLabel is the state word shown for a tool everywhere: the last probe result, "unknown"
// for a tool with no probe or no result yet, or "starting" while a ready window this deck opened
// is running and nothing has answered yet.
func liveStateLabel(model Model, toolName string) string {
	if model.live == nil {
		return "unknown"
	}
	result, ok := model.live.states[toolName]
	if model.readyWindowOpen(toolName) && !result.State.Answering() {
		return "starting"
	}
	if !ok || result.State == "" {
		return string(launch.ProbeUnknown)
	}
	return string(result.State)
}

// liveStateStyle is the one colour rule for a state word: up and idle (something is answering)
// are good, down is a problem, everything else is muted.
func liveStateStyle(word string) lipgloss.Style {
	switch word {
	case string(launch.ProbeUp), string(launch.ProbeIdle):
		return liveUpStyle
	case string(launch.ProbeDown):
		return problemStyle
	default:
		return mutedStyle
	}
}

package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The terminal size hivedeck lays out against before the first tea.WindowSizeMsg arrives.
// bubbletea delivers that message on startup, so these are only ever used for the very first
// frame — but a frame laid out against a zero width would divide by nothing and emit a single
// unbounded column, which is the failure this file exists to remove.
const (
	fallbackWidth  = 80
	fallbackHeight = 24
)

// A list always keeps at least one row: a list rendered with none cannot be navigated out of.
const minimumListRows = 1

const ellipsis = "…"

func (model Model) layoutWidth() int {
	if model.width <= 0 {
		return fallbackWidth
	}
	return model.width
}

func (model Model) layoutHeight() int {
	if model.height <= 0 {
		return fallbackHeight
	}
	return model.height
}

// truncate shortens text to at most limit display columns, marking the loss with a trailing
// ellipsis. Measurement is by DISPLAY width (lipgloss.Width), not rune count: a CJK title
// occupies two columns per rune, and counting runes would overflow the row it was meant to fit.
func truncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= limit {
		return text
	}
	if limit == 1 {
		return ellipsis
	}
	return takeColumns(text, limit-1) + ellipsis
}

// truncatePath keeps the END of a path, marking the loss with a LEADING ellipsis. A path's
// identity lives in its last segments — "…/runs/2026-09-05/evidence.html" still names the
// artifact, where a head-truncated "/home/example/.local/state/hiv…" names nothing.
func truncatePath(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= limit {
		return text
	}
	if limit == 1 {
		return ellipsis
	}
	return ellipsis + takeTrailingColumns(text, limit-1)
}

// pad right-aligns nothing and left-aligns text into exactly width columns, truncating first.
// It replaces the fixed "%-28s" verb, which pads by BYTE count and so mis-aligns any row
// containing a non-ASCII character.
func pad(text string, width int) string {
	if width <= 0 {
		return ""
	}
	fitted := truncate(text, width)
	gap := width - lipgloss.Width(fitted)
	if gap <= 0 {
		return fitted
	}
	return fitted + strings.Repeat(" ", gap)
}

func takeColumns(text string, limit int) string {
	var builder strings.Builder
	used := 0
	for _, character := range text {
		characterWidth := lipgloss.Width(string(character))
		if used+characterWidth > limit {
			break
		}
		builder.WriteRune(character)
		used += characterWidth
	}
	return builder.String()
}

func takeTrailingColumns(text string, limit int) string {
	characters := []rune(text)
	used := 0
	start := len(characters)
	for index := len(characters) - 1; index >= 0; index-- {
		characterWidth := lipgloss.Width(string(characters[index]))
		if used+characterWidth > limit {
			break
		}
		used += characterWidth
		start = index
	}
	return string(characters[start:])
}

// visibleWindow returns the half-open row range [start, end) a list should render so that
// cursor stays on screen. The window scrolls only when the cursor would leave it, so moving
// within the visible rows never repaints them at a different offset.
func visibleWindow(cursor, count, capacity int) (int, int) {
	if count <= 0 || capacity <= 0 {
		return 0, 0
	}
	if capacity >= count {
		return 0, count
	}
	start := cursor - capacity/2
	if start < 0 {
		start = 0
	}
	if start+capacity > count {
		start = count - capacity
	}
	return start, start + capacity
}

// listCapacity is how many list rows fit once the fixed chrome around them is accounted for.
// It never returns zero: a terminal too short for even one row still shows one, because a list
// with no rows at all cannot be navigated out of.
func listCapacity(height, chromeRows int) int {
	capacity := height - chromeRows
	if capacity < minimumListRows {
		return minimumListRows
	}
	return capacity
}

// clipLines bounds a body that has no cursor to scroll with — the detail view's prose and
// declarations. The last kept line is spent naming what was dropped, so a clipped body is
// never mistaken for a complete one.
func clipLines(lines []string, capacity int) []string {
	if capacity <= 0 || len(lines) <= capacity {
		return lines
	}
	if capacity == 1 {
		return []string{mutedStyle.Render(ellipsis + " " + plural(len(lines), "more lines"))}
	}
	kept := append([]string{}, lines[:capacity-1]...)
	dropped := len(lines) - (capacity - 1)
	return append(kept, mutedStyle.Render(ellipsis+" "+plural(dropped, "more lines")))
}

// scrollHint names the rows a window is hiding, so a truncated list never looks complete.
func scrollHint(start, end, count int) string {
	above, below := start, count-end
	switch {
	case above > 0 && below > 0:
		return "↑ " + plural(above, "more") + " · ↓ " + plural(below, "more")
	case above > 0:
		return "↑ " + plural(above, "more")
	case below > 0:
		return "↓ " + plural(below, "more")
	default:
		return ""
	}
}

func plural(count int, noun string) string {
	return strconv.Itoa(count) + " " + noun
}

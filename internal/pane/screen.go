package pane

import (
	"strings"
	"unicode/utf8"
)

// maximumEscapeLength bounds how long an unterminated escape sequence may grow before it is
// judged undecodable. Without it, one stray ESC byte from a child writing binary would make
// every byte after it disappear into a sequence that never ends.
const maximumEscapeLength = 128

const tabWidth = 8

// Screen decodes a child's raw terminal bytes into completed text lines. Nothing raw ever
// reaches a frame: escape sequences are consumed and discarded here, so a child cannot repaint
// the rest of hivedeck's screen, move the cursor out of its pane, or set a colour that leaks
// into the next pane's column.
//
// It is a single row of cells plus a cursor column rather than a full grid, because a pane
// renders as scrollback: a completed row leaves for the scrollback the moment it ends.
type Screen struct {
	columns int
	row     []rune
	column  int
	pending []byte
	resets  int
}

func NewScreen(columns int) *Screen {
	return &Screen{columns: columns}
}

// SetColumns re-widths the screen, which is what a pane does when the split resizes.
func (screen *Screen) SetColumns(columns int) {
	screen.columns = columns
}

// Write decodes data and returns every line it completed. A partial line stays in the screen
// and is readable through Row, so a pane shows a child's in-progress output rather than
// nothing until the next newline.
func (screen *Screen) Write(data []byte) []string {
	screen.pending = append(screen.pending, data...)
	var completed []string
	for len(screen.pending) > 0 {
		consumed, lines, wait := screen.step()
		completed = append(completed, lines...)
		if wait {
			break
		}
		screen.pending = screen.pending[consumed:]
	}
	return completed
}

// step decodes the next unit at the front of pending. It reports how many bytes it consumed,
// any lines that completed, and whether it needs more bytes before it can decide — the last of
// which is the difference between a sequence that is merely split across two reads and one
// that is undecodable.
func (screen *Screen) step() (consumed int, completed []string, wait bool) {
	if screen.pending[0] == 0x1b {
		return screen.stepEscape()
	}
	if screen.pending[0] < utf8.RuneSelf {
		return 1, screen.putByte(screen.pending[0]), false
	}
	character, size := utf8.DecodeRune(screen.pending)
	if character == utf8.RuneError && size <= 1 {
		if utf8.FullRune(screen.pending) || len(screen.pending) >= utf8.UTFMax {
			// Genuinely undecodable rather than merely truncated: the row in progress is
			// suspect, so it is reset instead of being filled with substitutes — and the
			// one bad byte is skipped, never the rest of the read. A 32 KiB read that
			// happened to contain one stray byte would otherwise lose everything after it.
			screen.Reset()
			return 1, nil, false
		}
		return 0, nil, true
	}
	return size, screen.putRune(character), false
}

func (screen *Screen) stepEscape() (consumed int, completed []string, wait bool) {
	length, complete := escapeLength(screen.pending)
	if complete {
		return length, nil, false
	}
	if len(screen.pending) >= maximumEscapeLength {
		// The whole run of bytes IS the undecodable sequence, so all of it is consumed —
		// decoding resumes at whatever the child writes next.
		screen.Reset()
		return len(screen.pending), nil, false
	}
	return 0, nil, true
}

func (screen *Screen) putByte(value byte) []string {
	switch value {
	case '\n':
		return []string{screen.takeRow()}
	case '\r':
		screen.column = 0
	case '\b':
		if screen.column > 0 {
			screen.column--
		}
	case '\t':
		next := (screen.column/tabWidth + 1) * tabWidth
		for screen.column < next {
			if lines := screen.putRune(' '); lines != nil {
				return lines
			}
		}
	default:
		if value >= 0x20 && value != 0x7f {
			return screen.putRune(rune(value))
		}
	}
	return nil
}

func (screen *Screen) putRune(character rune) []string {
	for len(screen.row) <= screen.column {
		screen.row = append(screen.row, ' ')
	}
	screen.row[screen.column] = character
	screen.column++
	if screen.columns > 0 && screen.column >= screen.columns {
		return []string{screen.takeRow()}
	}
	return nil
}

func (screen *Screen) takeRow() string {
	line := strings.TrimRight(string(screen.row), " ")
	screen.row, screen.column = screen.row[:0], 0
	return line
}

// Row is the line being written but not yet completed.
func (screen *Screen) Row() string {
	return strings.TrimRight(string(screen.row), " ")
}

// Reset discards the row in progress and the cursor's position on it. It is what a decode
// error costs — that row, rather than a frame full of replacement characters or an escape
// sequence that never terminates. The undecodable bytes themselves are consumed by the
// caller, so decoding resumes with the very next byte instead of losing the rest of a read.
func (screen *Screen) Reset() {
	screen.row, screen.column = screen.row[:0], 0
	screen.resets++
}

// Resets counts how many times a decode error has cost this screen its state, so a pane can
// say so rather than silently losing output.
func (screen *Screen) Resets() int {
	return screen.resets
}

// escapeLength measures the escape sequence at the front of data. CSI and the string-argument
// sequences (OSC and friends) run until their own terminator; every other escape is two bytes.
func escapeLength(data []byte) (length int, complete bool) {
	if len(data) < 2 {
		return 0, false
	}
	switch data[1] {
	case '[':
		for index := 2; index < len(data); index++ {
			if data[index] >= 0x40 && data[index] <= 0x7e {
				return index + 1, true
			}
		}
		return 0, false
	case ']', 'P', 'X', '^', '_':
		return stringSequenceLength(data)
	default:
		return 2, true
	}
}

func stringSequenceLength(data []byte) (length int, complete bool) {
	for index := 2; index < len(data); index++ {
		if data[index] == 0x07 {
			return index + 1, true
		}
		if data[index] == 0x1b && index+1 < len(data) && data[index+1] == '\\' {
			return index + 2, true
		}
	}
	return 0, false
}

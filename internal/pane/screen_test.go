package pane

import (
	"strings"
	"testing"
)

func TestScreenDecodesTextAndDiscardsEscapeSequences(t *testing.T) {
	testCases := []struct {
		name          string
		columns       int
		written       string
		expectedLines []string
		expectedRow   string
	}{
		{
			name: "plain lines", columns: 40, written: "first\nsecond\n",
			expectedLines: []string{"first", "second"}, expectedRow: "",
		},
		{
			name: "a partial line stays readable as the current row", columns: 40,
			written: "done\nin progress", expectedLines: []string{"done"}, expectedRow: "in progress",
		},
		{
			name: "a colour sequence is consumed, not rendered", columns: 40,
			written: "\x1b[31mred\x1b[0m\n", expectedLines: []string{"red"}, expectedRow: "",
		},
		{
			name: "an operating-system command is consumed to its terminator", columns: 40,
			written: "\x1b]0;a window title\x07after\n", expectedLines: []string{"after"}, expectedRow: "",
		},
		{
			name: "a carriage return rewrites the row in place", columns: 40,
			written: "50%\r100%\n", expectedLines: []string{"100%"}, expectedRow: "",
		},
		{
			name: "a backspace moves the cursor back", columns: 40,
			written: "abc\bd\n", expectedLines: []string{"abd"}, expectedRow: "",
		},
		{
			name: "a tab advances to the next stop", columns: 40,
			written: "a\tb\n", expectedLines: []string{"a       b"}, expectedRow: "",
		},
		{
			name: "a row longer than the width wraps into a new line", columns: 4,
			written: "abcdef", expectedLines: []string{"abcd"}, expectedRow: "ef",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			screen := NewScreen(testCase.columns)
			lines := screen.Write([]byte(testCase.written))
			if got := strings.Join(lines, "|"); got != strings.Join(testCase.expectedLines, "|") {
				t.Fatalf("completed lines are %q, want %q", got, strings.Join(testCase.expectedLines, "|"))
			}
			if screen.Row() != testCase.expectedRow {
				t.Fatalf("current row is %q, want %q", screen.Row(), testCase.expectedRow)
			}
			if screen.Resets() != 0 {
				t.Fatalf("decoding valid output reset the screen %d times", screen.Resets())
			}
		})
	}
}

func TestScreenKeepsASequenceSplitAcrossTwoReads(t *testing.T) {
	screen := NewScreen(40)
	// A colour sequence and a multi-byte rune, each cut in half by the read boundary — the
	// ordinary case for a child writing faster than the reader drains.
	if lines := screen.Write([]byte("\x1b[3")); len(lines) != 0 {
		t.Fatalf("a half-read escape sequence completed a line: %q", lines)
	}
	if lines := screen.Write([]byte("1mok\xe2\x9c")); len(lines) != 0 {
		t.Fatalf("a half-read rune completed a line: %q", lines)
	}
	lines := screen.Write([]byte("\x93\n"))
	if len(lines) != 1 || lines[0] != "ok✓" {
		t.Fatalf("lines are %q, want [ok✓]", lines)
	}
	if screen.Resets() != 0 {
		t.Fatalf("a merely split sequence was treated as a decode error (%d resets)", screen.Resets())
	}
}

func TestScreenResetsOnAnUndecodableByte(t *testing.T) {
	screen := NewScreen(40)
	screen.Write([]byte("partial row"))
	// A lone continuation byte: complete, and not valid UTF-8 in any continuation.
	screen.Write([]byte{0x80})
	if screen.Resets() != 1 {
		t.Fatalf("screen reset %d times, want 1", screen.Resets())
	}
	if screen.Row() != "" {
		t.Fatalf("the suspect row survived the reset: %q", screen.Row())
	}
	lines := screen.Write([]byte("recovered\n"))
	if len(lines) != 1 || lines[0] != "recovered" {
		t.Fatalf("the screen did not decode normally after the reset: %q", lines)
	}
}

func TestScreenResetsOnAnEscapeSequenceThatNeverTerminates(t *testing.T) {
	screen := NewScreen(40)
	screen.Write(append([]byte{0x1b, '['}, []byte(strings.Repeat("0;", maximumEscapeLength))...))
	if screen.Resets() != 1 {
		t.Fatalf("screen reset %d times, want 1 — an unterminated sequence must not swallow the pane", screen.Resets())
	}
	lines := screen.Write([]byte("recovered\n"))
	if len(lines) != 1 || lines[0] != "recovered" {
		t.Fatalf("the screen did not decode normally after the reset: %q", lines)
	}
}

func TestScreenNeverEmitsAControlByte(t *testing.T) {
	screen := NewScreen(200)
	// Everything a hostile or merely broken child might send to escape its pane: a cursor
	// move, a screen clear, a title change, and a bare bell.
	lines := screen.Write([]byte("\x1b[2J\x1b[H\x1b]0;title\x07safe\x07 text\n"))
	if len(lines) != 1 {
		t.Fatalf("lines are %q, want exactly one", lines)
	}
	if strings.ContainsAny(lines[0], "\x1b\x07") {
		t.Fatalf("a control byte reached the frame: %q", lines[0])
	}
	if lines[0] != "safe text" {
		t.Fatalf("line is %q, want \"safe text\"", lines[0])
	}
}

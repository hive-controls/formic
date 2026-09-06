package pane

import (
	"strings"
	"testing"
)

func TestScrollbackDropsTheOldestLinesAndSaysHowMany(t *testing.T) {
	testCases := []struct {
		name          string
		maximumLines  int
		maximumBytes  int
		appended      []string
		expectedLines []string
		expectedMark  string
	}{
		{
			name: "under both caps nothing is dropped", maximumLines: 4, maximumBytes: 1000,
			appended: []string{"one", "two"}, expectedLines: []string{"one", "two"}, expectedMark: "",
		},
		{
			name: "the line cap drops the oldest", maximumLines: 2, maximumBytes: 1000,
			appended: []string{"one", "two", "three"}, expectedLines: []string{"two", "three"},
			expectedMark: "↑ 1 earlier line dropped",
		},
		{
			name: "the byte cap drops the oldest even when the line count fits",
			// Four lines of five bytes against a fifteen-byte cap: the first must go.
			maximumLines: 100, maximumBytes: 15,
			appended: []string{"aaaaa", "bbbbb", "ccccc", "ddddd"},
			expectedLines: []string{
				"bbbbb", "ccccc", "ddddd",
			},
			expectedMark: "↑ 1 earlier line dropped",
		},
		{
			name: "two dropped lines are named in the plural", maximumLines: 1, maximumBytes: 1000,
			appended: []string{"one", "two", "three"}, expectedLines: []string{"three"},
			expectedMark: "↑ 2 earlier lines dropped",
		},
		{
			name:         "one line is always kept even when it alone exceeds the byte cap",
			maximumLines: 10, maximumBytes: 4, appended: []string{"aaaaaaaaaa"},
			expectedLines: []string{"aaaaaaaaaa"}, expectedMark: "",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			scrollback := NewScrollback(testCase.maximumLines, testCase.maximumBytes)
			for _, line := range testCase.appended {
				scrollback.Append(line)
			}
			if got := strings.Join(scrollback.Lines(), "|"); got != strings.Join(testCase.expectedLines, "|") {
				t.Fatalf("lines are %q, want %q", got, strings.Join(testCase.expectedLines, "|"))
			}
			if marker := scrollback.Marker(); marker != testCase.expectedMark {
				t.Fatalf("marker is %q, want %q", marker, testCase.expectedMark)
			}
		})
	}
}

func TestScrollbackNeverDropsTheNewestLine(t *testing.T) {
	scrollback := NewScrollback(3, 1000)
	for _, line := range []string{"1", "2", "3", "4", "5"} {
		scrollback.Append(line)
	}
	lines := scrollback.Lines()
	if lines[len(lines)-1] != "5" {
		t.Fatalf("newest line is %q, want the last one appended", lines[len(lines)-1])
	}
	if scrollback.Dropped() != 2 {
		t.Fatalf("dropped %d lines, want 2", scrollback.Dropped())
	}
}

func TestScrollbackLinesAreACopy(t *testing.T) {
	scrollback := NewScrollback(10, 1000)
	scrollback.Append("original")
	lines := scrollback.Lines()
	lines[0] = "mutated by the caller"
	if scrollback.Lines()[0] != "original" {
		t.Fatalf("a caller mutated the scrollback through the slice it was handed")
	}
}

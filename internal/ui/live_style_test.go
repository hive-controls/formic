package ui

import (
	"testing"

	"github.com/hive-controls/formic/internal/launch"
)

func TestLiveStateStyleTreatsIdleLikeUp(t *testing.T) {
	idle := liveStateStyle(string(launch.ProbeIdle)).GetForeground()
	if idle != liveUpStyle.GetForeground() {
		t.Fatalf("idle foreground = %v, want the good colour %v", idle, liveUpStyle.GetForeground())
	}
	if idle == mutedStyle.GetForeground() {
		t.Fatalf("idle renders muted, want the good colour")
	}
}

func TestLiveStateStyleKeepsTheExistingColours(t *testing.T) {
	cases := []struct {
		word string
		want any
	}{
		{"up", liveUpStyle.GetForeground()},
		{"down", problemStyle.GetForeground()},
		{"unknown", mutedStyle.GetForeground()},
		{"–", mutedStyle.GetForeground()},
		{"starting", mutedStyle.GetForeground()},
	}
	for _, testCase := range cases {
		if got := liveStateStyle(testCase.word).GetForeground(); got != testCase.want {
			t.Errorf("liveStateStyle(%q) foreground = %v, want %v", testCase.word, got, testCase.want)
		}
	}
}

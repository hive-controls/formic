//go:build !windows

// Open needs a real pseudo-terminal; see session_test.go's header for why this file is not
// built, and not skipped, where this module implements none.

package pane

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOpenCapturesARealChildAndRecordsHowItEnded(t *testing.T) {
	opened, err := Open("run-pty-1", "greeter", exec.Command("sh", "-c", "echo hello from the pane"), Size{Rows: 24, Columns: 80}, Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	waitForPane(t, opened)

	snapshot := opened.Snapshot()
	if !strings.Contains(strings.Join(snapshot.Lines, "\n"), "hello from the pane") {
		t.Fatalf("pane did not capture the child's output: %q", snapshot.Lines)
	}
	if snapshot.Running {
		t.Fatalf("pane still reports the child as running after it exited")
	}
	if opened.WaitError() != nil {
		t.Fatalf("child ended with %v, want a clean exit", opened.WaitError())
	}
}

func TestOpenedPaneTerminatesAChildThatIgnoresTheFirstSignal(t *testing.T) {
	opened, err := Open("run-pty-2", "stubborn",
		exec.Command("sh", "-c", `trap "" TERM; echo ready; while :; do sleep 0.05; done`),
		Size{Rows: 24, Columns: 80}, Options{KillGrace: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	if err := opened.Terminate(); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	waitForPane(t, opened)
	if opened.WaitError() == nil {
		t.Fatalf("a child killed after ignoring the first signal reported a clean exit")
	}
}

func waitForPane(t *testing.T, watched *Pane) {
	t.Helper()
	select {
	case <-watched.Done():
	case <-time.After(20 * time.Second):
		t.Fatalf("pane never finished")
	}
}

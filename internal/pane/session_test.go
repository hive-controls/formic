//go:build !windows

// Every test in this file needs a real pseudo-terminal, so the whole file is built only where
// this module implements one. Whether a captured pane works on Windows is UNVERIFIED — it has
// not been exercised — and a test that skipped there would read as a claim it might.

package pane

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestChildOnAPaneSessionGetsARealTerminal(t *testing.T) {
	session, output := startTestSession(t, "test -t 1 && echo has-tty")
	if err := session.Wait(); err != nil {
		t.Fatalf("child failed: %v", err)
	}
	waitForOutput(t, output, "has-tty")
}

func TestResizePropagatesTheNewSizeToTheChild(t *testing.T) {
	// The child re-reports its terminal size on every SIGWINCH. `sleep` is short and looped
	// rather than long and single because a POSIX shell runs a trap handler only once the
	// command it is waiting on returns.
	session, output := startTestSession(t, "trap 'stty size' WINCH; echo ready; while :; do sleep 0.05; done")
	waitForOutput(t, output, "ready")

	if err := session.Resize(Size{Rows: 30, Columns: 100}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	size, err := session.Size()
	if err != nil {
		t.Fatalf("read size: %v", err)
	}
	if size.Rows != 30 || size.Columns != 100 {
		t.Fatalf("pseudo-terminal size is %+v, want 30x100", size)
	}
	// The size the KERNEL reports is only half the guarantee: the point of the resize is that
	// the child is signalled and re-reads it, which is what this asserts.
	waitForOutput(t, output, "30 100")
}

func TestTerminateEscalatesToKillWhenTheChildIgnoresTheFirstSignal(t *testing.T) {
	session, output := startTestSession(t, `trap "" TERM; echo ready; while :; do sleep 0.05; done`)
	waitForOutput(t, output, "ready")

	if err := session.Terminate(150 * time.Millisecond); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if signal := terminatingSignalOf(t, session); signal != syscall.SIGKILL {
		t.Fatalf("child was terminated by %v, want SIGKILL after the grace elapsed", signal)
	}
}

func TestTerminateNeverEscalatesWhenTheChildHonoursTheFirstSignal(t *testing.T) {
	session, output := startTestSession(t, "echo ready; sleep 30")
	waitForOutput(t, output, "ready")

	// A grace far longer than the test may take: if Terminate waited it out, the elapsed
	// time below would show it, and the killing signal would be SIGKILL rather than SIGTERM.
	started := time.Now()
	if err := session.Terminate(30 * time.Second); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("terminate took %v — it waited out the grace instead of noticing the child left", elapsed)
	}
	if signal := terminatingSignalOf(t, session); signal != syscall.SIGTERM {
		t.Fatalf("child was terminated by %v, want SIGTERM without escalation", signal)
	}
}

func TestTerminateOnAnAlreadyFinishedSessionIsNotAnError(t *testing.T) {
	session, _ := startTestSession(t, "exit 0")
	_ = session.Wait()
	if err := session.Terminate(time.Second); err != nil {
		t.Fatalf("terminate after exit: %v", err)
	}
}

func terminatingSignalOf(t *testing.T, session *Session) syscall.Signal {
	t.Helper()
	waitError := session.Wait()
	exitError, ok := waitError.(*exec.ExitError)
	if !ok {
		t.Fatalf("child ended with %v, want a signal-terminated exit", waitError)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		t.Fatalf("child ended without being signalled: %v", waitError)
	}
	return status.Signal()
}

// collectedOutput accumulates everything a session's child writes, so a test can wait for a
// marker without racing the reader goroutine for the buffer.
type collectedOutput struct {
	mutex sync.Mutex
	text  strings.Builder
}

func (collected *collectedOutput) String() string {
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	return collected.text.String()
}

func startTestSession(t *testing.T, script string) (*Session, *collectedOutput) {
	t.Helper()
	session, err := StartSession(exec.Command("sh", "-c", script), Size{Rows: 24, Columns: 80})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Terminate(time.Second)
		_ = session.Close()
	})
	collected := &collectedOutput{}
	go func() {
		buffer := make([]byte, 4096)
		for {
			count, readErr := session.Output().Read(buffer)
			if count > 0 {
				collected.mutex.Lock()
				collected.text.Write(buffer[:count])
				collected.mutex.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()
	return session, collected
}

func waitForOutput(t *testing.T, collected *collectedOutput, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(collected.String(), marker) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child never produced %q; output so far:\n%s", marker, collected.String())
}

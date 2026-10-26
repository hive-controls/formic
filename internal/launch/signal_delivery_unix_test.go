//go:build !windows

// Both tests below deliver a signal to a helper process (see signal_helper_unix_test.go) via
// syscall.Kill to exercise the wrapper's signal-trap handback window; syscall.Kill has no
// Windows definition, so they build only where it does. Whether the handback window holds on
// Windows is UNVERIFIED — it has not been exercised — and a test that skipped there would read
// as a claim it might.

package launch

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestWrapperRecordsFailedOutcomeWithSignalExitCodeInsteadOfLeavingRunningForever(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv(helperModeEnvironment, "sleeper")
	t.Setenv(helperReadyPathEnv, readyPath)
	store, row := beginSignalFixtureRow(t, sleeperArgv())
	helper := startHelper(t, "wrapper", store.RowPath(row.ID), readyPath)
	waitForFile(t, readyPath)
	if err := syscall.Kill(helper.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code := waitForHelperExit(t, helper); code != 130 {
		t.Fatalf("wrapper exit code = %d, want 130 (128+SIGINT)", code)
	}
	completed, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Outcome != "failed" || completed.ExitCode == nil || *completed.ExitCode != 130 {
		t.Fatalf("completed row = %+v, want failed with exit 130, never running", completed)
	}
}

// TestWrapperKeepsSignalTrapArmedThroughTheHandbackWindow targets the gap between the child
// exiting and Finish persisting the row: a signal delivered there must never kill the wrapper
// before the row is saved. finishedAt is invoked by RunWrapper exactly once, as the argument
// to Finish — i.e. exactly in that window, after the child has already exited cleanly — so
// the helper sending the signal to itself from inside it hits the window deterministically.
func TestWrapperKeepsSignalTrapArmedThroughTheHandbackWindow(t *testing.T) {
	store, row := beginSignalFixtureRow(t, []string{"true"})
	helper := startHelper(t, "wrapper-self-signal-in-handback", store.RowPath(row.ID), "")
	if code := waitForHelperExit(t, helper); code != 0 {
		t.Fatalf("wrapper exit = %d, want 0 (the child already exited cleanly before the signal in the handback window)", code)
	}
	completed, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ExitCode == nil || *completed.ExitCode != 0 || completed.Outcome != "ok" {
		t.Fatalf("completed row = %+v, want the child's real exit 0/ok — a signal in the handback window must not lose the row", completed)
	}
}

//go:build !windows

// Every test in this file delivers a signal to the running process via syscall.Kill and spawns
// real Unix commands ("sleep", "true"), so the whole file is built only where that works.
// Whether signal forwarding holds on Windows is UNVERIFIED — it has not been exercised — and a
// test that skipped there would read as a claim it might.

package launch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSignalForwardingCommandForwardsAndWaits exercises the exact mechanism the foreground
// handoff installs while Bubble Tea has released the TTY (ui/actions.go wraps prepared.Command
// in SignalForwardingCommand and hands it to tea.Exec). Bubble Tea's own exec message is
// opaque outside its package, so this is executed directly here rather than through a
// Model.Update round trip — the alternative the codex finding calls out as previously leaving
// "signal handling and actual handoff... uncovered".
func TestSignalForwardingCommandForwardsAndWaits(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	helper := startHelper(t, "forward", "", readyPath)
	waitForFile(t, readyPath)
	if err := syscall.Kill(helper.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitForHelperExit(t, helper); code != 143 {
		t.Fatalf("ExitCode(err) = %d, want 143 (128+SIGTERM)", code)
	}
}

func TestSignalForwardingCommandSetStdioOnlyWhenUnset(t *testing.T) {
	command := &SignalForwardingCommand{Cmd: exec.Command("true")}
	command.SetStdin(os.Stdin)
	if command.Cmd.Stdin != os.Stdin {
		t.Fatal("SetStdin did not set an unset stdin")
	}

	preset := &bytes.Buffer{}
	command.Cmd.Stdout = preset
	command.SetStdout(os.Stdout)
	if command.Cmd.Stdout != preset {
		t.Fatal("SetStdout overwrote an already-set stdout")
	}

	command.SetStderr(os.Stderr)
	if command.Cmd.Stderr != os.Stderr {
		t.Fatal("SetStderr did not set an unset stderr")
	}
}

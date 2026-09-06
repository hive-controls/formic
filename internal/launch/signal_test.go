package launch

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSignalForwardingCommandForwardsAndWaits exercises the exact mechanism the foreground
// handoff installs while Bubble Tea has released the TTY (ui/actions.go wraps prepared.Command
// in SignalForwardingCommand and hands it to tea.Exec). Bubble Tea's own exec message is
// opaque outside its package, so this is executed directly here rather than through a
// Model.Update round trip — the alternative the codex finding calls out as previously leaving
// "signal handling and actual handoff... uncovered".
func TestSignalForwardingCommandForwardsAndWaits(t *testing.T) {
	signals, stop := ArmSignalTrap()
	defer stop()
	command := &SignalForwardingCommand{Cmd: exec.Command("sleep", "5"), Signals: signals}
	results := make(chan error, 1)
	go func() { results <- command.Run() }()
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if code := ExitCode(err); code != 143 {
			t.Fatalf("ExitCode(err) = %d, want 143 (128+SIGTERM)", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SignalForwardingCommand.Run did not return after SIGTERM — it is not forwarding/waiting")
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

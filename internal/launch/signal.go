package launch

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// trappedSignals is the fixed set forwarded to a running child and trapped against through
// the handback window that follows it.
var trappedSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// ArmSignalTrap starts trapping SIGINT/SIGTERM/SIGHUP and returns the channel they arrive on
// plus a stop function. The CALLER controls how long the trap stays armed, and it MUST stay
// armed past the point the child exits and through Finish actually persisting the run's
// outcome to disk — never merely until the child exits. Stopping any earlier reopens the
// handback window: a second signal, arriving after the child is gone but before the row is
// saved, would hit the default disposition and could kill this process (or hivedeck's own, on
// the foreground route) before the row is ever recorded, leaving it "running" forever.
func ArmSignalTrap() (signals chan os.Signal, stop func()) {
	signals = make(chan os.Signal, 1)
	signal.Notify(signals, trappedSignals...)
	return signals, func() { signal.Stop(signals) }
}

// ForwardSignalsAndWait starts cmd, forwards every signal received on signals to the child
// process, and returns once the child exits. It never assumes the signals channel produces
// anything: a run with no signals delivered simply waits for the child to finish on its own.
// It never stops the trap itself — see ArmSignalTrap: that stays the caller's job, done only
// after the run's outcome is fully persisted.
func ForwardSignalsAndWait(cmd *exec.Cmd, signals <-chan os.Signal) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case sig, open := <-signals:
			if !open {
				signals = nil
				continue
			}
			_ = cmd.Process.Signal(sig)
		case err := <-done:
			return err
		}
	}
}

// SignalForwardingCommand adapts an *exec.Cmd to bubbletea's ExecCommand interface (Run,
// SetStdin, SetStdout, SetStderr) while routing its Run through ForwardSignalsAndWait against
// an EXTERNALLY armed trap (Signals). The caller (ui/actions.go) arms the trap before handing
// this to tea.Exec and stops it only once Finish has persisted the run — after Bubble Tea's
// own terminal-restore and async message dispatch have run their course, not merely once this
// Run call returns — so the trap survives the entire handback window on the foreground route
// too.
type SignalForwardingCommand struct {
	*exec.Cmd
	Signals <-chan os.Signal
}

func (command *SignalForwardingCommand) Run() error {
	return ForwardSignalsAndWait(command.Cmd, command.Signals)
}

func (command *SignalForwardingCommand) SetStdin(reader io.Reader) {
	if command.Cmd.Stdin == nil {
		command.Cmd.Stdin = reader
	}
}

func (command *SignalForwardingCommand) SetStdout(writer io.Writer) {
	if command.Cmd.Stdout == nil {
		command.Cmd.Stdout = writer
	}
}

func (command *SignalForwardingCommand) SetStderr(writer io.Writer) {
	if command.Cmd.Stderr == nil {
		command.Cmd.Stderr = writer
	}
}

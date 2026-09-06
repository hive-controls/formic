// Package pane hosts a child process on its own pseudo-terminal and turns its output into
// bounded, decoded text a terminal UI can render. Everything here is deliberately free of any
// UI dependency: the reader side never produces a UI message, which is what lets the frame
// above it be rate-limited independently of how fast a child writes.
package pane

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Size is a pseudo-terminal's dimensions in character cells.
type Size struct {
	Rows    uint16
	Columns uint16
}

// ErrPTYUnsupported is returned by StartSession on a platform this build has no
// pseudo-terminal implementation for. It is a refusal, not a fallback: a captured pane
// without a pseudo-terminal would hand the child a pipe, and a tool that checks for a
// terminal would then behave differently in a pane than it does anywhere else.
var ErrPTYUnsupported = errors.New("captured panes need a pseudo-terminal, which is not implemented for this platform")

// Session is one child process running on its own pseudo-terminal.
type Session struct {
	command      *exec.Cmd
	terminalFile *os.File
	exited       chan struct{}
	waitError    error
}

// StartSession starts command attached to a new pseudo-terminal sized to size. The child gets
// a REAL terminal — the same guarantee the foreground route gives — so a tool that colours its
// output or draws a progress bar behaves in a pane exactly as it does anywhere else.
func StartSession(command *exec.Cmd, size Size) (*Session, error) {
	terminalFile, err := startOnPseudoTerminal(command, size)
	if err != nil {
		return nil, err
	}
	session := &Session{command: command, terminalFile: terminalFile, exited: make(chan struct{})}
	// The pseudo-terminal is deliberately NOT closed when the child is reaped: a reader is
	// usually still draining the last bytes the child wrote, and closing here would truncate
	// them. The reader ends on its own when the child's side of the terminal closes.
	go func() {
		session.waitError = command.Wait()
		close(session.exited)
	}()
	return session, nil
}

// Output reads whatever the child writes to its terminal. The read ends in an error once the
// child's side of the pseudo-terminal is closed, which is this reader's end-of-output.
func (session *Session) Output() io.Reader {
	return session.terminalFile
}

// Resize sets the pseudo-terminal's dimensions, which is what makes the kernel raise SIGWINCH
// in the child. A pane that changes shape without this leaves the child drawing to the size it
// was started with.
func (session *Session) Resize(size Size) error {
	return resizePseudoTerminal(session.terminalFile, size)
}

// Size reports the pseudo-terminal's current dimensions, read back from the kernel rather than
// remembered — so a caller is told what the child actually has, not what it was last asked for.
func (session *Session) Size() (Size, error) {
	return pseudoTerminalSize(session.terminalFile)
}

// Exited is closed once the child has been reaped.
func (session *Session) Exited() <-chan struct{} {
	return session.exited
}

// Wait blocks until the child exits and reports how it ended.
func (session *Session) Wait() error {
	<-session.exited
	return session.waitError
}

// Terminate is the explicit, two-stage kill: SIGTERM first, then SIGKILL once grace has
// elapsed without the child leaving. A child that honours SIGTERM is never killed, and one
// that ignores it never survives — there is no idle-timeout auto-kill anywhere in this
// package, because a long silent run is legal.
func (session *Session) Terminate(grace time.Duration) error {
	if session.command.Process == nil {
		return fmt.Errorf("session has no process to terminate")
	}
	select {
	case <-session.exited:
		return nil
	default:
	}
	if err := session.command.Process.Signal(terminateSignal); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("send terminate signal: %w", err)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-session.exited:
		return nil
	case <-timer.C:
	}
	if err := session.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill: %w", err)
	}
	<-session.exited
	return nil
}

// Close releases the pseudo-terminal. It is what unblocks a reader still waiting on a child's
// side that some surviving grandchild is holding open.
func (session *Session) Close() error {
	return session.terminalFile.Close()
}

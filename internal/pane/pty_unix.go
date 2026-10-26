//go:build !windows

package pane

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// terminateSignal is the first stage of Session.Terminate.
var terminateSignal os.Signal = syscall.SIGTERM

// CanHostPane reports whether this build can capture a tool on a real pseudo-terminal. Defined
// per-platform beside the pty implementation itself (see pty_windows.go's counterpart), so a
// caller never has to sniff runtime.GOOS to ask the same question this file's own build tag
// already answers.
func CanHostPane() bool { return true }

func startOnPseudoTerminal(command *exec.Cmd, size Size) (*os.File, error) {
	terminalFile, err := pty.StartWithSize(command, windowSize(size))
	if err != nil {
		return nil, fmt.Errorf("start on a pseudo-terminal: %w", err)
	}
	return terminalFile, nil
}

func resizePseudoTerminal(terminalFile *os.File, size Size) error {
	if err := pty.Setsize(terminalFile, windowSize(size)); err != nil {
		return fmt.Errorf("resize the pseudo-terminal: %w", err)
	}
	return nil
}

func pseudoTerminalSize(terminalFile *os.File) (Size, error) {
	rows, columns, err := pty.Getsize(terminalFile)
	if err != nil {
		return Size{}, fmt.Errorf("read the pseudo-terminal size: %w", err)
	}
	return Size{Rows: uint16(rows), Columns: uint16(columns)}, nil
}

func windowSize(size Size) *pty.Winsize {
	return &pty.Winsize{Rows: size.Rows, Cols: size.Columns}
}

//go:build windows

package pane

import (
	"os"
	"os/exec"
)

// terminateSignal is the first stage of Session.Terminate. It is never reached on this
// platform: StartSession refuses before a process exists.
var terminateSignal os.Signal = os.Interrupt

// Whether a captured pane can be hosted on this platform is UNVERIFIED — it has not been
// exercised, so this build refuses rather than claiming a capability nobody has run. The
// other two launch routes are unaffected.
func startOnPseudoTerminal(command *exec.Cmd, size Size) (*os.File, error) {
	return nil, ErrPTYUnsupported
}

func resizePseudoTerminal(terminalFile *os.File, size Size) error {
	return ErrPTYUnsupported
}

func pseudoTerminalSize(terminalFile *os.File) (Size, error) {
	return Size{}, ErrPTYUnsupported
}

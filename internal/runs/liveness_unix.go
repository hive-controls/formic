//go:build !windows

package runs

import (
	"os"
	"syscall"
)

// processAlive reports whether pid still names a running process. os.FindProcess never fails
// on Unix even for a pid that no longer exists, so existence is probed by sending signal 0 —
// delivered to no one, but still rejected with ESRCH if the process is gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// supervisorStartedAt is a no-op here: pid reuse is a Windows-specific problem for this store
// (signal-0 liveness above already distinguishes a genuinely running process from a gone one,
// unlike Windows' handle-based check), so Unix rows never carry a start token, and
// processSupervisorAlive below never has one to compare.
func supervisorStartedAt() string {
	return ""
}

// processSupervisorAlive on Unix is exactly the pid-existence check: unchanged from before this
// field existed (see supervisorStartedAt).
func processSupervisorAlive(pid int, startedAt string) bool {
	return processAlive(pid)
}

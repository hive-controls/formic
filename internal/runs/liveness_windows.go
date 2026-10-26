//go:build windows

package runs

import (
	"os"
	"strconv"
	"syscall"
)

// processAlive reports whether pid names a process that is STILL RUNNING right now. A plain
// OpenProcess/os.FindProcess success is not enough: the kernel keeps a process object (and its
// pid) alive for as long as anything still holds a handle to it, including a handle this very
// process (or a parent's exec.Cmd) is still holding on an already-exited child — a "zombie
// handle window". ✅ VERIFIED (s0923, scratchpad/pidcheck): spawning and waiting on 200
// short-lived child processes, OpenProcess-only succeeded on ~6% of them immediately after
// Wait() returned, and in every one of those cases GetExitCodeProcess's exit code was already
// set (not STILL_ACTIVE) — proof the process had genuinely exited despite the open handle. So
// liveness here also checks the exit code, matching Unix's actual-vs-handle distinction (see
// liveness_unix.go's signal-0 probe).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	const stillActive = 259 // STILL_ACTIVE
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == stillActive
}

// supervisorStartedAt captures THIS process's own creation time, for Store.Begin to stamp onto
// Row.SupervisorStartedAt at launch — the token processSupervisorAlive later compares against
// to tell this exact process instance apart from a different process Windows has since
// recycled the same pid onto: Windows recycles pids, so pid existence alone cannot distinguish
// "the original supervisor is still up" from "some unrelated process now happens to have that
// pid".
func supervisorStartedAt() string {
	return processStartToken(os.Getpid())
}

// processStartToken reads pid's process creation time via the Win32 API and returns it as a
// decimal token (the creation FILETIME's raw 100-nanosecond tick count) — opaque, but exactly
// reproducible for the SAME process instance every time it is read, and (short of a FILETIME
// collision) different for any other process. Returns "" when the creation time cannot be read
// (pid already gone, access denied, ...); callers treat "" as "unknown," never as a match.
func processStartToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)
	var creationTime, exitTime, kernelTime, userTime syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return ""
	}
	ticks := uint64(creationTime.HighDateTime)<<32 | uint64(creationTime.LowDateTime)
	return strconv.FormatUint(ticks, 10)
}

// processSupervisorAlive reports whether the supervisor recorded on a row is still the SAME
// process instance that wrote it: pid must both still name a running process (processAlive) and,
// when a start token was recorded, still carry that exact token — never merely "some process
// with this pid exists". A row with no recorded token (startedAt == "", either
// written before this field existed or on a platform that never sets it) falls back to the
// plain pid-liveness check, fail-safe in the same direction as before: never closes a run that
// might still be live.
func processSupervisorAlive(pid int, startedAt string) bool {
	if !processAlive(pid) {
		return false
	}
	if startedAt == "" {
		return true
	}
	return processStartToken(pid) == startedAt
}

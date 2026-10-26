package runs

import (
	"os"
	"os/exec"
	"testing"

	"github.com/hive-controls/formic/internal/testfixture"
)

// TestProcessAliveDistinguishesARealLiveProcessFromARealDeadOne exercises whichever platform
// implementation this build selected (liveness_unix.go / liveness_windows.go) against real
// processes rather than fixtures: this test's own pid is genuinely alive, and the fixture tool
// process, once waited on, is genuinely gone.
func TestProcessAliveDistinguishesARealLiveProcessFromARealDeadOne(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive(self) = false, want true — this test is running")
	}

	command := exec.Command(testfixture.Tool(t), "fixture:exit", "0")
	if err := command.Run(); err != nil {
		t.Fatalf("could not run the fixture tool to get a real dead pid: %v", err)
	}
	deadPID := command.Process.Pid
	if processAlive(deadPID) {
		t.Fatalf("processAlive(%d) = true, want false — that process has already exited", deadPID)
	}

	if processAlive(0) || processAlive(-1) {
		t.Fatal("processAlive should refuse a non-positive pid outright")
	}
}

// TestProcessSupervisorAliveRejectsAMismatchedStartToken is the pid-reuse regression test at the
// liveness layer: a live pid alone is not enough — if a start token was recorded, it must still
// match the process CURRENTLY holding that pid, or the pid has been recycled onto a different
// process and must not be reported alive. A row with no recorded token (startedAt == "", the
// legacy/no-op case) must still fall back to the plain pid check.
func TestProcessSupervisorAliveRejectsAMismatchedStartToken(t *testing.T) {
	selfPID := os.Getpid()
	selfToken := supervisorStartedAt()

	if !processSupervisorAlive(selfPID, "") {
		t.Fatal("processSupervisorAlive(self, \"\") = false, want true — no recorded token must fall back to plain liveness")
	}
	if selfToken == "" {
		t.Skip("this platform records no start token (see liveness_unix.go) — mismatch detection is Windows-only for now")
	}
	if !processSupervisorAlive(selfPID, selfToken) {
		t.Fatal("processSupervisorAlive(self, selfToken) = false, want true — this is genuinely the same process instance")
	}
	if processSupervisorAlive(selfPID, selfToken+"-mismatch") {
		t.Fatal("processSupervisorAlive(self, mismatched token) = true, want false — a recycled pid must not read as the original supervisor")
	}
}

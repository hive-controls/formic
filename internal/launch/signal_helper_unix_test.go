//go:build !windows

// Signal tests never signal the test binary itself: a signal delivered while no handler is
// armed (before Notify, after Stop, or leaked from a previous test) takes the default
// disposition and kills the binary ("signal: interrupt") or lands in the next test's trap
// (exit 130 where 143 was wanted). Each test instead re-executes the test binary as a helper
// process, signals THAT, and reads its exit status. The helper holds its own catch-all
// Notify for its whole lifetime, so a late self-signal cannot kill it either.

package launch

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

const (
	helperModeEnvironment = "HIVEDECK_SIGNAL_HELPER_MODE"
	helperRowPathEnv      = "HIVEDECK_SIGNAL_HELPER_ROW"
	helperReadyPathEnv    = "HIVEDECK_SIGNAL_HELPER_READY"
)

// sleeperArgv re-executes the test binary as a child that writes readyPath once its runtime is
// up (so its signal dispositions are final) and then sleeps. A shell wrapper is not used: a
// signal landing on sh between touch and exec is not reliably fatal.
func sleeperArgv() []string {
	return []string{os.Args[0], "-test.run=^TestSignalHelperProcess$"}
}

// TestSignalHelperProcess is the helper body, not a test: it returns immediately unless the
// parent re-executed the binary with the mode variable set.
func TestSignalHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnvironment)
	if mode == "" {
		return
	}
	if mode == "sleeper" {
		if err := os.WriteFile(os.Getenv(helperReadyPathEnv), nil, 0o600); err != nil {
			os.Exit(97)
		}
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	catchAll := make(chan os.Signal, 16)
	signal.Notify(catchAll, trappedSignals...)
	switch mode {
	case "wrapper":
		os.Exit(runHelperWrapper(func() time.Time { return time.Unix(200, 0) }))
	case "wrapper-self-signal-in-handback":
		os.Exit(runHelperWrapper(func() time.Time {
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
			return time.Unix(200, 0)
		}))
	case "forward":
		signals, stop := ArmSignalTrap()
		defer stop()
		sleeper := exec.Command(os.Args[0], "-test.run=^TestSignalHelperProcess$")
		sleeper.Env = append(os.Environ(), helperModeEnvironment+"=sleeper")
		command := &SignalForwardingCommand{Cmd: sleeper, Signals: signals}
		os.Exit(ExitCode(command.Run()))
	}
	os.Exit(98)
}

func runHelperWrapper(finishedAt func() time.Time) int {
	code, err := RunWrapper(os.Getenv(helperRowPathEnv), finishedAt)
	if err != nil {
		return 99
	}
	return code
}

// startHelper re-executes the test binary in the given mode.
func startHelper(t *testing.T, mode, rowPath, readyPath string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSignalHelperProcess$")
	command.Env = append(os.Environ(), helperModeEnvironment+"="+mode, helperRowPathEnv+"="+rowPath, helperReadyPathEnv+"="+readyPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	return command
}

// waitForFile blocks until path exists: the child writes it only after the trap is armed.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("helper never reached the armed state (%s missing)", path)
}

// waitForHelperExit returns the helper's exit code, failing if it was killed by a signal.
func waitForHelperExit(t *testing.T, command *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit — signal not forwarded/waited on")
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		t.Fatalf("helper was killed by %v — a signal arrived with no handler armed", status.Signal())
	}
	return command.ProcessState.ExitCode()
}

// beginSignalFixtureRow writes a run row plus its replay file for argv under a scratch store.
func beginSignalFixtureRow(t *testing.T, argv []string) (runs.Store, runs.Row) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("fixture", profile.ModeTab, repositoryRoot, argv, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteReplay(row.ID, argv, repositoryRoot, os.Environ()); err != nil {
		t.Fatal(err)
	}
	return store, row
}

package launch

import (
	"context"
	"os/exec"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/profile"
)

// HealthTimeout bounds how long a declared health.check may run before RunHealthCheck gives up
// on it (§15 N11: "a short timeout").
const HealthTimeout = 5 * time.Second

// HealthResult reports the outcome of running a manifest's declared health.check (§15 N11: run
// it in `describe` and `doctor`, never at list time, and never persist the result). It is built
// fresh on every call — never written through runs.Store, never read back from anywhere.
type HealthResult struct {
	Ran        bool   `json:"ran"`
	OK         bool   `json:"ok"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

// RunHealthCheck runs tool's declared health.check through the same executable resolution and
// declared-env scoping every other route in this package uses (resolveEnvironment,
// resolveExecutable), bounded by HealthTimeout, and never through runs.Store — the caller
// reports the result, nothing here persists it. It returns nil when the manifest declares no
// health check at all, so a caller can tell "not declared" apart from "declared but the run
// itself failed" (a non-nil HealthResult with Error set).
func RunHealthCheck(tool discovery.Tool, settings profile.Tool, processEnvironment []string, repositoryRoot string) *HealthResult {
	if tool.Manifest.Health == nil {
		return nil
	}
	result := &HealthResult{Ran: true}
	environment, err := resolveEnvironment(tool.Manifest, settings, processEnvironment)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	argv := tool.Manifest.Health.Check
	if len(argv) == 0 {
		result.Error = "health.check declares no command"
		return result
	}
	executable, err := resolveExecutable(argv[0], environment)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	cwd := tool.SourceRoot
	if cwd == "" {
		cwd = repositoryRoot
	}
	ctx, cancel := context.WithTimeout(context.Background(), HealthTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Path, command.Dir, command.Env = executable, cwd, environment
	start := time.Now()
	runError := command.Run()
	result.DurationMs = time.Since(start).Milliseconds()
	if ctx.Err() == context.DeadlineExceeded {
		result.Error = "health check timed out"
		return result
	}
	exitCode := ExitCode(runError)
	result.ExitCode = &exitCode
	result.OK = runError == nil
	return result
}

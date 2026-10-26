package launch

import (
	"path/filepath"
	"testing"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/testfixture"
)

// TestRunHealthCheckReturnsNilWhenNoHealthDeclared proves a manifest with no `health` block is
// reported as "not declared", not as a failed check — describe/doctor tell the two apart by
// this nil.
func TestRunHealthCheckReturnsNilWhenNoHealthDeclared(t *testing.T) {
	tool := discovery.Tool{Name: "plain", Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "echo"}}}
	if result := RunHealthCheck(tool, profile.Tool{}, nil, t.TempDir()); result != nil {
		t.Fatalf("RunHealthCheck = %+v, want nil for a manifest with no health block", result)
	}
}

// TestRunHealthCheckReportsSuccess proves a declared health.check that exits 0 is run through
// the launcher's own executable resolution and reported ok, with a recorded duration.
func TestRunHealthCheckReportsSuccess(t *testing.T) {
	tool := discovery.Tool{Name: "healthy", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: "echo"},
		Health: &formats.ToolManifestHealth{Check: []string{testfixture.Tool(t), "fixture:exit", "0"}},
	}}
	result := RunHealthCheck(tool, profile.Tool{}, nil, t.TempDir())
	if result == nil || !result.Ran || !result.OK {
		t.Fatalf("RunHealthCheck = %+v, want Ran=true OK=true", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result.ExitCode = %+v, want 0", result.ExitCode)
	}
}

// TestRunHealthCheckReportsFailureExitCode proves a declared health.check that exits non-zero
// is reported not-ok, with the real exit code, never treated as an error worth abandoning the
// report over.
func TestRunHealthCheckReportsFailureExitCode(t *testing.T) {
	tool := discovery.Tool{Name: "unhealthy", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: "echo"},
		Health: &formats.ToolManifestHealth{Check: []string{testfixture.Tool(t), "fixture:exit", "3"}},
	}}
	result := RunHealthCheck(tool, profile.Tool{}, nil, t.TempDir())
	if result == nil || !result.Ran || result.OK {
		t.Fatalf("RunHealthCheck = %+v, want Ran=true OK=false", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 3 {
		t.Fatalf("result.ExitCode = %+v, want 3", result.ExitCode)
	}
}

// TestRunHealthCheckAppliesDeclaredEnvScoping proves the health check runs through the same
// declared-env scoping regular launches use (internal/launch's own resolveEnvironment): a
// manifest that declares env[] only sees those keys, never an ambient variable it did not ask
// for.
func TestRunHealthCheckAppliesDeclaredEnvScoping(t *testing.T) {
	tool := discovery.Tool{Name: "scoped", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: "echo"},
		Health: &formats.ToolManifestHealth{Check: []string{testfixture.Tool(t), "fixture:print-env", "DECLARED_KEY"}},
		Env:    []formats.ToolManifestEnvElem{{Name: "DECLARED_KEY"}},
	}}
	processEnvironment := []string{"DECLARED_KEY=present", "UNDECLARED_KEY=also-present"}
	result := RunHealthCheck(tool, profile.Tool{}, processEnvironment, t.TempDir())
	if result == nil || !result.Ran || !result.OK {
		t.Fatalf("RunHealthCheck = %+v, want Ran=true OK=true (DECLARED_KEY was scoped through)", result)
	}
}

// TestRunHealthCheckNeverPersistsAnything proves the state and config directories a real
// launch would touch (run rows, status files) are untouched by a health check — it is reported,
// never persisted (§15 N11).
func TestRunHealthCheckNeverPersistsAnything(t *testing.T) {
	stateDirectory := t.TempDir()
	tool := discovery.Tool{Name: "healthy", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: "echo"},
		Health: &formats.ToolManifestHealth{Check: []string{testfixture.Tool(t), "fixture:exit", "0"}},
	}}
	if result := RunHealthCheck(tool, profile.Tool{}, nil, t.TempDir()); result == nil || !result.OK {
		t.Fatalf("RunHealthCheck = %+v, want OK=true", result)
	}
	entries, err := filepath.Glob(filepath.Join(stateDirectory, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("state directory entries = %v, want none written by a health check", entries)
	}
}

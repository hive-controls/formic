package launch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

// TestPrepareRefusesOnMissingRequiredInputNamingEveryOne proves resolveInputsForLaunch's
// "never just the first" discipline: two required inputs, neither given, both come back in
// one *InputsRefused, and no run row is ever written.
func TestPrepareRefusesOnMissingRequiredInputNamingEveryOne(t *testing.T) {
	stateDir := t.TempDir()
	tool := discovery.Tool{
		Name: "needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "echo", Args: []string{"{first}", "{second}"}},
			Inputs: []formats.Input{
				{Name: "first", Required: true},
				{Name: "second", Required: true},
			},
		},
	}
	_, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: stateDir},
		Environment: []string{"PATH=/usr/bin"}, StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)

	var refused *InputsRefused
	if !errors.As(err, &refused) {
		t.Fatalf("Prepare error = %v, want a *InputsRefused", err)
	}
	if len(refused.Missing) != 2 || refused.Missing[0] != "first" || refused.Missing[1] != "second" {
		t.Fatalf("refused.Missing = %v, want both required inputs named at once", refused.Missing)
	}
	entries, readErr := os.ReadDir(stateDir)
	if readErr != nil {
		t.Fatalf("read state dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("state dir entries = %v, want no run row written on an inputs refusal", entries)
	}
}

// TestPrepareRefusesOnAnUndeclaredInputName proves the Unknown half of the same check: a
// value given for a name the manifest never declared is refused, never silently ignored or
// silently substituted.
func TestPrepareRefusesOnAnUndeclaredInputName(t *testing.T) {
	tool := discovery.Tool{
		Name:     "plain",
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "echo"}},
	}
	_, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: t.TempDir()},
		Environment: []string{"PATH=/usr/bin"}, StartedAt: time.Unix(100, 0),
		Inputs: map[string]string{"mystery": "value"},
	}, profile.ModeForeground)

	var refused *InputsRefused
	if !errors.As(err, &refused) {
		t.Fatalf("Prepare error = %v, want a *InputsRefused", err)
	}
	if len(refused.Unknown) != 1 || refused.Unknown[0] != "mystery" {
		t.Fatalf("refused.Unknown = %v, want [\"mystery\"]", refused.Unknown)
	}
}

// TestPrepareLeavesManifestsWithNoDeclaredInputsUnaffected pins N10's own promise: a manifest
// declaring no inputs[] at all behaves exactly as before car 8, over the existing {repo}/{spec}
// fixtures — resolveInputsForLaunch must never refuse or otherwise change behavior for it.
func TestPrepareLeavesManifestsWithNoDeclaredInputsUnaffected(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tool := foregroundFixtureTool(t, 0, "ok")
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Store: runs.Store{StateDirectory: t.TempDir()},
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare of a manifest with no inputs[] returned an error: %v", err)
	}
	if prepared.Command == nil {
		t.Fatal("Prepare of a manifest with no inputs[] produced no command")
	}
}

// TestPrepareFillsADeclaredSpecInputFromRequestSpec proves the {spec} sugar (N10): a manifest
// that declares an input literally named "spec" is filled from Request.Spec exactly as the
// bare {spec} token is for a manifest with no such declared input — the caller never has to
// pass Inputs["spec"] itself.
func TestPrepareFillsADeclaredSpecInputFromRequestSpec(t *testing.T) {
	repositoryToken := "{repo}"
	tool := discovery.Tool{
		Name: "phone-lookup",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Cwd: &repositoryToken, Args: []string{"fixture:pwd", "{spec}"}},
			Inputs: []formats.Input{{Name: "spec", Required: true}},
		},
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Store: runs.Store{StateDirectory: t.TempDir()},
		Spec: "/repo/spec.yaml", Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if got := prepared.Command.Args[len(prepared.Command.Args)-1]; got != "/repo/spec.yaml" {
		t.Fatalf("substituted spec argument = %q, want the request's Spec value", got)
	}
}

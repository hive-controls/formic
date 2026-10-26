package launch

import (
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

// evidenceDirRequiredFixtureTool declares outputs.evidenceDir bound to a REQUIRED declared
// env[] key — the shape that could never launch if the evidence variable were injected only
// after Prepare returns, since declared-env scoping (which enforces `required`) runs inside
// Prepare, before any caller ever sees Prepared.Command.
func evidenceDirRequiredFixtureTool(t *testing.T) discovery.Tool {
	t.Helper()
	evidenceKey := "FIXTURE_EVIDENCE_DIR"
	return discovery.Tool{
		Name: "evidence-required-tool",
		Manifest: formats.ToolManifest{
			Launch:  formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:print-env", evidenceKey}},
			Outputs: &formats.ToolManifestOutputs{EvidenceDir: &evidenceKey},
			Env:     []formats.ToolManifestEnvElem{{Name: evidenceKey, Required: true}},
		},
	}
}

// TestPrepareMintsEvidenceInsideItselfSoARequiredEvidenceDirKeyIsSatisfied is the regression
// proof for the evidence-ownership fix: a manifest whose outputs.evidenceDir env key is
// `required: true` — which would refuse under declared-env scoping if that key were injected
// only AFTER Prepare returned — runs successfully when request.EvidenceRoot is set, because
// Prepare creates <EvidenceRoot>/<run-id>/ and wires it in BEFORE scoping enforces the
// required key.
func TestPrepareMintsEvidenceInsideItselfSoARequiredEvidenceDirKeyIsSatisfied(t *testing.T) {
	stateDir := t.TempDir()
	evidenceRoot := t.TempDir()
	tool := evidenceDirRequiredFixtureTool(t)
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: t.TempDir(), Store: runs.Store{StateDirectory: stateDir},
		Environment: os.Environ(), StartedAt: time.Unix(100, 0), EvidenceRoot: evidenceRoot,
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error (the required evidenceDir key should have been satisfied): %v", err)
	}
	wantEvidenceDir := filepath.Join(evidenceRoot, prepared.Row.ID)
	if prepared.Row.EvidenceDir != wantEvidenceDir {
		t.Fatalf("row.EvidenceDir = %q, want %q", prepared.Row.EvidenceDir, wantEvidenceDir)
	}
	if _, statErr := os.Stat(wantEvidenceDir); statErr != nil {
		t.Fatalf("evidence directory was not created: %v", statErr)
	}
	found := false
	for _, name := range prepared.Row.EnvNames {
		if name == "FIXTURE_EVIDENCE_DIR" {
			found = true
		}
	}
	if !found {
		t.Fatalf("row.EnvNames = %v, want FIXTURE_EVIDENCE_DIR among them", prepared.Row.EnvNames)
	}
	prepared.Command.Stdout, prepared.Command.Stderr = os.Stdout, os.Stderr
	if err := prepared.Command.Run(); err != nil {
		t.Fatalf("the child did not see FIXTURE_EVIDENCE_DIR (fixture:print-env exits 1 when unset): %v", err)
	}
}

// TestPrepareWithNoEvidenceRootBehavesExactlyAsPlainLaunch proves EvidenceRoot is opt-in: a
// request that leaves it empty (every non-mission caller) gets the pre-existing
// resolveEvidenceDir fallback, unchanged.
func TestPrepareWithNoEvidenceRootBehavesExactlyAsPlainLaunch(t *testing.T) {
	stateDir := t.TempDir()
	tool := evidenceDirRequiredFixtureTool(t)
	_, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: stateDir},
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err == nil {
		t.Fatal("Prepare should refuse: the required evidenceDir key has no value and no EvidenceRoot was given to satisfy it")
	}
}

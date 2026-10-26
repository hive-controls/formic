package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

func TestRunReportsInvalidManifestAndShadow(t *testing.T) {
	sourceOne := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-one"))
	sourceTwo := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-two"))
	catalog, err := discovery.Discover([]string{sourceOne, sourceTwo})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	var output bytes.Buffer
	if exitCode := Run(catalog, &output); exitCode == 0 {
		t.Fatalf("doctor returned success despite an invalid manifest:\n%s", output.String())
	}
	// source-one's "shared" tool shadows source-two's tool of the same name — informational
	// only under the admission rule's precedence-order shadowing (§12), never fatal by
	// itself; the invalid manifest is what makes the exit code non-zero.
	for _, expected := range []string{"invalid invalid-local", "↑ shared:", "problems:"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("doctor output missing %q:\n%s", expected, output.String())
		}
	}
}

// TestRunNeverFailsOnAShadowAlone proves a shadow is reported but stays exit 0 by itself —
// the admission rule (§12) retires the old repo-vs-user "fatal collision" special case:
// precedence is now the user's own explicit catalog order, not an implicit hierarchy.
func TestRunNeverFailsOnAShadowAlone(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name:     "mine",
		Source:   discovery.Source{Kind: discovery.SourceCatalog, Path: "/first/.formic/tools/mine.yaml"},
		Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: "mine"}},
		Shadowed: []discovery.Definition{{
			Name: "mine", Command: "a-different-command",
			Source: discovery.Source{Kind: discovery.SourceCatalog, Path: "/second/.formic/tools/mine.yaml"},
		}},
	}}}
	var output bytes.Buffer
	exitCode := Run(catalog, &output)
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0 (a shadow alone is informational):\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "↑ mine:") {
		t.Fatalf("doctor output missing the shadow badge:\n%s", output.String())
	}
}

func TestRunPrintsToolsAndSuccessSummary(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "sample", Source: discovery.Source{Kind: discovery.SourceProduct, Path: "/repo/sample.formic.yaml"},
	}}}
	var output bytes.Buffer
	if exitCode := Run(catalog, &output); exitCode != 0 {
		t.Fatalf("doctor returned %d for healthy catalog:\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "tool sample") || !strings.Contains(output.String(), "ok: 1 tools discovered") {
		t.Fatalf("doctor output missing tool or summary:\n%s", output.String())
	}
}

func TestRunWithProfileReportsEveryProfileProblem(t *testing.T) {
	problem := &profile.ValidationError{Problems: []string{"unknown key one", "unknown key two"}}
	var output bytes.Buffer
	exitCode := RunWithProfile(discovery.Catalog{}, problem, &output)
	if exitCode == 0 {
		t.Fatalf("doctor returned success despite profile problems:\n%s", output.String())
	}
	for _, expected := range problem.Problems {
		if !strings.Contains(output.String(), "profile "+expected) {
			t.Fatalf("doctor output missing %q:\n%s", expected, output.String())
		}
	}
}

// TestRecoverAndReportRecoversAnAbandonedForegroundRowBeforeReporting is `doctor`'s half of the
// stuck-row recovery: a foreground row still "running" with no live supervisor must not just
// sit there — doctor is the other place (besides opening the run list) recovery is required to
// run.
func TestRecoverAndReportRecoversAnAbandonedForegroundRowBeforeReporting(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.SupervisorPID = 999999
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	RecoverAndReport(discovery.Catalog{}, nil, nil, store, time.Unix(200, 0), &output)

	recovered, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Outcome == "running" {
		t.Fatalf("row = %+v, want doctor to have recovered it before reporting", recovered)
	}
}

func TestRunWithProfileWarningsKeepsAHealthyExitCode(t *testing.T) {
	var output bytes.Buffer
	exitCode := RunWithProfileWarnings(discovery.Catalog{}, nil, []string{"tool \"old\" has no discovered manifest"}, &output)
	if exitCode != 0 || !strings.Contains(output.String(), "profile warning:") {
		t.Fatalf("exit = %d, output:\n%s", exitCode, output.String())
	}
}

// TestRunWithMissionsAndHealthReportsAHealthCheckPerTool proves `doctor` runs a healthChecker
// callback for every discovered tool and prints what it reports (§15 N11) — never fatal to the
// rest of the report by itself.
func TestRunWithMissionsAndHealthReportsAHealthCheckPerTool(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "sample", Source: discovery.Source{Kind: discovery.SourceProduct, Path: "/repo/sample.formic.yaml"},
	}}}
	var output bytes.Buffer
	healthChecker := func(tool discovery.Tool) *launch.HealthResult {
		exitCode := 0
		return &launch.HealthResult{Ran: true, OK: true, ExitCode: &exitCode, DurationMs: 7}
	}
	exitCode := RunWithMissionsAndHealth(catalog, mission.Set{}, nil, nil, healthChecker, &output)
	if exitCode != 0 {
		t.Fatalf("doctor returned %d for a healthy catalog:\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "health sample:") {
		t.Fatalf("doctor output missing the health line:\n%s", output.String())
	}
}

// TestRunWithMissionsAndHealthOmitsTheLineWhenNoHealthDeclared proves a nil HealthResult (the
// manifest declares no health.check) prints no health line at all, rather than a misleading
// "not run" one.
func TestRunWithMissionsAndHealthOmitsTheLineWhenNoHealthDeclared(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "sample", Source: discovery.Source{Kind: discovery.SourceProduct, Path: "/repo/sample.formic.yaml"},
	}}}
	var output bytes.Buffer
	healthChecker := func(tool discovery.Tool) *launch.HealthResult { return nil }
	if exitCode := RunWithMissionsAndHealth(catalog, mission.Set{}, nil, nil, healthChecker, &output); exitCode != 0 {
		t.Fatalf("doctor returned %d:\n%s", exitCode, output.String())
	}
	if strings.Contains(output.String(), "health sample:") {
		t.Fatalf("doctor output should not report a health line when none was declared:\n%s", output.String())
	}
}

func TestRunReportsAnArgumentRefusalAsInvalid(t *testing.T) {
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: argtool\nlaunch:\n  command: echo\narguments:\n  - flag: --healer\n    kind: choice\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "argtool.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := discovery.Discover([]string{sourcePath})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	var output bytes.Buffer
	if exitCode := Run(catalog, &output); exitCode != 1 {
		t.Fatalf("exit = %d, want 1:\n%s", exitCode, output.String())
	}
	want := "arguments[0] (--healer): kind choice needs at least one option"
	if !strings.Contains(output.String(), "invalid argtool") || !strings.Contains(output.String(), want) {
		t.Fatalf("doctor output missing the invalid line with %q:\n%s", want, output.String())
	}
}

// TestRunReportsAnInvalidActionManifest proves the action admission rules reach doctor through
// the loader: a manifest with two actions of one name is reported invalid and fails the run.
func TestRunReportsAnInvalidActionManifest(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, ".formic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "products"), 0o755); err != nil {
		t.Fatal(err)
	}
	duplicate, err := os.ReadFile(filepath.Join("..", "..", "testdata", "manifests", "actions-duplicate-name.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "products", "dup.formic.yaml"), duplicate, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := discovery.Discover([]string{source})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	var output bytes.Buffer
	if exitCode := Run(catalog, &output); exitCode == 0 {
		t.Fatalf("doctor passed a duplicate-action manifest:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "name is already used by actions[0]") {
		t.Fatalf("doctor output missing the action problem:\n%s", output.String())
	}
}

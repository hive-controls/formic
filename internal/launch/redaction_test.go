package launch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

// sensitiveInputFixtureTool declares one required, sensitive input named "apiKey", substituted
// into launch.args — the shape N10 ruling 2 covers: a sensitive value may still reach the
// child's real argv, but the durable row must never hold it. It uses testfixture.Tool as its
// command so buildCommand's own executable lookup succeeds on every platform, including
// Windows, without depending on a real PATH.
func sensitiveInputFixtureTool(t *testing.T) discovery.Tool {
	return discovery.Tool{
		Name: "secret-tool",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:exit", "0", "--key", "{apiKey}"}},
			Inputs: []formats.Input{{Name: "apiKey", Required: true, Sensitive: true}},
		},
	}
}

// TestPrepareRedactsTheSensitiveInputInTheDurableRowButNotInTheRealCommand is the required
// redaction-bytes proof (brief §C, test 1 and 5): run.json on disk contains neither the real
// secret value nor even the substring, wherever the manifest put the {apiKey} token, while the
// literal "<redacted:apiKey>" appears in its place — and the REAL command still gets the real
// value.
func TestPrepareRedactsTheSensitiveInputInTheDurableRowButNotInTheRealCommand(t *testing.T) {
	stateDir := t.TempDir()
	tool := sensitiveInputFixtureTool(t)
	secretValue := "fake-input-value-123"
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: stateDir},
		Inputs: map[string]string{"apiKey": secretValue}, Environment: os.Environ(),
		StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}

	if prepared.Command.Args[len(prepared.Command.Args)-1] != secretValue {
		t.Fatalf("real command args = %v, want the real secret value reaching argv", prepared.Command.Args)
	}
	if prepared.Row.Argv[len(prepared.Row.Argv)-1] != "<redacted:apiKey>" {
		t.Fatalf("row.Argv = %v, want the sensitive value redacted as <redacted:apiKey>", prepared.Row.Argv)
	}

	rowBytes, err := os.ReadFile(runs.Store{StateDirectory: stateDir}.RowPath(prepared.Row.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rowBytes), secretValue) {
		t.Fatalf("run.json contains the real secret value:\n%s", rowBytes)
	}
	// json.Marshal HTML-escapes "<"/">" by default, so the marker is checked without them.
	if !strings.Contains(string(rowBytes), "redacted:apiKey") {
		t.Fatalf("run.json does not contain the redaction marker:\n%s", rowBytes)
	}
}

// TestPrepareRedactsTheDurableRowsEnvironmentToNamesOnly is the redaction-bytes proof for the
// full-child-environment leak this closes: run.json holds no child environment VALUE at all,
// only names.
func TestPrepareRedactsTheDurableRowsEnvironmentToNamesOnly(t *testing.T) {
	stateDir := t.TempDir()
	tool := discovery.Tool{Name: "fixture", Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}}}
	secretEnvironmentValue := "fake-env-secret-456"
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: stateDir},
		Environment: append(os.Environ(), "SOME_SECRET="+secretEnvironmentValue), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if len(prepared.Row.EnvNames) == 0 {
		t.Fatal("row.EnvNames is empty, want the resolved environment's names recorded")
	}

	rowBytes, err := os.ReadFile(runs.Store{StateDirectory: stateDir}.RowPath(prepared.Row.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rowBytes), secretEnvironmentValue) {
		t.Fatalf("run.json contains a child environment VALUE:\n%s", rowBytes)
	}
	if !strings.Contains(string(rowBytes), "SOME_SECRET") {
		t.Fatalf("run.json does not contain the environment NAME:\n%s", rowBytes)
	}
}

// TestPrepareWritesReplayOnlyOnTheTabRoute is the required proof (brief §C, tests 2 and 3):
// only mode == ModeTab produces replay.json; foreground and pane routes write none.
func TestPrepareWritesReplayOnlyOnTheTabRoute(t *testing.T) {
	tool := discovery.Tool{Name: "fixture", Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)}}}
	for _, testCase := range []struct {
		name       string
		mode       profile.LaunchMode
		wantReplay bool
	}{
		{"foreground", profile.ModeForeground, false},
		{"pane", profile.ModePane, false},
		{"tab", profile.ModeTab, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stateDir := t.TempDir()
			store := runs.Store{StateDirectory: stateDir}
			prepared, err := Prepare(Request{
				Tool: tool, RepositoryRoot: "/repo", Store: store,
				Environment: os.Environ(), StartedAt: time.Unix(100, 0),
			}, testCase.mode)
			if err != nil {
				t.Fatalf("Prepare returned an error: %v", err)
			}
			_, statErr := os.Stat(store.ReplayPath(prepared.Row.ID))
			gotReplay := statErr == nil
			if gotReplay != testCase.wantReplay {
				t.Fatalf("replay file present = %v, want %v (stat err: %v)", gotReplay, testCase.wantReplay, statErr)
			}
			// Exactly the durable row (and, on the tab route, the replay file) — nothing else.
			entries, err := os.ReadDir(filepath.Dir(store.RowPath(prepared.Row.ID)))
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if testCase.wantReplay {
				wantCount = 2
			}
			if len(entries) != wantCount {
				t.Fatalf("run directory has %d entries, want %d", len(entries), wantCount)
			}
		})
	}
}

// TestRunWrapperDeletesTheReplayFileAfterReadingIt is the required delete-on-read proof (brief
// §C, test 2): the file is present right after Prepare, and gone after RunWrapper has run,
// while the durable row remains.
func TestRunWrapperDeletesTheReplayFileAfterReadingIt(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: t.TempDir()}
	tool := discovery.Tool{Name: "fixture", Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{
		Command: testfixture.Tool(t), Args: []string{"fixture:exit", "0"},
	}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Store: store,
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeTab)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if _, statErr := os.Stat(store.ReplayPath(prepared.Row.ID)); statErr != nil {
		t.Fatalf("replay file missing right after Prepare: %v", statErr)
	}
	if exitCode, err := RunWrapper(store.RowPath(prepared.Row.ID), func() time.Time { return time.Unix(200, 0) }); err != nil || exitCode != 0 {
		t.Fatalf("RunWrapper = (%d, %v), want (0, nil)", exitCode, err)
	}
	if _, statErr := os.Stat(store.ReplayPath(prepared.Row.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replay file still present after RunWrapper (stat err: %v)", statErr)
	}
	if _, statErr := os.Stat(store.RowPath(prepared.Row.ID)); statErr != nil {
		t.Fatalf("durable row missing after RunWrapper: %v", statErr)
	}
}

// TestRunWrapperAbortsWhenTheReplayFileIsMissing is the required proof (brief §C, test 4): a
// row with no replay file (already consumed, or a non-tab row handed to the wrapper by
// mistake) is refused with a structured abort recorded on the durable row — never a launch off
// the redacted row.
func TestRunWrapperAbortsWhenTheReplayFileIsMissing(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("fixture", profile.ModeTab, "/repo", []string{"<redacted:apiKey>"}, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	// No WriteReplay call — the replay file never existed.
	exitCode, err := RunWrapper(store.RowPath(row.ID), func() time.Time { return time.Unix(200, 0) })
	if err == nil {
		t.Fatal("RunWrapper with no replay file should return an error")
	}
	if exitCode == 0 {
		t.Fatalf("RunWrapper exit code = %d, want non-zero", exitCode)
	}
	completed, loadErr := store.Load(row.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if completed.Outcome != "aborted" {
		t.Fatalf("completed row outcome = %q, want %q", completed.Outcome, "aborted")
	}
	if completed.Note == "" {
		t.Fatal("aborted row has no note explaining why")
	}
}

// TestPrepareResolvesEvidenceDirOnceOntoTheRowAndKeepsEnvNamesOnly proves the orchestrator
// ruling 2026-09-23 (inside founder ruling 2): outputs.evidenceDir is resolved once, at launch,
// into its own non-secret row field, so the deck's evidence-directory fallback keeps working
// without the durable row's environment carrying any value.
func TestPrepareResolvesEvidenceDirOnceOntoTheRowAndKeepsEnvNamesOnly(t *testing.T) {
	stateDir := t.TempDir()
	evidenceDirectoryKey := "FIXTURE_EVIDENCE_DIR"
	tool := discovery.Tool{Name: "fixture", Manifest: formats.ToolManifest{
		Launch:  formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
		Outputs: &formats.ToolManifestOutputs{EvidenceDir: &evidenceDirectoryKey},
	}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: stateDir},
		Environment: append(os.Environ(), "FIXTURE_EVIDENCE_DIR=evidence/run-1"), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	wantEvidenceDir := filepath.Join(prepared.Row.Cwd, "evidence", "run-1")
	if prepared.Row.EvidenceDir != wantEvidenceDir {
		t.Fatalf("row.EvidenceDir = %q, want %q (relative path made absolute against cwd)", prepared.Row.EvidenceDir, wantEvidenceDir)
	}
	rowBytes, err := os.ReadFile(runs.Store{StateDirectory: stateDir}.RowPath(prepared.Row.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rowBytes), "\"env\"") {
		t.Fatalf("run.json still has an env field carrying values:\n%s", rowBytes)
	}
}

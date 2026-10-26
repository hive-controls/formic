package launch

import (
	"encoding/json"
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

// missionSensitiveFixtureTool mirrors sensitiveInputFixtureTool (redaction_test.go) for a
// mission run: one required, sensitive input "apiKey" substituted into launch.args.
func missionSensitiveFixtureTool(t *testing.T) discovery.Tool {
	return discovery.Tool{
		Name: "healer",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:exit", "0", "--key", "{apiKey}"}},
			Inputs: []formats.Input{{Name: "apiKey", Required: true, Sensitive: true}},
		},
	}
}

// TestRunWrapperWritesMissionRecordForATabRouteMissionRun is the required proof (car 10b E1,
// test 1): a tab/split-route mission run — driven through RunWrapper against a hand-authored
// row plus the replay file Prepare itself wrote, mirroring
// TestRunWrapperDeletesTheReplayFileAfterReadingIt — leaves mission-run.json in the evidence
// folder, with the redacted inputs, once RunWrapper's own Finish call completes the row.
func TestRunWrapperWritesMissionRecordForATabRouteMissionRun(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stateDir, evidenceRoot := t.TempDir(), t.TempDir()
	store := runs.Store{StateDirectory: stateDir}
	secretValue := "fake-mission-secret-777"
	prepared, err := Prepare(Request{
		Tool: missionSensitiveFixtureTool(t), RepositoryRoot: repositoryRoot, Store: store,
		Inputs: map[string]string{"apiKey": secretValue}, Environment: os.Environ(),
		StartedAt: time.Unix(100, 0), Mission: "demo", EvidenceRoot: evidenceRoot,
	}, profile.ModeTab)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if exitCode, err := RunWrapper(store.RowPath(prepared.Row.ID), func() time.Time { return time.Unix(200, 0) }); err != nil || exitCode != 0 {
		t.Fatalf("RunWrapper = (%d, %v), want (0, nil)", exitCode, err)
	}
	recordPath := filepath.Join(evidenceRoot, prepared.Row.ID, "mission-run.json")
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read %s: %v", recordPath, err)
	}
	var record runs.MissionRecord
	if err := json.Unmarshal(recordBytes, &record); err != nil {
		t.Fatalf("decode mission-run.json: %v", err)
	}
	if record.Mission != "demo" || record.Inputs["apiKey"] != "<redacted:apiKey>" {
		t.Fatalf("record = %+v, want mission=demo apiKey redacted", record)
	}
}

// TestFinishWritesTheMissionRecordExactlyOnceForForegroundRoute is the required proof (test 2):
// the same shared Finish a CLI foreground route calls writes mission-run.json exactly once — no
// duplicate write path survives the hoist that removed cmd/formic/mission.go's own direct
// mission.WriteRecord call.
func TestFinishWritesTheMissionRecordExactlyOnceForForegroundRoute(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stateDir, evidenceRoot := t.TempDir(), t.TempDir()
	store := runs.Store{StateDirectory: stateDir}
	prepared, err := Prepare(Request{
		Tool: missionSensitiveFixtureTool(t), RepositoryRoot: repositoryRoot, Store: store,
		Inputs: map[string]string{"apiKey": "fake-secret-888"}, Environment: os.Environ(),
		StartedAt: time.Unix(100, 0), Mission: "demo", EvidenceRoot: evidenceRoot,
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	commandError := prepared.Command.Run()
	if _, err := Finish(store, prepared.Row.ID, commandError, time.Unix(200, 0)); err != nil {
		t.Fatalf("Finish returned an error: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(evidenceRoot, prepared.Row.ID))
	if err != nil {
		t.Fatal(err)
	}
	recordFiles := 0
	for _, entry := range entries {
		if entry.Name() == "mission-run.json" {
			recordFiles++
		}
	}
	if recordFiles != 1 {
		t.Fatalf("evidence folder has %d mission-run.json files, want exactly 1 (entries: %v)", recordFiles, entries)
	}
}

// TestFinishWritesNoMissionRecordForANonMissionRun is the required proof (test 3): a run never
// launched through `formic mission run` (Mission unset) gets no mission-run.json anywhere.
func TestFinishWritesNoMissionRecordForANonMissionRun(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	store := runs.Store{StateDirectory: stateDir}
	tool := discovery.Tool{Name: "fixture", Manifest: formats.ToolManifest{Launch: formats.ToolManifestLaunch{
		Command: testfixture.Tool(t), Args: []string{"fixture:exit", "0"},
	}}}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: repositoryRoot, Store: store,
		Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	commandError := prepared.Command.Run()
	if _, err := Finish(store, prepared.Row.ID, commandError, time.Unix(200, 0)); err != nil {
		t.Fatalf("Finish returned an error: %v", err)
	}
	runDirectory := filepath.Dir(store.RowPath(prepared.Row.ID))
	entries, err := os.ReadDir(runDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "mission-run.json" {
			t.Fatalf("non-mission run's own run directory has a mission-run.json: %v", entries)
		}
	}
}

// TestMissionRecordAndRunRowContainNoSensitiveBytes is the required redaction-bytes proof (test
// 4): neither mission-run.json nor run.json ever holds the real secret value — only the literal
// redaction marker.
func TestMissionRecordAndRunRowContainNoSensitiveBytes(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stateDir, evidenceRoot := t.TempDir(), t.TempDir()
	store := runs.Store{StateDirectory: stateDir}
	secretValue := "fake-bytes-secret-999"
	prepared, err := Prepare(Request{
		Tool: missionSensitiveFixtureTool(t), RepositoryRoot: repositoryRoot, Store: store,
		Inputs: map[string]string{"apiKey": secretValue}, Environment: os.Environ(),
		StartedAt: time.Unix(100, 0), Mission: "demo", EvidenceRoot: evidenceRoot,
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	commandError := prepared.Command.Run()
	if _, err := Finish(store, prepared.Row.ID, commandError, time.Unix(200, 0)); err != nil {
		t.Fatalf("Finish returned an error: %v", err)
	}
	rowBytes, err := os.ReadFile(store.RowPath(prepared.Row.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rowBytes), secretValue) {
		t.Fatalf("run.json contains the real secret value:\n%s", rowBytes)
	}
	recordPath := filepath.Join(evidenceRoot, prepared.Row.ID, "mission-run.json")
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recordBytes), secretValue) {
		t.Fatalf("mission-run.json contains the real secret value:\n%s", recordBytes)
	}
	if !strings.Contains(string(recordBytes), "redacted:apiKey") {
		t.Fatalf("mission-run.json does not contain the redaction marker:\n%s", recordBytes)
	}
}

// TestMissionRecordNamesItsSourceMissionFile is car 10b E0's restore proof: the record's own
// missionFile equals the discovered mission's Path, carried through Request.MissionPath -> Row.
// MissionPath -> MissionRecord.MissionFile.
func TestMissionRecordNamesItsSourceMissionFile(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stateDir, evidenceRoot := t.TempDir(), t.TempDir()
	store := runs.Store{StateDirectory: stateDir}
	const missionFilePath = "/fake/.formic/missions/demo.yaml"
	prepared, err := Prepare(Request{
		Tool: missionSensitiveFixtureTool(t), RepositoryRoot: repositoryRoot, Store: store,
		Inputs: map[string]string{"apiKey": "fake-secret-000"}, Environment: os.Environ(),
		StartedAt: time.Unix(100, 0), Mission: "demo", MissionPath: missionFilePath, EvidenceRoot: evidenceRoot,
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	commandError := prepared.Command.Run()
	if _, err := Finish(store, prepared.Row.ID, commandError, time.Unix(200, 0)); err != nil {
		t.Fatalf("Finish returned an error: %v", err)
	}
	recordPath := filepath.Join(evidenceRoot, prepared.Row.ID, "mission-run.json")
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record runs.MissionRecord
	if err := json.Unmarshal(recordBytes, &record); err != nil {
		t.Fatalf("decode mission-run.json: %v", err)
	}
	if record.MissionFile != missionFilePath {
		t.Fatalf("record.MissionFile = %q, want %q", record.MissionFile, missionFilePath)
	}
}

// TestAbandonWritesAMissionRecordWithAbortedOutcomeWhenTheReplayFileIsMissing is the required
// proof for the abort path (orchestrator ruling item 5): a mission run whose tab/split spawn
// never wrote a replay file still gets its own mission-run.json, recording outcome "aborted" —
// mirroring TestRunWrapperAbortsWhenTheReplayFileIsMissing, but with Mission/EvidenceDir set on
// the hand-authored row so the record-write path is exercised too.
func TestAbandonWritesAMissionRecordWithAbortedOutcomeWhenTheReplayFileIsMissing(t *testing.T) {
	stateDir, evidenceRoot := t.TempDir(), t.TempDir()
	store := runs.Store{StateDirectory: stateDir}
	row, err := store.Begin("healer", profile.ModeTab, "/repo", []string{"<redacted:apiKey>"}, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.Mission = "demo"
	row.EvidenceDir = filepath.Join(evidenceRoot, row.ID)
	row.Inputs = map[string]string{"apiKey": "<redacted:apiKey>"}
	if err := os.MkdirAll(row.EvidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(row); err != nil {
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
	recordPath := filepath.Join(row.EvidenceDir, "mission-run.json")
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read %s: %v", recordPath, err)
	}
	var record runs.MissionRecord
	if err := json.Unmarshal(recordBytes, &record); err != nil {
		t.Fatalf("decode mission-run.json: %v", err)
	}
	if record.Outcome != "aborted" {
		t.Fatalf("record.Outcome = %q, want %q", record.Outcome, "aborted")
	}
}

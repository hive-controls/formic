package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/profile"
)

func TestStateDirectoryUsesPlatformRows(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		environment map[string]string
		want        string
	}{
		{"macOS", "darwin", nil, filepath.Join("/home/test", "Library", "Application Support", "formic")},
		{"Linux XDG", "linux", map[string]string{"XDG_STATE_HOME": "/state"}, filepath.Join("/state", "formic")},
		{"Linux fallback", "linux", nil, filepath.Join("/home/test", ".local", "state", "formic")},
		{"Windows", "windows", map[string]string{"LOCALAPPDATA": `C:\state`}, filepath.Join(`C:\state`, "formic")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			getenv := func(name string) string { return testCase.environment[name] }
			got, err := stateDirectory(testCase.goos, getenv, func() (string, error) { return "/home/test", nil })
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("stateDirectory() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestDefaultStateDirectoryMakesARelativeOverrideAbsolute proves FORMIC_STATE_DIR is
// resolved to an absolute path exactly once, here, against formic's own invocation cwd — not
// left relative for every downstream consumer (RowPath, the wrapper's row-path argument) to
// resolve against whatever directory happens to be current at THAT later point instead.
func TestDefaultStateDirectoryMakesARelativeOverrideAbsolute(t *testing.T) {
	invocationDirectory := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.Chdir(invocationDirectory); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORMIC_STATE_DIR", "relative-state")

	directory, err := DefaultStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(directory) {
		t.Fatalf("DefaultStateDirectory() = %q, want an absolute path", directory)
	}
	want, err := filepath.Abs("relative-state")
	if err != nil {
		t.Fatal(err)
	}
	if directory != want {
		t.Fatalf("DefaultStateDirectory() = %q, want %q (resolved against the invocation cwd)", directory, want)
	}
}

// TestMigrateLegacyStateDirectoryMovesHivedeckHistory proves the pre-rename "hivedeck" state
// directory, run history included, becomes the "formic" one the first time formic resolves it.
func TestMigrateLegacyStateDirectoryMovesHivedeckHistory(t *testing.T) {
	root := t.TempDir()
	legacyRow := filepath.Join(root, "hivedeck", "runs", "run-1", rowFileName)
	if err := os.MkdirAll(filepath.Dir(legacyRow), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyRow, []byte(`{"id":"run-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "formic")

	if err := migrateLegacyStateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "runs", "run-1", rowFileName)); err != nil {
		t.Fatalf("run history not moved to %s: %v", directory, err)
	}
	if _, err := os.Stat(filepath.Join(root, "hivedeck")); !os.IsNotExist(err) {
		t.Fatalf("legacy directory still present after the move: %v", err)
	}
}

// TestMigrateLegacyStateDirectoryMovesRunsIntoASharedFormicDirectory proves the macOS case:
// there the config root and the state root are both ~/Library/Application Support, so a
// "formic" directory already holding catalog.yaml must still receive the legacy run history.
func TestMigrateLegacyStateDirectoryMovesRunsIntoASharedFormicDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "formic")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(directory, "catalog.yaml")
	if err := os.WriteFile(catalog, []byte("sources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyRow := filepath.Join(root, "hivedeck", "runs", "run-1", rowFileName)
	if err := os.MkdirAll(filepath.Dir(legacyRow), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyRow, []byte(`{"id":"run-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyStateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "runs", "run-1", rowFileName)); err != nil {
		t.Fatalf("run history not moved into the shared directory: %v", err)
	}
	if _, err := os.Stat(catalog); err != nil {
		t.Fatalf("the shared directory's catalog was disturbed: %v", err)
	}
}

// TestMigrateLegacyStateDirectoryLeavesExistingFormicRunsAlone proves the move happens once:
// when formic already has a runs directory, a leftover legacy one is never merged into it or
// removed.
func TestMigrateLegacyStateDirectoryLeavesExistingFormicRunsAlone(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "formic")
	legacyDirectory := filepath.Join(root, "hivedeck")
	for _, existing := range []string{directory, legacyDirectory} {
		if err := os.MkdirAll(filepath.Join(existing, "runs"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrateLegacyStateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacyDirectory, "runs")); err != nil {
		t.Fatalf("legacy runs were touched although formic's already existed: %v", err)
	}
}

func TestStorePersistsRowsNewestFirst(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	older, err := store.Begin("older", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := store.Begin("newer", profile.ModeTab, "/repo", nil, nil, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != newer.ID || rows[1].ID != older.ID {
		t.Fatalf("List() = %+v, want newer then older", rows)
	}
}

func TestCompleteDerivesOutcomeAndArtifact(t *testing.T) {
	cases := []struct {
		name         string
		exitCode     int
		status       string
		wantOutcome  string
		wantArtifact string
	}{
		{"no status success", 0, "", "ok", ""},
		{"no status failure", 7, "", "failed", ""},
		{"terminal ok overrides failure", 7, `{"event":"ok"}` + "\n", "ok", ""},
		{"terminal failed overrides success", 0, `{"event":"failed"}` + "\n", "failed", ""},
		{"needs human and latest artifact", 0, `{"event":"progress","artifact":"first.html"}` + "\n" + `{"event":"needs-human","artifact":"final.html"}` + "\n", "needs-human", "final.html"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := Store{StateDirectory: t.TempDir()}
			row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			if testCase.status != "" {
				if err := os.WriteFile(store.StatusFile(row.ID), []byte(testCase.status), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			completed, err := store.Complete(row.ID, testCase.exitCode, time.Unix(200, 0))
			if err != nil {
				t.Fatal(err)
			}
			if completed.Outcome != testCase.wantOutcome || completed.Artifact != testCase.wantArtifact {
				t.Fatalf("Complete() = outcome %q artifact %q, want %q %q", completed.Outcome, completed.Artifact, testCase.wantOutcome, testCase.wantArtifact)
			}
		})
	}
}

// TestCompleteNeverLosesARowWhenTheStatusFileCannotBeFullyRead proves the mandatory
// exit-code fallback (§8) survives every way a status file can fail to yield a terminal
// event, or can legitimately grow past the historical 64 KiB scanner limit: none of these
// may ever leave exit code/finish time unset — the row would otherwise sit "running" forever
// even though the process has already exited.
func TestCompleteNeverLosesARowWhenTheStatusFileCannotBeFullyRead(t *testing.T) {
	oversizedButWithinBudget := strings.Repeat("a", 128*1024) // over the old 64 KiB default
	cases := []struct {
		name          string
		writeStatus   func(t *testing.T, path string)
		wantOutcome   string
		wantArtifact  string
		wantNoteEmpty bool
	}{
		{
			name: "unreadable file",
			writeStatus: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"event":"ok"}`+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
			},
			wantOutcome: "failed", // exit-code fallback: the event is unreachable
		},
		{
			// The same unreadable-status class, reachable on every platform: permission
			// bits cannot deny a Windows owner (or root) a read, a directory always can.
			name: "a directory where the status file should be",
			writeStatus: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantOutcome: "failed",
		},
		{
			name: "line over the historical 64 KiB scanner limit",
			writeStatus: func(t *testing.T, path string) {
				line := `{"event":"ok","artifact":"` + oversizedButWithinBudget + `"}` + "\n"
				if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			// The raised scanner buffer means this line is now read successfully — the
			// outcome comes from the event itself, not the exit-code fallback, and no
			// diagnostic note is needed because nothing actually failed.
			wantOutcome:   "ok",
			wantArtifact:  oversizedButWithinBudget,
			wantNoteEmpty: true,
		},
		{
			name: "non-JSON line",
			writeStatus: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("not json at all\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantOutcome:   "failed",
			wantNoteEmpty: true,
		},
		{
			name: "truncated file",
			writeStatus: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"event":"ok`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantOutcome:   "failed",
			wantNoteEmpty: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if os.Geteuid() == 0 && testCase.name == "unreadable file" {
				t.Skip("root ignores file permissions")
			}
			if runtime.GOOS == "windows" && testCase.name == "unreadable file" {
				t.Skip("Windows keeps a read-only bit and nothing else: chmod 000 leaves the file readable")
			}
			store := Store{StateDirectory: t.TempDir()}
			row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			testCase.writeStatus(t, store.StatusFile(row.ID))
			completed, err := store.Complete(row.ID, 7, time.Unix(200, 0))
			if err != nil {
				t.Fatal(err)
			}
			if completed.ExitCode == nil || *completed.ExitCode != 7 || completed.FinishedAt == nil {
				t.Fatalf("Complete() = %+v, want exit code 7 and a finish time recorded regardless", completed)
			}
			if completed.Outcome != testCase.wantOutcome || completed.Artifact != testCase.wantArtifact {
				t.Fatalf("Complete() outcome/artifact = %q/%q, want %q/%q", completed.Outcome, completed.Artifact, testCase.wantOutcome, testCase.wantArtifact)
			}
			if testCase.wantNoteEmpty && completed.Note != "" {
				t.Fatalf("Complete() note = %q, want empty", completed.Note)
			}
			if !testCase.wantNoteEmpty && completed.Note == "" {
				t.Fatal("Complete() note is empty, want a diagnostic naming the read/scan failure")
			}
		})
	}
}

// TestAbortNeverInventsAnExitCodeOrClaimsTheToolFailed covers a launch surface that never
// actually started the tool (a failed tab-route spawn): Abort must leave ExitCode nil — the
// tool never produced one — and use an outcome distinct from both "ok" and "failed", which
// would otherwise read as though the TOOL itself finished.
func TestAbortNeverInventsAnExitCodeOrClaimsTheToolFailed(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeTab, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	aborted, err := store.Abort(row.ID, "could not open tmux tab: executable not found", time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if aborted.ExitCode != nil {
		t.Fatalf("Abort() ExitCode = %v, want nil — the tool never ran, so it never produced one", aborted.ExitCode)
	}
	if aborted.Outcome == "ok" || aborted.Outcome == "failed" || aborted.Outcome == "running" {
		t.Fatalf("Abort() Outcome = %q, want a value distinct from ok/failed/running", aborted.Outcome)
	}
	if aborted.Note == "" || aborted.FinishedAt == nil {
		t.Fatalf("Abort() = %+v, want a Note explaining why and a finish time", aborted)
	}
}

// TestRecoverableForegroundRowsSelectsOnlyAbandonedForegroundRunningRows is the pure half of
// recovery: given rows already loaded and a liveness check, it must recover a foreground row
// stuck at "running" whose supervising process is gone, and leave every other row alone — a
// tab row (whose real supervisor is the `hivedeck run` wrapper, not the process that recorded
// the row — see runs.Row.SupervisorPID), a pane row, a live process, a row with no recorded
// pid at all (an old row from before this field existed), and a row that already finished.
func TestRecoverableForegroundRowsSelectsOnlyAbandonedForegroundRunningRows(t *testing.T) {
	alive := func(pid int, startedAt string) bool { return pid == 111 }
	rows := []Row{
		{ID: "abandoned", Mode: profile.ModeForeground, Outcome: "running", SupervisorPID: 222},
		{ID: "live", Mode: profile.ModeForeground, Outcome: "running", SupervisorPID: 111},
		{ID: "tab", Mode: profile.ModeTab, Outcome: "running", SupervisorPID: 222},
		{ID: "pane", Mode: profile.ModePane, Outcome: "running", SupervisorPID: 222},
		{ID: "no-pid", Mode: profile.ModeForeground, Outcome: "running", SupervisorPID: 0},
		{ID: "finished", Mode: profile.ModeForeground, Outcome: "ok", SupervisorPID: 222},
	}
	got := recoverableForegroundRows(rows, alive)
	if len(got) != 1 || got[0].ID != "abandoned" {
		t.Fatalf("recoverableForegroundRows() = %+v, want only the abandoned foreground row", got)
	}
}

// TestRecoverableForegroundRowsTreatsAPidReuseMismatchAsAbandoned is the pid-reuse regression
// test: a row whose SupervisorPID names a process that IS alive right now, but under a
// different start token than the one recorded at launch, must be recovered — the pid was
// recycled onto a different process, so the row's real supervisor is still gone.
func TestRecoverableForegroundRowsTreatsAPidReuseMismatchAsAbandoned(t *testing.T) {
	alive := func(pid int, startedAt string) bool { return pid == 111 && startedAt == "original-token" }
	rows := []Row{
		{ID: "reused", Mode: profile.ModeForeground, Outcome: "running", SupervisorPID: 111, SupervisorStartedAt: "stale-token"},
		{ID: "live", Mode: profile.ModeForeground, Outcome: "running", SupervisorPID: 111, SupervisorStartedAt: "original-token"},
	}
	got := recoverableForegroundRows(rows, alive)
	if len(got) != 1 || got[0].ID != "reused" {
		t.Fatalf("recoverableForegroundRows() = %+v, want only the pid-reused row recovered", got)
	}
}

// TestRecoverAbandonedForegroundRunsMarksAndPersistsWithoutInventingAnExitCode is the Store
// half: a real abandoned row on disk is recovered honestly — no exit code the tool never
// produced, an outcome distinct from ok/failed/running, and a Note naming why — and the change
// actually reaches disk.
func TestRecoverAbandonedForegroundRunsMarksAndPersistsWithoutInventingAnExitCode(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.SupervisorPID = 999999 // a pid this test's fixed `alive` below reports as gone
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}

	recovered, err := store.recoverAbandonedForegroundRunsWithLiveness(func(int, string) bool { return false }, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ID != row.ID {
		t.Fatalf("recovered = %+v, want the one abandoned row", recovered)
	}
	if recovered[0].ExitCode != nil {
		t.Fatalf("recovered row ExitCode = %v, want nil", recovered[0].ExitCode)
	}
	if recovered[0].Outcome == "ok" || recovered[0].Outcome == "failed" || recovered[0].Outcome == "running" {
		t.Fatalf("recovered row Outcome = %q, want a value distinct from ok/failed/running", recovered[0].Outcome)
	}
	onDisk, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Outcome == "running" || onDisk.Note == "" {
		t.Fatalf("row on disk = %+v, want the recovered outcome and a Note persisted", onDisk)
	}
}

// TestCompletePersistsExitCodeToDiskBeforeReadingTheStatusFile proves the exit code and finish
// time reach DISK — a real, separate store.Save — before the status file is ever opened, not
// merely assigned in memory before a later single write. openStatusFile is monkey-patched to
// panic, modeling "a status read that panics/throws"; the row is then read directly from disk
// (bypassing Complete's own, now-panicked, return value) to confirm the first save already
// landed.
func TestCompletePersistsExitCodeToDiskBeforeReadingTheStatusFile(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}

	original := openStatusFile
	openStatusFile = func(string) (*os.File, error) { panic("simulated status read failure") }
	defer func() { openStatusFile = original }()

	func() {
		defer func() { _ = recover() }()
		_, _ = store.Complete(row.ID, 7, time.Unix(200, 0))
	}()

	onDisk, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.ExitCode == nil || *onDisk.ExitCode != 7 || onDisk.FinishedAt == nil {
		t.Fatalf("row on disk = %+v, want exit code 7 and a finish time — persisted BEFORE the status read panicked", onDisk)
	}
}

// TestPublishRenameSurvivesAConcurrentReaderHoldingTheDestinationOpenOnWindows is a
// forced-ordering (not load-dependent) reproduction of a Windows rename race: Go's os.Open opens
// without FILE_SHARE_DELETE, so a reader (store.Load/List, via os.ReadFile) that has the
// destination open at the moment of publish makes MoveFileEx fail with ERROR_ACCESS_DENIED
// ("Access is denied.") — exactly the surface error the gate observed. The reader's handle is
// held open for the first attempt (forcing a real failure) and closed shortly after on another
// goroutine, mirroring the real reader's short-lived os.ReadFile hold: this fails 100% of the
// time without the retry in publishRename, and passes 100% of the time with it.
func TestPublishRenameSurvivesAConcurrentReaderHoldingTheDestinationOpenOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("publishRename's retry path is Windows-only; os.Rename never fails this way elsewhere")
	}
	directory := t.TempDir()
	destination := filepath.Join(directory, "run.json")
	if err := os.WriteFile(destination, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	temporary := destination + ".tmp"
	if err := os.WriteFile(temporary, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}

	reader, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		time.Sleep(12 * time.Millisecond)
		reader.Close()
		close(closed)
	}()

	if err := publishRename(temporary, destination); err != nil {
		t.Fatalf("publishRename() = %v, want it to retry past the reader's open handle", err)
	}
	<-closed

	published, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("destination content = %q, want %q", published, "replacement")
	}
}

func TestRowWithoutActionKeyLoadsUnchangedAndActionRoundTrips(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	oldRow := `{"id":"20260101T000000Z-aaaa","tool":"checks","mode":"foreground","cwd":"/x","startedAt":"2026-01-01T00:00:00Z","outcome":"ok","exitCode":0}`
	rowDirectory := filepath.Dir(store.RowPath("20260101T000000Z-aaaa"))
	if err := os.MkdirAll(rowDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.RowPath("20260101T000000Z-aaaa"), []byte(oldRow), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("20260101T000000Z-aaaa")
	if err != nil {
		t.Fatalf("a pre-actions row must load: %v", err)
	}
	if loaded.Action != "" || loaded.Tool != "checks" {
		t.Fatalf("old row loaded as %+v, want Action empty", loaded)
	}
	loaded.Action = "restart"
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load(loaded.ID)
	if err != nil || again.Action != "restart" {
		t.Fatalf("Action did not round-trip: %+v, %v", again, err)
	}
	plain, _ := json.Marshal(Row{ID: "x", Tool: "t"})
	if strings.Contains(string(plain), `"action"`) {
		t.Fatalf("an empty Action must be omitted: %s", plain)
	}
}

func TestRecoverableForegroundRowsIncludesCapturedRows(t *testing.T) {
	dead := func(int, string) bool { return false }
	rows := []Row{{ID: "captured", Mode: profile.ModeCaptured, Outcome: "running", SupervisorPID: 222}}
	if got := recoverableForegroundRows(rows, dead); len(got) != 1 {
		t.Fatalf("a captured row with a dead supervisor must be recovered, got %+v", got)
	}
}

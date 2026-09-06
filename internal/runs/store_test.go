package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/hivedeck/internal/profile"
)

func TestStateDirectoryUsesPlatformRows(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		environment map[string]string
		want        string
	}{
		{"macOS", "darwin", nil, filepath.Join("/home/test", "Library", "Application Support", "hivedeck")},
		{"Linux XDG", "linux", map[string]string{"XDG_STATE_HOME": "/state"}, filepath.Join("/state", "hivedeck")},
		{"Linux fallback", "linux", nil, filepath.Join("/home/test", ".local", "state", "hivedeck")},
		{"Windows", "windows", map[string]string{"LOCALAPPDATA": `C:\state`}, filepath.Join(`C:\state`, "hivedeck")},
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

// TestDefaultStateDirectoryMakesARelativeOverrideAbsolute proves HIVEDECK_STATE_DIR is
// resolved to an absolute path exactly once, here, against hivedeck's own invocation cwd — not
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
	t.Setenv("HIVEDECK_STATE_DIR", "relative-state")

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

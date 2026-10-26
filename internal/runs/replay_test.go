package runs

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/profile"
)

// TestWriteReplayThenReadAndDeleteReplayRoundTripsTheRealValues proves the ephemeral file
// carries the real argv/cwd/env the tab-route wrapper needs, and that ReadAndDeleteReplay
// deletes it on a successful read (delete-on-read, N10's shortest-lifetime protection).
func TestWriteReplayThenReadAndDeleteReplayRoundTripsTheRealValues(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("fixture", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"tool", "--flag", "fake-secret-value"}
	wantCwd := "/repo/tool"
	wantEnv := []string{"PATH=/usr/bin", "API_KEY=fake-secret-value"}
	if err := store.WriteReplay(row.ID, wantArgv, wantCwd, wantEnv); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(store.ReplayPath(row.ID)); statErr != nil {
		t.Fatalf("replay file missing after WriteReplay: %v", statErr)
	}

	gotArgv, gotCwd, gotEnv, err := store.ReadAndDeleteReplay(row.ID)
	if err != nil {
		t.Fatalf("ReadAndDeleteReplay returned an error: %v", err)
	}
	if len(gotArgv) != len(wantArgv) || gotArgv[2] != wantArgv[2] {
		t.Fatalf("ReadAndDeleteReplay argv = %v, want %v", gotArgv, wantArgv)
	}
	if gotCwd != wantCwd {
		t.Fatalf("ReadAndDeleteReplay cwd = %q, want %q", gotCwd, wantCwd)
	}
	if len(gotEnv) != len(wantEnv) || gotEnv[1] != wantEnv[1] {
		t.Fatalf("ReadAndDeleteReplay env = %v, want %v", gotEnv, wantEnv)
	}
	if _, statErr := os.Stat(store.ReplayPath(row.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replay file still present after ReadAndDeleteReplay (stat err: %v)", statErr)
	}
}

// TestReadAndDeleteReplayReportsAMissingFileDistinctly proves the wrapper can tell "never
// written / already consumed" apart from any other failure.
func TestReadAndDeleteReplayReportsAMissingFileDistinctly(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("fixture", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ReadAndDeleteReplay(row.ID)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadAndDeleteReplay error = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

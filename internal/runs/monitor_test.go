package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/profile"
)

func TestMonitorReportsOnlyCompleteEventsAndKeepsTheLastArtifact(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.ndjson")
	monitor := NewMonitor(statusPath)

	if _, changed := monitor.Poll(); changed {
		t.Fatal("a run whose tool has written nothing reported a change")
	}

	appendStatus(t, statusPath, `{"event":"started"}`+"\n")
	update, changed := monitor.Poll()
	if !changed || update.State != "started" {
		t.Fatalf("update = %+v, changed = %v; want started", update, changed)
	}

	// A half-written event: the poll lands mid-append, and must see nothing rather than a
	// truncated object.
	appendStatus(t, statusPath, `{"event":"progress","artif`)
	if _, changed := monitor.Poll(); changed {
		t.Fatal("a half-written event was reported before its newline arrived")
	}
	appendStatus(t, statusPath, `act":"evidence.html"}`+"\n")
	update, changed = monitor.Poll()
	if !changed || update.State != "progress" || update.Artifact != "evidence.html" {
		t.Fatalf("update = %+v, changed = %v; want progress with the artifact", update, changed)
	}

	// A later event with no artifact of its own must not erase the one already reported.
	appendStatus(t, statusPath, `{"event":"ok"}`+"\n")
	update, changed = monitor.Poll()
	if !changed || update.State != "ok" || update.Artifact != "evidence.html" {
		t.Fatalf("update = %+v; want ok with the artifact still reported", update)
	}

	if _, changed := monitor.Poll(); changed {
		t.Fatal("a poll with nothing appended reported a change")
	}
}

func TestMonitorSkipsALineThatIsNotAnEvent(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.ndjson")
	monitor := NewMonitor(statusPath)
	appendStatus(t, statusPath, "not json at all\n"+`{"event":"started"}`+"\n")
	update, changed := monitor.Poll()
	if !changed || update.State != "started" {
		t.Fatalf("update = %+v, changed = %v — a stray line must not stop the watch", update, changed)
	}
}

func TestUpdateLiveMovesARunningRowAndNeverAFinishedOne(t *testing.T) {
	store := Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("checks", profile.ModeTab, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.UpdateLive(row.ID, Update{State: "progress", Artifact: "evidence.html"})
	if err != nil || live.Outcome != "progress" || live.Artifact != "evidence.html" {
		t.Fatalf("live row = %+v, error = %v", live, err)
	}

	if _, err := store.Complete(row.ID, 1, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	// A status line that lands after the run has been completed must not reopen the row: the
	// exit code decides the final outcome, once.
	after, err := store.UpdateLive(row.ID, Update{State: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Outcome != "failed" {
		t.Fatalf("finished row outcome = %q, want the completed outcome to stand", after.Outcome)
	}
	reloaded, err := store.Load(row.ID)
	if err != nil || reloaded.Outcome != "failed" {
		t.Fatalf("row on disk = %+v, error = %v", reloaded, err)
	}
}

func appendStatus(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

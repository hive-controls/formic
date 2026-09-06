package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
)

func TestVersionCommandPrintsAndExitsZero(t *testing.T) {
	originalStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd

	exitCode := run([]string{"version"})

	writeEnd.Close()
	os.Stdout = originalStdout
	var captured bytes.Buffer
	if _, err := io.Copy(&captured, readEnd); err != nil {
		t.Fatal(err)
	}

	if exitCode != 0 {
		t.Fatalf("version command exit = %d, want 0", exitCode)
	}
	if !strings.HasPrefix(captured.String(), "hivedeck ") {
		t.Fatalf("version output = %q, want prefix %q", captured.String(), "hivedeck ")
	}
}

func TestRunWrapperCompletesThePersistedRow(t *testing.T) {
	stateDirectory := t.TempDir()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDirectory}
	script := filepath.Join(repositoryRoot, "testdata", "foreground.sh")
	argv := []string{"/bin/sh", script, "0", "ok"}
	row, err := store.Begin("fixture", profile.ModeTab, repositoryRoot, argv, os.Environ(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	// The wrapper is invoked with ONLY the run-row path — no run id, no argv, no `--`, and
	// no environment on the command line — reconstructing everything from the row itself.
	exitCode := run([]string{"run", store.RowPath(row.ID)})
	if exitCode != 0 {
		t.Fatalf("run wrapper exit = %d, want 0", exitCode)
	}
	completed, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ExitCode == nil || *completed.ExitCode != 0 || completed.Outcome != "ok" {
		t.Fatalf("completed row = %+v", completed)
	}
}

func TestRunWrapperRejectsAMissingRowPath(t *testing.T) {
	if exitCode := run([]string{"run", filepath.Join(t.TempDir(), "missing", "run.json")}); exitCode != 1 {
		t.Fatalf("missing row path exit = %d, want 1", exitCode)
	}
}

func TestRunWrapperRejectsTheWrongArgumentCount(t *testing.T) {
	if exitCode := run([]string{"run"}); exitCode != 2 {
		t.Fatalf("missing argument exit = %d, want 2", exitCode)
	}
	if exitCode := run([]string{"run", "a", "b"}); exitCode != 2 {
		t.Fatalf("extra argument exit = %d, want 2", exitCode)
	}
}

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
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
	if !strings.HasPrefix(captured.String(), "formic ") {
		t.Fatalf("version output = %q, want prefix %q", captured.String(), "formic ")
	}
}

// TestLoadCatalogStartsOutsideAnyRepositoryWithAnEmptyArmory proves launching from anywhere
// (§17 car 6): repository.FindRoot failing to find a `.git` marker above the working
// directory is no longer a startup precondition. Started in a directory with no repository
// above it, loadCatalog returns no error and an empty armory rather than refusing to start.
func TestLoadCatalogStartsOutsideAnyRepositoryWithAnEmptyArmory(t *testing.T) {
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalWorkingDirectory); err != nil {
			t.Fatal(err)
		}
	}()
	// Never let this test read or write the developer's real %APPDATA%\formic: an empty
	// scratch config directory is the only source of catalogued sources here.
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())

	repositoryRoot, catalog, err := loadCatalog()
	if err != nil {
		t.Fatalf("loadCatalog outside any repository returned an error: %v", err)
	}
	if len(catalog.Tools) != 0 {
		t.Fatalf("catalog.Tools = %d, want 0 (nothing catalogued outside a repository)", len(catalog.Tools))
	}
	resolvedTemporaryDirectory, err := filepath.EvalSymlinks(temporaryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRepositoryRoot, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedRepositoryRoot != resolvedTemporaryDirectory {
		t.Fatalf("repositoryRoot = %q, want the working directory %q", resolvedRepositoryRoot, resolvedTemporaryDirectory)
	}
}

// TestLoadCatalogIsEmptyEvenInsideAToolkitRepoThatWasNeverAdded proves the admission
// rule's core claim (§12, step C proof 4): even standing inside a repository that carries
// its own valid `.formic/toolkit.yaml` (this repo, cutover in this same commit), the
// armory stays empty when the catalog is empty — nothing appears by accident, not even the
// repo formic itself is standing in.
func TestLoadCatalogIsEmptyEvenInsideAToolkitRepoThatWasNeverAdded(t *testing.T) {
	// Never let this test read or write the developer's real %APPDATA%\formic.
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())

	_, catalog, err := loadCatalog()
	if err != nil {
		t.Fatalf("loadCatalog inside this repository returned an error: %v", err)
	}
	if len(catalog.Tools) != 0 {
		t.Fatalf("catalog.Tools = %d, want 0 (this repo was never `formic add`-ed)", len(catalog.Tools))
	}
}

// TestRunAddCataloguesAValidSourceThenDiscoverSeesIt is the end-to-end wiring proof for
// `formic add` (§12, §15): adding a source with a valid toolkit descriptor makes its tools
// reachable through the same loadCatalog path the deck and doctor use.
func TestRunAddCataloguesAValidSourceThenDiscoverSeesIt(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "greet.yaml"), []byte("formic: 1\nname: greet\nlaunch:\n  command: echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("run add exit = %d, want 0", exitCode)
	}

	_, catalog, err := loadCatalog()
	if err != nil {
		t.Fatalf("loadCatalog after add returned an error: %v", err)
	}
	found := false
	for _, tool := range catalog.Tools {
		if tool.Name == "greet" {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog.Tools = %+v, want the newly catalogued source's greet tool", catalog.Tools)
	}
}

// TestRunAddRefusesAMissingDescriptor proves the CLI-level refusal path — the same
// admission gate `catalog.Add` enforces (proof 3), reachable through `formic add`.
func TestRunAddRefusesAMissingDescriptor(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	if exitCode := run([]string{"add", t.TempDir()}); exitCode == 0 {
		t.Fatal("run add of a path with no toolkit descriptor should refuse")
	}
}

func TestRunWrapperCompletesThePersistedRow(t *testing.T) {
	stateDirectory := t.TempDir()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	store := runs.Store{StateDirectory: stateDirectory}
	argv := []string{testfixture.Tool(t), "fixture:status", "0", "ok"}
	row, err := store.Begin("fixture", profile.ModeTab, repositoryRoot, argv, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	// A real launch puts the status-file variable on the environment and writes it into the
	// ephemeral replay file (N10) — the row's own Argv/Env never carry real values; the
	// wrapper reads the replay file, not the row, to reconstruct the child.
	environment := append(os.Environ(), "FORMIC_STATUS_FILE="+store.StatusFile(row.ID))
	if err := store.WriteReplay(row.ID, argv, repositoryRoot, environment); err != nil {
		t.Fatal(err)
	}
	// The wrapper is invoked with ONLY the run-row path — no run id, no argv, no `--`, and
	// no environment on the command line — reconstructing everything from the row itself.
	exitCode := run([]string{"__replay-row", store.RowPath(row.ID)})
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
	if exitCode := run([]string{"__replay-row", filepath.Join(t.TempDir(), "missing", "run.json")}); exitCode != 1 {
		t.Fatalf("missing row path exit = %d, want 1", exitCode)
	}
}

func TestRunWrapperRejectsTheWrongArgumentCount(t *testing.T) {
	if exitCode := run([]string{"__replay-row"}); exitCode != 2 {
		t.Fatalf("missing argument exit = %d, want 2", exitCode)
	}
	if exitCode := run([]string{"__replay-row", "a", "b"}); exitCode != 2 {
		t.Fatalf("extra argument exit = %d, want 2", exitCode)
	}
}

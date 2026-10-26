package discovery

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/repository"
)

// This package's own fixture (testdata/discovery/source-one/products/sample.formic.yaml)
// must NOT be part of the exact set below: the fixture directory passed in IS the walk's
// root (repositoryRoot), so "testdata" never appears as a descendant directory name here —
// TestDiscoverProductManifestsSkipsExcludedDirectories proves the exclusion itself, with a
// tree where "testdata" genuinely is a descendant.
func TestDiscoverProductManifestsFindsFixtureManifest(t *testing.T) {
	fixtureRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-one"))
	tools, err := discoverProductManifests(fixtureRoot)
	if err != nil {
		t.Fatalf("discoverProductManifests returned an error: %v", err)
	}
	assertExactProductNames(t, tools, "sample-product")
}

// A vendored/build/state directory is not tracked by this repository's own .gitignore for
// every excluded name (node_modules is; dist/build/vendor are not), so this proves the
// exclusion with runtime-generated files rather than testdata that could silently fail to
// ship. "testdata" is included here as a genuine descendant directory, proving the walk
// never discovers this package's own shipped test fixture (or any other) as a real tool.
func TestDiscoverProductManifestsSkipsExcludedDirectories(t *testing.T) {
	repositoryRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(repositoryRoot, "tools", "kept.formic.yaml"), `
formic: 1
name: kept-product
launch:
  command: kept-cmd
`)
	for _, excludedDirectory := range []string{"node_modules", "dist", "build", "vendor", ".state", "testdata"} {
		writeFixtureFile(t, filepath.Join(repositoryRoot, excludedDirectory, "excluded.formic.yaml"), `
formic: 1
name: should-never-appear
launch:
  command: excluded-cmd
`)
	}

	tools, err := discoverProductManifests(repositoryRoot)
	if err != nil {
		t.Fatalf("discoverProductManifests returned an error: %v", err)
	}
	assertExactProductNames(t, tools, "kept-product")
}

// TestDiscoverProductManifestsRejectsTheOldToolspecSuffix proves the hard cutover (§17 car 4):
// a `*.toolspec.yaml` file — the pre-cutover suffix — is never discovered, even though its
// content is otherwise a valid manifest. Only `*.formic.yaml` is a shipped product manifest now.
func TestDiscoverProductManifestsRejectsTheOldToolspecSuffix(t *testing.T) {
	repositoryRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(repositoryRoot, "tools", "old-suffix.toolspec.yaml"), `
formic: 1
name: old-suffix
launch:
  command: old-cmd
`)
	writeFixtureFile(t, filepath.Join(repositoryRoot, "tools", "new-suffix.formic.yaml"), `
formic: 1
name: new-suffix
launch:
  command: new-cmd
`)

	tools, err := discoverProductManifests(repositoryRoot)
	if err != nil {
		t.Fatalf("discoverProductManifests returned an error: %v", err)
	}
	assertExactProductNames(t, tools, "new-suffix")
}

// assertExactProductNames fails unless tools carries exactly the given names (order-
// independent) — a "contains" check alone would miss an unwanted extra tool such as a
// fixture that should have been excluded.
func assertExactProductNames(t *testing.T, tools []Tool, wantNames ...string) {
	t.Helper()
	gotNames := make([]string, len(tools))
	for index, tool := range tools {
		gotNames[index] = tool.Name
		if tool.Source.Kind != SourceProduct {
			t.Fatalf("tool %q source = %q, want %q", tool.Name, tool.Source.Kind, SourceProduct)
		}
	}
	sort.Strings(gotNames)
	sortedWantNames := append([]string(nil), wantNames...)
	sort.Strings(sortedWantNames)
	if !slices.Equal(gotNames, sortedWantNames) {
		t.Fatalf("discovered product tool names = %v, want exactly %v", gotNames, sortedWantNames)
	}
}

// liveDevloopToolkitRoot returns the workshop's devloop-tools toolkit directory, which is
// where E2E Doctor's manifest is declared (usecases/devloop-tools/.formic/tools/e2e-doctor.yaml).
// That toolkit lives in the private workshop monorepo alongside hivedeck but is NOT part of
// hivedeck's own export (see export/hivedeck/manifest.txt) — a standalone consumer's
// repository, including hivedeck's own exported public copy, has no reason to contain it.
// Skip rather than fail when it is absent, so this package's own `go test ./...` stays
// portable while still proving the live-discovery path inside the workshop where it is
// authored. The declared manifest is a stable gate for "am I inside the private workshop".
func liveDevloopToolkitRoot(t *testing.T) string {
	t.Helper()
	liveRoot, err := repository.FindRoot(".")
	if err != nil {
		t.Fatalf("find live repository root: %v", err)
	}
	toolkitRoot := filepath.Join(liveRoot, "usecases", "devloop-tools")
	declaredManifest := filepath.Join(toolkitRoot, ".formic", "tools", "e2e-doctor.yaml")
	if _, err := os.Stat(declaredManifest); os.IsNotExist(err) {
		t.Skip("usecases/devloop-tools/.formic/tools/e2e-doctor.yaml is not present in this repository (expected outside the private workshop)")
	}
	return toolkitRoot
}

// liveE2EDoctorTool loads E2E Doctor through the declared-manifest path, exactly as a
// catalogued devloop-tools source contributes it.
func liveE2EDoctorTool(t *testing.T) Tool {
	t.Helper()
	toolkitRoot := liveDevloopToolkitRoot(t)
	tools, err := discoverSourceTools(toolkitRoot)
	if err != nil {
		t.Fatalf("discoverSourceTools on the devloop-tools toolkit returned an error: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == "e2e-doctor" {
			if tool.Source.Kind != SourceCatalog {
				t.Fatalf("e2e-doctor source kind = %q, want %q (a declared .formic/tools manifest)", tool.Source.Kind, SourceCatalog)
			}
			if tool.SourceRoot != toolkitRoot {
				t.Fatalf("e2e-doctor SourceRoot = %q, want %q", tool.SourceRoot, toolkitRoot)
			}
			return tool
		}
	}
	t.Fatal("e2e-doctor was not discovered on the devloop-tools toolkit")
	return Tool{}
}

// TestDiscoverProductManifestsOnLiveRepository asserts the workshop root's own shipped
// product manifests: cleanup-sandboxes is discovered from the root's `**/*.formic.yaml`
// scan, and e2e-doctor is NOT — it is declared by the devloop-tools toolkit now, so the
// root source must not also discover it (it would list twice).
func TestDiscoverProductManifestsOnLiveRepository(t *testing.T) {
	toolkitRoot := liveDevloopToolkitRoot(t)
	liveRoot := filepath.Dir(filepath.Dir(toolkitRoot))
	tools, err := discoverProductManifests(liveRoot)
	if err != nil {
		t.Fatalf("discoverProductManifests on the live repository returned an error: %v", err)
	}
	cleanupSandboxesPresent := false
	for _, tool := range tools {
		switch tool.Name {
		case "cleanup-sandboxes":
			cleanupSandboxesPresent = true
		case "e2e-doctor":
			t.Fatalf("e2e-doctor was discovered as a shipped product manifest at %s; it is declared by devloop-tools only", tool.Source.Path)
		}
	}
	if !cleanupSandboxesPresent {
		t.Fatal("shipped product manifest \"cleanup-sandboxes\" was not discovered on the live repository")
	}
	liveE2EDoctorTool(t)
}

// TestE2EDoctorManifestDeclaresTheLauncherSetStatusFileName is the launcher-side half of
// the FORMIC_STATUS_FILE agreement proof (founder ruling 2026-09-22: E2E Doctor's status-
// file reader cut over from HIVEDECK_STATUS_FILE to FORMIC_STATUS_FILE, no fallback).
// launch.go writes the row's status-file variable under the literal name "FORMIC_STATUS_FILE"
// (internal/launch/launch.go's Prepare); scopeDeclaredEnvironment only lets a declared-env
// manifest's child process see a key it actually declares, so usecases/devloop-tools'
// .formic/tools/e2e-doctor.yaml must declare that exact name or the launcher would silently strip
// it before the child ever sees it. The reader-side half — that E2E Doctor's own env reader
// (process.env.FORMIC_STATUS_FILE in src/cli/e2e-doctor.mts and friends) actually picks up
// a variable under that name — is proven by
// usecases/devloop-tools/e2e-doctor/src/cli/e2e-doctor.test.mts's "the status-file opt-in..." tests, which
// set FORMIC_STATUS_FILE on a real CLI invocation and assert the file is written.
func TestE2EDoctorManifestDeclaresTheLauncherSetStatusFileName(t *testing.T) {
	e2eDoctor := liveE2EDoctorTool(t)
	declaresStatusFile := false
	for _, entry := range e2eDoctor.Manifest.Env {
		if entry.Name == "FORMIC_STATUS_FILE" {
			declaresStatusFile = true
			break
		}
	}
	if !declaresStatusFile {
		t.Fatalf(".formic/tools/e2e-doctor.yaml env[] = %+v, want an entry named FORMIC_STATUS_FILE (the exact name launch.go sets)", e2eDoctor.Manifest.Env)
	}
}

// TestDeclaredToolsRankAboveShippedProductManifestsWithinOneSource proves a source's own
// `.formic/tools/*.yaml` wins a same-name collision over its `**/*.formic.yaml` shipped
// manifests — a hand-authored declaration outranks a generically discovered one within the
// same catalogued source.
func TestDeclaredToolsRankAboveShippedProductManifestsWithinOneSource(t *testing.T) {
	sourcePath := t.TempDir()
	writeFixtureFile(t, filepath.Join(sourcePath, ".formic", "toolkit.yaml"), "toolkit: 1\nname: fixture\n")
	writeFixtureFile(t, filepath.Join(sourcePath, ".formic", "tools", "shared-name.yaml"), `
formic: 1
name: shared-name
launch:
  command: declared-cmd
`)
	writeFixtureFile(t, filepath.Join(sourcePath, "tools", "shared-name.formic.yaml"), `
formic: 1
name: shared-name
launch:
  command: product-cmd
`)

	catalog, err := Discover([]string{sourcePath})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	winner := requireTool(t, catalog, "shared-name")
	if winner.Source.Kind != SourceCatalog {
		t.Fatalf("winner source = %q, want %q", winner.Source.Kind, SourceCatalog)
	}
	if len(winner.Shadowed) != 1 {
		t.Fatalf("shadowed count = %d, want 1 (the product manifest)", len(winner.Shadowed))
	}
	if winner.Shadowed[0].Source.Kind != SourceProduct {
		t.Fatalf("shadow = %q, want %q", winner.Shadowed[0].Source.Kind, SourceProduct)
	}
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestE2EDoctorManifestDeclaresArgumentsAndEnvOptions proves the shipped E2E Doctor manifest
// is the first real consumer of arguments[] and env[].options (§21): it loads clean, declares
// heal's --healer as a choice, and enumerates E2E_DOCTOR_GATE's values.
func TestE2EDoctorManifestDeclaresArgumentsAndEnvOptions(t *testing.T) {
	e2eDoctor := liveE2EDoctorTool(t)
	if e2eDoctor.Problem != "" {
		t.Fatalf(".formic/tools/e2e-doctor.yaml has a problem: %s", e2eDoctor.Problem)
	}
	healerIsChoice := false
	for _, argument := range e2eDoctor.Manifest.Arguments {
		if argument.Flag == "--healer" && argument.Kind == formats.ArgumentKindChoice {
			healerIsChoice = true
		}
	}
	if !healerIsChoice {
		t.Fatalf("arguments = %+v, want a --healer argument of kind choice", e2eDoctor.Manifest.Arguments)
	}
	for _, entry := range e2eDoctor.Manifest.Env {
		if entry.Name == "E2E_DOCTOR_GATE" {
			if len(entry.Options) == 0 {
				t.Fatalf("E2E_DOCTOR_GATE options are empty, want solari, local and cdp")
			}
			return
		}
	}
	t.Fatal(".formic/tools/e2e-doctor.yaml declares no E2E_DOCTOR_GATE env entry")
}

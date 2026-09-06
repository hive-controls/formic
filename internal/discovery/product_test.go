package discovery

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/hive-controls/hivedeck/internal/repository"
)

// This package's own fixture (testdata/discovery/repository/products/sample.toolspec.yaml)
// must NOT be part of the exact set below: the fixture directory passed in IS the walk's
// root (repositoryRoot), so "testdata" never appears as a descendant directory name here —
// TestDiscoverProductManifestsSkipsExcludedDirectories proves the exclusion itself, with a
// tree where "testdata" genuinely is a descendant.
func TestDiscoverProductManifestsFindsFixtureManifest(t *testing.T) {
	fixtureRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "repository"))
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
	writeFixtureFile(t, filepath.Join(repositoryRoot, "tools", "kept.toolspec.yaml"), `
toolspec: 1
name: kept-product
launch:
  command: kept-cmd
`)
	for _, excludedDirectory := range []string{"node_modules", "dist", "build", "vendor", ".state", "testdata"} {
		writeFixtureFile(t, filepath.Join(repositoryRoot, excludedDirectory, "excluded.toolspec.yaml"), `
toolspec: 1
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

// This asserts against packages/harness's shipped manifests, which live in the private
// workshop monorepo alongside hivedeck but are NOT part of hivedeck's own export (see
// export/hivedeck/manifest.txt) — a standalone consumer's repository, including hivedeck's
// own exported public copy, has no reason to contain them. Skip rather than fail when that
// sibling package is absent, so this package's own `go test ./...` stays portable while
// still proving the live-discovery path inside the workshop where it is authored.
func TestDiscoverProductManifestsOnLiveRepository(t *testing.T) {
	liveRoot, err := repository.FindRoot(".")
	if err != nil {
		t.Fatalf("find live repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(liveRoot, "packages", "harness")); os.IsNotExist(err) {
		t.Skip("packages/harness is not present in this repository (expected outside the private workshop)")
	}
	tools, err := discoverProductManifests(liveRoot)
	if err != nil {
		t.Fatalf("discoverProductManifests on the live repository returned an error: %v", err)
	}
	for _, expectedName := range []string{
		"e2e-doctor", "cleanup-sandboxes", "gate-browserstack", "gate-saucelabs", "host-app-in-solari",
	} {
		present := false
		for _, tool := range tools {
			if tool.Name == expectedName {
				present = true
				break
			}
		}
		if !present {
			t.Fatalf("shipped product manifest %q was not discovered on the live repository", expectedName)
		}
	}
}

func TestProductManifestsRankBelowRepoLocalAndUserLibrary(t *testing.T) {
	repositoryRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(repositoryRoot, ".git"), "")
	writeFixtureFile(t, filepath.Join(repositoryRoot, ".hivedeck", "tools", "shared-name.yaml"), `
toolspec: 1
name: shared-name
launch:
  command: repo-local-cmd
`)
	writeFixtureFile(t, filepath.Join(repositoryRoot, "tools", "shared-name.toolspec.yaml"), `
toolspec: 1
name: shared-name
launch:
  command: product-cmd
`)
	userConfigRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(userConfigRoot, "hivedeck", "tools", "shared-name.yaml"), `
toolspec: 1
name: shared-name
launch:
  command: user-library-cmd
`)

	catalog, err := Discover(repositoryRoot, userConfigRoot)
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	winner := requireTool(t, catalog, "shared-name")
	if winner.Source.Kind != SourceLocal {
		t.Fatalf("winner source = %q, want %q", winner.Source.Kind, SourceLocal)
	}
	if len(winner.Shadowed) != 2 {
		t.Fatalf("shadowed count = %d, want user-library and product", len(winner.Shadowed))
	}
	if winner.Shadowed[0].Source.Kind != SourceUser {
		t.Fatalf("first shadow = %q, want %q (scanned before product manifests)", winner.Shadowed[0].Source.Kind, SourceUser)
	}
	if winner.Shadowed[1].Source.Kind != SourceProduct {
		t.Fatalf("second shadow = %q, want %q", winner.Shadowed[1].Source.Kind, SourceProduct)
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

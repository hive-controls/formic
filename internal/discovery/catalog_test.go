package discovery

import (
	"encoding/json"
	"path/filepath"
	"testing"

	toolspec "github.com/hive-controls/toolspec"

	"github.com/hive-controls/hivedeck/internal/repository"
)

func TestDiscoverUsesSourcePrecedenceAndSurfacesCollisions(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "repository"))
	userConfigRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "user-config"))
	catalog, err := Discover(repositoryRoot, userConfigRoot)
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}

	shared := requireTool(t, catalog, "shared")
	if shared.Manifest.Launch.Command != "local-command" {
		t.Fatalf("shared command = %q, want first repo-local hit", shared.Manifest.Launch.Command)
	}
	if shared.Source.Kind != SourceLocal {
		t.Fatalf("shared source = %q, want %q", shared.Source.Kind, SourceLocal)
	}
	if len(shared.Shadowed) != 2 {
		t.Fatalf("shared shadowed definitions = %d, want user and npm", len(shared.Shadowed))
	}
	if shared.Shadowed[0].Source.Kind != SourceUser || shared.Shadowed[1].Source.Kind != SourceNPM {
		t.Fatalf("shadow order = %q then %q, want user then npm", shared.Shadowed[0].Source.Kind, shared.Shadowed[1].Source.Kind)
	}
}

func TestDiscoverKeepsInvalidManifestVisible(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "repository"))
	catalog, err := Discover(repositoryRoot, t.TempDir())
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	invalid := requireTool(t, catalog, "invalid-local")
	if invalid.Problem == "" {
		t.Fatal("invalid manifest should carry its validation problem")
	}
}

func TestNPMScriptsEnumerateFixtureAndLiveRepository(t *testing.T) {
	fixtureRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "repository"))
	fixtureCatalog, err := Discover(fixtureRoot, t.TempDir())
	if err != nil {
		t.Fatalf("discover fixture scripts: %v", err)
	}
	requireTool(t, fixtureCatalog, "root-task")
	workspaceTool := requireTool(t, fixtureCatalog, "web--build")
	if workspaceTool.Title != "web · npm run build" {
		t.Fatalf("workspace title = %q, want workspace-qualified label", workspaceTool.Title)
	}
	if workspaceTool.Problem != "" {
		t.Fatalf("workspace-qualified npm tool failed schema validation: %s", workspaceTool.Problem)
	}
	requireSchemaValidManifest(t, workspaceTool.Manifest)

	liveRoot, err := repository.FindRoot(".")
	if err != nil {
		t.Fatalf("find live repository root: %v", err)
	}
	liveCatalog, err := Discover(liveRoot, t.TempDir())
	if err != nil {
		t.Fatalf("discover live scripts: %v", err)
	}
	livePackage, err := readPackageDocument(filepath.Join(liveRoot, "package.json"))
	if err != nil {
		t.Fatalf("read live package.json: %v", err)
	}
	if len(livePackage.Scripts) == 0 {
		t.Fatal("live package.json should declare at least one script")
	}
	for scriptName := range livePackage.Scripts {
		requireTool(t, liveCatalog, scriptName)
	}
}

func requireTool(t *testing.T, catalog Catalog, name string) Tool {
	t.Helper()
	for _, tool := range catalog.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("catalog does not contain %q", name)
	return Tool{}
}

// requireSchemaValidManifest proves a synthetic manifest goes through the same gate as a
// hand-authored one: it fails red the moment an adapter regresses to an illegal `name`
// (e.g. reintroducing ":" as the workspace/script separator, which the schema's
// ^[a-z0-9][a-z0-9-]*$ pattern forbids).
func requireSchemaValidManifest(t *testing.T, manifest toolspec.ToolManifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest for validation: %v", err)
	}
	if err := toolspec.Validate(data); err != nil {
		t.Fatalf("synthetic manifest is not schema-valid: %v", err)
	}
}

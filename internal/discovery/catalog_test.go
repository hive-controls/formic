package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/repository"
	"gopkg.in/yaml.v3"
)

func TestDiscoverUsesSourcePrecedenceAndSurfacesCollisions(t *testing.T) {
	sourceOne := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-one"))
	sourceTwo := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-two"))
	catalog, err := Discover([]string{sourceOne, sourceTwo})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}

	shared := requireTool(t, catalog, "shared")
	if shared.Manifest.Launch.Command != "local-command" {
		t.Fatalf("shared command = %q, want the first catalogued source's hit", shared.Manifest.Launch.Command)
	}
	if len(shared.Shadowed) != 1 {
		t.Fatalf("shared shadowed definitions = %d, want the second source only", len(shared.Shadowed))
	}
	if shared.Shadowed[0].Command != "user-command" {
		t.Fatalf("shadow command = %q, want %q", shared.Shadowed[0].Command, "user-command")
	}
}

// TestDiscoverExcludesInvalidManifestFromArmory proves the admission rule's part 1 (§12
// item 2): a tool that fails validation never enters the armory (catalog.Tools) — it is
// reported separately, in catalog.Invalid, with its validation problem intact.
func TestDiscoverExcludesInvalidManifestFromArmory(t *testing.T) {
	sourceOne := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "source-one"))
	catalog, err := Discover([]string{sourceOne})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	for _, tool := range catalog.Tools {
		if tool.Name == "invalid-local" {
			t.Fatalf("an invalid manifest must never appear in the armory: %+v", tool)
		}
	}
	var invalid Tool
	found := false
	for _, tool := range catalog.Invalid {
		if tool.Name == "invalid-local" {
			invalid, found = tool, true
			break
		}
	}
	if !found {
		t.Fatal("invalid-local should be reported in catalog.Invalid")
	}
	if invalid.Problem == "" {
		t.Fatal("invalid manifest should carry its validation problem")
	}
}

// TestDiscoverIgnoresTheOldHivedeckFolder proves the hard cutover (§17 car 4, N6): a manifest
// sitting under the pre-cutover `.hivedeck/tools/` folder is never discovered, even though
// its content is otherwise a valid manifest. Only `.formic/tools/` is read now.
func TestDiscoverIgnoresTheOldHivedeckFolder(t *testing.T) {
	sourcePath := t.TempDir()
	writeFixtureFile(t, filepath.Join(sourcePath, ".formic", "toolkit.yaml"), "toolkit: 1\nname: fixture\n")
	writeFixtureFile(t, filepath.Join(sourcePath, ".hivedeck", "tools", "old-folder.yaml"), `
formic: 1
name: old-folder
launch:
  command: old-cmd
`)

	catalog, err := Discover([]string{sourcePath})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	for _, tool := range catalog.Tools {
		if tool.Name == "old-folder" {
			t.Fatalf("a manifest under the old .hivedeck/tools/ folder must not be discovered: %+v", tool)
		}
	}
}

// TestDiscoverIgnoresPackageJSONScripts is Step A's proof 1 (§12 item 4): with the npm
// adapter removed, a catalogued source whose only tool-shaped file is package.json yields
// zero tools — npm scripts are no longer an accidental source.
func TestDiscoverIgnoresPackageJSONScripts(t *testing.T) {
	sourcePath := t.TempDir()
	writeFixtureFile(t, filepath.Join(sourcePath, ".formic", "toolkit.yaml"), "toolkit: 1\nname: fixture\n")
	writeFixtureFile(t, filepath.Join(sourcePath, "package.json"), `{
  "name": "package-json-only",
  "scripts": { "test": "echo hi" }
}`)

	catalog, err := Discover([]string{sourcePath})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	if len(catalog.Tools) != 0 {
		t.Fatalf("catalog.Tools = %d, want 0 (package.json scripts are no longer discovered)", len(catalog.Tools))
	}
}

// TestDiscoverAdmitsNothingFromAnUncataloguedSource proves the admission rule's core claim
// (§12): even a directory that carries a perfectly valid toolkit and tools contributes
// nothing unless its path is passed to Discover — nothing is seeded, nothing appears by
// accident. Passing an EMPTY sources list, even pointed at a directory full of valid
// manifests one level up, yields an empty armory.
func TestDiscoverAdmitsNothingFromAnUncataloguedSource(t *testing.T) {
	catalog, err := Discover(nil)
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	if len(catalog.Tools) != 0 || len(catalog.Invalid) != 0 {
		t.Fatalf("catalog = %+v, want completely empty with no catalogued sources", catalog)
	}
}

// TestDiscoverSkipsASourceWithNoValidToolkitDescriptor proves the admission rule holds at
// discover time too, not just at `formic add` time: a catalogued path whose toolkit
// descriptor is missing or invalid contributes no tools, without failing the whole scan.
func TestDiscoverSkipsASourceWithNoValidToolkitDescriptor(t *testing.T) {
	bareSource := t.TempDir()
	writeFixtureFile(t, filepath.Join(bareSource, ".formic", "tools", "orphan.yaml"), `
formic: 1
name: orphan
launch:
  command: orphan-cmd
`)
	catalog, err := Discover([]string{bareSource})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	if len(catalog.Tools) != 0 {
		t.Fatalf("catalog.Tools = %d, want 0 (no valid toolkit descriptor at this source)", len(catalog.Tools))
	}
}

// TestWorkshopToolkitDescriptorValidates proves the workshop repo's own root-level
// `.formic/toolkit.yaml` (§17 car 6: "a toolkit.yaml for THIS workshop repo ... is part of
// this commit") is a real, schema-valid toolkit descriptor — required before the founder
// can `formic add` this repo and keep its tools reachable. Skipped outside the private
// workshop, where this file does not exist.
func TestWorkshopToolkitDescriptorValidates(t *testing.T) {
	liveRoot, err := repository.FindRoot(".")
	if err != nil {
		t.Fatalf("find live repository root: %v", err)
	}
	toolkitPath := filepath.Join(liveRoot, ".formic", "toolkit.yaml")
	data, err := os.ReadFile(toolkitPath)
	if os.IsNotExist(err) {
		t.Skip("no workshop .formic/toolkit.yaml here (expected outside the private workshop)")
	}
	if err != nil {
		t.Fatalf("read %s: %v", toolkitPath, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode %s as YAML: %v", toolkitPath, err)
	}
	jsonData, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode %s as JSON: %v", toolkitPath, err)
	}
	if err := formats.ValidateToolkit(jsonData); err != nil {
		t.Fatalf("%s should validate: %v", toolkitPath, err)
	}
}

// TestMergeNeedsOwnKindOverridesToolkitInherited proves §12's override rule directly (PR #194
// deviation 6, closed here): a tool under a toolkit that shares one need KIND with its own
// inherits everything else from the toolkit, but that one kind stays the tool's own entry,
// never the toolkit's.
func TestMergeNeedsOwnKindOverridesToolkitInherited(t *testing.T) {
	sourcePath := t.TempDir()
	writeFixtureFile(t, filepath.Join(sourcePath, ".formic", "toolkit.yaml"), `
toolkit: 1
name: fixture
needs:
  - kind: secret
    name: TOOLKIT_SECRET
  - kind: gate
    class: browser
    env: E2E_DOCTOR_GATE
    providers: [solari, local]
`)
	writeFixtureFile(t, filepath.Join(sourcePath, ".formic", "tools", "needy.yaml"), `
formic: 1
name: needy
launch:
  command: needy-cmd
needs:
  - kind: secret
    name: OWN_SECRET
`)

	catalog, err := Discover([]string{sourcePath})
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	tool := requireTool(t, catalog, "needy")
	if len(tool.Manifest.Needs) != 2 {
		t.Fatalf("needy's merged needs = %+v, want exactly 2 (its own secret + the toolkit's gate)", tool.Manifest.Needs)
	}
	foundOwnSecret, foundToolkitGate, foundToolkitSecret := false, false, false
	for _, need := range tool.Manifest.Needs {
		kind, ok := needKind(need)
		if !ok {
			t.Fatalf("need has no kind: %+v", need)
		}
		switch kind {
		case "secret":
			data, err := json.Marshal(need)
			if err != nil {
				t.Fatalf("marshal need: %v", err)
			}
			var decoded struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal need: %v", err)
			}
			if decoded.Name == "OWN_SECRET" {
				foundOwnSecret = true
			}
			if decoded.Name == "TOOLKIT_SECRET" {
				foundToolkitSecret = true
			}
		case "gate":
			foundToolkitGate = true
		default:
			t.Fatalf("unexpected need kind %q", kind)
		}
	}
	if !foundOwnSecret {
		t.Fatal("needy's own secret need should survive the merge")
	}
	if !foundToolkitGate {
		t.Fatal("the toolkit's gate need should be inherited (no collision on that kind)")
	}
	if foundToolkitSecret {
		t.Fatal("the toolkit's secret need should NOT be inherited — needy's own secret entry of the same kind overrides it")
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

func serviceTool(name string, probe bool, needs ...formats.Need) Tool {
	tool := Tool{Name: name, Manifest: formats.ToolManifest{Needs: needs}}
	if probe {
		tool.Manifest.Live = &formats.Live{Probe: map[string]any{"tcp": "127.0.0.1:1"}}
	}
	return tool
}

func TestServiceNeedCrossManifestRule(t *testing.T) {
	need := func(tool, action string) formats.Need {
		return map[string]any{"kind": "service", "tool": tool, "action": action}
	}
	docker := serviceTool("docker", true)
	docker.Manifest.Actions = []formats.Action{{Name: "start"}}
	cases := []struct {
		name    string
		tool    Tool
		invalid bool
	}{
		{"valid", serviceTool("app", false, need("docker", "start")), false},
		{"unknown tool", serviceTool("app", false, need("ghost", "")), true},
		{"tool without probe", serviceTool("app", false, need("noprobe", "")), true},
		{"undeclared action", serviceTool("app", false, need("docker", "stop")), true},
	}
	for _, tc := range cases {
		catalog := Catalog{Tools: []Tool{docker, serviceTool("noprobe", false), tc.tool}}
		refuseBrokenServiceNeeds(&catalog)
		if got := len(catalog.Invalid) == 1 && catalog.Invalid[0].Name == "app" && catalog.Invalid[0].Problem != ""; got != tc.invalid {
			t.Errorf("%s: invalid = %v, want %v (%+v)", tc.name, got, tc.invalid, catalog.Invalid)
		}
	}
}

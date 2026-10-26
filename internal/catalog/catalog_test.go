package catalog

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAbsentFileIsAnEmptyCatalogNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	document, err := Load(path)
	if err != nil {
		t.Fatalf("Load of an absent catalog returned an error: %v", err)
	}
	if len(document.Sources) != 0 {
		t.Fatalf("document.Sources = %d, want 0", len(document.Sources))
	}
}

func TestAddValidatesAndAppendsAValidSource(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	sourcePath := writeValidToolkit(t, t.TempDir(), "kept")

	document, err := Add(catalogPath, sourcePath)
	if err != nil {
		t.Fatalf("Add returned an error: %v", err)
	}
	if len(document.Sources) != 1 || filepath.Clean(document.Sources[0].Path) != filepath.Clean(sourcePath) {
		t.Fatalf("document.Sources = %+v, want exactly one entry for %s", document.Sources, sourcePath)
	}

	reloaded, err := Load(catalogPath)
	if err != nil {
		t.Fatalf("Load after Add returned an error: %v", err)
	}
	if len(reloaded.Sources) != 1 {
		t.Fatalf("reloaded.Sources = %d, want 1", len(reloaded.Sources))
	}
}

// TestAddRefusesAMissingPathAndLeavesTheFileByteIdentical is proof 3 (step C): a refusal
// must never write a partial or corrupted catalog file.
func TestAddRefusesAMissingPathAndLeavesTheFileByteIdentical(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	seeded := writeValidToolkit(t, t.TempDir(), "seeded")
	if _, err := Add(catalogPath, seeded); err != nil {
		t.Fatalf("seed Add returned an error: %v", err)
	}
	before := hashFile(t, catalogPath)

	missingPath := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := Add(catalogPath, missingPath); err == nil {
		t.Fatal("Add of a missing path should refuse")
	}
	after := hashFile(t, catalogPath)
	if before != after {
		t.Fatalf("catalog file changed after a refused Add: before=%s after=%s", before, after)
	}
}

// TestAddRefusesAPathWithNoToolkitDescriptorAndLeavesTheFileByteIdentical proves the same
// byte-identical refusal for the "path exists but carries no valid descriptor" case.
func TestAddRefusesAPathWithNoToolkitDescriptorAndLeavesTheFileByteIdentical(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	seeded := writeValidToolkit(t, t.TempDir(), "seeded")
	if _, err := Add(catalogPath, seeded); err != nil {
		t.Fatalf("seed Add returned an error: %v", err)
	}
	before := hashFile(t, catalogPath)

	bareDirectory := t.TempDir()
	if _, err := Add(catalogPath, bareDirectory); err == nil {
		t.Fatal("Add of a directory with no toolkit descriptor should refuse")
	}
	after := hashFile(t, catalogPath)
	if before != after {
		t.Fatalf("catalog file changed after a refused Add: before=%s after=%s", before, after)
	}
}

// TestAddRefusesAnInvalidToolkitDescriptor proves the mutation half of proof 3: disabling
// the toolkit validation call must turn this red — Add must actually call the schema
// validator, not just decode the YAML. The fixture carries every field the generated
// Toolkit type's own UnmarshalJSON requires (name, toolkit) so ONLY schema validation's
// additionalProperties:false catches the unknown field — a fixture missing a required
// field would go red from the generated decoder alone, proving nothing about the
// validator call this test exists to pin.
func TestAddRefusesAnInvalidToolkitDescriptor(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	sourcePath := t.TempDir()
	writeFile(t, filepath.Join(sourcePath, ".formic", "toolkit.yaml"), "toolkit: 1\nname: sample\nunknownField: true\n")

	if _, err := Add(catalogPath, sourcePath); err == nil {
		t.Fatal("Add of a source with an invalid toolkit descriptor should refuse")
	}
	if _, err := os.Stat(catalogPath); !os.IsNotExist(err) {
		t.Fatalf("catalog file should not have been created by a refused Add, stat err = %v", err)
	}
}

func TestAddIsIdempotentByCleanedAbsolutePath(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	sourcePath := writeValidToolkit(t, t.TempDir(), "kept")

	if _, err := Add(catalogPath, sourcePath); err != nil {
		t.Fatalf("first Add returned an error: %v", err)
	}
	document, err := Add(catalogPath, sourcePath+string(filepath.Separator))
	if err != nil {
		t.Fatalf("second Add returned an error: %v", err)
	}
	if len(document.Sources) != 1 {
		t.Fatalf("document.Sources = %d, want 1 (deduped by cleaned absolute path)", len(document.Sources))
	}
}

// TestAddOrderIsPrecedence proves sources accumulate in the order they are added, which is
// the catalog's precedence order (§12).
func TestAddOrderIsPrecedence(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	first := writeValidToolkit(t, t.TempDir(), "first")
	second := writeValidToolkit(t, t.TempDir(), "second")

	if _, err := Add(catalogPath, first); err != nil {
		t.Fatal(err)
	}
	document, err := Add(catalogPath, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Sources) != 2 {
		t.Fatalf("document.Sources = %d, want 2", len(document.Sources))
	}
	if filepath.Clean(document.Sources[0].Path) != filepath.Clean(first) {
		t.Fatalf("first source = %q, want %q", document.Sources[0].Path, first)
	}
	if filepath.Clean(document.Sources[1].Path) != filepath.Clean(second) {
		t.Fatalf("second source = %q, want %q", document.Sources[1].Path, second)
	}
}

// TestUncataloguedAtReportsAValidUncataloguedToolkitThenGoesNilOnceAdded is N8 point 3's
// deck-offer/list-json shared computation: a directory carrying a valid toolkit descriptor not
// yet in sourcePaths is reported; adding its path to sourcePaths makes it nil.
func TestUncataloguedAtReportsAValidUncataloguedToolkitThenGoesNilOnceAdded(t *testing.T) {
	sourcePath := writeValidToolkit(t, t.TempDir(), "car10-source")

	found := UncataloguedAt(sourcePath, nil)
	if found == nil || found.Name != "car10-source" || filepath.Clean(found.Path) != filepath.Clean(sourcePath) {
		t.Fatalf("UncataloguedAt(no sources) = %+v, want name=car10-source path=%s", found, sourcePath)
	}

	if got := UncataloguedAt(sourcePath, []string{sourcePath}); got != nil {
		t.Fatalf("UncataloguedAt(already catalogued) = %+v, want nil", got)
	}
}

// TestUncataloguedAtIsNilWithNoValidToolkitDescriptor covers both "nothing there" and "an
// invalid descriptor" — neither offers anything, silently, the same way `formic list`'s
// uncatalogued field stays absent rather than surfacing a validation error as a refusal.
func TestUncataloguedAtIsNilWithNoValidToolkitDescriptor(t *testing.T) {
	if got := UncataloguedAt(t.TempDir(), nil); got != nil {
		t.Fatalf("UncataloguedAt(no descriptor) = %+v, want nil", got)
	}
	invalid := t.TempDir()
	writeFile(t, filepath.Join(invalid, ".formic", "toolkit.yaml"), "toolkit: 1\nunknownField: true\n")
	if got := UncataloguedAt(invalid, nil); got != nil {
		t.Fatalf("UncataloguedAt(invalid descriptor) = %+v, want nil", got)
	}
}

// TestSourcePathsReturnsEveryAdmittedSourceInPrecedenceOrder proves the hoisted helper reads
// exactly what Add wrote, in the order Add appended them.
func TestSourcePathsReturnsEveryAdmittedSourceInPrecedenceOrder(t *testing.T) {
	userConfigRoot := t.TempDir()
	catalogPath := DefaultPath(userConfigRoot)
	first := writeValidToolkit(t, t.TempDir(), "first")
	second := writeValidToolkit(t, t.TempDir(), "second")
	if _, err := Add(catalogPath, first); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(catalogPath, second); err != nil {
		t.Fatal(err)
	}

	sourcePaths, err := SourcePaths(userConfigRoot)
	if err != nil {
		t.Fatalf("SourcePaths returned an error: %v", err)
	}
	if len(sourcePaths) != 2 || filepath.Clean(sourcePaths[0]) != filepath.Clean(first) || filepath.Clean(sourcePaths[1]) != filepath.Clean(second) {
		t.Fatalf("SourcePaths = %v, want [%s, %s] in that order", sourcePaths, first, second)
	}
}

func writeValidToolkit(t *testing.T, root, name string) string {
	t.Helper()
	writeFile(t, filepath.Join(root, ".formic", "toolkit.yaml"), "toolkit: 1\nname: "+name+"\n")
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return string(sum[:])
}

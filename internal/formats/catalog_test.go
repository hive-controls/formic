package formats_test

import (
	"encoding/json"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

const catalogFixturesDir = "fixtures/catalog"

// TestCatalogValidFixturesUnmarshalAndValidate proves every fixtures/catalog/valid/*.json
// fixture unmarshals into the generated Catalog type and passes ValidateCatalog.
// fixtures/catalog/catalog.md documents what each fixture exercises.
func TestCatalogValidFixturesUnmarshalAndValidate(t *testing.T) {
	names := listFormatFixtureNames(t, catalogFixturesDir, "valid")
	if len(names) == 0 {
		t.Fatal("expected at least one valid catalog fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, catalogFixturesDir, "valid", name)

			var catalog formats.Catalog
			if err := json.Unmarshal(data, &catalog); err != nil {
				t.Fatalf("%s should unmarshal into Catalog; got: %v", name, err)
			}
			if err := formats.ValidateCatalog(data); err != nil {
				t.Fatalf("%s should pass ValidateCatalog; got: %v", name, err)
			}
		})
	}
}

// TestCatalogInvalidFixturesAreRejected proves every fixtures/catalog/invalid/*.json fixture
// is rejected by ValidateCatalog. fixtures/catalog/catalog.md documents why each is invalid.
func TestCatalogInvalidFixturesAreRejected(t *testing.T) {
	names := listFormatFixtureNames(t, catalogFixturesDir, "invalid")
	if len(names) == 0 {
		t.Fatal("expected at least one invalid catalog fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, catalogFixturesDir, "invalid", name)
			if err := formats.ValidateCatalog(data); err == nil {
				t.Fatalf("%s should be rejected by ValidateCatalog", name)
			}
		})
	}
}

// TestCatalogTwoSourcesFixtureOrderIsPrecedence proves two-sources.json's specific claim in
// catalog.md: two dir sources, in precedence order.
func TestCatalogTwoSourcesFixtureOrderIsPrecedence(t *testing.T) {
	data := readFormatFixture(t, catalogFixturesDir, "valid", "two-sources.json")
	var catalog formats.Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("two-sources.json should unmarshal into Catalog; got: %v", err)
	}
	if len(catalog.Sources) != 2 {
		t.Fatalf("two-sources.json should declare 2 sources; got %d", len(catalog.Sources))
	}
	if catalog.Sources[0].Path != "C:/devprojects/Tooling" {
		t.Errorf("two-sources.json's first source should be C:/devprojects/Tooling; got %q", catalog.Sources[0].Path)
	}
}

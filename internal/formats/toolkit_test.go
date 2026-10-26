package formats_test

import (
	"encoding/json"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

const toolkitFixturesDir = "fixtures/toolkit"

// TestToolkitValidFixturesUnmarshalAndValidate proves every fixtures/toolkit/valid/*.json
// fixture unmarshals into the generated Toolkit type and passes ValidateToolkit.
// fixtures/toolkit/toolkit.md documents what each fixture exercises.
func TestToolkitValidFixturesUnmarshalAndValidate(t *testing.T) {
	names := listFormatFixtureNames(t, toolkitFixturesDir, "valid")
	if len(names) == 0 {
		t.Fatal("expected at least one valid toolkit fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, toolkitFixturesDir, "valid", name)

			var descriptor formats.Toolkit
			if err := json.Unmarshal(data, &descriptor); err != nil {
				t.Fatalf("%s should unmarshal into Toolkit; got: %v", name, err)
			}
			if err := formats.ValidateToolkit(data); err != nil {
				t.Fatalf("%s should pass ValidateToolkit; got: %v", name, err)
			}
		})
	}
}

// TestToolkitInvalidFixturesAreRejected proves every fixtures/toolkit/invalid/*.json fixture
// is rejected by ValidateToolkit. fixtures/toolkit/toolkit.md documents why each is invalid.
func TestToolkitInvalidFixturesAreRejected(t *testing.T) {
	names := listFormatFixtureNames(t, toolkitFixturesDir, "invalid")
	if len(names) == 0 {
		t.Fatal("expected at least one invalid toolkit fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, toolkitFixturesDir, "invalid", name)
			if err := formats.ValidateToolkit(data); err == nil {
				t.Fatalf("%s should be rejected by ValidateToolkit", name)
			}
		})
	}
}

// TestToolkitOsintSuiteFixtureFields proves osint-suite.json's specific claim in toolkit.md:
// it exercises title, conventions.caseFolder, and two needs of different kinds.
func TestToolkitOsintSuiteFixtureFields(t *testing.T) {
	data := readFormatFixture(t, toolkitFixturesDir, "valid", "osint-suite.json")
	var descriptor formats.Toolkit
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatalf("osint-suite.json should unmarshal into Toolkit; got: %v", err)
	}
	if descriptor.Title == nil || *descriptor.Title != "OSINT Suite" {
		t.Errorf("osint-suite.json should set title; got %+v", descriptor.Title)
	}
	if descriptor.Conventions == nil || descriptor.Conventions.CaseFolder == nil || *descriptor.Conventions.CaseFolder != "{case}/" {
		t.Errorf("osint-suite.json should set conventions.caseFolder; got %+v", descriptor.Conventions)
	}
	if len(descriptor.Needs) != 2 {
		t.Fatalf("osint-suite.json should declare 2 needs; got %d", len(descriptor.Needs))
	}
}

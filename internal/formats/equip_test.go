package formats_test

import (
	"encoding/json"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

const equipFixturesDir = "fixtures/equip"

// TestEquipValidFixturesUnmarshalAndValidate proves every fixtures/equip/valid/*.json fixture
// unmarshals into the generated Equip type and passes ValidateEquip. fixtures/equip/equip.md
// documents what each fixture exercises.
func TestEquipValidFixturesUnmarshalAndValidate(t *testing.T) {
	names := listFormatFixtureNames(t, equipFixturesDir, "valid")
	if len(names) == 0 {
		t.Fatal("expected at least one valid equip fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, equipFixturesDir, "valid", name)

			var equip formats.Equip
			if err := json.Unmarshal(data, &equip); err != nil {
				t.Fatalf("%s should unmarshal into Equip; got: %v", name, err)
			}
			if err := formats.ValidateEquip(data); err != nil {
				t.Fatalf("%s should pass ValidateEquip; got: %v", name, err)
			}
		})
	}
}

// TestEquipInvalidFixturesAreRejected proves every fixtures/equip/invalid/*.json fixture is
// rejected by ValidateEquip. fixtures/equip/equip.md documents why each is invalid.
func TestEquipInvalidFixturesAreRejected(t *testing.T) {
	names := listFormatFixtureNames(t, equipFixturesDir, "invalid")
	if len(names) == 0 {
		t.Fatal("expected at least one invalid equip fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, equipFixturesDir, "invalid", name)
			if err := formats.ValidateEquip(data); err == nil {
				t.Fatalf("%s should be rejected by ValidateEquip", name)
			}
		})
	}
}

// TestEquipReservedFormsRoundTrip proves reserved-forms.json's specific claim in equip.md:
// keychain:NAME and file:<path>#key are accepted references, not rejected as unknown.
func TestEquipReservedFormsRoundTrip(t *testing.T) {
	data := readFormatFixture(t, equipFixturesDir, "valid", "reserved-forms.json")
	var equip formats.Equip
	if err := json.Unmarshal(data, &equip); err != nil {
		t.Fatalf("reserved-forms.json should unmarshal into Equip; got: %v", err)
	}
	if equip.Secrets["KEYCHAIN_SECRET"] != "keychain:KEYCHAIN_SECRET" {
		t.Errorf("reserved-forms.json should keep the keychain: reference verbatim; got %+v", equip.Secrets)
	}
	if equip.Secrets["FILE_SECRET"] != "file:./secrets.yaml#file-secret" {
		t.Errorf("reserved-forms.json should keep the file: reference verbatim; got %+v", equip.Secrets)
	}
}

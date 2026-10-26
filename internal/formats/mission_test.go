package formats_test

import (
	"encoding/json"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

const missionFixturesDir = "fixtures/mission"

// TestMissionValidFixturesUnmarshalAndValidate proves every fixtures/mission/valid/*.json
// fixture unmarshals into the generated Mission type and passes ValidateMission.
// fixtures/mission/mission.md documents what each fixture exercises.
func TestMissionValidFixturesUnmarshalAndValidate(t *testing.T) {
	names := listFormatFixtureNames(t, missionFixturesDir, "valid")
	if len(names) == 0 {
		t.Fatal("expected at least one valid mission fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, missionFixturesDir, "valid", name)

			var mission formats.Mission
			if err := json.Unmarshal(data, &mission); err != nil {
				t.Fatalf("%s should unmarshal into Mission; got: %v", name, err)
			}
			if err := formats.ValidateMission(data); err != nil {
				t.Fatalf("%s should pass ValidateMission; got: %v", name, err)
			}
		})
	}
}

// TestMissionInvalidFixturesAreRejected proves every fixtures/mission/invalid/*.json fixture
// is rejected by both ValidateMission (schema) and struct-level unmarshal — the generated
// Mission type's own UnmarshalJSON enforces the steps length bounds too, per §9's "exactly
// one step" rule. fixtures/mission/mission.md documents why each is invalid.
func TestMissionInvalidFixturesAreRejected(t *testing.T) {
	names := listFormatFixtureNames(t, missionFixturesDir, "invalid")
	if len(names) == 0 {
		t.Fatal("expected at least one invalid mission fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			data := readFormatFixture(t, missionFixturesDir, "invalid", name)
			if err := formats.ValidateMission(data); err == nil {
				t.Fatalf("%s should be rejected by ValidateMission", name)
			}
			var mission formats.Mission
			if err := json.Unmarshal(data, &mission); err == nil {
				t.Fatalf("%s should also be rejected by Mission's own UnmarshalJSON", name)
			}
		})
	}
}

// TestMissionHealCheckoutFixtureFields proves heal-checkout.json's specific claim in
// mission.md: the design doc's own §9 worked example, exactly one step.
func TestMissionHealCheckoutFixtureFields(t *testing.T) {
	data := readFormatFixture(t, missionFixturesDir, "valid", "heal-checkout.json")
	var mission formats.Mission
	if err := json.Unmarshal(data, &mission); err != nil {
		t.Fatalf("heal-checkout.json should unmarshal into Mission; got: %v", err)
	}
	if len(mission.Steps) != 1 {
		t.Fatalf("heal-checkout.json should declare exactly 1 step; got %d", len(mission.Steps))
	}
	if mission.Steps[0].Tool != "e2e-doctor" {
		t.Errorf("heal-checkout.json's step should launch e2e-doctor; got %q", mission.Steps[0].Tool)
	}
	if mission.Evidence != "evidence/" {
		t.Errorf("heal-checkout.json should set evidence to evidence/; got %q", mission.Evidence)
	}
}

// TestMissionNoInputsFixtureFieldsOmitsWith proves no-inputs.json's specific claim in
// mission.md: a saved launch of a tool that declares no inputs, with no `with` key on its
// step at all.
func TestMissionNoInputsFixtureFieldsOmitsWith(t *testing.T) {
	data := readFormatFixture(t, missionFixturesDir, "valid", "no-inputs.json")
	var mission formats.Mission
	if err := json.Unmarshal(data, &mission); err != nil {
		t.Fatalf("no-inputs.json should unmarshal into Mission; got: %v", err)
	}
	if len(mission.Steps) != 1 {
		t.Fatalf("no-inputs.json should declare exactly 1 step; got %d", len(mission.Steps))
	}
	if mission.Steps[0].Tool != "restart-service" {
		t.Errorf("no-inputs.json's step should launch restart-service; got %q", mission.Steps[0].Tool)
	}
	if len(mission.Steps[0].With) != 0 {
		t.Errorf("no-inputs.json's step should carry no with entries; got %+v", mission.Steps[0].With)
	}
}

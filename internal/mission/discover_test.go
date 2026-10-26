package mission

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMissionFile(t *testing.T, directory, fileName, contents string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, fileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validMissionYAML = "mission: 1\nname: heal-checkout\nsteps:\n  - tool: e2e-doctor\n" +
	"    with: { spec: value }\nevidence: evidence/\n"

// TestDiscoverPersonalMissionShadowsSourceMissionOfTheSameName proves §9's precedence: the
// user's personal folder is read first, and a later same-named definition from a catalogued
// source is reported as shadowed by the personal winner, not silently dropped or swapped.
func TestDiscoverPersonalMissionShadowsSourceMissionOfTheSameName(t *testing.T) {
	userConfigRoot := t.TempDir()
	sourceRoot := t.TempDir()
	writeMissionFile(t, filepath.Join(userConfigRoot, "formic", "missions"), "heal-checkout.yaml", validMissionYAML)
	writeMissionFile(t, filepath.Join(sourceRoot, ".formic", "missions"), "heal-checkout.yaml", validMissionYAML)

	set, err := Discover(userConfigRoot, []Source{{Path: sourceRoot}})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(set.Missions) != 1 {
		t.Fatalf("Missions = %+v, want exactly one winner", set.Missions)
	}
	winner := set.Missions[0]
	if !winner.Personal {
		t.Fatalf("winner.Personal = false, want the personal definition to win (§9 precedence)")
	}
	if len(winner.ShadowedBy) != 1 {
		t.Fatalf("winner.ShadowedBy = %v, want the source definition named as shadowed", winner.ShadowedBy)
	}
	found, ok := set.Find("heal-checkout")
	if !ok || !found.Personal {
		t.Fatalf("Find(%q) = %+v, %v, want the personal winner", "heal-checkout", found, ok)
	}
}

// TestDiscoverInvalidMissionFileNeverAppearsInMissionsOnlyInvalid proves the admission rule:
// an invalid mission file is reported in Set.Invalid and never enters Set.Missions — never
// findable, never eligible to shadow or be shadowed.
func TestDiscoverInvalidMissionFileNeverAppearsInMissionsOnlyInvalid(t *testing.T) {
	userConfigRoot := t.TempDir()
	sourceRoot := t.TempDir()
	writeMissionFile(t, filepath.Join(sourceRoot, ".formic", "missions"), "broken.yaml", "mission: 1\nname: broken\nsteps: []\nevidence: e/\n")

	set, err := Discover(userConfigRoot, []Source{{Path: sourceRoot}})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(set.Missions) != 0 {
		t.Fatalf("Missions = %+v, want none (the only file is invalid)", set.Missions)
	}
	if len(set.Invalid) != 1 {
		t.Fatalf("Invalid = %+v, want exactly one invalid mission reported", set.Invalid)
	}
	if _, ok := set.Find("broken"); ok {
		t.Fatal("Find(\"broken\") succeeded, want the invalid mission to never be findable")
	}
}

// TestDiscoverUncataloguedSourceNeverContributesMissions proves N8: a mission in a directory
// that is not among the passed-in catalogued sources never appears at all.
func TestDiscoverUncataloguedSourceNeverContributesMissions(t *testing.T) {
	userConfigRoot := t.TempDir()
	uncataloguedRoot := t.TempDir()
	writeMissionFile(t, filepath.Join(uncataloguedRoot, ".formic", "missions"), "ghost.yaml", "mission: 1\nname: ghost\nsteps:\n  - tool: x\nevidence: e/\n")

	set, err := Discover(userConfigRoot, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(set.Missions) != 0 || len(set.Invalid) != 0 {
		t.Fatalf("set = %+v, want nothing discovered from an uncatalogued source", set)
	}
}

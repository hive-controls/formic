package equip_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
)

// TestLoadOfAMissingFileReturnsAnEmptyDocument proves the "absent file = empty document"
// contract (§13, matching internal/catalog.Load's own precedent for catalog.yaml).
func TestLoadOfAMissingFileReturnsAnEmptyDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formic", "equip.yaml")
	document, err := equip.Load(path)
	if err != nil {
		t.Fatalf("Load of a missing file returned an error: %v", err)
	}
	if document.Equip != 1 || len(document.Secrets) != 0 || len(document.Providers) != 0 {
		t.Fatalf("document = %+v, want an empty v1 document", document)
	}
}

// TestSaveThenLoadRoundTripsSecretsAndProviders proves Save/Load agree with each other and
// with the schema — the reserved reference forms round-trip untouched too (§13).
func TestSaveThenLoadRoundTripsSecretsAndProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formic", "equip.yaml")
	document := formats.Equip{
		Equip:     1,
		Secrets:   formats.EquipSecrets{"NUMVERIFY_API_KEY": "env.NUMVERIFY_API_KEY", "RESERVED": "keychain:RESERVED"},
		Providers: formats.EquipProviders{"gate.browser": "solari"},
	}
	if err := equip.Save(path, document); err != nil {
		t.Fatalf("Save returned an error: %v", err)
	}
	loaded, err := equip.Load(path)
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if loaded.Secrets["NUMVERIFY_API_KEY"] != "env.NUMVERIFY_API_KEY" {
		t.Fatalf("loaded.Secrets = %+v, want NUMVERIFY_API_KEY to round-trip", loaded.Secrets)
	}
	if loaded.Secrets["RESERVED"] != "keychain:RESERVED" {
		t.Fatalf("loaded.Secrets = %+v, want the reserved keychain: form to round-trip untouched", loaded.Secrets)
	}
	if loaded.Providers["gate.browser"] != "solari" {
		t.Fatalf("loaded.Providers = %+v, want gate.browser to round-trip", loaded.Providers)
	}
}

// TestSetSecretRefusesALiteralValueAndLeavesTheFileByteIdentical proves the whole "a literal
// secret value" detection (§13, §15): a value that is not a recognized reference form is
// refused, and the file on disk is untouched, down to the byte.
func TestSetSecretRefusesALiteralValueAndLeavesTheFileByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formic", "equip.yaml")
	if err := equip.Save(path, formats.Equip{Equip: 1, Secrets: formats.EquipSecrets{"EXISTING": "env.EXISTING"}}); err != nil {
		t.Fatalf("seed Save returned an error: %v", err)
	}
	before := fileHash(t, path)

	_, err := equip.SetSecret(path, "NUMVERIFY_API_KEY", "sk-literal-abc123")
	if err == nil {
		t.Fatal("SetSecret should refuse a literal value")
	}
	if err.Error() != equip.ErrLiteralValue.Error() {
		t.Fatalf("error = %v, want ErrLiteralValue's own fixed text (never echoing the argument)", err)
	}

	after := fileHash(t, path)
	if before != after {
		t.Fatalf("file hash changed from %x to %x; a refused write must leave the file byte-identical", before, after)
	}
}

// TestSetSecretRecordsAnEnvReference is SetSecret's positive case: a recognized env.NAME
// reference is written and round-trips through Load.
func TestSetSecretRecordsAnEnvReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formic", "equip.yaml")
	document, err := equip.SetSecret(path, "NUMVERIFY_API_KEY", "env.NUMVERIFY_API_KEY")
	if err != nil {
		t.Fatalf("SetSecret returned an error: %v", err)
	}
	if document.Secrets["NUMVERIFY_API_KEY"] != "env.NUMVERIFY_API_KEY" {
		t.Fatalf("document.Secrets = %+v, want the recorded reference", document.Secrets)
	}
	loaded, err := equip.Load(path)
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if loaded.Secrets["NUMVERIFY_API_KEY"] != "env.NUMVERIFY_API_KEY" {
		t.Fatalf("loaded.Secrets = %+v, want the write to persist", loaded.Secrets)
	}
}

// TestSetSecretLeavesEveryOtherKeyUntouched proves a write round-trips reserved forms and
// every other secret/provider it did not touch, rather than overwriting the whole file.
func TestSetSecretLeavesEveryOtherKeyUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formic", "equip.yaml")
	seed := formats.Equip{
		Equip:     1,
		Secrets:   formats.EquipSecrets{"OTHER": "env.OTHER", "RESERVED": "file:./secrets.yaml#key"},
		Providers: formats.EquipProviders{"gate.browser": "solari"},
	}
	if err := equip.Save(path, seed); err != nil {
		t.Fatalf("seed Save returned an error: %v", err)
	}
	if _, err := equip.SetSecret(path, "NEW_KEY", "env.NEW_KEY"); err != nil {
		t.Fatalf("SetSecret returned an error: %v", err)
	}
	loaded, err := equip.Load(path)
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if loaded.Secrets["OTHER"] != "env.OTHER" || loaded.Secrets["RESERVED"] != "file:./secrets.yaml#key" {
		t.Fatalf("loaded.Secrets = %+v, want OTHER and RESERVED untouched", loaded.Secrets)
	}
	if loaded.Providers["gate.browser"] != "solari" {
		t.Fatalf("loaded.Providers = %+v, want gate.browser untouched", loaded.Providers)
	}
	if loaded.Secrets["NEW_KEY"] != "env.NEW_KEY" {
		t.Fatalf("loaded.Secrets = %+v, want NEW_KEY recorded", loaded.Secrets)
	}
}

// TestSetProviderRecordsAChoice is SetProvider's basic write/round-trip proof.
func TestSetProviderRecordsAChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formic", "equip.yaml")
	document, err := equip.SetProvider(path, "gate.browser", "solari")
	if err != nil {
		t.Fatalf("SetProvider returned an error: %v", err)
	}
	if document.Providers["gate.browser"] != "solari" {
		t.Fatalf("document.Providers = %+v, want gate.browser recorded", document.Providers)
	}
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(data)
}

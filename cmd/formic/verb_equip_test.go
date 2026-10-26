package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeNeedySource(t *testing.T, name, needsYAML string) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: " + name + "\nlaunch:\n  command: echo\n" + needsYAML
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", name+".yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

// TestRunEquipListGroupsNeedsAcrossTheArmory proves `formic equip`'s no-argument form (§15):
// two tools declaring the SAME secret need share one grouped entry naming both tools.
func TestRunEquipListGroupsNeedsAcrossTheArmory(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourceOne := writeNeedySource(t, "needy-one", "needs:\n  - kind: secret\n    name: SHARED_KEY\n")
	sourceTwo := writeNeedySource(t, "needy-two", "needs:\n  - kind: secret\n    name: SHARED_KEY\n")
	if exitCode := run([]string{"add", sourceOne}); exitCode != 0 {
		t.Fatalf("add sourceOne exit = %d, want 0", exitCode)
	}
	if exitCode := run([]string{"add", sourceTwo}); exitCode != 0 {
		t.Fatalf("add sourceTwo exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"equip", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("equip --json exit = %d:\n%s", exitCode, output)
	}
	var result equipListResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode equip --json: %v\n%s", err, output)
	}
	if len(result.Needs) != 1 {
		t.Fatalf("result.Needs = %+v, want exactly one grouped need", result.Needs)
	}
	if result.Needs[0].Met {
		t.Fatal("the grouped need should be unmet (nothing equipped)")
	}
	if len(result.Needs[0].Tools) != 2 {
		t.Fatalf("result.Needs[0].Tools = %v, want both declaring tools listed", result.Needs[0].Tools)
	}
}

// TestRunEquipSecretRefusesALiteralValueLeavingTheFileByteIdentical is the CLI-level twin of
// internal/equip's own proof: the refusal text never echoes the argument, and the equip file
// is left byte-identical.
func TestRunEquipSecretRefusesALiteralValueLeavingTheFileByteIdentical(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	if exitCode := run([]string{"equip", "secret", "EXISTING", "env.EXISTING"}); exitCode != 0 {
		t.Fatalf("seed equip secret exit = %d, want 0", exitCode)
	}
	equipPath, err := defaultEquipPath()
	if err != nil {
		t.Fatal(err)
	}
	before := fileHash(t, equipPath)

	output, exitCode := captureStdout(t, func() int {
		return run([]string{"equip", "secret", "NUMVERIFY_API_KEY", "sk-literal-abc123", "--json"})
	})
	if exitCode == 0 {
		t.Fatalf("equip secret with a literal value should refuse:\n%s", output)
	}
	var result refusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused {
		t.Fatal("result.Refused should be true")
	}
	if containsSubstring(result.Reason, "sk-literal-abc123") {
		t.Fatalf("refusal reason = %q, must never echo the literal argument", result.Reason)
	}

	after := fileHash(t, equipPath)
	if before != after {
		t.Fatalf("equip file hash changed from %x to %x; a refused write must leave it byte-identical", before, after)
	}
}

// TestRunEquipProviderRefusesAProviderNoAdmittedToolLists proves §13's "formic refuses a
// choice the tool does not list."
func TestRunEquipProviderRefusesAProviderNoAdmittedToolLists(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeNeedySource(t, "gated", "needs:\n  - kind: gate\n    class: browser\n    env: E2E_DOCTOR_GATE\n    providers: [solari, local]\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int {
		return run([]string{"equip", "provider", "gate.browser", "not-a-listed-provider", "--json"})
	})
	if exitCode == 0 {
		t.Fatalf("equip provider with an unlisted provider should refuse:\n%s", output)
	}
	var result refusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused {
		t.Fatal("result.Refused should be true")
	}
}

// TestRunEquipProviderRecordsAnAcceptedChoice is the accepted-choice counterpart.
func TestRunEquipProviderRecordsAnAcceptedChoice(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeNeedySource(t, "gated", "needs:\n  - kind: gate\n    class: browser\n    env: E2E_DOCTOR_GATE\n    providers: [solari, local]\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	if exitCode := run([]string{"equip", "provider", "gate.browser", "solari"}); exitCode != 0 {
		t.Fatalf("equip provider exit = %d, want 0 for an accepted provider", exitCode)
	}
}

func containsSubstring(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(data)
}

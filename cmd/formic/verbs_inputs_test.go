package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/testfixture"
)

// writeInputToolSource catalogues one tool declaring the given inputs[] YAML block, its command
// echoing back the substituted argv so a test can assert on the real value that reached the
// child.
func writeInputToolSource(t *testing.T, toolName, inputsYAML string) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: " + toolName + "\nlaunch:\n  command: " + testfixture.Tool(t) +
		"\n  args: [\"fixture:exit\", \"0\"]\n" + inputsYAML
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", toolName+".yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

// TestRunLaunchRefusesWithAllMissingInputsListed proves the CLI proof from the brief's Method
// step 5: launch <fixture> --json refused with all missing inputs listed, never just the first.
func TestRunLaunchRefusesWithAllMissingInputsListed(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeInputToolSource(t, "twoinputs", "inputs:\n  - name: first\n    required: true\n  - name: second\n    required: true\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "twoinputs", "--json"}) })
	if exitCode == 0 {
		t.Fatalf("launch with two missing required inputs should refuse:\n%s", output)
	}
	var result inputsRefusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused || len(result.Missing) != 2 {
		t.Fatalf("result = %+v, want both missing inputs named at once", result)
	}
}

// TestRunLaunchRefusesADuplicateInputNameGivenTwice proves the CLI proof: --input given twice
// for the same name is refused, never last-wins.
func TestRunLaunchRefusesADuplicateInputNameGivenTwice(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeInputToolSource(t, "oneinput", "inputs:\n  - name: value\n    required: true\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int {
		return run([]string{"launch", "oneinput", "--input", "value=a", "--input", "value=b", "--json"})
	})
	if exitCode == 0 {
		t.Fatalf("launch with --input value given twice should refuse:\n%s", output)
	}
	var result inputsRefusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused || len(result.Duplicate) != 1 || result.Duplicate[0] != "value" {
		t.Fatalf("result = %+v, want a Duplicate refusal naming \"value\"", result)
	}
}

// TestRunLaunchRefusesASensitiveInputViaFlagWithoutEchoingTheValue proves ruling 1: a
// sensitive: true input passed via --input is refused, and the refusal never carries the
// value — only the name.
func TestRunLaunchRefusesASensitiveInputViaFlagWithoutEchoingTheValue(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeInputToolSource(t, "secrettool", "inputs:\n  - name: apiKey\n    required: true\n    sensitive: true\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	secretValue := "fake-secret-value-777"

	output, exitCode := captureStdout(t, func() int {
		return run([]string{"launch", "secrettool", "--input", "apiKey=" + secretValue, "--json"})
	})
	if exitCode == 0 {
		t.Fatalf("launch with a sensitive input via --input should refuse:\n%s", output)
	}
	if strings.Contains(output, secretValue) {
		t.Fatalf("refusal output echoed the sensitive value:\n%s", output)
	}
	var result inputsRefusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused || len(result.Sensitive) != 1 || result.Sensitive[0] != "apiKey" {
		t.Fatalf("result = %+v, want a Sensitive refusal naming \"apiKey\"", result)
	}
}

// TestRunLaunchSucceedsWithASensitiveInputViaFormicInputEnvironmentVariable proves the other
// half of ruling 1: FORMIC_INPUT_<NAME> is accepted for a sensitive input, and the run
// actually completes.
func TestRunLaunchSucceedsWithASensitiveInputViaFormicInputEnvironmentVariable(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeInputToolSource(t, "secrettool2", "inputs:\n  - name: apiKey\n    required: true\n    sensitive: true\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	t.Setenv("FORMIC_INPUT_APIKEY", "fake-secret-value-888")

	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "secrettool2", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("launch with the sensitive input via FORMIC_INPUT_APIKEY exit = %d:\n%s", exitCode, output)
	}
	var result launchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode launch --json: %v\n%s", err, output)
	}
	if result.Outcome != "ok" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v, want a completed ok run", result)
	}
}

// TestRunDescribeReportsInputRequiredSensitiveAndSource proves section B's describe extension:
// each input gains required/sensitive/source, and keeps its declared label/kind (car 10's
// input prompt is built from this shape and needs the label — dropping it would be a loss).
func TestRunDescribeReportsInputRequiredSensitiveAndSource(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeInputToolSource(t, "describetool",
		"inputs:\n  - name: apiKey\n    label: API key\n    kind: text\n    required: true\n    sensitive: true\n  - name: displayName\n    required: false\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "describetool", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, output)
	}
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode describe --json: %v\n%s", err, output)
	}
	if len(result.Inputs) != 2 {
		t.Fatalf("result.Inputs = %+v, want 2 entries", result.Inputs)
	}
	byName := map[string]describedInput{}
	for _, input := range result.Inputs {
		byName[input.Name] = input
	}
	apiKey := byName["apiKey"]
	if !apiKey.Required || !apiKey.Sensitive || apiKey.Source != "env" {
		t.Fatalf("apiKey input = %+v, want required=true sensitive=true source=env", apiKey)
	}
	if apiKey.Label != "API key" || apiKey.Kind != "text" {
		t.Fatalf("apiKey input = %+v, want its declared label/kind carried through", apiKey)
	}
	displayName := byName["displayName"]
	if displayName.Required || displayName.Sensitive || displayName.Source != "flag-or-env" {
		t.Fatalf("displayName input = %+v, want required=false sensitive=false source=flag-or-env", displayName)
	}
	if displayName.Label != "" || displayName.Kind != "" {
		t.Fatalf("displayName input = %+v, want empty label/kind (none declared)", displayName)
	}
}

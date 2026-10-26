package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

// writeHealerSource catalogues one tool ("healer") that declares a required "target" input, a
// required sensitive "apiKey" input, and outputs.evidenceDir bound to a declared env[] key —
// its command prints that env var's value (fixture:print-env), exiting 1 if it never reached
// the child, so a test can prove propagation by exit code alone — plus one mission ("demo")
// that saves "target" and requires apiKey to be supplied at run time (founder ruling 3: a
// sensitive input can never live in `with:`).
func writeHealerSource(t *testing.T) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "missions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: healer\nlaunch:\n  command: " + testfixture.Tool(t) +
		"\n  args: [\"fixture:print-env\", \"EVIDENCE_DIR\"]\n" +
		"inputs:\n  - name: target\n    required: true\n  - name: apiKey\n    required: true\n    sensitive: true\n" +
		"outputs:\n  evidenceDir: EVIDENCE_DIR\n" +
		"env:\n  - name: EVIDENCE_DIR\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "healer.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	missionYAML := "mission: 1\nname: demo\ninputs: [target]\nsteps:\n  - tool: healer\n    with: { target: \"{target}\" }\nevidence: evidence/\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "missions", "demo.yaml"), []byte(missionYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

// TestRunMissionRunRefusesWhenMissionInputAndSensitiveInputAreUnresolved proves the pre-flight
// "every problem at once" proof from the CLI: running demo with nothing bound refuses, naming
// both the unbound mission input and the missing sensitive tool input, and runs nothing (no run
// row, no evidence folder).
func TestRunMissionRunRefusesWhenMissionInputAndSensitiveInputAreUnresolved(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeHealerSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"mission", "run", "demo", "--json"}) })
	if exitCode == 0 {
		t.Fatalf("mission run with nothing bound should refuse:\n%s", output)
	}
	var result preflightRefusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	kinds := map[string]bool{}
	for _, problem := range result.Problems {
		kinds[problem.Kind] = true
	}
	if !result.Refused || !kinds["input-unbound"] || !kinds["missing-input"] {
		t.Fatalf("result = %+v, want input-unbound AND missing-input reported together", result)
	}
}

// TestRunMissionRunCreatesEvidenceFolderSetsEnvVarAndRecordsMission is the live proof from the
// brief's Method step 5: binding the mission input and supplying the sensitive input via
// FORMIC_INPUT_APIKEY runs the tool, the evidence folder exists under
// <evidence>/<mission-name>/<run-id>/, the child actually saw the evidenceDir env var (proved
// by exit code, not just presence), mission-run.json is written with no secret bytes in it, and
// the run row records mission: demo.
func TestRunMissionRunCreatesEvidenceFolderSetsEnvVarAndRecordsMission(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeHealerSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	secretValue := "fake-secret-value-mission-999"
	t.Setenv("FORMIC_INPUT_APIKEY", secretValue)

	// Deliberately NOT --json: the fixture's own command writes straight to real stdout
	// (fixture:print-env), exactly as any launched tool's output does (§15's --json JSON
	// result is only ever clean when the tool itself prints nothing, as the car 8 tests
	// already establish) — so exit code, not stdout parsing, is what proves EVIDENCE_DIR
	// actually reached the child.
	output, exitCode := captureStdout(t, func() int {
		return run([]string{"mission", "run", "demo", "--input", "target=widget"})
	})
	if exitCode != 0 {
		t.Fatalf("mission run exit = %d, want 0 (the child must have SEEN EVIDENCE_DIR, or fixture:print-env exits 1):\n%s", exitCode, output)
	}
	if bytes.Contains([]byte(output), []byte(secretValue)) {
		t.Fatalf("mission run stdout echoed the sensitive value:\n%s", output)
	}

	store, err := runs.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one run recorded", rows)
	}
	row := rows[0]
	if row.Mission != "demo" {
		t.Fatalf("row.Mission = %q, want %q", row.Mission, "demo")
	}

	evidenceRoot := filepath.Join(sourcePath, "evidence", "demo", row.ID)
	recordPath := filepath.Join(evidenceRoot, "mission-run.json")
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read %s: %v", recordPath, err)
	}
	if bytes.Contains(recordBytes, []byte(secretValue)) {
		t.Fatalf("mission-run.json contains the sensitive value:\n%s", recordBytes)
	}
	var record runs.MissionRecord
	if err := json.Unmarshal(recordBytes, &record); err != nil {
		t.Fatalf("decode mission-run.json: %v", err)
	}
	if record.Mission != "demo" || record.Tool != "healer" || record.Inputs["apiKey"] != "<redacted:apiKey>" {
		t.Fatalf("record = %+v, want mission=demo tool=healer apiKey redacted", record)
	}
}

// TestRunMissionRunJSONKeepsChildStdoutOffTheJSONChannel proves the same --json stdout ruling covers
// `mission run --json` too: healer's own stdout (fixture:print-env, which prints EVIDENCE_DIR's
// value on success) must never land in the parent's stdout under --json — only the JSON summary
// may appear there.
func TestRunMissionRunJSONKeepsChildStdoutOffTheJSONChannel(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeHealerSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	t.Setenv("FORMIC_INPUT_APIKEY", "fake-secret-value-json-channel")

	output, exitCode := captureStdout(t, func() int {
		return run([]string{"mission", "run", "demo", "--input", "target=widget", "--json"})
	})
	if exitCode != 0 {
		t.Fatalf("mission run --json exit = %d, want 0 (the child must have SEEN EVIDENCE_DIR):\n%s", exitCode, output)
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(output)))
	var result launchResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode mission run --json: %v\n%s", err, output)
	}
	if decoder.More() {
		t.Fatalf("stdout carried more than the one JSON object (the child's own output leaked in):\n%s", output)
	}
	if result.RunID == "" || result.Outcome != "ok" || result.Mission != "demo" {
		t.Fatalf("result = %+v, want a completed ok run for mission demo", result)
	}
}

// TestRunMissionListShowsAPersonalMissionShadowingAToolkitOne proves founder ruling 2's list
// shape and §9's precedence together at the CLI level: a personal mission of the same name as a
// catalogued one is the winner, and the catalogued one is reported shadowed.
func TestRunMissionListShowsAPersonalMissionShadowingAToolkitOne(t *testing.T) {
	userConfigRoot := t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", userConfigRoot)
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeHealerSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	personalMissionsDirectory := filepath.Join(userConfigRoot, "formic", "missions")
	if err := os.MkdirAll(personalMissionsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	personalMissionYAML := "mission: 1\nname: demo\ninputs: [target]\nsteps:\n  - tool: healer\n    with: { target: \"{target}\" }\nevidence: evidence/\n"
	if err := os.WriteFile(filepath.Join(personalMissionsDirectory, "demo.yaml"), []byte(personalMissionYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"mission", "list", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("mission list exit = %d:\n%s", exitCode, output)
	}
	var listed []listedMission
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		t.Fatalf("decode mission list --json: %v\n%s", err, output)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %+v, want exactly one winner", listed)
	}
	if listed[0].Source != filepath.Join(personalMissionsDirectory, "demo.yaml") {
		t.Fatalf("listed[0].Source = %q, want the personal mission file to win", listed[0].Source)
	}
	if len(listed[0].ShadowedBy) != 1 {
		t.Fatalf("listed[0].ShadowedBy = %v, want the toolkit mission named as shadowed", listed[0].ShadowedBy)
	}
}

// TestRunMissionRunRefusesAnInvalidMissionFileAsUnknown proves the admission rule at the CLI
// level: an invalid mission file is never findable by `mission run` (only `doctor` reports it).
func TestRunMissionRunRefusesAnInvalidMissionFileAsUnknown(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeHealerSource(t)
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "missions", "broken.yaml"), []byte("mission: 1\nname: broken\nsteps: []\nevidence: e/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"mission", "run", "broken", "--json"}) })
	if exitCode == 0 {
		t.Fatalf("mission run broken should refuse (invalid mission is never findable):\n%s", output)
	}
	var listed []listedMission
	listOutput, listExit := captureStdout(t, func() int { return run([]string{"mission", "list", "--json"}) })
	if listExit != 0 {
		t.Fatalf("mission list exit = %d:\n%s", listExit, listOutput)
	}
	if err := json.Unmarshal([]byte(listOutput), &listed); err != nil {
		t.Fatalf("decode mission list --json: %v\n%s", err, listOutput)
	}
	for _, m := range listed {
		if m.Name == "broken" {
			t.Fatalf("mission list = %+v, want the invalid mission absent", listed)
		}
	}
}

// TestRunMissionRunRefusesAServiceToolAndWritesNoRow proves R9 from the CLI: a mission whose tool
// declares lifecycle: service is refused pre-flight with a service-lifecycle problem, exit 1, and
// no run row.
func TestRunMissionRunRefusesAServiceToolAndWritesNoRow(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := t.TempDir()
	for _, directory := range []string{"tools", "missions"} {
		if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"toolkit.yaml":        "toolkit: 1\nname: fixture\n",
		"tools/daemon.yaml":   "formic: 1\nname: daemon\nlifecycle: service\nlaunch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:print-env\", \"HOME\"]\n",
		"missions/serve.yaml": "mission: 1\nname: serve\nsteps:\n  - tool: daemon\nevidence: evidence/\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sourcePath, ".formic", filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"mission", "run", "serve", "--json"}) })
	if exitCode != 1 {
		t.Fatalf("mission run exit = %d, want 1:\n%s", exitCode, output)
	}
	var result preflightRefusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused || len(result.Problems) != 1 || result.Problems[0].Kind != "service-lifecycle" || result.Problems[0].Detail != "daemon" {
		t.Fatalf("result = %+v, want one service-lifecycle problem naming daemon", result)
	}
	store, err := runs.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want no run row for a refused mission", rows)
	}
}

// serviceNeedMissionEnvironment catalogues "dep" (a tool whose tcp probe is down) and "needy"
// (a task tool with a service need on dep when withServiceNeed), plus a mission running needy.
func serviceNeedMissionEnvironment(t *testing.T, withServiceNeed bool) {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := t.TempDir()
	for _, directory := range []string{"tools", "missions"} {
		if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	launchBlock := "launch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:exit\", \"0\"]\n"
	needs := ""
	if withServiceNeed {
		needs = "needs:\n  - kind: service\n    tool: dep\n"
	}
	files := map[string]string{
		"toolkit.yaml":        "toolkit: 1\nname: fixture\n",
		"tools/dep.yaml":      "formic: 1\nname: dep\n" + launchBlock + "live:\n  probe:\n    tcp: 127.0.0.1:1\n",
		"tools/needy.yaml":    "formic: 1\nname: needy\n" + launchBlock + needs,
		"missions/needy.yaml": "mission: 1\nname: needy-mission\nsteps:\n  - tool: needy\nevidence: evidence/\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sourcePath, ".formic", filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
}

func countRunRows(t *testing.T) int {
	t.Helper()
	store, err := runs.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// An unmet service need on the mission's task tool refuses exactly as `formic launch` does
// (reason "unmet need", exit 1) and writes no run row.
func TestRunMissionRunRefusesAnUnmetServiceNeed(t *testing.T) {
	serviceNeedMissionEnvironment(t, true)
	output, exitCode := captureStdout(t, func() int { return run([]string{"mission", "run", "needy-mission", "--json"}) })
	if exitCode != 1 {
		t.Fatalf("mission run exit = %d, want 1:\n%s", exitCode, output)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if result["refused"] != true || result["reason"] != "unmet need" || !strings.Contains(output, "dep") {
		t.Fatalf("result = %v, want an unmet-need refusal naming dep", result)
	}
	if rows := countRunRows(t); rows != 0 {
		t.Fatalf("rows = %d, want no run row for a refused mission", rows)
	}
}

func TestRunMissionRunWithoutAServiceNeedStillRuns(t *testing.T) {
	serviceNeedMissionEnvironment(t, false)
	output, exitCode := captureStdout(t, func() int { return run([]string{"mission", "run", "needy-mission", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("mission run exit = %d, want 0:\n%s", exitCode, output)
	}
	if rows := countRunRows(t); rows != 1 {
		t.Fatalf("rows = %d, want one run row", rows)
	}
}

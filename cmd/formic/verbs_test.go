package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/repository"
	"github.com/hive-controls/formic/internal/testfixture"
)

// captureStdout runs action with os.Stdout redirected into a pipe and returns everything
// written plus action's own return value.
func captureStdout(t *testing.T, action func() int) (string, int) {
	t.Helper()
	originalStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	exitCode := action()
	writeEnd.Close()
	os.Stdout = originalStdout
	var captured bytes.Buffer
	if _, err := io.Copy(&captured, readEnd); err != nil {
		t.Fatal(err)
	}
	return captured.String(), exitCode
}

func writeGreetSource(t *testing.T) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: greet\nlaunch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:status\", \"0\", \"ok\"]\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "greet.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

// writeStdoutPrinterSource is writeGreetSource's twin, wired to fixturetool's `fixture:pwd`
// subcommand instead of `fixture:status` — that subcommand prints a line to its OWN stdout
// (the working directory) rather than the status-file protocol, standing in for a launched
// tool that talks to its stdout directly.
func writeStdoutPrinterSource(t *testing.T) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: chatty\nlaunch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:pwd\"]\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "chatty.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

// TestRunLaunchJSONKeepsChildStdoutOffTheJSONChannel proves the orchestrator's ruling: under
// `--json` the launched tool's own stdout must never land in the parent's stdout — only the
// JSON summary object may appear there. Before the fix, the child (fixturetool `fixture:pwd`,
// which prints its working directory) shared os.Stdout with the parent, so the captured
// stream held the child's line before the JSON object and failed to decode as a single value.
func TestRunLaunchJSONKeepsChildStdoutOffTheJSONChannel(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeStdoutPrinterSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "chatty", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("launch --json exit = %d:\n%s", exitCode, output)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	var result launchResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode launch --json: %v\n%s", err, output)
	}
	if decoder.More() {
		t.Fatalf("stdout carried more than the one JSON object (the child's own output leaked in):\n%s", output)
	}
	if result.RunID == "" || result.Outcome != "ok" {
		t.Fatalf("result = %+v, want a completed ok run", result)
	}
}

// TestRunListJSONReportsUncataloguedThenTheAddedSource is Step D's proof 5: `list --json`
// from cwd = this worktree with an empty catalog names the workshop's own toolkit as
// uncatalogued; after `add`-ing it, the same tool set discovery would find directly is
// listed and `uncatalogued` goes empty.
func TestRunListJSONReportsUncataloguedThenTheAddedSource(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	liveRoot, err := repository.FindRoot(".")
	if err != nil {
		t.Fatalf("find live repository root: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(liveRoot, ".formic", "toolkit.yaml")); os.IsNotExist(statErr) {
		t.Skip("no workshop .formic/toolkit.yaml here (expected outside the private workshop)")
	}

	before, exitCode := captureStdout(t, func() int { return run([]string{"list", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("list --json before add exit = %d:\n%s", exitCode, before)
	}
	var beforeResult listResult
	if err := json.Unmarshal([]byte(before), &beforeResult); err != nil {
		t.Fatalf("decode list --json before add: %v\n%s", err, before)
	}
	if len(beforeResult.Tools) != 0 {
		t.Fatalf("beforeResult.Tools = %d, want 0 (empty catalog)", len(beforeResult.Tools))
	}
	expectedToolkit, err := catalog.DescriptorAt(liveRoot)
	if err != nil {
		t.Fatalf("read the live repository's own toolkit descriptor: %v", err)
	}
	if beforeResult.Uncatalogued == nil || beforeResult.Uncatalogued.Name != expectedToolkit.Name {
		t.Fatalf("beforeResult.Uncatalogued = %+v, want the workshop's own toolkit name %q", beforeResult.Uncatalogued, expectedToolkit.Name)
	}

	if exitCode := run([]string{"add", liveRoot}); exitCode != 0 {
		t.Fatalf("add of the live repository root exit = %d, want 0", exitCode)
	}

	after, exitCode := captureStdout(t, func() int { return run([]string{"list", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("list --json after add exit = %d:\n%s", exitCode, after)
	}
	var afterResult listResult
	if err := json.Unmarshal([]byte(after), &afterResult); err != nil {
		t.Fatalf("decode list --json after add: %v\n%s", err, after)
	}
	if afterResult.Uncatalogued != nil {
		t.Fatalf("afterResult.Uncatalogued = %+v, want nil (the source is now catalogued)", afterResult.Uncatalogued)
	}
	// Compared against Discover run directly on the same source, never a hand-typed count.
	directCatalog, err := discovery.Discover([]string{liveRoot})
	if err != nil {
		t.Fatalf("Discover on the live repository returned an error: %v", err)
	}
	if len(afterResult.Tools) != len(directCatalog.Tools) {
		t.Fatalf("afterResult.Tools = %d, want %d (Discover's own count for this source)", len(afterResult.Tools), len(directCatalog.Tools))
	}
}

// TestRunDescribeReportsRealEquippedStatusForEveryNeed proves car 7's real behaviour: describe
// no longer invents a fixed "unknown" — it reads the machine's equip file and reports each
// need's real met/unmet status, with a reason when unmet.
func TestRunDescribeReportsRealEquippedStatusForEveryNeed(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: needy\nlaunch:\n  command: echo\nneeds:\n  - kind: secret\n    name: SOME_KEY\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "needy.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "needy", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, output)
	}
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode describe --json: %v\n%s", err, output)
	}
	if len(result.Needs) != 1 || result.Needs[0].Equipped {
		t.Fatalf("result.Needs = %+v, want exactly one unmet need (nothing equipped yet)", result.Needs)
	}
	if result.Needs[0].Reason == "" {
		t.Fatal("an unmet need should name why")
	}

	if exitCode := run([]string{"equip", "secret", "SOME_KEY", "env.SOME_KEY"}); exitCode != 0 {
		t.Fatalf("equip secret exit = %d, want 0", exitCode)
	}
	t.Setenv("SOME_KEY", "irrelevant-value")

	afterOutput, exitCode := captureStdout(t, func() int { return run([]string{"describe", "needy", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, afterOutput)
	}
	var afterResult describeResult
	if err := json.Unmarshal([]byte(afterOutput), &afterResult); err != nil {
		t.Fatalf("decode describe --json: %v\n%s", err, afterOutput)
	}
	if len(afterResult.Needs) != 1 || !afterResult.Needs[0].Equipped {
		t.Fatalf("afterResult.Needs = %+v, want the need now reported met", afterResult.Needs)
	}
}

// TestRunLaunchRunsATrivialToolAndReportsOutcome proves `formic launch` reuses the same
// launch/runs packages the deck uses: it actually runs the tool and reports a completed
// row's run id, outcome, and exit code.
func TestRunLaunchRunsATrivialToolAndReportsOutcome(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := writeGreetSource(t)
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "greet", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("launch --json exit = %d:\n%s", exitCode, output)
	}
	var result launchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode launch --json: %v\n%s", err, output)
	}
	if result.RunID == "" || result.Outcome != "ok" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v, want a completed ok run with exit code 0", result)
	}
}

// TestRunLaunchRefusesOnUnmetNeedAndWritesNoRunRow proves §13/§15's needs-resolution step:
// an unmet need refuses `launch` with a structured object naming every unmet need and a
// hint, exits non-zero, and — critically — never writes a run row at all.
func TestRunLaunchRefusesOnUnmetNeedAndWritesNoRunRow(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDir := t.TempDir()
	t.Setenv("FORMIC_STATE_DIR", stateDir)
	sourcePath := writeNeedySource(t, "needy", "needs:\n  - kind: secret\n    name: NEVER_EQUIPPED\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "needy", "--json"}) })
	if exitCode == 0 {
		t.Fatalf("launch with an unmet need should refuse:\n%s", output)
	}
	var result needsRefusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused || result.Reason != "unmet need" || len(result.Needs) != 1 || result.Hint == "" {
		t.Fatalf("result = %+v, want a structured unmet-need refusal with a hint", result)
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("state dir entries = %v, want no run row written on a needs refusal", entries)
	}
}

// TestRunLaunchDeliversTheChosenGateProviderToTheDeclaredEnvName proves the other half of
// §13's gate contract: a met gate need's chosen provider reaches the child as
// <env>=<provider>, through the manifest's own declared env[] scoping.
func TestRunLaunchDeliversTheChosenGateProviderToTheDeclaredEnvName(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: gated\nlaunch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:print-env\", \"E2E_DOCTOR_GATE\"]\n" +
		"env:\n  - name: E2E_DOCTOR_GATE\n" +
		"needs:\n  - kind: gate\n    class: browser\n    env: E2E_DOCTOR_GATE\n    providers: [solari, local]\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "gated.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	if exitCode := run([]string{"equip", "provider", "gate.browser", "solari"}); exitCode != 0 {
		t.Fatalf("equip provider exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "gated"}) })
	if exitCode != 0 {
		t.Fatalf("launch exit = %d, want 0 (the gate need is met):\n%s", exitCode, output)
	}
	if !strings.Contains(output, "solari") {
		t.Fatalf("output = %q, want the child to have received E2E_DOCTOR_GATE=solari", output)
	}
}

// TestRunLaunchRefusesAnUnknownTool proves the structured-refusal contract (§15): an
// unknown tool refuses with a reason, exit non-zero, and nothing is launched.
func TestRunLaunchRefusesAnUnknownTool(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	output, exitCode := captureStdout(t, func() int { return run([]string{"launch", "does-not-exist", "--json"}) })
	if exitCode == 0 {
		t.Fatalf("launch of an unknown tool should refuse:\n%s", output)
	}
	var result refusal
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode refusal JSON: %v\n%s", err, output)
	}
	if !result.Refused || result.Reason == "" {
		t.Fatalf("result = %+v, want a structured refusal naming the reason", result)
	}
}

func addArgumentDeclaringSource(t *testing.T) {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: argtool\nlaunch:\n  command: echo\n" +
		"arguments:\n  - flag: --healer\n    kind: choice\n    default: a\n    options:\n      - value: a\n      - value: b\n" +
		"  - flag: --write\n    kind: flag\n" +
		"env:\n  - name: MODE\n    options:\n      - value: fast\n      - value: slow\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "argtool.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
}

// Guard test: green at birth, because the manifest types already carry the fields and describe
// --json embeds the manifest verbatim.
func TestRunDescribeJSONCarriesArgumentsAndEnvOptions(t *testing.T) {
	addArgumentDeclaringSource(t)
	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "argtool", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, output)
	}
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode describe --json: %v\n%s", err, output)
	}
	arguments := result.Manifest.Arguments
	if len(arguments) != 2 || arguments[0].Flag != "--healer" || arguments[0].Kind != formats.ArgumentKindChoice || len(arguments[0].Options) != 2 {
		t.Fatalf("manifest.arguments = %+v", arguments)
	}
	if options := result.Manifest.Env[0].Options; len(options) != 2 || options[0].Value != "fast" {
		t.Fatalf("manifest.env[0].options = %+v", options)
	}
}

func TestRunDescribeTextListsArgumentsAndEnvOptions(t *testing.T) {
	addArgumentDeclaringSource(t)
	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "argtool"}) })
	if exitCode != 0 {
		t.Fatalf("describe exit = %d:\n%s", exitCode, output)
	}
	for _, want := range []string{
		"argument: --healer kind=choice options=[a b] default=a",
		"argument: --write kind=flag",
		"env option: MODE [fast slow]",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("describe text missing %q:\n%s", want, output)
		}
	}
}

func TestFormatExitCodePrintsTheValueNotThePointer(t *testing.T) {
	zero := 0
	if got := formatExitCode(&zero); got != "0" {
		t.Fatalf("formatExitCode(&0) = %q, want %q", got, "0")
	}
	if got := formatExitCode(nil); got != "unknown" {
		t.Fatalf("formatExitCode(nil) = %q, want %q", got, "unknown")
	}
}

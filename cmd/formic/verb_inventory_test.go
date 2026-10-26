package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/pane"
)

// TestRunInventoryReportsProvidersAgentsRuntimesAndTerminal is a shape proof for
// `formic inventory --json` (§15, N11): declared gate providers surface with the equip
// file's chosen provider, and the fixed agent/runtime lists are all reported (present or not).
func TestRunInventoryReportsProvidersAgentsRuntimesAndTerminal(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	sourcePath := writeNeedySource(t, "gated", "needs:\n  - kind: gate\n    class: browser\n    env: E2E_DOCTOR_GATE\n    providers: [solari, local]\n")
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d, want 0", exitCode)
	}
	if exitCode := run([]string{"equip", "provider", "gate.browser", "solari"}); exitCode != 0 {
		t.Fatalf("equip provider exit = %d, want 0", exitCode)
	}

	output, exitCode := captureStdout(t, func() int { return run([]string{"inventory", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("inventory --json exit = %d:\n%s", exitCode, output)
	}
	var result inventoryResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode inventory --json: %v\n%s", err, output)
	}
	if len(result.Providers) != 2 {
		t.Fatalf("result.Providers = %+v, want both solari and local listed", result.Providers)
	}
	foundChosen := false
	for _, provider := range result.Providers {
		if provider.Name == "solari" {
			if provider.Chosen != "solari" || !provider.Usable || provider.Probe != "declared" {
				t.Fatalf("solari provider = %+v, want chosen=solari usable=true probe=declared", provider)
			}
			foundChosen = true
		}
	}
	if !foundChosen {
		t.Fatal("expected the solari provider entry")
	}

	agentNames := make([]string, 0, len(result.Agents))
	for _, agent := range result.Agents {
		agentNames = append(agentNames, agent.Name)
	}
	sort.Strings(agentNames)
	if len(agentNames) != len(inventoryAgentNames) {
		t.Fatalf("result.Agents names = %v, want exactly the fixed agent list", agentNames)
	}

	runtimeNames := make([]string, 0, len(result.Runtimes))
	for _, runtimeComponent := range result.Runtimes {
		runtimeNames = append(runtimeNames, runtimeComponent.Name)
	}
	sort.Strings(runtimeNames)
	if len(runtimeNames) != len(inventoryRuntimeNames) {
		t.Fatalf("result.Runtimes names = %v, want exactly the fixed runtime list", runtimeNames)
	}

	foundGo := false
	for _, runtimeComponent := range result.Runtimes {
		if runtimeComponent.Name == "go" {
			if !runtimeComponent.Present {
				t.Fatal("go should be present (this suite's own toolchain)")
			}
			foundGo = true
		}
	}
	if !foundGo {
		t.Fatal("expected a go runtime entry")
	}
}

// TestVerifiedTerminalRoutesTablePinsExactlyTheHandExercisedPairs is ruling 1's ledger test: only
// the Windows Terminal tab (hand-verified 2026-09-18), the Windows Terminal split
// (car 10's own live proof), and the Ghostty, Terminal.app and cmux tab routes (observed live
// 2026-10-15) are marked verified — every other (terminal, route) pair a person
// could name defaults to false, and this table is the ONLY place that can ever change. A new
// entry added here without a live hand-exercise to back it is exactly the failure mode ruling 1
// exists to prevent.
func TestVerifiedTerminalRoutesTablePinsExactlyTheHandExercisedPairs(t *testing.T) {
	want := map[terminalRoute]bool{
		{launch.TerminalWindows, "tab"}:   true,
		{launch.TerminalWindows, "split"}: true,
		{launch.TerminalGhostty, "tab"}:   true,
		{launch.TerminalApple, "tab"}:     true,
		{launch.TerminalCmux, "tab"}:      true,
	}
	if len(verifiedTerminalRoutes) != len(want) {
		t.Fatalf("verifiedTerminalRoutes = %+v, want exactly %+v", verifiedTerminalRoutes, want)
	}
	for pair, verified := range want {
		if verifiedTerminalRoutes[pair] != verified {
			t.Fatalf("verifiedTerminalRoutes[%+v] = %v, want %v", pair, verifiedTerminalRoutes[pair], verified)
		}
	}
	// Every pair not in the ledger must read false, not merely absent, since
	// inventoryTerminalReport reads this map with a plain index expression.
	if verifiedTerminalRoutes[terminalRoute{launch.TerminalTmux, "split"}] {
		t.Fatal("tmux split must default to unverified — it has never been hand-exercised")
	}
}

// TestInventoryTerminalReportMarksRoutesVerifiedPerTheLedger proves inventoryTerminalReport
// itself consults the ledger rather than hardcoding true/false: a detected Windows Terminal
// reports tab AND split both verified (per the table above), and foreground — which carries no
// host-terminal argv at all — is always verified regardless of what terminal is detected. pane
// is asserted separately (TestInventoryTerminalReportMarksPaneVerifiedOnlyWhenThisBuildCanHostOne)
// since founder ruling 6 makes its verified status platform-dependent, not a constant true.
func TestInventoryTerminalReportMarksRoutesVerifiedPerTheLedger(t *testing.T) {
	// TMUX is cleared explicitly: DetectTerminal checks it before WT_SESSION, and t.Setenv
	// never unsets a variable this test process's real environment already carries.
	t.Setenv("TMUX", "")
	t.Setenv("WT_SESSION", "session")
	report := inventoryTerminalReport()
	byRoute := make(map[string]bool, len(report.Routes))
	for _, route := range report.Routes {
		byRoute[route.Route] = route.Verified
	}
	for _, route := range []string{"foreground", "tab", "split"} {
		verified, present := byRoute[route]
		if !present {
			t.Fatalf("report.Routes = %+v, missing route %q", report.Routes, route)
		}
		if !verified {
			t.Fatalf("route %q verified = false, want true for a detected Windows Terminal", route)
		}
	}
}

// TestInventoryTerminalReportMarksPaneVerifiedOnlyWhenThisBuildCanHostOne is founder ruling 6's
// regression test: `formic inventory` must never claim the pane route is
// verified on a platform where the deck itself refuses to offer pane at all
// (paneUnavailableReason in internal/ui consults exactly this same pane.CanHostPane() — the
// inventory report must agree with the deck's own refusal, not hardcode true). On Windows,
// pane.CanHostPane() is false (internal/pane/pty_windows.go), so this asserts pane reports
// verified=false there; on a platform where hosting a pane is possible, it asserts true —
// the SAME boolean the deck's mode chooser already keys off, never a duplicated constant.
func TestInventoryTerminalReportMarksPaneVerifiedOnlyWhenThisBuildCanHostOne(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("WT_SESSION", "")
	report := inventoryTerminalReport()
	var paneVerified bool
	var found bool
	for _, route := range report.Routes {
		if route.Route == "pane" {
			paneVerified, found = route.Verified, true
		}
	}
	if !found {
		t.Fatalf("report.Routes = %+v, missing the pane route", report.Routes)
	}
	if paneVerified != pane.CanHostPane() {
		t.Fatalf("pane route verified = %v, want %v (pane.CanHostPane(), the same boolean the deck's mode chooser consults)", paneVerified, pane.CanHostPane())
	}
}

// TestInventoryTerminalReportOmitsSplitWhenTheDetectedTerminalHasNoSplitCommand proves split is
// left out of the routes list entirely (never listed and marked unverified) when the detected
// terminal has no split command at all — mirroring how tab is already left out for an
// undetected terminal.
func TestInventoryTerminalReportOmitsSplitWhenTheDetectedTerminalHasNoSplitCommand(t *testing.T) {
	// TMUX and WT_SESSION are cleared explicitly: both are ahead of KITTY_WINDOW_ID in
	// DetectTerminal's priority order, and t.Setenv never unsets whatever this test process's
	// real environment (e.g. a Windows Terminal session) already carries.
	t.Setenv("TMUX", "")
	t.Setenv("WT_SESSION", "")
	t.Setenv("KITTY_WINDOW_ID", "1")
	report := inventoryTerminalReport()
	for _, route := range report.Routes {
		if route.Route == "split" {
			t.Fatalf("report.Routes = %+v, split should not be offered for a terminal with no split command", report.Routes)
		}
	}
}

// TestInventoryWritesNoFile is car 7's REQUIRED test (N11 "Commits us to: a test that
// `inventory` writes no file"): the user-config dir, the state dir, and cwd are all
// byte-identical before and after `formic inventory`.
func TestInventoryWritesNoFile(t *testing.T) {
	configDir := t.TempDir()
	stateDir := t.TempDir()
	workingDirectory := t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	t.Setenv("FORMIC_STATE_DIR", stateDir)

	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })

	// Seed a non-empty equip file so Load has something real to read, proving inventory does
	// not even rewrite an existing file, not merely that an absent one stays absent.
	if exitCode := run([]string{"equip", "secret", "SEED", "env.SEED"}); exitCode != 0 {
		t.Fatalf("seed equip secret exit = %d, want 0", exitCode)
	}

	before := snapshotDirectory(t, configDir)
	beforeState := snapshotDirectory(t, stateDir)
	beforeCwd := snapshotDirectory(t, workingDirectory)

	if exitCode := run([]string{"inventory", "--json"}); exitCode != 0 {
		t.Fatalf("inventory --json exit = %d, want 0", exitCode)
	}

	after := snapshotDirectory(t, configDir)
	afterState := snapshotDirectory(t, stateDir)
	afterCwd := snapshotDirectory(t, workingDirectory)

	assertSameSnapshot(t, "config dir", before, after)
	assertSameSnapshot(t, "state dir", beforeState, afterState)
	assertSameSnapshot(t, "cwd", beforeCwd, afterCwd)
}

type fileSnapshot struct {
	path string
	hash [32]byte
}

func snapshotDirectory(t *testing.T, root string) []fileSnapshot {
	t.Helper()
	var snapshots []fileSnapshot
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relativePath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		snapshots = append(snapshots, fileSnapshot{path: relativePath, hash: sha256.Sum256(data)})
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].path < snapshots[j].path })
	return snapshots
}

func assertSameSnapshot(t *testing.T, label string, before, after []fileSnapshot) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s: file count changed from %d to %d", label, len(before), len(after))
	}
	for index := range before {
		if before[index].path != after[index].path {
			t.Fatalf("%s: file list changed: %+v -> %+v", label, before, after)
		}
		if before[index].hash != after[index].hash {
			t.Fatalf("%s: %s content changed", label, before[index].path)
		}
	}
}

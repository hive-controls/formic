package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

// writeActionsSource catalogues a tool "svc" with three actions: stop (confirming), start
// (a service) and ping (a plain task), plus an action-less "plain" tool.
func writeActionsSource(t *testing.T) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := testfixture.Tool(t)
	svc := "formic: 1\nname: svc\nlaunch:\n  command: " + fixture + "\n  args: [\"fixture:status\", \"0\", \"base\"]\n" +
		"actions:\n" +
		"  - name: stop\n    confirm: Stops everything.\n    command: " + fixture + "\n    args: [\"fixture:status\", \"0\", \"stopped\"]\n" +
		"  - name: start\n    lifecycle: service\n" +
		"  - name: ping\n    command: " + fixture + "\n    args: [\"fixture:status\", \"0\", \"pinged\"]\n"
	plain := "formic: 1\nname: plain\nlaunch:\n  command: " + fixture + "\n  args: [\"fixture:status\", \"0\", \"ok\"]\n"
	for name, content := range map[string]string{"svc.yaml": svc, "plain.yaml": plain} {
		if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sourcePath
}

func actionsEnvironment(t *testing.T) string {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDirectory := t.TempDir()
	t.Setenv("FORMIC_STATE_DIR", stateDirectory)
	for _, name := range []string{"TMUX", "WT_SESSION", "KITTY_WINDOW_ID", "WEZTERM_PANE", "CMUX_BUNDLE_ID", "GHOSTTY_SURFACE_ID", "TERM_PROGRAM", "GNOME_TERMINAL_SERVICE"} {
		t.Setenv(name, "")
	}
	if exitCode := run([]string{"add", writeActionsSource(t)}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
	return stateDirectory
}

func launchJSON(t *testing.T, arguments ...string) (map[string]any, int) {
	t.Helper()
	output, exitCode := captureStdout(t, func() int { return run(append([]string{"launch"}, arguments...)) })
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("launch %v: decode %q: %v", arguments, output, err)
	}
	return decoded, exitCode
}

func assertNoRunRows(t *testing.T, stateDirectory string) {
	t.Helper()
	rows, err := runs.Store{StateDirectory: stateDirectory}.List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("run rows = %d (%v), want none for a refused launch", len(rows), err)
	}
}

func TestLaunchRefusesAMultiActionToolWithoutAnActionAndListsThem(t *testing.T) {
	stateDirectory := actionsEnvironment(t)
	result, exitCode := launchJSON(t, "svc", "--json")
	actions, _ := result["actions"].([]any)
	if exitCode != 1 || result["reason"] != "action required" || len(actions) != 3 {
		t.Fatalf("exit %d result %v, want exit 1 'action required' listing 3 actions", exitCode, result)
	}
	assertNoRunRows(t, stateDirectory)
}

func TestLaunchRefusesUnknownAndMisplacedActions(t *testing.T) {
	stateDirectory := actionsEnvironment(t)
	result, exitCode := launchJSON(t, "svc", "nope", "--json")
	if exitCode != 1 || result["reason"] != "unknown action" {
		t.Fatalf("unknown action: exit %d %v", exitCode, result)
	}
	result, exitCode = launchJSON(t, "plain", "start", "--json")
	if exitCode != 1 || result["reason"] != "plain declares no actions" {
		t.Fatalf("action on an action-less tool: exit %d %v", exitCode, result)
	}
	assertNoRunRows(t, stateDirectory)
}

func TestLaunchRefusesAConfirmingActionWithoutYesThenRunsWithIt(t *testing.T) {
	stateDirectory := actionsEnvironment(t)
	result, exitCode := launchJSON(t, "svc", "stop", "--json")
	if exitCode != 1 || result["reason"] != "confirmation required" || result["confirm"] != "Stops everything." {
		t.Fatalf("exit %d %v, want a confirmation refusal carrying the sentence", exitCode, result)
	}
	assertNoRunRows(t, stateDirectory)
	result, exitCode = launchJSON(t, "svc", "stop", "--yes", "--json")
	if exitCode != 0 || result["action"] != "stop" || result["outcome"] != "ok" {
		t.Fatalf("exit %d %v, want an ok run recording action stop", exitCode, result)
	}
	rows, _ := runs.Store{StateDirectory: stateDirectory}.List()
	if len(rows) != 1 || rows[0].Action != "stop" {
		t.Fatalf("rows = %+v, want one row with Action stop", rows)
	}
}

func TestLaunchRefusesAServiceWithoutDetachAndWithoutATerminal(t *testing.T) {
	stateDirectory := actionsEnvironment(t)
	result, exitCode := launchJSON(t, "svc", "start", "--json")
	if exitCode != 1 || !strings.Contains(result["reason"].(string), "--detach") {
		t.Fatalf("service without --detach: exit %d %v", exitCode, result)
	}
	result, exitCode = launchJSON(t, "svc", "start", "--detach", "--json")
	if exitCode != 1 || result["reason"] != "no host terminal to detach into" {
		t.Fatalf("--detach with no terminal: exit %d %v, want a refusal, never a foreground fallback", exitCode, result)
	}
	assertNoRunRows(t, stateDirectory)
}

func TestLaunchRunsAPlainActionAndRejectsAThirdPositional(t *testing.T) {
	actionsEnvironment(t)
	result, exitCode := launchJSON(t, "svc", "ping", "--json")
	if exitCode != 0 || result["action"] != "ping" {
		t.Fatalf("exit %d %v", exitCode, result)
	}
	if exitCode := run([]string{"launch", "svc", "ping", "extra"}); exitCode != 2 {
		t.Fatalf("third positional exit = %d, want 2", exitCode)
	}
}

// writeReadySource catalogues the task "ready" (exit code from fixtureExit) whose live probe
// asks serverURL and whose ready window is within.
func writeReadySource(t *testing.T, serverURL, fixtureExit, within string) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := "formic: 1\nname: ready\nlaunch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:exit\", \"" + fixtureExit + "\"]\n" +
		"live:\n  probe:\n    http: " + serverURL + "\n  ready:\n    within: " + within + "\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "ready.yaml"), []byte(tool), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

func readyEnvironment(t *testing.T, server *httptest.Server, fixtureExit, within string) string {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDirectory := t.TempDir()
	t.Setenv("FORMIC_STATE_DIR", stateDirectory)
	launch.ReadyPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { launch.ReadyPollInterval = time.Second })
	if exitCode := run([]string{"add", writeReadySource(t, server.URL, fixtureExit, within)}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
	return stateDirectory
}

func TestLaunchTaskWithReadyWaitsAndReportsReadyTrue(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 3 { // pre-launch probe plus two ready polls
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	readyEnvironment(t, server, "0", "10s")
	result, exitCode := launchJSON(t, "ready", "--json")
	if exitCode != 0 || result["ready"] != true || result["outcome"] != "ok" {
		t.Fatalf("exit %d %v, want exit 0, outcome ok, ready true", exitCode, result)
	}
	if hits.Load() < 4 {
		t.Fatalf("probe hit %d times, want the wait to poll until it answered", hits.Load())
	}
}

func TestLaunchReadyFalseKeepsTheRunsOwnExitCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	stateDirectory := readyEnvironment(t, server, "0", "300ms")
	result, exitCode := launchJSON(t, "ready", "--json")
	if exitCode != 0 || result["ready"] != false || result["outcome"] != "ok" {
		t.Fatalf("exit %d %v, want exit 0, outcome ok, ready false (R5)", exitCode, result)
	}
	rows, _ := runs.Store{StateDirectory: stateDirectory}.List()
	if len(rows) != 1 || rows[0].Outcome != "ok" {
		t.Fatalf("rows = %+v, want one ok row", rows)
	}
	output, _ := captureStdout(t, func() int { return run([]string{"launch", "ready"}) })
	if !strings.Contains(output, " · not ready within 300ms") {
		t.Fatalf("text output %q lacks the not-ready suffix", output)
	}
}

func TestLaunchFailedTaskDoesNotWait(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	readyEnvironment(t, server, "1", "10s")
	result, exitCode := launchJSON(t, "ready", "--json")
	if _, present := result["ready"]; present || exitCode != 1 {
		t.Fatalf("exit %d %v, want exit 1 and no ready key", exitCode, result)
	}
	if hits.Load() != 1 {
		t.Fatalf("probe hit %d times, want only the pre-launch probe", hits.Load())
	}
}

func TestLaunchWithoutAReadyWindowHasNoReadyKey(t *testing.T) {
	actionsEnvironment(t)
	result, exitCode := launchJSON(t, "plain", "--json")
	if _, present := result["ready"]; present || exitCode != 0 {
		t.Fatalf("exit %d %v, want no ready key", exitCode, result)
	}
}

func TestReadySuffixWording(t *testing.T) {
	yes, no := true, false
	if readySuffix(nil, 0) != "" || readySuffix(&yes, time.Second) != " · ready" || readySuffix(&no, 2*time.Minute) != " · not ready within 2m0s" {
		t.Fatal("unexpected suffix wording")
	}
}

// anywayEnvironment catalogues "svc" (whenUp: skip, an http probe at serverURL) and "needy",
// the same tool with a service need on "dep", whose probe is down.
func anywayEnvironment(t *testing.T, serverURL string) string {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDirectory := t.TempDir()
	t.Setenv("FORMIC_STATE_DIR", stateDirectory)
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launchBlock := "launch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:exit\", \"0\"]\n"
	live := "live:\n  whenUp: skip\n  probe:\n    http: " + serverURL + "\n"
	tools := map[string]string{
		"svc.yaml":   "formic: 1\nname: svc\n" + launchBlock + live,
		"dep.yaml":   "formic: 1\nname: dep\n" + launchBlock + "live:\n  probe:\n    tcp: 127.0.0.1:1\n",
		"needy.yaml": "formic: 1\nname: needy\n" + launchBlock + live + "needs:\n  - kind: service\n    tool: dep\n",
	}
	for name, content := range tools {
		if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
	return stateDirectory
}

func TestLaunchAnywayIsNotMistakenForAnAction(t *testing.T) {
	parsed, ok := parseLaunchArguments([]string{"svc", "--anyway", "--json"})
	if !ok || parsed.tool != "svc" || parsed.action != "" || !parsed.anyway || !parsed.jsonOutput {
		t.Fatalf("parsed %+v ok=%v, want tool svc, no action, anyway and json set", parsed, ok)
	}
	if _, ok := parseLaunchArguments([]string{"svc", "start", "--anyway"}); !ok {
		t.Fatal("--anyway must not count as a third positional")
	}
}

func TestLaunchAnywaySpawnsPastAnAlreadyUpProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	stateDirectory := anywayEnvironment(t, server.URL)
	result, exitCode := launchJSON(t, "svc", "--json")
	if exitCode != 0 || result["launched"] != false || result["outcome"] != "already-running" {
		t.Fatalf("without the flag: exit %d %v, want launched:false already-running", exitCode, result)
	}
	assertNoRunRows(t, stateDirectory)
	result, exitCode = launchJSON(t, "svc", "--anyway", "--json")
	if exitCode != 0 || result["outcome"] != "ok" {
		t.Fatalf("with --anyway: exit %d %v, want a launched ok run", exitCode, result)
	}
	rows, err := runs.Store{StateDirectory: stateDirectory}.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d (%v), want exactly one", len(rows), err)
	}
}

// The ruling: --anyway overrides the already-running skip and nothing else; an unmet need
// still refuses.
func TestLaunchAnywayStillRefusesAnUnmetServiceNeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	stateDirectory := anywayEnvironment(t, server.URL)
	result, exitCode := launchJSON(t, "needy", "--anyway", "--json")
	if exitCode != 1 || result["reason"] != "unmet need" {
		t.Fatalf("exit %d %v, want an unmet-need refusal even with --anyway", exitCode, result)
	}
	assertNoRunRows(t, stateDirectory)
}

// detachedServiceEnvironment catalogues "svc2", a tool whose only action "start" is a service
// that waits up to within for serverURL, and makes `launch --detach` reachable: TMUX names the
// host terminal and an executable "tmux" that exits 0 stands in for the real tab spawn.
func detachedServiceEnvironment(t *testing.T, serverURL, within string) string {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDirectory := t.TempDir()
	t.Setenv("FORMIC_STATE_DIR", stateDirectory)
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := "formic: 1\nname: svc2\nlaunch:\n  command: " + testfixture.Tool(t) + "\n  args: [\"fixture:exit\", \"0\"]\n" +
		"live:\n  probe:\n    http: " + serverURL + "\n" +
		"actions:\n  - name: start\n    lifecycle: service\n    ready:\n      within: " + within + "\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "svc2.yaml"), []byte(tool), 0o644); err != nil {
		t.Fatal(err)
	}
	if exitCode := run([]string{"add", sourcePath}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
	fakeBinaries := t.TempDir()
	testfixture.InstallAs(t, fakeBinaries, "tmux")
	t.Setenv("PATH", fakeBinaries+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "/tmp/fake-tmux,1,0")
	launch.ReadyPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { launch.ReadyPollInterval = time.Second })
	return stateDirectory
}

func TestLaunchDetachedServiceWaitsForReady(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 3 { // pre-launch probe plus two ready polls
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	stateDirectory := detachedServiceEnvironment(t, server.URL, "10s")
	result, exitCode := launchJSON(t, "svc2", "start", "--detach", "--json")
	if exitCode != 0 || result["ready"] != true || result["outcome"] != "running" || result["detached"] != true {
		t.Fatalf("exit %d %v, want exit 0, outcome running, detached, ready true", exitCode, result)
	}
	if hits.Load() < 4 {
		t.Fatalf("probe hit %d times, want the wait to poll until it answered", hits.Load())
	}
	rows, err := runs.Store{StateDirectory: stateDirectory}.List()
	if err != nil || len(rows) != 1 || rows[0].Outcome != "running" {
		t.Fatalf("rows = %+v (%v), want one row still running", rows, err)
	}
}

func TestLaunchDetachedServiceNotReadyStillExitsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	stateDirectory := detachedServiceEnvironment(t, server.URL, "300ms")
	result, exitCode := launchJSON(t, "svc2", "start", "--detach", "--json")
	if exitCode != 0 || result["ready"] != false || result["outcome"] != "running" {
		t.Fatalf("exit %d %v, want exit 0, outcome running, ready false (R5)", exitCode, result)
	}
	rows, err := runs.Store{StateDirectory: stateDirectory}.List()
	if err != nil || len(rows) != 1 || rows[0].Outcome != "running" {
		t.Fatalf("rows = %+v (%v), want the one row untouched by the wait", rows, err)
	}
}

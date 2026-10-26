package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/testfixture"
)

func writeDescribeActionsSource(t *testing.T) string {
	t.Helper()
	sourcePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourcePath, ".formic", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "formic: 1\nname: multi\nlaunch:\n  command: '" + testfixture.Tool(t) + "'\n  args: [\"fixture:exit\"]\n" +
		"actions:\n  - name: start\n    title: Start it\n    args: [\"0\"]\n" +
		"  - name: stop\n    confirm: Stops everything.\n    interactive: true\n    args: [\"0\"]\n"
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "tools", "multi.yaml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcePath
}

func TestRunDescribeListsActions(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	if exitCode := run([]string{"add", writeDescribeActionsSource(t)}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
	output, exitCode := captureStdout(t, func() int { return run([]string{"describe", "multi", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("describe --json exit = %d:\n%s", exitCode, output)
	}
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode: %v\n%s", err, output)
	}
	if len(result.Actions) != 2 || result.Actions[0].Name != "start" || result.Actions[0].Title != "Start it" {
		t.Fatalf("actions = %+v", result.Actions)
	}
	if result.Actions[1].Confirm != "Stops everything." || !result.Actions[1].Interactive || result.Actions[0].Interactive {
		t.Fatalf("stop/start flags = %+v", result.Actions)
	}

	text, exitCode := captureStdout(t, func() int { return run([]string{"describe", "multi"}) })
	if exitCode != 0 || !strings.Contains(text, "action: start") || !strings.Contains(text, "action: stop") || !strings.Contains(text, "Stops everything.") {
		t.Fatalf("text describe (exit %d):\n%s", exitCode, text)
	}
}

func TestRunDescribeOmitsActionsForAnActionlessTool(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	if exitCode := run([]string{"add", writeGreetSource(t)}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
	output, _ := captureStdout(t, func() int { return run([]string{"describe", "greet", "--json"}) })
	if strings.Contains(output, `"actions"`) {
		t.Fatalf("an action-less describe carries actions:\n%s", output)
	}
}

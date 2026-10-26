package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/testfixture"
)

// writeProbedSource writes one source holding tools: name -> probe YAML ("" declares no live block).
func writeProbedSource(t *testing.T, tools [][2]string) string {
	t.Helper()
	sourcePath := t.TempDir()
	toolsDirectory := filepath.Join(sourcePath, ".formic", "tools")
	if err := os.MkdirAll(toolsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"), []byte("toolkit: 1\nname: probes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, entry := range tools {
		manifestYAML := "formic: 1\nname: " + entry[0] + "\nlaunch:\n  command: '" + testfixture.Tool(t) + "'\n  args: [\"fixture:exit\", \"0\"]\n"
		if entry[1] != "" {
			manifestYAML += "live:\n  probe:\n    " + entry[1] + "\n"
		}
		if err := os.WriteFile(filepath.Join(toolsDirectory, entry[0]+".yaml"), []byte(manifestYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sourcePath
}

func addProbedSource(t *testing.T, tools [][2]string) {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	t.Setenv("FORMIC_STATE_DIR", t.TempDir())
	if exitCode := run([]string{"add", writeProbedSource(t, tools)}); exitCode != 0 {
		t.Fatalf("add exit = %d", exitCode)
	}
}

func decodeProbes(t *testing.T, output string) []probeReport {
	t.Helper()
	var result probeVerbResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode probe --json: %v\n%s", err, output)
	}
	return result.Probes
}

func TestProbeVerbReportsEveryDeclaredToolInCatalogOrder(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer fast.Close()
	addProbedSource(t, [][2]string{{"a-slow", "http: " + slow.URL}, {"b-fast", "http: " + fast.URL}, {"c-none", ""}})
	output, exitCode := captureStdout(t, func() int { return run([]string{"probe", "--json"}) })
	if exitCode != 0 {
		t.Fatalf("exit = %d:\n%s", exitCode, output)
	}
	probes := decodeProbes(t, output)
	if len(probes) != 2 || probes[0].Tool != "a-slow" || probes[0].State != "up" || probes[1].Tool != "b-fast" || probes[1].State != "down" {
		t.Fatalf("probes = %+v, want a-slow up then b-fast down (no c-none)", probes)
	}
}

func TestProbeVerbRefusesUnknownTool(t *testing.T) {
	addProbedSource(t, [][2]string{{"known", ""}})
	if _, exitCode := captureStdout(t, func() int { return run([]string{"probe", "ghost", "--json"}) }); exitCode != 1 {
		t.Fatalf("exit = %d, want 1", exitCode)
	}
}

func TestProbeVerbNamedToolWithoutProbeIsUnknown(t *testing.T) {
	addProbedSource(t, [][2]string{{"plain", ""}})
	output, exitCode := captureStdout(t, func() int { return run([]string{"probe", "plain", "--json"}) })
	probes := decodeProbes(t, output)
	if exitCode != 0 || len(probes) != 1 || probes[0].State != "unknown" {
		t.Fatalf("exit = %d probes = %+v", exitCode, probes)
	}
}

func TestProbeWritesNoFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	addProbedSource(t, [][2]string{{"svc", "http: " + server.URL}})
	configDirectory, stateDirectory := os.Getenv("FORMIC_CONFIG_DIR"), os.Getenv("FORMIC_STATE_DIR")
	workingDirectory := t.TempDir()
	original, _ := os.Getwd()
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	snapshot := func(root string) int {
		count := 0
		_ = filepath.Walk(root, func(string, os.FileInfo, error) error { count++; return nil })
		return count
	}
	before := [3]int{snapshot(configDirectory), snapshot(stateDirectory), snapshot(workingDirectory)}
	captureStdout(t, func() int { return run([]string{"probe", "--json"}) })
	after := [3]int{snapshot(configDirectory), snapshot(stateDirectory), snapshot(workingDirectory)}
	if before != after {
		t.Fatalf("probe wrote files: before %v after %v", before, after)
	}
}

func TestListRunsNoProbeAndDescribeReportsLiveState(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	addProbedSource(t, [][2]string{{"svc", "http: " + server.URL}})
	if _, exitCode := captureStdout(t, func() int { return run([]string{"list", "--json"}) }); exitCode != 0 || hits.Load() != 0 {
		t.Fatalf("list exit = %d, probe hits = %d, want 0 hits", exitCode, hits.Load())
	}
	output, _ := captureStdout(t, func() int { return run([]string{"describe", "svc", "--json"}) })
	var result describeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode: %v\n%s", err, output)
	}
	if result.Live == nil || result.Live.State != "up" || hits.Load() != 1 {
		t.Fatalf("live = %+v hits = %d", result.Live, hits.Load())
	}
}

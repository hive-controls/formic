package launch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

func needFromJSON(t *testing.T, jsonText string) formats.Need {
	t.Helper()
	var decoded formats.Need
	if err := json.Unmarshal([]byte(jsonText), &decoded); err != nil {
		t.Fatalf("decode need fixture: %v", err)
	}
	return decoded
}

// TestPrepareRefusesOnUnmetNeedAndWritesNoRunRow proves the needs-resolution step lives in
// the launcher itself (§13 "a resolution step before every launch"): every caller of Prepare
// — not just the cmd/formic verb — is refused with a *NeedsRefused, and no run row is ever
// written.
func TestPrepareRefusesOnUnmetNeedAndWritesNoRunRow(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDir := t.TempDir()
	tool := discovery.Tool{
		Name: "needy",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "echo"},
			Needs:  []formats.Need{needFromJSON(t, `{"kind":"secret","name":"NEVER_EQUIPPED"}`)},
		},
	}
	_, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: stateDir},
		Environment: []string{"PATH=/usr/bin"}, StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)

	var refused *NeedsRefused
	if !errors.As(err, &refused) {
		t.Fatalf("Prepare error = %v, want a *NeedsRefused", err)
	}
	if len(refused.Needs) != 1 || refused.Needs[0].Met {
		t.Fatalf("refused.Needs = %+v, want exactly one unmet need", refused.Needs)
	}
	if refused.Hint == "" {
		t.Fatal("refused.Hint should name a formic equip invocation")
	}

	entries, readErr := os.ReadDir(stateDir)
	if readErr != nil {
		t.Fatalf("read state dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("state dir entries = %v, want no run row written on a needs refusal", entries)
	}
}

// TestPrepareDeliversTheChosenGateProviderToTheDeclaredEnvName proves the launcher-level
// twin of the same contract: a met gate need's chosen provider reaches the child as
// <env>=<provider>, through the manifest's own declared env[] scoping.
func TestPrepareDeliversTheChosenGateProviderToTheDeclaredEnvName(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("FORMIC_CONFIG_DIR", configDir)
	writeEquipFile(t, configDir, "equip: 1\nproviders:\n  gate.browser: solari\n")

	toolDirectory := t.TempDir()
	testfixture.InstallAs(t, toolDirectory, "tool")
	tool := discovery.Tool{
		Name: "gated",
		Manifest: formats.ToolManifest{
			Launch: formats.ToolManifestLaunch{Command: "tool", Args: []string{"fixture:print-env", "E2E_DOCTOR_GATE"}},
			Env:    []formats.ToolManifestEnvElem{{Name: "E2E_DOCTOR_GATE"}},
			Needs:  []formats.Need{needFromJSON(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["solari","local"]}`)},
		},
	}
	prepared, err := Prepare(Request{
		Tool: tool, RepositoryRoot: "/repo", Store: runs.Store{StateDirectory: t.TempDir()},
		Environment: []string{"PATH=" + toolDirectory}, StartedAt: time.Unix(100, 0),
	}, profile.ModeForeground)
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	environment := strings.Join(prepared.Command.Env, "\n")
	if !strings.Contains(environment, "E2E_DOCTOR_GATE=solari") {
		t.Fatalf("command environment = %v, want E2E_DOCTOR_GATE=solari delivered", prepared.Command.Env)
	}
}

func writeEquipFile(t *testing.T, configDir, content string) {
	t.Helper()
	path := filepath.Join(configDir, "formic", "equip.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveNeedStatusesUncheckedServiceNeedDoesNotBlock(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	tool := discovery.Tool{Name: "app", Manifest: formats.ToolManifest{Needs: []formats.Need{
		map[string]any{"kind": "service", "tool": "docker", "action": "start"},
		map[string]any{"kind": "network", "policy": "offline"},
	}}}
	if _, err := resolveNeedsForLaunch(tool, nil, nil); err != nil {
		t.Fatalf("unchecked needs must not refuse a launch: %v", err)
	}
	down := func(string) (string, string) { return "down", "" }
	statuses, err := ResolveNeedStatusesWith(tool, nil, down)
	if err != nil || !statuses[0].Blocks() || statuses[1].Blocks() {
		t.Fatalf("statuses = %+v err = %v, want service blocking and network not", statuses, err)
	}
}

func TestPrepareRefusesALaunchWhoseServiceNeedIsDown(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	tool := discovery.Tool{Name: "app", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: "echo"},
		Needs:  []formats.Need{map[string]any{"kind": "service", "tool": "docker", "action": "start"}},
	}}
	down := func(string) (string, string) { return "down", "docker is down" }
	_, err := Prepare(Request{Tool: tool, Services: down, Store: runs.Store{StateDirectory: t.TempDir()}}, profile.ModeForeground)
	var refused *NeedsRefused
	if !errors.As(err, &refused) {
		t.Fatalf("Prepare err = %v, want *NeedsRefused for a down service need", err)
	}
}

func TestServiceCheckerFromAnswersFromTheHeldResults(t *testing.T) {
	checker := ServiceCheckerFrom(map[string]ProbeResult{
		"up-svc":   {State: ProbeUp, Detail: "http 200"},
		"idle-svc": {State: ProbeState("idle"), Detail: "command exit 3 (idle)"},
		"down-svc": {State: ProbeDown, Detail: "command exit 1"},
	})
	for _, name := range []string{"up-svc", "idle-svc"} {
		if state, _ := checker(name); state != "up" && state != "idle" {
			t.Errorf("%s: state = %q, want a met state", name, state)
		}
	}
	state, reason := checker("down-svc")
	if state != "down" || reason != "service down-svc is down (command exit 1)" {
		t.Errorf("down-svc = (%q, %q)", state, reason)
	}
	state, reason = checker("never-probed")
	if state != string(ProbeUnknown) || !strings.Contains(reason, "never-probed was not probed") {
		t.Errorf("never-probed = (%q, %q), want unknown and a not-probed reason", state, reason)
	}
}

func TestServiceCheckerFromAndCatalogCheckerShareTheirReasonWords(t *testing.T) {
	result := ProbeResult{State: ProbeDown, Detail: "command exit 1"}
	_, held := ServiceCheckerFrom(map[string]ProbeResult{"svc": result})("svc")
	if held != serviceReason("svc", result) {
		t.Fatalf("held reason = %q, want the shared sentence %q", held, serviceReason("svc", result))
	}
}

func TestPrepareRefusesWithHeldServiceResults(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	stateDir := t.TempDir()
	tool := discovery.Tool{Name: "app", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t)},
		Needs:  []formats.Need{map[string]any{"kind": "service", "tool": "docker", "action": "start"}},
	}}
	held := map[string]ProbeResult{"docker": {State: ProbeDown, Detail: "command exit 1"}}
	_, err := Prepare(Request{Tool: tool, Services: ServiceCheckerFrom(held), Store: runs.Store{StateDirectory: stateDir}}, profile.ModeForeground)
	var refused *NeedsRefused
	if !errors.As(err, &refused) || len(refused.Needs) != 1 || refused.Needs[0].Reason != "service docker is down (command exit 1)" {
		t.Fatalf("Prepare err = %v, want a *NeedsRefused carrying the held reason", err)
	}
	if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
		t.Fatalf("a refused launch wrote %v", entries)
	}
	held["docker"] = ProbeResult{State: ProbeUp}
	if _, err := Prepare(Request{Tool: tool, Services: ServiceCheckerFrom(held), Store: runs.Store{StateDirectory: stateDir}}, profile.ModeForeground); err != nil {
		t.Fatalf("an up service must let the launch through: %v", err)
	}
}

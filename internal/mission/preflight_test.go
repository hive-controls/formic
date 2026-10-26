package mission

import (
	"path/filepath"
	"testing"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
)

func multiInputTool() discovery.Tool {
	return discovery.Tool{
		Name: "multitool",
		Manifest: formats.ToolManifest{
			Name: "multitool",
			Inputs: []formats.Input{
				{Name: "required1", Required: true},
				{Name: "apiKey", Required: true, Sensitive: true},
			},
		},
	}
}

func catalogWith(tools ...discovery.Tool) discovery.Catalog {
	return discovery.Catalog{Tools: tools}
}

func problemKinds(problems []Problem) map[string]bool {
	kinds := make(map[string]bool, len(problems))
	for _, problem := range problems {
		kinds[problem.Kind] = true
	}
	return kinds
}

// TestPreflightRefusesWithSensitive proves founder ruling 3: a `with:` key naming a
// sensitive: true tool input is refused, regardless of the value given.
func TestPreflightRefusesWithSensitive(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	m := Mission{Definition: formats.Mission{
		Name: "leaky", Evidence: "evidence/",
		Steps: []formats.MissionStepsElem{{Tool: "multitool", With: formats.MissionStepsElemWith{"apiKey": "fake-secret-value"}}},
	}}
	_, refused := Preflight(m, catalogWith(multiInputTool()), "", nil, nil, false)
	if refused == nil {
		t.Fatal("Preflight succeeded, want a with-sensitive refusal")
	}
	if !problemKinds(refused.Problems)["with-sensitive"] {
		t.Fatalf("Problems = %+v, want with-sensitive", refused.Problems)
	}
}

// TestPreflightCollectsEveryProblemAtOnce proves §9/§15: "a refusal lists every problem and
// runs nothing" — an undeclared with: key, a sensitive with: key, a missing required tool
// input, and an unbound mission input are ALL reported together, never just the first.
func TestPreflightCollectsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	m := Mission{Definition: formats.Mission{
		Name: "multi", Evidence: "evidence/", Inputs: []string{"missionInput"},
		Steps: []formats.MissionStepsElem{{Tool: "multitool", With: formats.MissionStepsElemWith{
			"bogus": "x", "apiKey": "y",
		}}},
	}}
	_, refused := Preflight(m, catalogWith(multiInputTool()), "", nil, nil, false)
	if refused == nil {
		t.Fatal("Preflight succeeded, want a refusal")
	}
	kinds := problemKinds(refused.Problems)
	for _, want := range []string{"with-undeclared", "with-sensitive", "missing-input", "input-unbound"} {
		if !kinds[want] {
			t.Fatalf("Problems = %+v, want %s among them", refused.Problems, want)
		}
	}
}

// TestPreflightSucceedsAndSubstitutesMissionInputIntoWith proves the happy path end to end: a
// mission input bound via --input is substituted into a with: token, merged with a direct
// --input override for the tool's other declared input, and the plan carries both.
func TestPreflightSucceedsAndSubstitutesMissionInputIntoWith(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	tool := discovery.Tool{
		Name: "echoer",
		Manifest: formats.ToolManifest{
			Name: "echoer",
			Inputs: []formats.Input{
				{Name: "target", Required: true},
				{Name: "mode"},
			},
		},
	}
	m := Mission{Definition: formats.Mission{
		Name: "sweep", Evidence: "evidence/", Inputs: []string{"who"},
		Steps: []formats.MissionStepsElem{{Tool: "echoer", With: formats.MissionStepsElemWith{"target": "{who}"}}},
	}, SourceRoot: "/repo"}
	plan, refused := Preflight(m, catalogWith(tool), "/cwd", Values{"who=alice", "mode=fast"}, nil, false)
	if refused != nil {
		t.Fatalf("Preflight refused: %+v", refused.Problems)
	}
	if plan.Inputs["target"] != "alice" {
		t.Fatalf("plan.Inputs[target] = %q, want the mission input substituted in", plan.Inputs["target"])
	}
	if plan.Inputs["mode"] != "fast" {
		t.Fatalf("plan.Inputs[mode] = %q, want the direct --input override", plan.Inputs["mode"])
	}
	wantEvidenceRoot := filepath.Join("/repo", "evidence/", "sweep")
	if plan.EvidenceRoot != wantEvidenceRoot {
		t.Fatalf("plan.EvidenceRoot = %q, want %q (toolkit mission resolves against SourceRoot)", plan.EvidenceRoot, wantEvidenceRoot)
	}
}

// TestPreflightRefusesToolNotFound proves an unresolved tool name refuses with tool-not-found
// and no other checks are attempted (there is no manifest to check them against).
func TestPreflightRefusesToolNotFound(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	m := Mission{Definition: formats.Mission{
		Name: "ghost", Evidence: "evidence/",
		Steps: []formats.MissionStepsElem{{Tool: "nonexistent"}},
	}}
	_, refused := Preflight(m, discovery.Catalog{}, "", nil, nil, false)
	if refused == nil {
		t.Fatal("Preflight succeeded, want a tool-not-found refusal")
	}
	if len(refused.Problems) != 1 || refused.Problems[0].Kind != "tool-not-found" {
		t.Fatalf("Problems = %+v, want exactly one tool-not-found", refused.Problems)
	}
}

// TestPreflightRefusesAServiceLifecycleTool proves R9: a mission cannot run a tool whose item-level
// lifecycle is service — a service never exits, so a mission step could not finish.
func TestPreflightRefusesAServiceLifecycleTool(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	service := formats.ToolManifestLifecycleService
	tool := discovery.Tool{Name: "daemon", Manifest: formats.ToolManifest{Name: "daemon", Lifecycle: &service}}
	m := Mission{Definition: formats.Mission{
		Name: "serve", Evidence: "evidence/",
		Steps: []formats.MissionStepsElem{{Tool: "daemon"}},
	}}
	plan, refused := Preflight(m, catalogWith(tool), "", nil, nil, false)
	if refused == nil {
		t.Fatal("Preflight succeeded, want a service-lifecycle refusal")
	}
	if plan.Tool.Name != "" {
		t.Fatalf("plan = %+v, want an empty Plan on refusal", plan)
	}
	if len(refused.Problems) != 1 || refused.Problems[0].Kind != "service-lifecycle" || refused.Problems[0].Detail != "daemon" {
		t.Fatalf("Problems = %+v, want exactly one service-lifecycle naming daemon", refused.Problems)
	}
}

// TestPreflightAcceptsATaskThatDeclaresALiveProbe proves the refusal keys on lifecycle, not on a
// live probe: a task with a probe (a detached start that reports readiness) is still a valid step.
func TestPreflightAcceptsATaskThatDeclaresALiveProbe(t *testing.T) {
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	task := formats.ToolManifestLifecycleTask
	tool := discovery.Tool{Name: "starter", Manifest: formats.ToolManifest{
		Name:      "starter",
		Lifecycle: &task,
		Live:      &formats.Live{Probe: map[string]interface{}{"http": "http://127.0.0.1:1/health"}},
	}}
	m := Mission{Definition: formats.Mission{
		Name: "start-it", Evidence: "evidence/",
		Steps: []formats.MissionStepsElem{{Tool: "starter"}},
	}}
	if _, refused := Preflight(m, catalogWith(tool), "", nil, nil, false); refused != nil {
		t.Fatalf("Preflight refused: %+v", refused.Problems)
	}
}

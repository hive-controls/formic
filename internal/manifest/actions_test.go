package manifest

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

func loadLlamaLoader(t *testing.T, name string) formats.ToolManifest {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "manifests", "llama-loader", name+".yaml")
	loaded := LoadFile(path, t.TempDir())
	if loaded.Problem != nil {
		t.Fatalf("%s: %v", name, loaded.Problem)
	}
	return loaded.Manifest
}

func TestActionNamesInDeclaredOrder(t *testing.T) {
	got := ActionNames(loadLlamaLoader(t, "docker-desktop"))
	if !reflect.DeepEqual(got, []string{"start", "stop", "restart"}) {
		t.Fatalf("ActionNames = %v", got)
	}
	if names := ActionNames(loadLlamaLoader(t, "mcp-proxies")); names != nil {
		t.Fatalf("action-less manifest names = %v, want nil", names)
	}
}

func TestForActionPrefixesLaunchArgs(t *testing.T) {
	tool := loadLlamaLoader(t, "docker-desktop")
	effective, err := ForAction(tool, "restart")
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := ActionSpec(tool, "restart")
	want := append(append([]string{}, tool.Launch.Args...), spec.Args...)
	if !reflect.DeepEqual(effective.Launch.Args, want) || effective.Launch.Command != tool.Launch.Command {
		t.Fatalf("launch = %+v, want args %v", effective.Launch, want)
	}
}

func TestForActionStandaloneCommandDropsLaunchArgs(t *testing.T) {
	tool := loadLlamaLoader(t, "llama-swap")
	effective, err := ForAction(tool, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Launch.Command != "node" {
		t.Fatalf("command = %q", effective.Launch.Command)
	}
	for _, argument := range effective.Launch.Args {
		if strings.Contains(argument, "config.yaml") {
			t.Fatalf("launch.args leaked into a standalone action: %v", effective.Launch.Args)
		}
	}
	if effective.Launch.Cwd == nil || *effective.Launch.Cwd != "{repo}" {
		t.Fatalf("cwd = %v, want {repo}", effective.Launch.Cwd)
	}
}

func TestForActionInheritsLaunchCwd(t *testing.T) {
	tool := loadLlamaLoader(t, "librechat")
	effective, err := ForAction(tool, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if tool.Launch.Cwd == nil || effective.Launch.Cwd == nil || *effective.Launch.Cwd != *tool.Launch.Cwd {
		t.Fatalf("cwd = %v, want %v", effective.Launch.Cwd, tool.Launch.Cwd)
	}
}

func TestForActionImplicitActionIsUnchanged(t *testing.T) {
	tool := loadLlamaLoader(t, "mcp-proxies")
	effective, err := ForAction(tool, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effective, tool) {
		t.Fatalf("implicit action changed the manifest")
	}
	if _, err := ForAction(tool, "start"); err == nil || !strings.Contains(err.Error(), "declares no actions") {
		t.Fatalf("named action on action-less manifest: %v", err)
	}
}

func TestForActionRequiresAndRejectsNames(t *testing.T) {
	tool := loadLlamaLoader(t, "docker-desktop")
	_, err := ForAction(tool, "")
	required, ok := err.(*ActionRequired)
	if !ok {
		t.Fatalf("err = %T %v, want *ActionRequired", err, err)
	}
	if len(required.Actions) != 3 || required.Actions[0].Name != "start" || !strings.Contains(err.Error(), "start, stop, restart") {
		t.Fatalf("ActionRequired = %+v / %v", required, err)
	}
	_, err = ForAction(tool, "nope")
	unknown, ok := err.(*UnknownAction)
	if !ok || unknown.Action != "nope" || len(unknown.Actions) != 3 {
		t.Fatalf("err = %T %v, want *UnknownAction", err, err)
	}
}

func TestForActionInteractiveAndLifecycleCarried(t *testing.T) {
	tool := loadLlamaLoader(t, "llama-swap")
	tui, _ := ForAction(tool, "tui")
	if !RequiresRealTerminal(tui) {
		t.Fatal("tui should require a real terminal")
	}
	stop, _ := ForAction(tool, "stop")
	if RequiresRealTerminal(stop) {
		t.Fatal("stop should not require a real terminal")
	}
	if tool.Pane != nil && RequiresRealTerminal(tool) {
		t.Fatal("the shared pane was mutated")
	}
	start, _ := ForAction(tool, "start")
	if start.Lifecycle == nil || *start.Lifecycle != formats.ToolManifestLifecycleService {
		t.Fatalf("lifecycle = %v, want service", start.Lifecycle)
	}
}

func TestForActionExitOverlayAndSharedFirstArguments(t *testing.T) {
	tool := formats.ToolManifest{
		Name:      "x",
		Launch:    formats.ToolManifestLaunch{Command: "x"},
		Exit:      formats.ToolManifestExit{"0": "ok", "1": "tool failed"},
		Arguments: []formats.Argument{{Flag: "--shared", Kind: formats.ArgumentKindFlag}},
		Actions: []formats.Action{{
			Name:      "go",
			Exit:      formats.ActionExit{"1": "action failed"},
			Arguments: []formats.Argument{{Flag: "--own", Kind: formats.ArgumentKindFlag}},
		}},
	}
	effective, err := ForAction(tool, "go")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Exit["1"] != "action failed" || effective.Exit["0"] != "ok" {
		t.Fatalf("exit = %v", effective.Exit)
	}
	if len(effective.Arguments) != 2 || effective.Arguments[0].Flag != "--shared" || effective.Arguments[1].Flag != "--own" {
		t.Fatalf("arguments = %+v", effective.Arguments)
	}
	effective.Exit["0"] = "mutated"
	effective.Arguments[0].Flag = "--mutated"
	if tool.Exit["0"] != "ok" || tool.Arguments[0].Flag != "--shared" {
		t.Fatal("projection aliases the original manifest")
	}
}

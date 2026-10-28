package manifest

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

func platformManifest() formats.ToolManifest {
	windowsCommand := "{repo}/.venv/Scripts/tool.exe"
	darwinCwd := "{repo}/mac"
	return formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{
			Command: "{repo}/.venv/bin/tool",
			Args:    []string{"--base"},
			Windows: &formats.LaunchOverride{Command: &windowsCommand},
			Darwin:  &formats.LaunchOverride{Cwd: &darwinCwd, Args: []string{}},
		},
		Health: &formats.ToolManifestHealth{
			Check:   []string{".venv/bin/tool", "doctor"},
			Windows: &formats.HealthOverride{Check: []string{".venv/Scripts/tool.exe", "doctor"}},
		},
	}
}

func TestApplyPlatformOverrideReplacesOnlyTheKeysTheHostOverrides(t *testing.T) {
	tool := platformManifest()
	applyPlatformOverride(&tool, "windows")
	if tool.Launch.Command != "{repo}/.venv/Scripts/tool.exe" {
		t.Fatalf("windows command = %q", tool.Launch.Command)
	}
	if !slices.Equal(tool.Launch.Args, []string{"--base"}) || tool.Launch.Cwd != nil {
		t.Fatalf("windows kept args %v cwd %v, want the base args and no cwd", tool.Launch.Args, tool.Launch.Cwd)
	}
	if !slices.Equal(tool.Health.Check, []string{".venv/Scripts/tool.exe", "doctor"}) {
		t.Fatalf("windows health = %v", tool.Health.Check)
	}
}

func TestApplyPlatformOverrideAnEmptyArgsListReplacesTheBaseArgs(t *testing.T) {
	tool := platformManifest()
	applyPlatformOverride(&tool, "darwin")
	if tool.Launch.Command != "{repo}/.venv/bin/tool" || len(tool.Launch.Args) != 0 || *tool.Launch.Cwd != "{repo}/mac" {
		t.Fatalf("darwin launch = %q %v %v", tool.Launch.Command, tool.Launch.Args, tool.Launch.Cwd)
	}
	if !slices.Equal(tool.Health.Check, []string{".venv/bin/tool", "doctor"}) {
		t.Fatalf("darwin health = %v, want the base check", tool.Health.Check)
	}
}

func TestApplyPlatformOverrideLeavesAHostWithoutAnOverrideOnTheBase(t *testing.T) {
	tool := platformManifest()
	applyPlatformOverride(&tool, "linux")
	if tool.Launch.Command != "{repo}/.venv/bin/tool" || !slices.Equal(tool.Launch.Args, []string{"--base"}) {
		t.Fatalf("linux launch = %q %v", tool.Launch.Command, tool.Launch.Args)
	}
	if tool.Launch.Windows != nil || tool.Launch.Darwin != nil || tool.Health.Windows != nil {
		t.Fatal("override blocks should be cleared once folded")
	}
}

func TestLoadFileAppliesTheHostOverride(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "tool.yaml")
	document := strings.Join([]string{
		"formic: 1",
		"name: tool",
		"launch:",
		"  command: base-command",
		"  " + runtime.GOOS + ":",
		"    command: host-command",
		"health:",
		"  check: [base-check]",
		"  " + runtime.GOOS + ":",
		"    check: [host-check]",
	}, "\n")
	if err := os.WriteFile(manifestPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := LoadFile(manifestPath, directory)
	if loaded.Problem != nil {
		t.Fatalf("manifest with a host override returned a problem: %v", loaded.Problem)
	}
	if loaded.Manifest.Launch.Command != "host-command" || !slices.Equal(loaded.Manifest.Health.Check, []string{"host-check"}) {
		t.Fatalf("loaded launch %q health %v, want the host override", loaded.Manifest.Launch.Command, loaded.Manifest.Health.Check)
	}
}

func TestLoadFileRejectsAnEmptyOrUnknownOverride(t *testing.T) {
	for name, override := range map[string]string{
		"empty launch override":    "  darwin: {}",
		"unknown operating system": "  freebsd:\n    command: x",
	} {
		directory := t.TempDir()
		manifestPath := filepath.Join(directory, "tool.yaml")
		document := "formic: 1\nname: tool\nlaunch:\n  command: base\n" + override + "\n"
		if err := os.WriteFile(manifestPath, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if loaded := LoadFile(manifestPath, directory); loaded.Problem == nil {
			t.Fatalf("%s: expected a schema problem, got none", name)
		}
	}
}

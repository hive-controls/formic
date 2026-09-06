package doctor

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	toolspec "github.com/hive-controls/toolspec"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/profile"
)

func TestRunReportsInvalidManifestAndFatalCollision(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "repository"))
	userConfigRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "discovery", "user-config"))
	catalog, err := discovery.Discover(repositoryRoot, userConfigRoot)
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	var output bytes.Buffer
	if exitCode := Run(catalog, &output); exitCode == 0 {
		t.Fatalf("doctor returned success despite problems:\n%s", output.String())
	}
	// The fixture's repo-local "shared" tool shadows BOTH a different user-library command
	// (the fatal class) and the npm-derived "shared" script (non-fatal, badge only).
	for _, expected := range []string{"invalid invalid-local", "collision shared", "↑ shared:", "problems:"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("doctor output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestRunClassifiesShadowsByFatality(t *testing.T) {
	cases := []struct {
		name         string
		shadowedKind discovery.SourceKind
		shadowedCmd  string
		wantExit     int
		wantBadge    bool
	}{
		{"repo-local shadowing a different user-library command is fatal", discovery.SourceUser, "other-command", 1, false},
		{"repo-local shadowing the same user-library command is not fatal", discovery.SourceUser, "mine", 0, true},
		{"repo-local shadowing an npm script is never fatal", discovery.SourceNPM, "npm", 0, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			catalog := discovery.Catalog{Tools: []discovery.Tool{{
				Name:     "mine",
				Source:   discovery.Source{Kind: discovery.SourceLocal, Path: "/repo/.hivedeck/tools/mine.yaml"},
				Manifest: toolspec.ToolManifest{Launch: toolspec.ToolManifestLaunch{Command: "mine"}},
				Shadowed: []discovery.Definition{{
					Name: "mine", Command: testCase.shadowedCmd,
					Source: discovery.Source{Kind: testCase.shadowedKind, Path: "/other/path"},
				}},
			}}}
			var output bytes.Buffer
			exitCode := Run(catalog, &output)
			if exitCode != testCase.wantExit {
				t.Fatalf("exit = %d, want %d:\n%s", exitCode, testCase.wantExit, output.String())
			}
			hasBadge := strings.Contains(output.String(), "↑ mine:")
			if hasBadge != testCase.wantBadge {
				t.Fatalf("badge presence = %v, want %v:\n%s", hasBadge, testCase.wantBadge, output.String())
			}
		})
	}
}

func TestRunPrintsToolsAndSuccessSummary(t *testing.T) {
	catalog := discovery.Catalog{Tools: []discovery.Tool{{
		Name: "sample", Source: discovery.Source{Kind: discovery.SourceNPM, Path: "/repo/package.json"},
	}}}
	var output bytes.Buffer
	if exitCode := Run(catalog, &output); exitCode != 0 {
		t.Fatalf("doctor returned %d for healthy catalog:\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "tool sample") || !strings.Contains(output.String(), "ok: 1 tools discovered") {
		t.Fatalf("doctor output missing tool or summary:\n%s", output.String())
	}
}

func TestRunWithProfileReportsEveryProfileProblem(t *testing.T) {
	problem := &profile.ValidationError{Problems: []string{"unknown key one", "unknown key two"}}
	var output bytes.Buffer
	exitCode := RunWithProfile(discovery.Catalog{}, problem, &output)
	if exitCode == 0 {
		t.Fatalf("doctor returned success despite profile problems:\n%s", output.String())
	}
	for _, expected := range problem.Problems {
		if !strings.Contains(output.String(), "profile "+expected) {
			t.Fatalf("doctor output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestRunWithProfileWarningsKeepsAHealthyExitCode(t *testing.T) {
	var output bytes.Buffer
	exitCode := RunWithProfileWarnings(discovery.Catalog{}, nil, []string{"tool \"old\" has no discovered manifest"}, &output)
	if exitCode != 0 || !strings.Contains(output.String(), "profile warning:") {
		t.Fatalf("exit = %d, output:\n%s", exitCode, output.String())
	}
}

package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCollectsSecretAndUnknownKeyProblems(t *testing.T) {
	_, err := Parse([]byte(`
default:
  mode: sideways
  surprise: true
tools:
  checks:
    apiKey: literal-secret
    token: literal-token
    unknown: value
    apiKeyFrom: NOT_AN_ENV_REFERENCE
`))
	var validationError *ValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("Parse error = %v, want ValidationError", err)
	}
	for _, expected := range []string{
		"default.mode must be foreground, tab, or pane",
		`default: unknown key "surprise"`,
		"use apiKeyFrom: env.NAME instead of apiKey",
		"use tokenFrom: env.NAME instead of token",
		`unknown key "unknown"`,
		"apiKeyFrom must match env.NAME",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("validation error missing %q:\n%s", expected, err)
		}
	}
}

func TestParseRejectsLiteralSecretEnvironmentValues(t *testing.T) {
	_, err := Parse([]byte(`
tools:
  checks:
    env:
      API_TOKEN: literal-secret
      SAFE_VALUE: local
`))
	if err == nil || !strings.Contains(err.Error(), "use env.NAME instead of a literal secret") {
		t.Fatalf("Parse error = %v, want literal-secret refusal", err)
	}
}

func TestResolveModeUsesTheRequiredPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		file        File
		toolName    string
		interactive bool
		want        LaunchMode
	}{
		{"built-in default", File{}, "checks", false, ModeForeground},
		{"profile default", File{Default: Default{Mode: ModeTab}}, "checks", false, ModeTab},
		{"per-tool", File{Default: Default{Mode: ModeTab}, Tools: map[string]Tool{"checks": {Mode: ModePane}}}, "checks", false, ModePane},
		{"manifest constraint", File{Tools: map[string]Tool{"checks": {Mode: ModePane}}}, "checks", true, ModeForeground},
		{"manifest permits tab", File{Tools: map[string]Tool{"checks": {Mode: ModeTab}}}, "checks", true, ModeTab},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ResolveMode(testCase.file, testCase.toolName, testCase.interactive); got != testCase.want {
				t.Fatalf("ResolveMode() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestUnknownToolWarningsAreSortedAndNonDestructive(t *testing.T) {
	file := File{Tools: map[string]Tool{"present": {}, "z-old": {}, "a-old": {}}}
	warnings := UnknownToolWarnings(file, []string{"present"})
	if strings.Join(warnings, "\n") != "tool \"a-old\" has no discovered manifest\ntool \"z-old\" has no discovered manifest" {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestResolveEnvironmentUsesNamesAndKeepsNonSecretLiterals(t *testing.T) {
	environment, err := ResolveEnvironment(Tool{Env: map[string]string{
		"API_TOKEN":   "env.SOURCE_TOKEN",
		"FORMIC_GATE": "local",
	}}, []string{"SOURCE_TOKEN=resolved-secret", "UNCHANGED=yes"})
	if err != nil {
		t.Fatalf("ResolveEnvironment returned an error: %v", err)
	}
	joined := strings.Join(environment, "\n")
	for _, expected := range []string{"API_TOKEN=resolved-secret", "FORMIC_GATE=local", "UNCHANGED=yes"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("resolved environment missing %q: %v", expected, environment)
		}
	}
}

func TestResolveEnvironmentReferencesResolveAgainstTheOriginalEnvironmentIndependentOfKeyOrder(t *testing.T) {
	// Keys "A" then "B" are processed in that order (ResolveEnvironment sorts them), so a
	// naive implementation that writes each resolved value back into the SAME map it reads
	// from would let A's literal override contaminate B's env.A lookup.
	environment, err := ResolveEnvironment(Tool{Env: map[string]string{
		"A": "changed",
		"B": "env.A",
	}}, []string{"A=original"})
	if err != nil {
		t.Fatalf("ResolveEnvironment returned an error: %v", err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "A=changed") {
		t.Fatalf("resolved environment missing A=changed: %v", environment)
	}
	if !strings.Contains(joined, "B=original") {
		t.Fatalf("B=env.A should resolve against the ORIGINAL process environment (A=original), not another profile override — got: %v", environment)
	}
}

func TestSavePreservesCommentsOnUntouchedKeys(t *testing.T) {
	repositoryRoot := t.TempDir()
	profilePath := Path(repositoryRoot)
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	input := "tools:\n  checks:\n    mode: foreground\n    args: # keep this comment\n      - --evidence\n"
	if err := os.WriteFile(profilePath, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	tool := file.Tools["checks"]
	tool.Mode = ModeTab
	file.SetTool("checks", tool)
	if err := file.Save(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "# keep this comment") || !strings.Contains(string(written), "mode: tab") {
		t.Fatalf("saved profile lost an untouched comment or mode update:\n%s", written)
	}
}

func TestSaveAdvancesTheBaselineSoARoundTripLandsOnDisk(t *testing.T) {
	repositoryRoot := t.TempDir()
	profilePath := Path(repositoryRoot)
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte("tools:\n  checks:\n    mode: foreground\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := Load(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}

	toTab := file.Tools["checks"]
	toTab.Mode = ModeTab
	file.SetTool("checks", toTab)
	if err := file.Save(repositoryRoot); err != nil {
		t.Fatal(err)
	}

	// Returning to the value the file was LOADED with must still be treated as a real
	// change from what is now on disk (mode: tab) — not a no-op against the load-time
	// baseline.
	backToForeground := file.Tools["checks"]
	backToForeground.Mode = ModeForeground
	file.SetTool("checks", backToForeground)
	if err := file.Save(repositoryRoot); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "mode: foreground") {
		t.Fatalf("second save did not land on disk — still shows the intermediate value:\n%s", written)
	}
}

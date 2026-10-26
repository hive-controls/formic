package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileValidatesAndResolvesManifest(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", "testdata", "repository"))
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "valid.yaml")
	loaded := LoadFile(manifestPath, repositoryRoot)
	if loaded.Problem != nil {
		t.Fatalf("valid manifest returned a problem: %v", loaded.Problem)
	}
	specPath := filepath.Join(repositoryRoot, "specs", "sample.yaml")
	resolved, err := ResolveLaunch(loaded.Manifest, repositoryRoot, specPath, nil)
	if err != nil {
		t.Fatalf("ResolveLaunch returned an error: %v", err)
	}
	if resolved.Cwd != repositoryRoot {
		t.Fatalf("resolved cwd = %q, want %q", resolved.Cwd, repositoryRoot)
	}
	if resolved.Args[0] != specPath {
		t.Fatalf("resolved spec argument = %q, want %q", resolved.Args[0], specPath)
	}
	if !RequiresSpec(loaded.Manifest) {
		t.Fatal("RequiresSpec should recognize the manifest's {spec} argument")
	}
}

func TestLoadFileRejectsUnknownToken(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "unknown-token.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), "unknown substitution token {mystery}") {
		t.Fatalf("expected unknown-token problem, got: %v", loaded.Problem)
	}
}

func TestLoadFileRejectsAnUnsupportedManifestVersion(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "unsupported-version.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), UnsupportedManifestVersionPrefix) {
		t.Fatalf("expected an %q problem, got: %v", UnsupportedManifestVersionPrefix, loaded.Problem)
	}
	if !IsUnsupportedManifestVersion(loaded.Problem.Error()) {
		t.Fatalf("IsUnsupportedManifestVersion did not recognize: %v", loaded.Problem)
	}
}

func TestLoadFileRejectsADottedToken(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "dotted-token.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), "unknown substitution token {repo.path}") {
		t.Fatalf("expected unknown-token problem for {repo.path}, got: %v", loaded.Problem)
	}
}

func TestLoadFileRejectsNestedBraces(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "nested-token.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), "unknown substitution token") {
		t.Fatalf("expected unknown-token problem for nested braces, got: %v", loaded.Problem)
	}
}

func TestSubstituteTokensResolvesKnownTokens(t *testing.T) {
	resolved, err := substituteTokens("{repo}/bin --spec={spec}", map[string]string{
		"repo": "/root", "spec": "/root/spec.yaml",
	})
	if err != nil {
		t.Fatalf("substituteTokens returned an error: %v", err)
	}
	if want := "/root/bin --spec=/root/spec.yaml"; resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}

func TestSubstituteTokensRejectsAnExtraCharacterInsideBraces(t *testing.T) {
	_, err := substituteTokens("{repo.path}", map[string]string{"repo": "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "unknown substitution token {repo.path}") {
		t.Fatalf("expected an unknown-token error for {repo.path}, got: %v", err)
	}
}

func TestSubstituteTokensRejectsNestedBraces(t *testing.T) {
	_, err := substituteTokens("{{repo}}", map[string]string{"repo": "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "unknown substitution token") {
		t.Fatalf("expected an unknown-token error for nested braces, got: %v", err)
	}
}

func TestSubstituteTokensRejectsAnUnterminatedToken(t *testing.T) {
	_, err := substituteTokens("prefix {repo", map[string]string{"repo": "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "unterminated substitution token") {
		t.Fatalf("expected an unterminated-token error, got: %v", err)
	}
}

// TestLoadFileAcceptsDeclaredEnvOutputsAndExit exercises the manifest format's v1.1 three additive fields
// together — env[] (a required key, a defaulted key, and a secret key), outputs.evidenceDir,
// and exit — proving a v1.1 manifest still loads and validates cleanly end to end.
func TestLoadFileAcceptsDeclaredEnvOutputsAndExit(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "env-outputs-exit.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem != nil {
		t.Fatalf("v1.1 manifest returned a problem: %v", loaded.Problem)
	}
	if len(loaded.Manifest.Env) != 4 {
		t.Fatalf("declared env entries = %d, want 4", len(loaded.Manifest.Env))
	}
	targetURL := loaded.Manifest.Env[0]
	if targetURL.Name != "FIXTURE_TARGET_URL" || !targetURL.Required {
		t.Fatalf("first env entry = %+v, want FIXTURE_TARGET_URL required", targetURL)
	}
	mode := loaded.Manifest.Env[1]
	if mode.Default == nil || *mode.Default != "replay" {
		t.Fatalf("FIXTURE_MODE default = %v, want %q", mode.Default, "replay")
	}
	apiKey := loaded.Manifest.Env[2]
	if !apiKey.Secret {
		t.Fatalf("FIXTURE_API_KEY entry = %+v, want secret", apiKey)
	}
	if loaded.Manifest.Outputs == nil || loaded.Manifest.Outputs.EvidenceDir == nil ||
		*loaded.Manifest.Outputs.EvidenceDir != "FIXTURE_EVIDENCE_DIR" {
		t.Fatalf("outputs.evidenceDir = %+v, want FIXTURE_EVIDENCE_DIR", loaded.Manifest.Outputs)
	}
	if loaded.Manifest.Exit["0"] != "passed" || loaded.Manifest.Exit["1"] != "needs-human" {
		t.Fatalf("exit table = %+v, want the declared 0/1 meanings", loaded.Manifest.Exit)
	}
}

func TestLoadFileRejectsSchemaViolation(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "invalid.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), "additional properties") {
		t.Fatalf("expected schema-validation problem, got: %v", loaded.Problem)
	}
}

func writeTempManifest(t *testing.T, body string) string {
	t.Helper()
	manifestPath := filepath.Join(t.TempDir(), "tool.yaml")
	if err := os.WriteFile(manifestPath, []byte("formic: 1\nname: argtool\nlaunch:\n  command: echo\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

func TestLoadFileRefusesAChoiceArgumentWithoutOptions(t *testing.T) {
	manifestPath := writeTempManifest(t, "arguments:\n  - flag: --healer\n    kind: choice\n")
	loaded := LoadFile(manifestPath, t.TempDir())
	want := "arguments[0] (--healer): kind choice needs at least one option"
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), want) {
		t.Fatalf("expected %q, got: %v", want, loaded.Problem)
	}
}

func TestLoadFileRefusesAnArgumentDefaultOutsideItsOptions(t *testing.T) {
	manifestPath := writeTempManifest(t, "arguments:\n  - flag: --healer\n    kind: choice\n    default: nope\n    options:\n      - value: a\n      - value: b\n")
	loaded := LoadFile(manifestPath, t.TempDir())
	want := `arguments[0] (--healer): default "nope" is not among its options`
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), want) {
		t.Fatalf("expected %q, got: %v", want, loaded.Problem)
	}
}

func TestLoadFileRefusesTheSameFlagDeclaredTwice(t *testing.T) {
	manifestPath := writeTempManifest(t, "arguments:\n  - flag: --write\n    kind: flag\n  - flag: --app\n    kind: value\n  - flag: --write\n    kind: flag\n")
	loaded := LoadFile(manifestPath, t.TempDir())
	want := `arguments[2] (--write): flag is already declared by arguments[0]`
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), want) {
		t.Fatalf("expected %q, got: %v", want, loaded.Problem)
	}
}

func TestLoadFileAcceptsDeclaredArgumentsAndEnvOptions(t *testing.T) {
	manifestPath := writeTempManifest(t, "arguments:\n  - flag: --healer\n    kind: choice\n    default: a\n    options:\n      - value: a\n      - value: b\n  - flag: --write\n    kind: flag\n    default: ignored-for-flags\n  - flag: --app\n    kind: value\nenv:\n  - name: MODE\n    options:\n      - value: x\n")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem != nil {
		t.Fatalf("valid arguments returned a problem: %v", loaded.Problem)
	}
	if len(loaded.Manifest.Arguments) != 3 || len(loaded.Manifest.Env[0].Options) != 1 {
		t.Fatalf("decoded arguments=%d env options=%d", len(loaded.Manifest.Arguments), len(loaded.Manifest.Env[0].Options))
	}
}

// The five manifests below are copies of a real consumer's tool suite, kept byte for byte.
// They exercise actions, live probes, lifecycle and the service need together.
func TestLoadFileAcceptsTheLlamaLoaderSuite(t *testing.T) {
	names := []string{"docker-desktop", "librechat", "llama-swap", "mcp-proxies", "llama-loader"}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			manifestPath := filepath.Join("..", "..", "testdata", "manifests", "llama-loader", name+".yaml")
			loaded := LoadFile(manifestPath, t.TempDir())
			if loaded.Problem != nil {
				t.Fatalf("%s should load; got: %v", name, loaded.Problem)
			}
		})
	}
}

func TestLoadFileAcceptsTheLlamaLoaderActionSuite(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "manifests", "llama-loader", "*.yaml"))
	if err != nil || len(paths) != 5 {
		t.Fatalf("expected 5 llama-loader copies, got %d (%v)", len(paths), err)
	}
	for _, path := range paths {
		if loaded := LoadFile(path, t.TempDir()); loaded.Problem != nil {
			t.Errorf("%s: %v", filepath.Base(path), loaded.Problem)
		}
	}
}

func TestLoadFileRefusesInvalidActionManifests(t *testing.T) {
	cases := map[string]string{
		"actions-duplicate-name.yaml":     "name is already used by actions[0]",
		"actions-flag-both-levels.yaml":   "already declared at manifest level",
		"actions-choice-no-options.yaml":  "kind choice needs at least one option",
		"actions-unknown-token.yaml":      "unknown substitution token {nope}",
		"actions-lifecycle-beside.yaml":   "lifecycle",
		"actions-live-whenup-beside.yaml": "live.whenUp",
	}
	for file, want := range cases {
		loaded := LoadFile(filepath.Join("..", "..", "testdata", "manifests", file), t.TempDir())
		if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), want) {
			t.Errorf("%s: problem = %v, want containing %q", file, loaded.Problem, want)
		}
	}
}

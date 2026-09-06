package manifest

import (
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
	resolved, err := ResolveLaunch(loaded.Manifest, repositoryRoot, specPath)
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

func TestLoadFileRejectsSchemaViolation(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "manifests", "invalid.yaml")
	loaded := LoadFile(manifestPath, t.TempDir())
	if loaded.Problem == nil || !strings.Contains(loaded.Problem.Error(), "additional properties") {
		t.Fatalf("expected schema-validation problem, got: %v", loaded.Problem)
	}
}

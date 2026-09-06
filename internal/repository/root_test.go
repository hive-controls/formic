package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRootWalksToNearestGitMarker(t *testing.T) {
	repositoryRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repositoryRoot, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nestedDirectory := filepath.Join(repositoryRoot, "one", "two")
	if err := os.MkdirAll(nestedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	foundRoot, err := FindRoot(nestedDirectory)
	if err != nil {
		t.Fatalf("FindRoot returned an error: %v", err)
	}
	if foundRoot != repositoryRoot {
		t.Fatalf("FindRoot = %q, want %q", foundRoot, repositoryRoot)
	}
}

func TestFindRootRejectsDirectoryOutsideRepository(t *testing.T) {
	_, err := FindRoot(t.TempDir())
	if err == nil {
		t.Fatal("FindRoot should reject a directory without a .git ancestor")
	}
}

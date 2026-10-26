package formats_test

import (
	"os"
	"path/filepath"
	"testing"
)

// listFormatFixtureNames lists the fixture file names under dir/subdir, for one of the
// non-manifest formats' fixtures/<format>/{valid,invalid} directories.
func listFormatFixtureNames(t *testing.T, dir, subdir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, subdir))
	if err != nil {
		t.Fatalf("listing fixtures in %s/%s: %v", dir, subdir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// readFormatFixture reads one fixture file under dir/subdir/name.
func readFormatFixture(t *testing.T, dir, subdir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, subdir, name))
	if err != nil {
		t.Fatalf("reading fixture %s/%s/%s: %v", dir, subdir, name, err)
	}
	return data
}

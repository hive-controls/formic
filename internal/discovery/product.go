package discovery

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hive-controls/formic/internal/manifest"
)

const productManifestSuffix = ".formic.yaml"

// excludedProductDirectories are the vendor/build/state/fixture directories a shipped-
// manifest walk never descends into. This is a fixed list rather than real .gitignore
// parsing (no new dependency is authorised for that), but it covers the shapes those
// directories take in practice: dependency trees, build output, dot-directories (VCS
// metadata, tool state), and Go's own test-fixture convention — `testdata` is never a
// package's real source, so a `*.formic.yaml` planted there (this package's own fixtures
// included) must never surface as a discoverable tool.
var excludedProductDirectories = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
	"testdata":     true,
}

// discoverProductManifests walks the whole repository for `*.formic.yaml` files — the
// shape a shipped product manifest takes, distinct from the `.formic/tools/*.yaml` repo-
// local convention — and loads each one exactly like any other manifest.
func discoverProductManifests(repositoryRoot string) ([]Tool, error) {
	var tools []Tool
	walkErr := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != repositoryRoot && shouldSkipProductDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), productManifestSuffix) {
			return nil
		}
		loaded := manifest.LoadFile(path, repositoryRoot)
		tools = append(tools, toolFromLoaded(loaded, SourceProduct, repositoryRoot))
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan product manifests: %w", walkErr)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Source.Path < tools[j].Source.Path })
	return tools, nil
}

func shouldSkipProductDirectory(name string) bool {
	if excludedProductDirectories[name] {
		return true
	}
	return strings.HasPrefix(name, ".")
}

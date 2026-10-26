// Package catalog reads, writes, and grows the user's catalog file — the list of
// admitted sources `formic add` maintains (design/specs/formic-platform-realignment.md
// §12). Nothing outside this file is ever scanned by discovery.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hive-controls/formic/internal/formats"
	"gopkg.in/yaml.v3"
)

// DefaultPath returns <user-config>/formic/catalog.yaml.
func DefaultPath(userConfigRoot string) string {
	return filepath.Join(userConfigRoot, "formic", "catalog.yaml")
}

// Load reads and validates the catalog file at path. A file that does not exist is an
// empty catalog, not an error (§12: "absent file = empty catalog").
func Load(path string) (formats.Catalog, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return formats.Catalog{Catalog: 1}, nil
	}
	if err != nil {
		return formats.Catalog{}, fmt.Errorf("read catalog %s: %w", path, err)
	}
	jsonData, err := yamlToJSON(data)
	if err != nil {
		return formats.Catalog{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := formats.ValidateCatalog(jsonData); err != nil {
		return formats.Catalog{}, fmt.Errorf("%s: %w", path, err)
	}
	var document formats.Catalog
	if err := json.Unmarshal(jsonData, &document); err != nil {
		return formats.Catalog{}, fmt.Errorf("decode catalog %s: %w", path, err)
	}
	return document, nil
}

// Save writes the catalog file at path, creating its parent directory if needed.
func Save(path string, document formats.Catalog) error {
	data, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create catalog directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write catalog %s: %w", path, err)
	}
	return nil
}

// Add validates candidatePath's toolkit descriptor and, only on success, appends it to
// the catalog file at path (deduped by cleaned absolute path; order is precedence) and
// saves it. Any refusal leaves the file exactly as it was — never partially written.
func Add(path, candidatePath string) (formats.Catalog, error) {
	absolutePath, err := filepath.Abs(candidatePath)
	if err != nil {
		return formats.Catalog{}, fmt.Errorf("resolve %s: %w", candidatePath, err)
	}
	absolutePath = filepath.Clean(absolutePath)
	if err := requireToolkitDescriptor(absolutePath); err != nil {
		return formats.Catalog{}, err
	}

	existing, err := Load(path)
	if err != nil {
		return formats.Catalog{}, err
	}
	for _, source := range existing.Sources {
		if filepath.Clean(source.Path) == absolutePath {
			return existing, nil
		}
	}
	updated := existing
	updated.Catalog = 1
	updated.Sources = append(append([]formats.CatalogSourcesElem(nil), existing.Sources...),
		formats.CatalogSourcesElem{Kind: "dir", Path: absolutePath})
	if err := Save(path, updated); err != nil {
		return formats.Catalog{}, err
	}
	return updated, nil
}

// requireToolkitDescriptor is the admission gate a source must pass to be added at all
// (§12): it must exist, and it must carry a `.formic/toolkit.yaml` that validates.
func requireToolkitDescriptor(absolutePath string) error {
	info, statErr := os.Stat(absolutePath)
	if statErr != nil {
		return fmt.Errorf("path does not exist: %s", absolutePath)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", absolutePath)
	}
	_, err := DescriptorAt(absolutePath)
	return err
}

// DescriptorAt reads and validates sourcePath's `.formic/toolkit.yaml`, decoded into the
// generated Toolkit type. Used both by Add (the admission gate) and by callers that need to
// know whether an UNCATALOGUED directory carries a real toolkit descriptor (§15 `list`'s
// `uncatalogued` report).
func DescriptorAt(sourcePath string) (formats.Toolkit, error) {
	toolkitPath := filepath.Join(sourcePath, ".formic", "toolkit.yaml")
	data, err := os.ReadFile(toolkitPath)
	if errors.Is(err, os.ErrNotExist) {
		return formats.Toolkit{}, fmt.Errorf("no toolkit descriptor at %s", toolkitPath)
	}
	if err != nil {
		return formats.Toolkit{}, fmt.Errorf("read %s: %w", toolkitPath, err)
	}
	jsonData, err := yamlToJSON(data)
	if err != nil {
		return formats.Toolkit{}, fmt.Errorf("%s: %w", toolkitPath, err)
	}
	if err := formats.ValidateToolkit(jsonData); err != nil {
		return formats.Toolkit{}, fmt.Errorf("%s: %w", toolkitPath, err)
	}
	var toolkit formats.Toolkit
	if err := json.Unmarshal(jsonData, &toolkit); err != nil {
		return formats.Toolkit{}, fmt.Errorf("decode %s: %w", toolkitPath, err)
	}
	return toolkit, nil
}

// Uncatalogued describes a directory carrying a valid `.formic/toolkit.yaml` that is not yet
// among the catalog's admitted sources (§12 item 3, N8's deck-side add offer).
type Uncatalogued struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// SourcePaths returns the paths of every source the catalog file at
// DefaultPath(userConfigRoot) admits, in precedence order — shared by every caller that needs
// the plain path list without a full discovered tool set (`formic list`, `formic mission`'s
// source list, and the deck's add-this-repo offer, N8).
func SourcePaths(userConfigRoot string) ([]string, error) {
	document, err := Load(DefaultPath(userConfigRoot))
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	sourcePaths := make([]string, 0, len(document.Sources))
	for _, source := range document.Sources {
		sourcePaths = append(sourcePaths, source.Path)
	}
	return sourcePaths, nil
}

// UncataloguedAt reports repositoryRoot's own toolkit when it carries a valid
// `.formic/toolkit.yaml` that is not already among sourcePaths — the exact computation N8 point
// 3 says both `formic list --json`'s `uncatalogued` field and the deck's one-keypress offer
// share.
func UncataloguedAt(repositoryRoot string, sourcePaths []string) *Uncatalogued {
	for _, sourcePath := range sourcePaths {
		if filepath.Clean(sourcePath) == filepath.Clean(repositoryRoot) {
			return nil
		}
	}
	toolkit, err := DescriptorAt(repositoryRoot)
	if err != nil {
		return nil
	}
	return &Uncatalogued{Name: toolkit.Name, Path: repositoryRoot}
}

func yamlToJSON(data []byte) ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	jsonData, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode YAML as JSON: %w", err)
	}
	return jsonData, nil
}

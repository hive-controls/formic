package discovery

import (
	"fmt"
	"path/filepath"

	toolspec "github.com/hive-controls/toolspec"

	"github.com/hive-controls/hivedeck/internal/manifest"
)

type SourceKind string

const (
	SourceLocal   SourceKind = "repo"
	SourceUser    SourceKind = "user"
	SourceNPM     SourceKind = "npm"
	SourceProduct SourceKind = "product"
)

type Source struct {
	Kind SourceKind
	Path string
}

type Definition struct {
	Name    string
	Command string
	Source  Source
	Problem string
}

type Tool struct {
	Manifest    toolspec.ToolManifest
	Name        string
	Title       string
	Description string
	Source      Source
	Problem     string
	Shadowed    []Definition
}

type Catalog struct {
	Tools []Tool
}

func Discover(repositoryRoot, userConfigRoot string) (Catalog, error) {
	catalog := Catalog{}
	toolIndexes := make(map[string]int)

	localDirectory := filepath.Join(repositoryRoot, ".hivedeck", "tools")
	if err := addManifestDirectory(&catalog, toolIndexes, localDirectory, repositoryRoot, SourceLocal); err != nil {
		return Catalog{}, err
	}
	userDirectory := filepath.Join(userConfigRoot, "hivedeck", "tools")
	if err := addManifestDirectory(&catalog, toolIndexes, userDirectory, repositoryRoot, SourceUser); err != nil {
		return Catalog{}, err
	}
	// Built-in adapters (§4 tier 3): shipped product manifests before npm scripts, so a
	// hand-authored manifest wins a same-name collision over a generically inferred script
	// (e.g. a "e2e-doctor" npm script alongside the shipped e2e-doctor.toolspec.yaml).
	productTools, err := discoverProductManifests(repositoryRoot)
	if err != nil {
		return Catalog{}, err
	}
	for _, tool := range productTools {
		addTool(&catalog, toolIndexes, tool)
	}
	npmTools, err := discoverNPMScripts(repositoryRoot)
	if err != nil {
		return Catalog{}, err
	}
	for _, tool := range npmTools {
		addTool(&catalog, toolIndexes, tool)
	}
	return catalog, nil
}

func addManifestDirectory(catalog *Catalog, indexes map[string]int, directory, repositoryRoot string, kind SourceKind) error {
	paths, err := filepath.Glob(filepath.Join(directory, "*.yaml"))
	if err != nil {
		return fmt.Errorf("scan %s manifests: %w", kind, err)
	}
	for _, path := range paths {
		loaded := manifest.LoadFile(path, repositoryRoot)
		addTool(catalog, indexes, toolFromLoaded(loaded, kind))
	}
	return nil
}

func addTool(catalog *Catalog, indexes map[string]int, candidate Tool) {
	if winnerIndex, exists := indexes[candidate.Name]; exists {
		winner := &catalog.Tools[winnerIndex]
		winner.Shadowed = append(winner.Shadowed, definitionFromTool(candidate))
		return
	}
	indexes[candidate.Name] = len(catalog.Tools)
	catalog.Tools = append(catalog.Tools, candidate)
}

func toolFromLoaded(loaded manifest.Loaded, kind SourceKind) Tool {
	title := loaded.Title
	if title == "" {
		title = loaded.Name
	}
	problem := ""
	if loaded.Problem != nil {
		problem = loaded.Problem.Error()
	}
	return Tool{
		Manifest: loaded.Manifest, Name: loaded.Name, Title: title,
		Description: loaded.Description, Source: Source{Kind: kind, Path: loaded.SourcePath},
		Problem: problem,
	}
}

func definitionFromTool(tool Tool) Definition {
	return Definition{
		Name: tool.Name, Command: tool.Manifest.Launch.Command,
		Source: tool.Source, Problem: tool.Problem,
	}
}

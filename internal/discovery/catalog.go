package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"gopkg.in/yaml.v3"
)

type SourceKind string

const (
	// SourceCatalog is a tool declared under a catalogued source's own `.formic/tools/*.yaml`.
	SourceCatalog SourceKind = "source"
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
	Manifest    formats.ToolManifest
	Name        string
	Title       string
	Description string
	Source      Source
	// SourceRoot is the catalogued source's own root directory — what `{repo}` resolves to
	// in this tool's manifest (§12: "`{repo}` in a manifest resolves to its SOURCE's path").
	// Distinct from Source.Path, which names the manifest FILE for display/shadow purposes.
	SourceRoot string
	Problem    string
	Shadowed   []Definition
}

type Catalog struct {
	Tools []Tool
	// Invalid holds every discovered definition that failed validation. Per the admission
	// rule (§12 item 2), an invalid definition never enters Tools (the armory) and never
	// shadows or is shadowed — it is reported here, for `doctor` alone, with its reason.
	Invalid []Tool
}

// Find looks up an admitted tool by name — the one by-name lookup shared by every caller that
// needs it (cmd/formic's own launch/describe verbs and, from car 9, mission run's pre-flight),
// rather than each keeping its own private linear scan over Tools.
func (catalog Catalog) Find(name string) (Tool, bool) {
	for _, tool := range catalog.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// Discover scans every catalogued source, in precedence order, for tools. The admission
// rule (§12) is enforced here, not just at `formic add` time: a source that no longer
// carries a valid `.formic/toolkit.yaml` contributes nothing, silently — one broken source
// never takes down discovery for every other one. Toolkit-level `needs` are merged onto
// each tool the source contributes (a tool's own entry of the same kind overrides).
func Discover(sourcePaths []string) (Catalog, error) {
	catalog := Catalog{}
	toolIndexes := make(map[string]int)
	for _, sourcePath := range sourcePaths {
		toolkitNeeds, ok := loadToolkitNeeds(sourcePath)
		if !ok {
			continue
		}
		tools, err := discoverSourceTools(sourcePath)
		if err != nil {
			return Catalog{}, err
		}
		for _, tool := range tools {
			if tool.Problem == "" {
				tool.Manifest.Needs = mergeNeeds(toolkitNeeds, tool.Manifest.Needs)
			}
			addTool(&catalog, toolIndexes, tool)
		}
	}
	refuseBrokenServiceNeeds(&catalog)
	return catalog, nil
}

// refuseBrokenServiceNeeds is the cross-manifest rule (§22): a tool whose service need names a
// tool that is not admitted, a tool with no live.probe, or an action the target does not declare
// is Invalid. Moving a tool out can break its own dependents, so it repeats until stable.
func refuseBrokenServiceNeeds(catalog *Catalog) {
	for changed := true; changed; {
		changed = false
		kept := catalog.Tools[:0:0]
		for _, tool := range catalog.Tools {
			if problem := serviceNeedProblem(*catalog, tool); problem != "" {
				tool.Problem = problem
				catalog.Invalid = append(catalog.Invalid, tool)
				changed = true
				continue
			}
			kept = append(kept, tool)
		}
		catalog.Tools = kept
	}
}

func serviceNeedProblem(catalog Catalog, tool Tool) string {
	for _, need := range tool.Manifest.Needs {
		if kind, ok := needKind(need); !ok || kind != "service" {
			continue
		}
		var decoded struct {
			Tool   string `json:"tool"`
			Action string `json:"action"`
		}
		data, err := json.Marshal(need)
		if err != nil || json.Unmarshal(data, &decoded) != nil {
			return "service need is malformed"
		}
		target, found := catalog.Find(decoded.Tool)
		switch {
		case !found:
			return fmt.Sprintf("service need names tool %q, which is not in the catalog", decoded.Tool)
		case target.Manifest.Live == nil || target.Manifest.Live.Probe == nil:
			return fmt.Sprintf("service need names tool %q, which declares no live.probe", decoded.Tool)
		case decoded.Action != "" && !declaresAction(target, decoded.Action):
			return fmt.Sprintf("service need names action %q, which tool %q does not declare", decoded.Action, decoded.Tool)
		}
	}
	return ""
}

func declaresAction(tool Tool, name string) bool {
	for _, action := range tool.Manifest.Actions {
		if action.Name == name {
			return true
		}
	}
	return false
}

// loadToolkitNeeds reads and validates sourcePath's toolkit descriptor. ok is false when
// the descriptor is missing or invalid — the caller's signal to admit nothing from this
// source at all.
func loadToolkitNeeds(sourcePath string) (needs []formats.ToolkitNeed, ok bool) {
	data, err := os.ReadFile(filepath.Join(sourcePath, ".formic", "toolkit.yaml"))
	if err != nil {
		return nil, false
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, false
	}
	jsonData, err := json.Marshal(document)
	if err != nil {
		return nil, false
	}
	if err := formats.ValidateToolkit(jsonData); err != nil {
		return nil, false
	}
	var toolkit formats.Toolkit
	if err := json.Unmarshal(jsonData, &toolkit); err != nil {
		return nil, false
	}
	return toolkit.Needs, true
}

// mergeNeeds applies the toolkit's shared needs beneath the tool's own, per §12: "a tool's
// own entry of the same kind overrides" one inherited from the toolkit. Kind is read as a
// generic JSON object field, since formats.Need's concrete shape is not exported here.
func mergeNeeds(toolkitNeeds []formats.ToolkitNeed, toolNeeds []formats.Need) []formats.Need {
	if len(toolkitNeeds) == 0 {
		return toolNeeds
	}
	ownKinds := make(map[string]bool, len(toolNeeds))
	for _, need := range toolNeeds {
		if kind, ok := needKind(need); ok {
			ownKinds[kind] = true
		}
	}
	merged := append([]formats.Need(nil), toolNeeds...)
	for _, inherited := range toolkitNeeds {
		need, ok := needFromToolkitNeed(inherited)
		if !ok {
			continue
		}
		kind, ok := needKind(need)
		if ok && ownKinds[kind] {
			continue
		}
		merged = append(merged, need)
	}
	return merged
}

func needKind(need formats.Need) (string, bool) {
	data, err := json.Marshal(need)
	if err != nil {
		return "", false
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.Kind == "" {
		return "", false
	}
	return probe.Kind, true
}

func needFromToolkitNeed(toolkitNeed formats.ToolkitNeed) (formats.Need, bool) {
	data, err := json.Marshal(toolkitNeed)
	if err != nil {
		return nil, false
	}
	var need formats.Need
	if err := json.Unmarshal(data, &need); err != nil {
		return nil, false
	}
	return need, true
}

// discoverSourceTools scans one catalogued source root for its declared tools — its
// `.formic/tools/*.yaml` first, then any shipped `**/*.formic.yaml` beneath it.
func discoverSourceTools(sourcePath string) ([]Tool, error) {
	declaredDirectory := filepath.Join(sourcePath, ".formic", "tools")
	declaredTools, err := loadManifestDirectory(declaredDirectory, sourcePath)
	if err != nil {
		return nil, err
	}
	productTools, err := discoverProductManifests(sourcePath)
	if err != nil {
		return nil, err
	}
	return append(declaredTools, productTools...), nil
}

func loadManifestDirectory(directory, sourceRoot string) ([]Tool, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("scan manifests: %w", err)
	}
	tools := make([]Tool, 0, len(paths))
	for _, path := range paths {
		loaded := manifest.LoadFile(path, sourceRoot)
		tools = append(tools, toolFromLoaded(loaded, SourceCatalog, sourceRoot))
	}
	return tools, nil
}

// addTool routes a definition into the armory (catalog.Tools) when valid, or into
// catalog.Invalid when it failed validation — the admission rule (§12 item 2): an invalid
// definition is reported by `doctor`, never listed, launchable, or eligible to shadow.
func addTool(catalog *Catalog, indexes map[string]int, candidate Tool) {
	if candidate.Problem != "" {
		catalog.Invalid = append(catalog.Invalid, candidate)
		return
	}
	if winnerIndex, exists := indexes[candidate.Name]; exists {
		winner := &catalog.Tools[winnerIndex]
		winner.Shadowed = append(winner.Shadowed, definitionFromTool(candidate))
		return
	}
	indexes[candidate.Name] = len(catalog.Tools)
	catalog.Tools = append(catalog.Tools, candidate)
}

func toolFromLoaded(loaded manifest.Loaded, kind SourceKind, sourceRoot string) Tool {
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
		SourceRoot: sourceRoot, Problem: problem,
	}
}

func definitionFromTool(tool Tool) Definition {
	return Definition{
		Name: tool.Name, Command: tool.Manifest.Launch.Command,
		Source: tool.Source, Problem: tool.Problem,
	}
}

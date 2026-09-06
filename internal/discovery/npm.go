package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	toolspec "github.com/hive-controls/toolspec"
)

type packageDocument struct {
	Name       string            `json:"name"`
	Scripts    map[string]string `json:"scripts"`
	Workspaces json.RawMessage   `json:"workspaces"`
}

func discoverNPMScripts(repositoryRoot string) ([]Tool, error) {
	rootPackagePath := filepath.Join(repositoryRoot, "package.json")
	rootDocument, err := readPackageDocument(rootPackagePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tools := toolsFromPackage(rootDocument, rootPackagePath, "")
	patterns, err := workspacePatterns(rootDocument.Workspaces)
	if err != nil {
		return nil, fmt.Errorf("read workspaces from %s: %w", rootPackagePath, err)
	}

	workspacePaths, err := expandWorkspacePaths(repositoryRoot, patterns)
	if err != nil {
		return nil, err
	}
	for _, workspacePath := range workspacePaths {
		packagePath := filepath.Join(workspacePath, "package.json")
		document, readErr := readPackageDocument(packagePath)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		workspaceName := document.Name
		if workspaceName == "" {
			workspaceName = filepath.Base(workspacePath)
		}
		tools = append(tools, toolsFromPackage(document, packagePath, workspaceName)...)
	}
	return tools, nil
}

func readPackageDocument(path string) (packageDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return packageDocument{}, err
	}
	var document packageDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return packageDocument{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return document, nil
}

func toolsFromPackage(document packageDocument, packagePath, workspaceName string) []Tool {
	scriptNames := make([]string, 0, len(document.Scripts))
	for scriptName := range document.Scripts {
		scriptNames = append(scriptNames, scriptName)
	}
	sort.Strings(scriptNames)
	tools := make([]Tool, 0, len(scriptNames))
	for _, scriptName := range scriptNames {
		tools = append(tools, npmTool(document, packagePath, workspaceName, scriptName))
	}
	disambiguateToolNames(tools, scriptNames)
	return tools
}

// disambiguateToolNames makes the sanitized `name` injective within this one package.json:
// two distinct script keys (e.g. "test:unit" and "test-unit") can sanitize to the same
// schema-legal name, which would otherwise make the catalog treat the second as a shadow of
// the first — invisible and unlaunchable, not merely cosmetically duplicated. Every member
// of a collision group is renamed with a suffix derived from ITS OWN original script key
// (never from iteration order), so the resolution is deterministic across runs and both
// tools stay independently launchable. tools and scriptNames are the same length and index-
// aligned: both were built from one pass over the same sorted scriptNames.
func disambiguateToolNames(tools []Tool, scriptNames []string) {
	groups := make(map[string][]int)
	for index, tool := range tools {
		groups[tool.Name] = append(groups[tool.Name], index)
	}
	for _, indexes := range groups {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			disambiguatedName := tools[index].Name + nameSegmentSeparator + stableKeySuffix(scriptNames[index])
			tools[index].Name = disambiguatedName
			tools[index].Manifest.Name = disambiguatedName
			tools[index].Problem = validateSyntheticManifest(tools[index].Manifest)
		}
	}
}

// stableKeySuffix derives a short, deterministic disambiguator from a script's original
// key, so a collision resolves the same way on every run regardless of map iteration order.
func stableKeySuffix(scriptName string) string {
	hash := fnv.New32a()
	hash.Write([]byte(scriptName))
	return fmt.Sprintf("%06x", hash.Sum32()&0xffffff)
}

// nameSegmentSeparator joins a workspace name and a script name into one schema-legal
// `name` (pattern `^[a-z0-9][a-z0-9-]*$`, which forbids ":"). A double hyphen keeps the
// two sanitized segments visually distinguishable from a single hyphen occurring inside
// either segment.
const nameSegmentSeparator = "--"

var invalidNameSegmentCharacters = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeNameSegment folds an arbitrary npm workspace or script identifier into the
// toolspec name pattern: lowercase, collapse every run of disallowed characters (including
// "@", "/", ".", "_") into a single hyphen, and trim leading/trailing hyphens.
func sanitizeNameSegment(value string) string {
	lowered := strings.ToLower(value)
	collapsed := invalidNameSegmentCharacters.ReplaceAllString(lowered, "-")
	trimmed := strings.Trim(collapsed, "-")
	if trimmed == "" {
		return "x"
	}
	return trimmed
}

func npmTool(document packageDocument, packagePath, workspaceName, scriptName string) Tool {
	name := sanitizeNameSegment(scriptName)
	title := "npm run " + scriptName
	arguments := []string{"run", scriptName}
	if workspaceName != "" {
		name = sanitizeNameSegment(workspaceName) + nameSegmentSeparator + sanitizeNameSegment(scriptName)
		title = workspaceName + " · npm run " + scriptName
		arguments = append(arguments, "--workspace", workspaceName)
	}
	description := document.Scripts[scriptName]
	repositoryToken := "{repo}"
	toolManifest := toolspec.ToolManifest{
		Toolspec: 1, Name: name, Title: &title, Description: &description,
		Launch: toolspec.ToolManifestLaunch{Command: "npm", Args: arguments, Cwd: &repositoryToken},
		Pane:    interactivePane(description),
	}
	absolutePath, _ := filepath.Abs(packagePath)
	return Tool{
		Manifest: toolManifest, Name: name, Title: title, Description: description,
		Source: Source{Kind: SourceNPM, Path: absolutePath}, Problem: validateSyntheticManifest(toolManifest),
	}
}

// interactiveCommandPattern matches §11 Q5's one-directional heuristic: a script whose
// underlying command looks like it wants a real TTY (a watch/UI mode, a bare "-i", or a
// REPL/auto-restart tool) is marked interactive. It never clears interactive — a false
// negative just costs a pane, never a broken TTY.
var interactiveCommandPattern = regexp.MustCompile(
	`--watch|--ui|--interactive|(?:^|\s)-i(?:\s|$)|\brepl\b|\bnodemon\b`,
)

// interactivePane returns a `pane.interactive: true` block when the script's command
// matches the heuristic, or nil otherwise (letting the effective mode fall through to the
// profile default and then the built-in `foreground`, per §11 Q5).
func interactivePane(command string) *toolspec.ToolManifestPane {
	if !interactiveCommandPattern.MatchString(command) {
		return nil
	}
	interactive := true
	return &toolspec.ToolManifestPane{Interactive: &interactive}
}

// validateSyntheticManifest runs a built-in adapter's generated manifest through the same
// schema gate every hand-authored manifest goes through, so a bug in adapter code surfaces
// as a visible, greyed-out "invalid" tool instead of an unchecked assumption.
func validateSyntheticManifest(toolManifest toolspec.ToolManifest) string {
	data, err := json.Marshal(toolManifest)
	if err != nil {
		return fmt.Sprintf("encode synthetic manifest: %v", err)
	}
	if err := toolspec.Validate(data); err != nil {
		return err.Error()
	}
	return ""
}

func workspacePatterns(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var patterns []string
	if err := json.Unmarshal(raw, &patterns); err == nil {
		return patterns, nil
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	return object.Packages, nil
}

func expandWorkspacePaths(repositoryRoot string, patterns []string) ([]string, error) {
	uniquePaths := make(map[string]struct{})
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(repositoryRoot, pattern))
		if err != nil {
			return nil, fmt.Errorf("expand workspace pattern %q: %w", pattern, err)
		}
		for _, match := range matches {
			uniquePaths[match] = struct{}{}
		}
	}
	paths := make([]string, 0, len(uniquePaths))
	for path := range uniquePaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

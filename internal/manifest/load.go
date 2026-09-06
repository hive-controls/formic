package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toolspec "github.com/hive-controls/toolspec"
	"gopkg.in/yaml.v3"
)

// UnsupportedManifestVersionPrefix names the state §5/§11 Q2 require for a manifest whose
// `toolspec` version this consumer does not know: a distinct, named state (never a generic
// schema-violation message, and never silently skipped) that both LoadFile and the UI
// recognize.
const UnsupportedManifestVersionPrefix = "unsupported manifest version"

// supportedToolspecVersion is the only `toolspec` value this consumer accepts, per §11 Q2:
// a consumer must accept every version it knows and never guess one from field presence.
const supportedToolspecVersion = 1

type Loaded struct {
	Manifest    toolspec.ToolManifest
	SourcePath  string
	Name        string
	Title       string
	Description string
	Problem     error
}

type ResolvedLaunch struct {
	Command string
	Args    []string
	Cwd     string
}

func LoadFile(path, repositoryRoot string) Loaded {
	absolutePath, _ := filepath.Abs(path)
	loaded := Loaded{SourcePath: absolutePath, Name: manifestNameFallback(path)}
	data, err := os.ReadFile(path)
	if err != nil {
		loaded.Problem = fmt.Errorf("read manifest: %w", err)
		return loaded
	}

	jsonData, metadata, err := yamlToJSON(data)
	if err != nil {
		loaded.Problem = err
		return loaded
	}
	loaded.applyMetadata(metadata)
	if err := unsupportedVersionProblem(jsonData); err != nil {
		loaded.Problem = err
		return loaded
	}
	if err := toolspec.Validate(jsonData); err != nil {
		loaded.Problem = err
		return loaded
	}
	if err := json.Unmarshal(jsonData, &loaded.Manifest); err != nil {
		loaded.Problem = fmt.Errorf("decode manifest: %w", err)
		return loaded
	}
	if _, err := ResolveLaunch(loaded.Manifest, repositoryRoot, "{spec}"); err != nil {
		loaded.Problem = err
	}
	return loaded
}

func ResolveLaunch(tool toolspec.ToolManifest, repositoryRoot, spec string) (ResolvedLaunch, error) {
	values := map[string]string{"repo": repositoryRoot, "spec": spec}
	command, err := substituteTokens(tool.Launch.Command, values)
	if err != nil {
		return ResolvedLaunch{}, fmt.Errorf("launch.command: %w", err)
	}
	arguments := make([]string, len(tool.Launch.Args))
	for index, argument := range tool.Launch.Args {
		arguments[index], err = substituteTokens(argument, values)
		if err != nil {
			return ResolvedLaunch{}, fmt.Errorf("launch.args[%d]: %w", index, err)
		}
	}
	workingDirectory := repositoryRoot
	if tool.Launch.Cwd != nil {
		workingDirectory, err = substituteTokens(*tool.Launch.Cwd, values)
		if err != nil {
			return ResolvedLaunch{}, fmt.Errorf("launch.cwd: %w", err)
		}
	}
	return ResolvedLaunch{Command: command, Args: arguments, Cwd: workingDirectory}, nil
}

// RequiresRealTerminal reports the manifest's one HARD constraint: a tool that declares
// `pane.interactive` needs a real terminal, so capturing it in a pane is illegal whatever a
// profile asks for. It is defined here, once, so the resolver that degrades the mode and the
// launch seam that refuses the route can never disagree about which tools it covers.
func RequiresRealTerminal(tool toolspec.ToolManifest) bool {
	return tool.Pane != nil && tool.Pane.Interactive != nil && *tool.Pane.Interactive
}

func RequiresSpec(tool toolspec.ToolManifest) bool {
	if strings.Contains(tool.Launch.Command, "{spec}") {
		return true
	}
	for _, argument := range tool.Launch.Args {
		if strings.Contains(argument, "{spec}") {
			return true
		}
	}
	return tool.Launch.Cwd != nil && strings.Contains(*tool.Launch.Cwd, "{spec}")
}

func yamlToJSON(data []byte) ([]byte, map[string]any, error) {
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, nil, fmt.Errorf("decode manifest YAML: %w", err)
	}
	jsonData, err := json.Marshal(document)
	if err != nil {
		return nil, nil, fmt.Errorf("encode manifest as JSON: %w", err)
	}
	return jsonData, document, nil
}

// substituteTokens replaces every `{name}` span with its resolved value. Per §5, any span
// between the next unmatched "{" and the following "}" that is not EXACTLY a known token
// name is an unknown-token error — this rejects a stray extra character inside the braces
// ("{repo.path}") and nested braces ("{{repo}}", whose inner span is "{repo", not "repo")
// rather than silently passing them through or partially substituting.
func substituteTokens(value string, values map[string]string) (string, error) {
	var result strings.Builder
	var firstError error
	for index := 0; index < len(value); {
		if value[index] != '{' {
			result.WriteByte(value[index])
			index++
			continue
		}
		closingOffset := strings.IndexByte(value[index:], '}')
		if closingOffset == -1 {
			result.WriteString(value[index:])
			if firstError == nil {
				firstError = fmt.Errorf("unterminated substitution token in %q", value)
			}
			break
		}
		closingIndex := index + closingOffset
		token := value[index : closingIndex+1]
		name := value[index+1 : closingIndex]
		replacement, ok := values[name]
		if !ok {
			if firstError == nil {
				firstError = fmt.Errorf("unknown substitution token %s", token)
			}
			result.WriteString(token)
		} else {
			result.WriteString(replacement)
		}
		index = closingIndex + 1
	}
	return result.String(), firstError
}

// unsupportedVersionProblem intercepts a wrong `toolspec` value BEFORE schema validation,
// so the resulting Problem names the state ("unsupported manifest version") instead of
// surfacing the schema's opaque `const` mismatch. A missing or non-numeric `toolspec` falls
// through unreported here — the schema's "required"/"type" errors already cover those.
func unsupportedVersionProblem(jsonData []byte) error {
	var probe struct {
		Toolspec *float64 `json:"toolspec"`
	}
	if err := json.Unmarshal(jsonData, &probe); err != nil || probe.Toolspec == nil {
		return nil
	}
	if *probe.Toolspec == float64(supportedToolspecVersion) {
		return nil
	}
	return fmt.Errorf("%s: %v", UnsupportedManifestVersionPrefix, *probe.Toolspec)
}

// IsUnsupportedManifestVersion reports whether a Loaded.Problem (or a discovery.Tool's
// carried-forward Problem string) names the unsupported-manifest-version state, so the UI
// can render it distinctly from a generic invalid manifest.
func IsUnsupportedManifestVersion(problem string) bool {
	return strings.HasPrefix(problem, UnsupportedManifestVersionPrefix)
}

func (loaded *Loaded) applyMetadata(document map[string]any) {
	if name, ok := document["name"].(string); ok && name != "" {
		loaded.Name = name
	}
	if title, ok := document["title"].(string); ok {
		loaded.Title = title
	}
	if description, ok := document["description"].(string); ok {
		loaded.Description = description
	}
}

func manifestNameFallback(path string) string {
	baseName := filepath.Base(path)
	return strings.TrimSuffix(baseName, filepath.Ext(baseName))
}

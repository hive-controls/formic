package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hive-controls/formic/internal/formats"
	"gopkg.in/yaml.v3"
)

// UnsupportedManifestVersionPrefix names the state §5/§11 Q2 require for a manifest whose
// `formic` version this consumer does not know: a distinct, named state (never a generic
// schema-violation message, and never silently skipped) that both LoadFile and the UI
// recognize.
const UnsupportedManifestVersionPrefix = "unsupported manifest version"

// supportedFormicVersion is the only `formic` value this consumer accepts, per §11 Q2:
// a consumer must accept every version it knows and never guess one from field presence.
const supportedFormicVersion = 1

type Loaded struct {
	Manifest    formats.ToolManifest
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
	if err := formats.Validate(jsonData); err != nil {
		loaded.Problem = err
		return loaded
	}
	if err := json.Unmarshal(jsonData, &loaded.Manifest); err != nil {
		loaded.Problem = fmt.Errorf("decode manifest: %w", err)
		return loaded
	}
	if err := checkDeclaredArguments(loaded.Manifest.Arguments); err != nil {
		loaded.Problem = err
		return loaded
	}
	if _, err := ResolveLaunch(loaded.Manifest, repositoryRoot, "{spec}", placeholderInputValues(loaded.Manifest.Inputs)); err != nil {
		loaded.Problem = err
		return loaded
	}
	if err := checkActions(loaded.Manifest, repositoryRoot); err != nil {
		loaded.Problem = err
	}
	return loaded
}

// checkDeclaredArguments returns the first semantic problem in a list of declared arguments that
// the schema cannot express: a choice with no options, a default outside its options, or a flag
// declared twice (the argument parser needs each flag to match exactly one row). It takes a plain
// list so any caller holding a merged set of arguments can run the same check.
func checkDeclaredArguments(arguments []formats.Argument) error {
	firstIndexByFlag := make(map[string]int, len(arguments))
	for index, argument := range arguments {
		if earlier, seen := firstIndexByFlag[argument.Flag]; seen {
			return fmt.Errorf("arguments[%d] (%s): flag is already declared by arguments[%d]", index, argument.Flag, earlier)
		}
		firstIndexByFlag[argument.Flag] = index
		if argument.Kind == formats.ArgumentKindChoice && len(argument.Options) == 0 {
			return fmt.Errorf("arguments[%d] (%s): kind choice needs at least one option", index, argument.Flag)
		}
		if argument.Default != nil && len(argument.Options) > 0 && !optionsContain(argument.Options, *argument.Default) {
			return fmt.Errorf("arguments[%d] (%s): default %q is not among its options", index, argument.Flag, *argument.Default)
		}
	}
	return nil
}

func optionsContain(options []formats.Option, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

// placeholderInputValues gives LoadFile's validation-only ResolveLaunch call a value for every
// declared input — the same "{name}" placeholder pattern the call already uses for spec — so a
// manifest whose launch.args/cwd reference a declared input's token loads cleanly without a real
// value being available yet (real values are only known per-launch, via resolveInputsForLaunch).
func placeholderInputValues(declared []formats.Input) map[string]string {
	values := make(map[string]string, len(declared))
	for _, input := range declared {
		values[input.Name] = "{" + input.Name + "}"
	}
	return values
}

// ResolveLaunch substitutes launch.command/args/cwd's "{name}" tokens against repo, spec, and
// every declared input's resolved value (inputs, per N10 — never launch.env; env is resolved
// separately, by name, with no token substitution at all).
func ResolveLaunch(tool formats.ToolManifest, repositoryRoot, spec string, inputs map[string]string) (ResolvedLaunch, error) {
	values := map[string]string{"repo": repositoryRoot, "spec": spec}
	for name, value := range inputs {
		values[name] = value
	}
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
func RequiresRealTerminal(tool formats.ToolManifest) bool {
	return tool.Pane != nil && tool.Pane.Interactive != nil && *tool.Pane.Interactive
}

func RequiresSpec(tool formats.ToolManifest) bool {
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

// unsupportedVersionProblem intercepts a wrong `formic` value BEFORE schema validation,
// so the resulting Problem names the state ("unsupported manifest version") instead of
// surfacing the schema's opaque `const` mismatch. A missing or non-numeric `formic` falls
// through unreported here — the schema's "required"/"type" errors already cover those.
func unsupportedVersionProblem(jsonData []byte) error {
	var probe struct {
		Formic *float64 `json:"formic"`
	}
	if err := json.Unmarshal(jsonData, &probe); err != nil || probe.Formic == nil {
		return nil
	}
	if *probe.Formic == float64(supportedFormicVersion) {
		return nil
	}
	return fmt.Errorf("%s: %v", UnsupportedManifestVersionPrefix, *probe.Formic)
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

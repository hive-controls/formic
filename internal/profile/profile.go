package profile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const FileName = "profile.yaml"

type LaunchMode string

const (
	ModeForeground LaunchMode = "foreground"
	ModeTab        LaunchMode = "tab"
	ModeSplit      LaunchMode = "split"
	ModePane       LaunchMode = "pane"
	// ModeCaptured is recorded on a run row only (the deck's captured-result route); it is
	// never a configurable mode, so validMode does not accept it.
	ModeCaptured LaunchMode = "captured"
)

type Default struct {
	Mode LaunchMode `yaml:"mode,omitempty"`
}

type Tool struct {
	Mode       LaunchMode        `yaml:"mode,omitempty"`
	Spec       string            `yaml:"spec,omitempty"`
	Args       []string          `yaml:"args,omitempty"`
	Env        map[string]string `yaml:"env,omitempty"`
	APIKeyFrom string            `yaml:"apiKeyFrom,omitempty"`
	// Actions holds per-action stored arguments; Args keeps meaning the implicit action's.
	Actions map[string]ActionSettings `yaml:"actions,omitempty"`
}

// ActionSettings is one action's stored settings.
type ActionSettings struct {
	Args []string `yaml:"args,omitempty"`
}

// ArgsFor returns the stored arguments for action; "" is the implicit action (Args).
func (tool Tool) ArgsFor(action string) []string {
	if action == "" {
		return tool.Args
	}
	return tool.Actions[action].Args
}

// WithActionArgs returns a copy of tool with action's stored arguments replaced. It never
// writes into the receiver's Actions map (copy-on-write): that map is shared with the
// File's baseline, and an in-place write would make syncTool see "no change". Empty args
// delete the action's entry; an empty map becomes nil.
func (tool Tool) WithActionArgs(action string, args []string) Tool {
	if action == "" {
		tool.Args = args
		return tool
	}
	updated := cloneActions(tool.Actions)
	if updated == nil {
		updated = make(map[string]ActionSettings)
	}
	if len(args) == 0 {
		delete(updated, action)
	} else {
		updated[action] = ActionSettings{Args: append([]string(nil), args...)}
	}
	if len(updated) == 0 {
		updated = nil
	}
	tool.Actions = updated
	return tool
}

func cloneActions(source map[string]ActionSettings) map[string]ActionSettings {
	if source == nil {
		return nil
	}
	clone := make(map[string]ActionSettings, len(source))
	for name, settings := range source {
		clone[name] = ActionSettings{Args: append([]string(nil), settings.Args...)}
	}
	return clone
}

type File struct {
	Default Default         `yaml:"default,omitempty"`
	Tools   map[string]Tool `yaml:"tools,omitempty"`

	document   *yaml.Node
	original   map[string]Tool
	dirtyTools map[string]bool
}

type ValidationError struct {
	Problems []string
}

func (validationError *ValidationError) Error() string {
	return "profile file is invalid:\n  - " + strings.Join(validationError.Problems, "\n  - ")
}

func Path(repositoryRoot string) string {
	return filepath.Join(repositoryRoot, ".formic", FileName)
}

func Load(repositoryRoot string) (File, error) {
	data, err := os.ReadFile(Path(repositoryRoot))
	if errors.Is(err, os.ErrNotExist) {
		return newFile(), nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read profile: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (File, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return File{}, fmt.Errorf("decode profile YAML: %w", err)
	}
	problems := validateDocument(&document)
	if len(problems) > 0 {
		return File{}, &ValidationError{Problems: problems}
	}
	var parsed File
	if err := document.Decode(&parsed); err != nil {
		return File{}, fmt.Errorf("decode profile: %w", err)
	}
	if parsed.Tools == nil {
		parsed.Tools = make(map[string]Tool)
	}
	parsed.document = &document
	parsed.original = cloneTools(parsed.Tools)
	parsed.dirtyTools = make(map[string]bool)
	return parsed, nil
}

func (file *File) SetTool(name string, tool Tool) {
	if file.Tools == nil {
		file.Tools = make(map[string]Tool)
	}
	file.Tools[name] = tool
	if file.dirtyTools == nil {
		file.dirtyTools = make(map[string]bool)
	}
	file.dirtyTools[name] = true
}

func (file *File) Save(repositoryRoot string) error {
	if err := Validate(*file); err != nil {
		return err
	}
	data, err := file.encoded()
	if err != nil {
		return err
	}
	directory := filepath.Dir(Path(repositoryRoot))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	if err := os.WriteFile(Path(repositoryRoot), data, 0o644); err != nil {
		return fmt.Errorf("write profile: %w", err)
	}
	file.commitBaseline()
	return nil
}

// commitBaseline advances `original` to the values just written to disk and clears
// dirtyTools. Without this, `original` stays frozen at whatever the file was loaded with —
// so a save that returns a tool to its LOAD-TIME value (e.g. foreground -> tab -> foreground)
// is wrongly diffed as "no change" against that stale baseline, and syncTool skips writing
// it, silently leaving an intervening save's value (tab) on disk.
func (file *File) commitBaseline() {
	for name := range file.dirtyTools {
		baseline := file.Tools[name]
		baseline.Actions = cloneActions(baseline.Actions)
		file.original[name] = baseline
	}
	file.dirtyTools = make(map[string]bool)
}

func Validate(file File) error {
	data, err := yaml.Marshal(struct {
		Default Default         `yaml:"default,omitempty"`
		Tools   map[string]Tool `yaml:"tools,omitempty"`
	}{Default: file.Default, Tools: file.Tools})
	if err != nil {
		return fmt.Errorf("encode profile for validation: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode profile for validation: %w", err)
	}
	if problems := validateDocument(&document); len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func (file *File) encoded() ([]byte, error) {
	if file.document == nil {
		return yaml.Marshal(struct {
			Default Default         `yaml:"default,omitempty"`
			Tools   map[string]Tool `yaml:"tools,omitempty"`
		}{Default: file.Default, Tools: file.Tools})
	}
	for name := range file.dirtyTools {
		syncTool(file.document, name, file.original[name], file.Tools[name])
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(file.document); err != nil {
		return nil, fmt.Errorf("encode profile YAML: %w", err)
	}
	return buffer.Bytes(), nil
}

func ResolveMode(file File, toolName string, interactive bool) LaunchMode {
	mode := ModeForeground
	if file.Default.Mode != "" {
		mode = file.Default.Mode
	}
	if tool, ok := file.Tools[toolName]; ok && tool.Mode != "" {
		mode = tool.Mode
	}
	if interactive && mode == ModePane {
		return ModeForeground
	}
	return mode
}

func UnknownToolWarnings(file File, knownToolNames []string) []string {
	known := make(map[string]bool, len(knownToolNames))
	for _, name := range knownToolNames {
		known[name] = true
	}
	var warnings []string
	for name := range file.Tools {
		if !known[name] {
			warnings = append(warnings, fmt.Sprintf("tool %q has no discovered manifest", name))
		}
	}
	sort.Strings(warnings)
	return warnings
}

func ResolveEnvironment(tool Tool, processEnvironment []string) ([]string, error) {
	// original is an IMMUTABLE snapshot: every env.NAME reference resolves against it, never
	// against `values` (the map being built). Resolving against `values` instead — as the
	// prior implementation did — makes one profile entry's literal override contaminate a
	// LATER entry's env.NAME lookup, with the result depending on iteration (key sort) order.
	original := environmentMap(processEnvironment)
	values := environmentMap(processEnvironment)
	keys := make([]string, 0, len(tool.Env))
	for name := range tool.Env {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		resolved, err := resolveEnvironmentValue(tool.Env[name], original)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", name, err)
		}
		values[name] = resolved
	}
	return environmentSlice(values), nil
}

var environmentReferencePattern = regexp.MustCompile(`^env\.[A-Za-z_][A-Za-z0-9_]*$`)
var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var normalizedSecretPattern = regexp.MustCompile(`apikey|token|password|secret|credential|privatekey|accesskey`)

func validateDocument(document *yaml.Node) []string {
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return []string{"profile file must be a mapping with optional default: and tools: mappings"}
	}
	root := document.Content[0]
	problems := unknownKeys(root, map[string]bool{"default": true, "tools": true}, "profile")
	if defaultNode := mappingValue(root, "default"); defaultNode != nil {
		problems = append(problems, validateDefault(defaultNode)...)
	}
	if toolsNode := mappingValue(root, "tools"); toolsNode != nil {
		problems = append(problems, validateTools(toolsNode)...)
	}
	return problems
}

func validateDefault(node *yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return []string{"default must be a mapping"}
	}
	problems := unknownKeys(node, map[string]bool{"mode": true}, "default")
	if mode := mappingValue(node, "mode"); mode != nil {
		problems = append(problems, validateMode("default.mode", mode)...)
	}
	return problems
}

func validateTools(node *yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return []string{"tools must be a mapping of tool name to settings"}
	}
	var problems []string
	for index := 0; index < len(node.Content); index += 2 {
		name := node.Content[index].Value
		problems = append(problems, validateTool(name, node.Content[index+1])...)
	}
	return problems
}

func validateTool(name string, node *yaml.Node) []string {
	prefix := fmt.Sprintf("tool %q", name)
	if node.Kind != yaml.MappingNode {
		return []string{prefix + " must be a mapping"}
	}
	known := map[string]bool{"mode": true, "spec": true, "args": true, "env": true, "apiKeyFrom": true, "actions": true}
	problems := unknownKeys(node, known, prefix)
	if mode := mappingValue(node, "mode"); mode != nil {
		problems = append(problems, validateMode(prefix+".mode", mode)...)
	}
	if spec := mappingValue(node, "spec"); spec != nil && (spec.Kind != yaml.ScalarNode || spec.Tag != "!!str" || spec.Value == "") {
		problems = append(problems, prefix+".spec must be a non-empty string")
	}
	problems = append(problems, validateArguments(prefix, mappingValue(node, "args"))...)
	problems = append(problems, validateEnvironment(prefix, mappingValue(node, "env"))...)
	problems = append(problems, validateReference(prefix+".apiKeyFrom", mappingValue(node, "apiKeyFrom"))...)
	problems = append(problems, validateActions(prefix, mappingValue(node, "actions"))...)
	return problems
}

var actionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validateActions(prefix string, node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return []string{prefix + ".actions must be a mapping of action name to settings"}
	}
	var problems []string
	for index := 0; index < len(node.Content); index += 2 {
		name, value := node.Content[index].Value, node.Content[index+1]
		actionPrefix := fmt.Sprintf("%s.actions.%s", prefix, name)
		if !actionNamePattern.MatchString(name) {
			problems = append(problems, fmt.Sprintf("%s.actions: action name %q must match %s", prefix, name, actionNamePattern))
		}
		if value.Kind != yaml.MappingNode {
			problems = append(problems, actionPrefix+" must be a mapping")
			continue
		}
		problems = append(problems, unknownKeys(value, map[string]bool{"args": true}, actionPrefix)...)
		problems = append(problems, validateArguments(actionPrefix, mappingValue(value, "args"))...)
	}
	return problems
}

func unknownKeys(node *yaml.Node, known map[string]bool, prefix string) []string {
	var problems []string
	for index := 0; index < len(node.Content); index += 2 {
		name := node.Content[index].Value
		if known[name] {
			continue
		}
		if secretShaped(name) && !strings.HasSuffix(strings.ToLower(name), "from") {
			problems = append(problems, fmt.Sprintf("%s: the file is committed; use %sFrom: env.NAME instead of %s", prefix, name, name))
			continue
		}
		problems = append(problems, fmt.Sprintf("%s: unknown key %q", prefix, name))
	}
	return problems
}

func validateMode(path string, node *yaml.Node) []string {
	if node.Kind != yaml.ScalarNode || !validMode(LaunchMode(node.Value)) {
		return []string{fmt.Sprintf("%s must be foreground, tab, split, or pane", path)}
	}
	return nil
}

func validateArguments(prefix string, node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return []string{prefix + ".args must be a list of strings"}
	}
	var problems []string
	for index, argument := range node.Content {
		if argument.Kind != yaml.ScalarNode || argument.Tag != "!!str" {
			problems = append(problems, fmt.Sprintf("%s.args[%d] must be a string", prefix, index))
		}
	}
	return problems
}

func validateEnvironment(prefix string, node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return []string{prefix + ".env must be a mapping"}
	}
	var problems []string
	for index := 0; index < len(node.Content); index += 2 {
		name, value := node.Content[index].Value, node.Content[index+1]
		if !environmentNamePattern.MatchString(name) {
			problems = append(problems, fmt.Sprintf("%s.env has invalid environment name %q", prefix, name))
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
			problems = append(problems, fmt.Sprintf("%s.env.%s must be a non-empty string", prefix, name))
		} else if secretShaped(name) && !environmentReferencePattern.MatchString(value.Value) {
			problems = append(problems, fmt.Sprintf("%s.env.%s: the file is committed; use env.NAME instead of a literal secret", prefix, name))
		}
	}
	return problems
}

func validateReference(path string, node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.ScalarNode || !environmentReferencePattern.MatchString(node.Value) {
		return []string{fmt.Sprintf("%s must match env.NAME", path)}
	}
	return nil
}

func validMode(mode LaunchMode) bool {
	return mode == ModeForeground || mode == ModeTab || mode == ModeSplit || mode == ModePane
}

func secretShaped(name string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(name))
	return normalizedSecretPattern.MatchString(normalized)
}

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

func resolveEnvironmentValue(value string, environment map[string]string) (string, error) {
	if !environmentReferencePattern.MatchString(value) {
		return value, nil
	}
	name := strings.TrimPrefix(value, "env.")
	resolved, exists := environment[name]
	if !exists {
		return "", fmt.Errorf("source %s is not set", name)
	}
	return resolved, nil
}

func environmentSlice(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result
}

func newFile() File {
	return File{Tools: make(map[string]Tool), original: make(map[string]Tool), dirtyTools: make(map[string]bool)}
}

// Clone returns a fully independent copy of file, including its YAML document tree, so a
// caller can attempt SetTool + Save on the copy and only adopt it once Save actually
// succeeds — a rejected edit (a validation problem OR a write failure) must never mutate the
// caller's live profile, on pain of a later Esc, or a launch, silently picking up settings
// that were never actually saved.
func (file File) Clone() File {
	return File{
		Default:    file.Default,
		Tools:      cloneTools(file.Tools),
		document:   cloneNode(file.document),
		original:   cloneTools(file.original),
		dirtyTools: cloneDirty(file.dirtyTools),
	}
}

func cloneTools(source map[string]Tool) map[string]Tool {
	clone := make(map[string]Tool, len(source))
	for name, tool := range source {
		tool.Actions = cloneActions(tool.Actions)
		clone[name] = tool
	}
	return clone
}

func cloneDirty(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source))
	for name, dirty := range source {
		clone[name] = dirty
	}
	return clone
}

// cloneNode deep-copies a yaml.Node tree. A shallow copy of *yaml.Node would still share
// every child through its Content slice, so syncTool's in-place `*existing = encoded`
// mutation on a "cloned" document would silently corrupt the original document too.
func cloneNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	if node.Content != nil {
		clone.Content = make([]*yaml.Node, len(node.Content))
		for index, child := range node.Content {
			clone.Content[index] = cloneNode(child)
		}
	}
	clone.Alias = cloneNode(node.Alias)
	return &clone
}

func mappingValue(mapping *yaml.Node, name string) *yaml.Node {
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func syncTool(document *yaml.Node, name string, original, updated Tool) {
	root := document.Content[0]
	tools := ensureMappingValue(root, "tools")
	tool := ensureMappingValue(tools, name)
	if original.Mode != updated.Mode {
		setEncodedValue(tool, "mode", updated.Mode)
	}
	if original.Spec != updated.Spec {
		setEncodedValue(tool, "spec", updated.Spec)
	}
	if !reflect.DeepEqual(original.Args, updated.Args) {
		setEncodedValue(tool, "args", updated.Args)
	}
	if !reflect.DeepEqual(original.Actions, updated.Actions) {
		setEncodedValue(tool, "actions", updated.Actions)
	}
	if !reflect.DeepEqual(original.Env, updated.Env) {
		setEncodedValue(tool, "env", updated.Env)
	}
	if original.APIKeyFrom != updated.APIKeyFrom {
		setEncodedValue(tool, "apiKeyFrom", updated.APIKeyFrom)
	}
}

func ensureMappingValue(mapping *yaml.Node, name string) *yaml.Node {
	if existing := mappingValue(mapping, name); existing != nil {
		return existing
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, key, value)
	return value
}

func setEncodedValue(mapping *yaml.Node, name string, value any) {
	if reflect.ValueOf(value).IsZero() {
		removeMappingValue(mapping, name)
		return
	}
	var encoded yaml.Node
	_ = encoded.Encode(value)
	if existing := mappingValue(mapping, name); existing != nil {
		encoded.HeadComment, encoded.LineComment = existing.HeadComment, existing.LineComment
		*existing = encoded
		return
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	mapping.Content = append(mapping.Content, key, &encoded)
}

func removeMappingValue(mapping *yaml.Node, name string) {
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != name {
			continue
		}
		mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
		return
	}
}

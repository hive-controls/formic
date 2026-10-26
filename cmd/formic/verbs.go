package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
)

// --- shared helpers ---

func hasJSONFlag(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--json" {
			return true
		}
	}
	return false
}

// positionalArgument returns the first argument that is not a recognized flag.
func positionalArgument(arguments []string) string {
	for _, argument := range arguments {
		if argument != "--json" {
			return argument
		}
	}
	return ""
}

// defaultEquipPath resolves <user-config>/formic/equip.yaml, honouring FORMIC_CONFIG_DIR the
// same way catalogSourcePaths resolves catalog.yaml.
func defaultEquipPath() (string, error) {
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return "", err
	}
	return equip.DefaultPath(userConfigRoot), nil
}

// refusal is the structured object every verb's refusal returns under --json (§15: "A
// refusal is a structured object, never an attempted capture").
type refusal struct {
	Refused bool   `json:"refused"`
	Reason  string `json:"reason"`
}

// childStdout is the launched tool's own stdout target: under --json, the parent's
// stdout is reserved for the JSON summary alone, so the child's stdout is redirected to the
// parent's stderr instead — a human still sees it, but a machine consumer parsing stdout for
// exactly one JSON object never receives the child's bytes. Without --json, nothing changes.
func childStdout(jsonOutput bool) *os.File {
	if jsonOutput {
		return os.Stderr
	}
	return os.Stdout
}

func refuse(jsonOutput bool, reason string) int {
	if jsonOutput {
		printJSON(refusal{Refused: true, Reason: reason})
	} else {
		fmt.Fprintf(os.Stderr, "formic: %s\n", reason)
	}
	return 1
}

// --- list ---

type listedTool struct {
	Name       string   `json:"name"`
	Title      string   `json:"title"`
	Source     string   `json:"source"`
	ShadowedBy []string `json:"shadowedBy,omitempty"`
}

type listResult struct {
	Tools        []listedTool          `json:"tools"`
	Uncatalogued *catalog.Uncatalogued `json:"uncatalogued"`
}

// runList is `formic list` (§15): every admitted tool, plus the current directory's own
// toolkit when it is valid and not yet catalogued.
func runList(arguments []string) int {
	jsonOutput := hasJSONFlag(arguments)
	repositoryRoot, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	sourcePaths, err := catalogSourcePaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	result := buildListResult(discoveredCatalog, repositoryRoot, sourcePaths)
	if jsonOutput {
		printJSON(result)
		return 0
	}
	for _, tool := range result.Tools {
		fmt.Printf("%s\t%s\t%s\n", tool.Name, tool.Title, tool.Source)
		for _, shadow := range tool.ShadowedBy {
			fmt.Printf("  shadowed-by %s\n", shadow)
		}
	}
	if result.Uncatalogued != nil {
		fmt.Printf("uncatalogued: %s (%s)\n", result.Uncatalogued.Name, result.Uncatalogued.Path)
	}
	return 0
}

func buildListResult(discoveredCatalog discovery.Catalog, repositoryRoot string, sourcePaths []string) listResult {
	tools := make([]listedTool, 0, len(discoveredCatalog.Tools))
	for _, tool := range discoveredCatalog.Tools {
		shadowedBy := make([]string, 0, len(tool.Shadowed))
		for _, shadow := range tool.Shadowed {
			shadowedBy = append(shadowedBy, shadow.Source.Path)
		}
		tools = append(tools, listedTool{Name: tool.Name, Title: tool.Title, Source: tool.Source.Path, ShadowedBy: shadowedBy})
	}
	return listResult{Tools: tools, Uncatalogued: catalog.UncataloguedAt(repositoryRoot, sourcePaths)}
}

// --- describe ---

type describedNeed struct {
	Need formats.Need `json:"need"`
	// Equipped is the need's real resolution against the machine's equip file (§13, §15),
	// via internal/equip.Resolve — real per-need status now that car 7 has landed the equip
	// file, never a fixed placeholder.
	Equipped bool `json:"equipped"`
	// State is met, unmet, or unchecked (a network policy, or a service need not probed here).
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// describedInput reports the manifest's declared input fields (Label/Kind, carried through
// unchanged — car 10's input prompt is built from this shape and needs the label) plus how the
// operator may pass it non-interactively (§14 N10) — never a value.
type describedInput struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Required  bool   `json:"required"`
	Sensitive bool   `json:"sensitive"`
	// Source is "env" for a sensitive input (--input would echo it on the command line, and is
	// refused) or "flag-or-env" for a non-sensitive one, which may use either form.
	Source string `json:"source"`
}

type describeResult struct {
	Manifest formats.ToolManifest `json:"manifest"`
	Mode     string               `json:"mode"`
	Inputs   []describedInput     `json:"inputs"`
	Needs    []describedNeed      `json:"needs"`
	// Actions lists the declared actions[]; omitted for a manifest with none.
	Actions []describedAction `json:"actions,omitempty"`
	// Health is the tool's declared health.check, run here and never persisted (§15 N11) — nil
	// when the manifest declares no health block at all.
	Health *launch.HealthResult `json:"health,omitempty"`
	// Live is the tool's declared live.probe, run once here and never persisted (§22 b) — nil
	// when the manifest declares none.
	Live *probeReport `json:"live,omitempty"`
}

func describeInputs(declared []formats.Input) []describedInput {
	described := make([]describedInput, 0, len(declared))
	for _, input := range declared {
		source := "flag-or-env"
		if input.Sensitive {
			source = "env"
		}
		var label, kind string
		if input.Label != nil {
			label = *input.Label
		}
		if input.Kind != nil {
			kind = *input.Kind
		}
		described = append(described, describedInput{
			Name: input.Name, Label: label, Kind: kind,
			Required: input.Required, Sensitive: input.Sensitive, Source: source,
		})
	}
	return described
}

// printDeclaredArguments prints one text line per declared launch argument and one per env
// entry that enumerates its values (`describe --json` carries both inside the embedded manifest).
func printDeclaredArguments(declared formats.ToolManifest) {
	for _, argument := range declared.Arguments {
		line := fmt.Sprintf("argument: %s kind=%s", argument.Flag, argument.Kind)
		if len(argument.Options) > 0 {
			line += fmt.Sprintf(" options=%v", optionValues(argument.Options))
		}
		if argument.Default != nil {
			line += " default=" + *argument.Default
		}
		fmt.Println(line)
	}
	for _, entry := range declared.Env {
		if len(entry.Options) > 0 {
			fmt.Printf("env option: %s %v\n", entry.Name, optionValues(entry.Options))
		}
	}
}

func optionValues(options []formats.Option) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	return values
}

// runDescribe is `formic describe <tool>` (§15): the manifest, resolved launch mode, its
// inputs, and each need's real resolution against the machine's equip file.
func runDescribe(arguments []string) int {
	jsonOutput := hasJSONFlag(arguments)
	name := positionalArgument(arguments)
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: formic describe <tool> [--json]")
		return 2
	}
	repositoryRoot, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	tool, found := discoveredCatalog.Find(name)
	if !found {
		return refuse(jsonOutput, fmt.Sprintf("unknown tool %q", name))
	}
	repositoryProfile, _ := profile.Load(repositoryRoot)
	mode := profile.ResolveMode(repositoryProfile, tool.Name, manifest.RequiresRealTerminal(tool.Manifest))
	equipPath, err := defaultEquipPath()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	equipFile, err := equip.Load(equipPath)
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	statuses := equip.Resolve(tool.Manifest.Needs, tool.Manifest.Env, equipFile, os.Environ())
	needs := make([]describedNeed, 0, len(statuses))
	for _, status := range statuses {
		needs = append(needs, describedNeed{Need: status.Need, Equipped: status.Met, State: status.State, Reason: status.Reason, Reference: status.Reference})
	}
	health := launch.RunHealthCheck(tool, repositoryProfile.Tools[tool.Name], os.Environ(), repositoryRoot)
	result := describeResult{Manifest: tool.Manifest, Mode: string(mode), Inputs: describeInputs(tool.Manifest.Inputs), Needs: needs, Health: health}
	if len(tool.Manifest.Actions) > 0 {
		result.Actions = describeActions(tool.Manifest.Actions)
	}
	if tool.Manifest.Live != nil {
		liveReport := toProbeReport(tool.Name, launch.Probe(context.Background(), tool, repositoryRoot))
		result.Live = &liveReport
	}
	if jsonOutput {
		printJSON(result)
		return 0
	}
	fmt.Printf("%s (%s)\n", tool.Name, tool.Title)
	fmt.Printf("mode: %s\n", result.Mode)
	fmt.Printf("launch: %s %v\n", tool.Manifest.Launch.Command, tool.Manifest.Launch.Args)
	for _, input := range result.Inputs {
		fmt.Printf("input: %+v\n", input)
	}
	printDescribedActions(result.Actions)
	printDeclaredArguments(tool.Manifest)
	for _, need := range result.Needs {
		fmt.Printf("need: %+v equipped=%v\n", need.Need, need.Equipped)
	}
	if result.Health != nil {
		fmt.Printf("health: ran=%v ok=%v (exit code %s) error=%q durationMs=%d\n",
			result.Health.Ran, result.Health.OK, formatExitCode(result.Health.ExitCode), result.Health.Error, result.Health.DurationMs)
	}
	if result.Live != nil {
		fmt.Printf("live: %s (%s)\n", result.Live.State, result.Live.Detail)
	}
	return 0
}

// --- launch ---

// needsRefusal is `launch`'s structured refusal (§13, §15): "a refusal is a structured
// object, never an attempted capture." hint names one `formic equip` invocation that would
// help, drawn from the first unmet need.
type needsRefusal struct {
	Refused bool               `json:"refused"`
	Reason  string             `json:"reason"`
	Needs   []equip.NeedStatus `json:"needs"`
	Hint    string             `json:"hint"`
}

// refuseNeeds reports every unmet need at once, never just the first, mirroring
// scopeDeclaredEnvironment's own "name every missing key at once" discipline.
func refuseNeeds(jsonOutput bool, refused *launch.NeedsRefused) int {
	if jsonOutput {
		printJSON(needsRefusal{Refused: true, Reason: "unmet need", Needs: refused.Needs, Hint: refused.Hint})
		return 1
	}
	fmt.Fprintln(os.Stderr, "formic: unmet need")
	for _, status := range refused.Needs {
		fmt.Fprintf(os.Stderr, "  %+v: %s\n", status.Need, status.Reason)
	}
	fmt.Fprintf(os.Stderr, "hint: %s\n", refused.Hint)
	return 1
}

// inputsRefusal is `launch`'s structured refusal for an input problem (§14 N10, §15): the same
// "refused: true, reason, ..., hint" shape refuseNeeds renders, extended with every list a
// *launch.InputsRefused may carry. Every list names NAMES only — never a value, sensitive or
// not, so a refusal can never itself echo the thing it is refusing to accept.
type inputsRefusal struct {
	Refused   bool     `json:"refused"`
	Reason    string   `json:"reason"`
	Missing   []string `json:"missing,omitempty"`
	Unknown   []string `json:"unknown,omitempty"`
	Sensitive []string `json:"sensitive,omitempty"`
	Duplicate []string `json:"duplicate,omitempty"`
	Hint      string   `json:"hint"`
}

// refuseInputs renders a *launch.InputsRefused the same way refuseNeeds renders a
// *launch.NeedsRefused, whether it came from this verb's own CLI-parsing-time check
// (Duplicate/Sensitive) or from launch.Prepare (Missing/Unknown).
func refuseInputs(jsonOutput bool, refused *launch.InputsRefused) int {
	if jsonOutput {
		printJSON(inputsRefusal{
			Refused: true, Reason: "missing input", Missing: refused.Missing, Unknown: refused.Unknown,
			Sensitive: refused.Sensitive, Duplicate: refused.Duplicate, Hint: refused.Hint,
		})
		return 1
	}
	fmt.Fprintln(os.Stderr, "formic: input problem")
	for _, name := range refused.Missing {
		fmt.Fprintf(os.Stderr, "  missing: %s\n", name)
	}
	for _, name := range refused.Unknown {
		fmt.Fprintf(os.Stderr, "  unknown: %s\n", name)
	}
	for _, name := range refused.Sensitive {
		fmt.Fprintf(os.Stderr, "  sensitive (must not be passed via --input): %s\n", name)
	}
	for _, name := range refused.Duplicate {
		fmt.Fprintf(os.Stderr, "  given twice: %s\n", name)
	}
	fmt.Fprintf(os.Stderr, "hint: %s\n", refused.Hint)
	return 1
}

func formatExitCode(exitCode *int) string {
	if exitCode == nil {
		return "unknown"
	}
	return strconv.Itoa(*exitCode)
}

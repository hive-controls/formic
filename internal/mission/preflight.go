package mission

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
)

// Values is one mission run's raw "--input name=value" pairs, in the order they were given —
// the same shape cmd/formic's own extractInputFlags returns for an ordinary launch, so a
// duplicate name given twice is detected the same way.
type Values []string

// Problem is one pre-flight failure. Kind is one of: tool-not-found, tool-invalid, unmet-need,
// with-undeclared, with-sensitive, input-unbound, with-token-unresolved, missing-input,
// unknown-input, duplicate-input, sensitive-on-argv, not-runnable, evidence-undeclared, service-lifecycle.
type Problem struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// PreflightRefused is returned when Preflight finds any problem at all — every problem found,
// never just the first (§9/§15: "a refusal lists every problem and runs nothing").
type PreflightRefused struct {
	Mission  string
	Problems []Problem
}

func (refused *PreflightRefused) Error() string {
	return fmt.Sprintf("mission %s refused pre-flight (%d problem(s))", refused.Mission, len(refused.Problems))
}

// Plan is what a successful pre-flight hands mission run: the resolved tool, its merged input
// values (ready to become launch.Request.Inputs), and the mission's resolved evidence ROOT
// (<evidence>/<mission-name>/, per the orchestrator's path ruling 2026-09-23) — ready to become
// launch.Request.EvidenceRoot, which mints the per-run <run-id> segment itself, inside Prepare.
type Plan struct {
	Tool         discovery.Tool
	Inputs       map[string]string
	EvidenceRoot string
}

// ResolveEvidenceRoot resolves a mission's `evidence:` path (§9), per the orchestrator's ruling
// (2026-09-23): absolute as-is; relative against the mission's SourceRoot for a toolkit
// mission, against workingDirectory for a personal one — then the mission's own name segment,
// since every run's evidence folder is <resolved evidence>/<mission-name>/<run-id>/ and the run
// id is minted later, inside launch.Prepare.
func ResolveEvidenceRoot(m Mission, workingDirectory string) string {
	base := m.Definition.Evidence
	if !filepath.IsAbs(base) {
		if m.Personal {
			base = filepath.Join(workingDirectory, base)
		} else {
			base = filepath.Join(m.SourceRoot, base)
		}
	}
	return filepath.Join(base, m.Definition.Name)
}

// Preflight checks EVERY problem §9 names before a mission run is allowed to launch anything:
// the tool resolves and validates, its needs are equipped, every `with:` key is a declared
// input, no `with:` value ever supplies a sensitive input (founder ruling 3), every mission
// input has a value, and the tool can run in this context.
func Preflight(m Mission, catalog discovery.Catalog, workingDirectory string, values Values, environment []string, jsonMode bool) (Plan, *PreflightRefused) {
	var problems []Problem
	step := m.Definition.Steps[0]

	tool, toolProblems := resolveStepTool(catalog, step.Tool)
	problems = append(problems, toolProblems...)
	if len(toolProblems) > 0 {
		// No declared tool to check with:/needs/inputs against — every further check would be
		// meaningless (a nil Manifest), so this refusal reports only what it already found.
		return Plan{}, &PreflightRefused{Mission: m.Definition.Name, Problems: problems}
	}

	if tool.Manifest.Lifecycle != nil && *tool.Manifest.Lifecycle == formats.ToolManifestLifecycleService {
		problems = append(problems, Problem{Kind: "service-lifecycle", Detail: tool.Name})
	}

	problems = append(problems, unmetNeedProblems(tool, environment)...)

	declaredInputs := make(map[string]formats.Input, len(tool.Manifest.Inputs))
	for _, input := range tool.Manifest.Inputs {
		declaredInputs[input.Name] = input
	}

	withProblems, substitutionValues := checkWith(step.With, declaredInputs)
	problems = append(problems, withProblems...)

	missionInputSet := make(map[string]bool, len(m.Definition.Inputs))
	for _, name := range m.Definition.Inputs {
		missionInputSet[name] = true
	}

	classified, classifyProblems := classifyValues(values, environment, missionInputSet, declaredInputs)
	problems = append(problems, classifyProblems...)

	for _, name := range m.Definition.Inputs {
		if _, bound := classified.missionInputValues[name]; !bound {
			problems = append(problems, Problem{Kind: "input-unbound", Detail: name})
		}
	}

	substituted, tokenProblems := substituteMissionTokens(substitutionValues, classified.missionInputValues)
	problems = append(problems, tokenProblems...)

	mergedInputs := mergeInputValues(substituted, classified.toolOverrides)
	for name, input := range declaredInputs {
		if input.Required {
			if _, given := mergedInputs[name]; !given {
				problems = append(problems, Problem{Kind: "missing-input", Detail: name})
			}
		}
	}

	if jsonMode {
		if _, isTerminal := launch.DetectTerminal(environment); !isTerminal && manifest.RequiresRealTerminal(tool.Manifest) {
			problems = append(problems, Problem{Kind: "not-runnable", Detail: tool.Name})
		}
	}

	if tool.Manifest.Outputs != nil && tool.Manifest.Outputs.EvidenceDir != nil {
		if !declaresEnvironmentKey(tool.Manifest.Env, *tool.Manifest.Outputs.EvidenceDir) {
			problems = append(problems, Problem{Kind: "evidence-undeclared", Detail: *tool.Manifest.Outputs.EvidenceDir})
		}
	}

	sortProblems(problems)
	if len(problems) > 0 {
		return Plan{}, &PreflightRefused{Mission: m.Definition.Name, Problems: problems}
	}
	return Plan{Tool: tool, Inputs: mergedInputs, EvidenceRoot: ResolveEvidenceRoot(m, workingDirectory)}, nil
}

func resolveStepTool(catalog discovery.Catalog, name string) (discovery.Tool, []Problem) {
	if tool, found := catalog.Find(name); found {
		return tool, nil
	}
	for _, invalid := range catalog.Invalid {
		if invalid.Name == name {
			return discovery.Tool{}, []Problem{{Kind: "tool-invalid", Detail: invalid.Problem}}
		}
	}
	return discovery.Tool{}, []Problem{{Kind: "tool-not-found", Detail: name}}
}

// unmetNeedProblems resolves tool's merged needs against the machine's equip file, through
// launch.ResolveNeedStatuses — the one shared implementation resolveNeedsForLaunch (internal to
// internal/launch) and this pre-flight check both build on.
func unmetNeedProblems(tool discovery.Tool, environment []string) []Problem {
	statuses, err := launch.ResolveNeedStatuses(tool, environment)
	if err != nil {
		return []Problem{{Kind: "unmet-need", Detail: err.Error()}}
	}
	var problems []Problem
	for _, status := range statuses {
		if status.Blocks() {
			problems = append(problems, Problem{Kind: "unmet-need", Detail: status.Reason})
		}
	}
	return problems
}

// checkWith validates every `with:` key against tool's declared inputs (founder rulings — an
// undeclared key refuses, a sensitive one refuses unconditionally regardless of its value) and
// returns the surviving keys' raw values for token substitution.
func checkWith(with map[string]string, declaredInputs map[string]formats.Input) ([]Problem, map[string]string) {
	var problems []Problem
	values := make(map[string]string, len(with))
	for key, value := range with {
		input, declared := declaredInputs[key]
		if !declared {
			problems = append(problems, Problem{Kind: "with-undeclared", Detail: key})
			continue
		}
		if input.Sensitive {
			problems = append(problems, Problem{Kind: "with-sensitive", Detail: key})
			continue
		}
		values[key] = value
	}
	return problems, values
}

func declaresEnvironmentKey(declared []formats.ToolManifestEnvElem, name string) bool {
	for _, entry := range declared {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func mergeInputValues(base, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overrides))
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range overrides {
		merged[name] = value
	}
	return merged
}

func sortProblems(problems []Problem) {
	sort.Slice(problems, func(left, right int) bool {
		if problems[left].Kind != problems[right].Kind {
			return problems[left].Kind < problems[right].Kind
		}
		return problems[left].Detail < problems[right].Detail
	})
}

// classifiedValues is the result of binding a mission run's --input/FORMIC_INPUT_<NAME> values
// (founder ruling 4): a value whose name matches a mission-level input binds it (for {x} token
// substitution into with:); otherwise a value whose name matches a declared tool input
// overrides/completes that tool input directly; any other name is unknown.
type classifiedValues struct {
	missionInputValues map[string]string
	toolOverrides      map[string]string
}

// classifyValues mirrors cmd/formic/inputs.go's own parseInputArguments grammar (repeatable
// --input, duplicates refused, a sensitive input on argv refused, FORMIC_INPUT_<NAME> accepted
// for any declared name) but over the UNION of mission-level input names and the step tool's
// declared input names, per founder ruling 4's binding rule.
func classifyValues(values Values, environment []string, missionInputs map[string]bool, declaredInputs map[string]formats.Input) (classifiedValues, []Problem) {
	flagValues := map[string]string{}
	duplicateNames := map[string]bool{}
	for _, pair := range values {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			name, value = pair, ""
		}
		if _, exists := flagValues[name]; exists {
			duplicateNames[name] = true
			continue
		}
		flagValues[name] = value
	}

	result := classifiedValues{missionInputValues: map[string]string{}, toolOverrides: map[string]string{}}
	var unknown, sensitiveViaFlag []string
	for name, value := range flagValues {
		switch {
		case missionInputs[name]:
			result.missionInputValues[name] = value
		case declaredInputs[name].Name == name:
			if declaredInputs[name].Sensitive {
				sensitiveViaFlag = append(sensitiveViaFlag, name)
				continue
			}
			result.toolOverrides[name] = value
		default:
			unknown = append(unknown, name)
		}
	}

	environmentValues := environmentByName(environment)
	candidateNames := make(map[string]bool, len(missionInputs)+len(declaredInputs))
	for name := range missionInputs {
		candidateNames[name] = true
	}
	for name := range declaredInputs {
		candidateNames[name] = true
	}
	for name := range candidateNames {
		envValue, ok := environmentValues[inputEnvironmentName(name)]
		if !ok || envValue == "" {
			continue
		}
		if _, alsoFromFlag := flagValues[name]; alsoFromFlag {
			duplicateNames[name] = true
			continue
		}
		if missionInputs[name] {
			result.missionInputValues[name] = envValue
		} else {
			result.toolOverrides[name] = envValue
		}
	}

	var duplicate []string
	for name := range duplicateNames {
		duplicate = append(duplicate, name)
	}
	sort.Strings(duplicate)
	sort.Strings(unknown)
	sort.Strings(sensitiveViaFlag)

	var problems []Problem
	for _, name := range duplicate {
		problems = append(problems, Problem{Kind: "duplicate-input", Detail: name})
	}
	for _, name := range unknown {
		problems = append(problems, Problem{Kind: "unknown-input", Detail: name})
	}
	for _, name := range sensitiveViaFlag {
		problems = append(problems, Problem{Kind: "sensitive-on-argv", Detail: name})
	}
	return result, problems
}

func environmentByName(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

// inputEnvironmentName mirrors cmd/formic/inputs.go's own unexported helper of the same name:
// an input's FORMIC_INPUT_<NAME> variable, upper-cased with every non-alphanumeric byte
// replaced by '_'.
func inputEnvironmentName(name string) string {
	var builder strings.Builder
	builder.WriteString("FORMIC_INPUT_")
	for _, character := range strings.ToUpper(name) {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

// substituteMissionTokens replaces every `{x}` span in each with: value with its bound mission
// input value. A token naming something other than a declared mission input is
// with-token-unresolved — a genuinely unknown token, distinct from input-unbound's "declared but
// no value yet" (a declared-but-unbound token substitutes as empty here, so it is reported once,
// by the input-unbound check, never twice).
func substituteMissionTokens(values, missionInputValues map[string]string) (map[string]string, []Problem) {
	var problems []Problem
	substituted := make(map[string]string, len(values))
	for key, value := range values {
		result, unresolved := substituteTokens(value, missionInputValues)
		for _, token := range unresolved {
			problems = append(problems, Problem{Kind: "with-token-unresolved", Detail: key + ": " + token})
		}
		substituted[key] = result
	}
	return substituted, problems
}

// substituteTokens replaces every `{name}` span in value. A name present in bound (even with an
// empty string) substitutes; a name absent from bound entirely is reported as unresolved and
// left as-is.
func substituteTokens(value string, bound map[string]string) (string, []string) {
	var result strings.Builder
	var unresolved []string
	for index := 0; index < len(value); {
		if value[index] != '{' {
			result.WriteByte(value[index])
			index++
			continue
		}
		closingOffset := strings.IndexByte(value[index:], '}')
		if closingOffset == -1 {
			result.WriteString(value[index:])
			break
		}
		closingIndex := index + closingOffset
		token := value[index : closingIndex+1]
		name := value[index+1 : closingIndex]
		replacement, ok := bound[name]
		if !ok {
			unresolved = append(unresolved, token)
			result.WriteString(token)
		} else {
			result.WriteString(replacement)
		}
		index = closingIndex + 1
	}
	return result.String(), unresolved
}

package main

import (
	"sort"
	"strings"

	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
)

// extractInputFlags pulls every "--input name=value" pair out of arguments, in order,
// returning the raw "name=value" strings and the remaining arguments with those two-token
// pairs removed — so the existing --json/positional-tool-name parsing sees exactly what it saw
// before --input existed. A trailing "--input" with nothing after it is left in rest, where the
// existing positional-argument scan will reject it as an unrecognized value.
func extractInputFlags(arguments []string) (inputPairs []string, rest []string) {
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == "--input" && index+1 < len(arguments) {
			inputPairs = append(inputPairs, arguments[index+1])
			index++
			continue
		}
		rest = append(rest, arguments[index])
	}
	return inputPairs, rest
}

// inputEnvironmentName derives a declared input's FORMIC_INPUT_<NAME> variable name (§14 N10):
// the input's name, upper-cased, with every non-alphanumeric byte replaced by '_'.
func inputEnvironmentName(name string) string {
	var builder strings.Builder
	for _, character := range strings.ToUpper(name) {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	return "FORMIC_INPUT_" + builder.String()
}

// parseInputArguments resolves a tool's per-run input values from the CLI's two
// non-interactive forms (§14 N10): repeated "--input name=value" pairs, and
// FORMIC_INPUT_<NAME> environment entries, one per declared input. It refuses, naming every
// problem at once, rather than resolving partially: the same name given via both forms, or
// twice via --input, is Duplicate; a declared sensitive input given via --input (rather than
// its environment form, which never appears on formic's own command line) is Sensitive.
// Missing/Unknown are checked later, inside launch.Prepare, since every caller of Prepare (not
// just this CLI verb) shares that check.
func parseInputArguments(toolName string, declared []formats.Input, flagPairs, environment []string) (map[string]string, *launch.InputsRefused) {
	flagValues := map[string]string{}
	duplicateNames := map[string]bool{}
	for _, pair := range flagPairs {
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

	sensitiveNames := map[string]bool{}
	for _, input := range declared {
		sensitiveNames[input.Name] = input.Sensitive
	}
	var sensitiveViaFlag []string
	values := map[string]string{}
	for name, value := range flagValues {
		if sensitiveNames[name] {
			sensitiveViaFlag = append(sensitiveViaFlag, name)
			continue
		}
		values[name] = value
	}

	environmentValues := environmentByName(environment)
	for _, input := range declared {
		envValue, ok := environmentValues[inputEnvironmentName(input.Name)]
		if !ok || envValue == "" {
			continue
		}
		if _, alsoFromFlag := flagValues[input.Name]; alsoFromFlag {
			duplicateNames[input.Name] = true
			continue
		}
		values[input.Name] = envValue
	}

	var duplicate []string
	for name := range duplicateNames {
		duplicate = append(duplicate, name)
	}
	sort.Strings(duplicate)
	sort.Strings(sensitiveViaFlag)
	if len(duplicate) > 0 || len(sensitiveViaFlag) > 0 {
		return nil, &launch.InputsRefused{
			Tool: toolName, Duplicate: duplicate, Sensitive: sensitiveViaFlag,
			Hint: "pass a sensitive input as FORMIC_INPUT_<NAME>; give each input once",
		}
	}
	return values, nil
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

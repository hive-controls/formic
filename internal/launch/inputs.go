package launch

import (
	"fmt"
	"sort"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
)

// InputsRefused is returned by Prepare (Missing/Unknown) — and, for a CLI-parsing-time
// violation such as a name given twice or a sensitive value on the command line, constructed
// directly by the CLI verb before Prepare is ever reached (Duplicate/Sensitive) — when a tool's
// inputs[] and the values a caller offered do not agree. Every list names every offending name
// at once, never just the first, mirroring NeedsRefused.
type InputsRefused struct {
	Tool      string
	Missing   []string
	Unknown   []string
	Sensitive []string
	Duplicate []string
	Hint      string
}

func (refused *InputsRefused) Error() string {
	return fmt.Sprintf("%s has an input problem (hint: %s)", refused.Tool, refused.Hint)
}

// resolveInputsForLaunch resolves tool's declared inputs[] against request.Inputs, the same
// structural shape resolveNeedsForLaunch checks needs[] with: every declared input either gets
// a value (Missing when required and absent) or, if request.Inputs names something the
// manifest never declared, that name is Unknown. Both lists are reported together, never just
// the first.
//
// It also carries the one piece of sugar N10 keeps: a manifest that declares an input literally
// named "spec" is filled from request.Spec / the profile's own spec setting when
// request.Inputs["spec"] is absent — exactly the fallback that already fills the bare {spec}
// token for a manifest with no such declared input.
func resolveInputsForLaunch(tool discovery.Tool, request Request) (map[string]string, error) {
	declared := tool.Manifest.Inputs
	resolved := make(map[string]string, len(declared))
	knownNames := make(map[string]bool, len(declared))
	settings := request.Profile.Tools[tool.Name]
	var missing []string
	for _, input := range declared {
		knownNames[input.Name] = true
		value, given := request.Inputs[input.Name]
		if !given && input.Name == "spec" {
			if request.Spec != "" {
				value, given = request.Spec, true
			} else if settings.Spec != "" {
				value, given = settings.Spec, true
			}
		}
		if !given {
			if input.Required {
				missing = append(missing, input.Name)
			}
			continue
		}
		resolved[input.Name] = value
	}
	var unknown []string
	for name := range request.Inputs {
		if !knownNames[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(missing) > 0 || len(unknown) > 0 {
		return nil, &InputsRefused{
			Tool: tool.Name, Missing: missing, Unknown: unknown,
			Hint: "pass --input name=value, or FORMIC_INPUT_<NAME> for a sensitive input",
		}
	}
	return resolved, nil
}

// RedactSensitiveInputValues returns a copy of values with every declared sensitive input's
// value replaced by the literal "<redacted:NAME>" — used to build the durable row's argv/cwd
// through a SECOND ResolveLaunch pass, rather than string-replacing the resolved (real) argv
// after the fact, which would risk redacting a coincidental substring match elsewhere in a
// non-sensitive argument. Exported so every caller that must write a redacted view of a tool's
// resolved inputs to disk (this package's own Prepare, and internal/mission's mission-run.json)
// shares the one implementation.
func RedactSensitiveInputValues(declared []formats.Input, values map[string]string) map[string]string {
	redacted := make(map[string]string, len(values))
	for name, value := range values {
		redacted[name] = value
	}
	for _, input := range declared {
		if input.Sensitive {
			if _, given := redacted[input.Name]; given {
				redacted[input.Name] = "<redacted:" + input.Name + ">"
			}
		}
	}
	return redacted
}

// Package arguments converts between a tool's declared launch arguments and the profile's
// stored args list. It is a pure function of the declared list and the stored tokens, so a
// caller can pass any merged declaration list.
//
// Precondition: the declared flags are unique (the loader's checkDeclaredArguments refuses
// duplicates); with duplicates, only the first declaration of a flag can match.
package arguments

import "github.com/hive-controls/formic/internal/formats"

// Form is the customize screen's state for one tool's declared arguments.
type Form struct {
	// Values maps a declared flag to its value. For kind flag, presence means chosen and the
	// value is empty. A choice value outside the current options is kept as-is.
	Values map[string]string
	// Extra holds every stored token that matched no declared row, in stored order.
	Extra []string
}

// Parse splits stored profile args into per-argument values and leftover tokens.
func Parse(declared []formats.Argument, stored []string) Form {
	kindByFlag := make(map[string]formats.ArgumentKind, len(declared))
	for _, argument := range declared {
		if _, seen := kindByFlag[argument.Flag]; !seen {
			kindByFlag[argument.Flag] = argument.Kind
		}
	}
	form := Form{Values: map[string]string{}}
	for index := 0; index < len(stored); index++ {
		token := stored[index]
		consumed := matchToken(kindByFlag, form.Values, stored, index)
		if consumed == 0 {
			form.Extra = append(form.Extra, token)
			continue
		}
		index += consumed - 1
	}
	return form
}

// matchToken records the declared argument starting at stored[index] and returns how many
// tokens it consumed, or 0 when the token belongs in Extra.
func matchToken(kindByFlag map[string]formats.ArgumentKind, values map[string]string, stored []string, index int) int {
	flag := stored[index]
	kind, declared := kindByFlag[flag]
	if !declared {
		return 0
	}
	if _, alreadyMatched := values[flag]; alreadyMatched {
		return 0
	}
	switch kind {
	case formats.ArgumentKindFlag:
		values[flag] = ""
		return 1
	case formats.ArgumentKindValue, formats.ArgumentKindChoice:
		if index+1 >= len(stored) || stored[index+1] == "" {
			return 0
		}
		values[flag] = stored[index+1]
		return 2
	}
	return 0
}

// Serialize writes the form as the profile's args list: declared rows in declaration order,
// then Extra verbatim. It returns nil when there is nothing to write.
func Serialize(declared []formats.Argument, form Form) []string {
	var args []string
	emitted := make(map[string]bool, len(declared))
	for _, argument := range declared {
		if emitted[argument.Flag] {
			continue
		}
		value, chosen := form.Values[argument.Flag]
		switch {
		case argument.Kind == formats.ArgumentKindFlag && chosen:
			args = append(args, argument.Flag)
		case argument.Kind != formats.ArgumentKindFlag && value != "":
			args = append(args, argument.Flag, value)
		default:
			continue
		}
		emitted[argument.Flag] = true
	}
	return append(args, form.Extra...)
}

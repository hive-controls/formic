package arguments

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

func sampleDeclared() []formats.Argument {
	return []formats.Argument{
		{Flag: "--healer", Kind: formats.ArgumentKindChoice, Options: []formats.Option{{Value: "openai-compatible"}, {Value: "agent:claude"}}},
		{Flag: "--write", Kind: formats.ArgumentKindFlag},
		{Flag: "--app", Kind: formats.ArgumentKindValue},
	}
}

func TestParseTable(t *testing.T) {
	cases := []struct {
		name   string
		stored []string
		want   Form
	}{
		{"empty", nil, Form{Values: map[string]string{}}},
		{"spec example", []string{"--healer", "agent:claude", "--yes"}, Form{Values: map[string]string{"--healer": "agent:claude"}, Extra: []string{"--yes"}}},
		{"flag twice", []string{"--write", "--write"}, Form{Values: map[string]string{"--write": ""}, Extra: []string{"--write"}}},
		{"value looks like a flag", []string{"--app", "--write"}, Form{Values: map[string]string{"--app": "--write"}}},
		{"choice outside options is kept", []string{"--healer", "legacy"}, Form{Values: map[string]string{"--healer": "legacy"}}},
		{"dangling value flag", []string{"--app"}, Form{Values: map[string]string{}, Extra: []string{"--app"}}},
		{"dangling choice flag", []string{"--healer"}, Form{Values: map[string]string{}, Extra: []string{"--healer"}}},
		{"equals form", []string{"--app=web"}, Form{Values: map[string]string{}, Extra: []string{"--app=web"}}},
		{"undeclared only", []string{"a", "--b", "c"}, Form{Values: map[string]string{}, Extra: []string{"a", "--b", "c"}}},
		{"empty value token", []string{"--app", ""}, Form{Values: map[string]string{}, Extra: []string{"--app", ""}}},
		{"value twice", []string{"--app", "a", "--app", "b"}, Form{Values: map[string]string{"--app": "a"}, Extra: []string{"--app", "b"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := Parse(sampleDeclared(), testCase.stored)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("Parse(%q) = %#v, want %#v", testCase.stored, got, testCase.want)
			}
		})
	}
}

func TestSerializeOrdersByDeclarationAndAppendsExtra(t *testing.T) {
	form := Form{Values: map[string]string{"--app": "web", "--write": "", "--healer": "agent:claude"}, Extra: []string{"--yes"}}
	want := []string{"--healer", "agent:claude", "--write", "--app", "web", "--yes"}
	if got := Serialize(sampleDeclared(), form); !slices.Equal(got, want) {
		t.Fatalf("Serialize = %q, want %q", got, want)
	}
}

func TestSerializeEmptyIsNil(t *testing.T) {
	if got := Serialize(sampleDeclared(), Form{Values: map[string]string{}}); got != nil {
		t.Fatalf("Serialize(empty) = %#v, want nil", got)
	}
	if got := Serialize(sampleDeclared(), Form{Values: map[string]string{"--app": ""}}); got != nil {
		t.Fatalf("an empty value must emit nothing, got %#v", got)
	}
}

func randomStoredList(random *rand.Rand) []string {
	alphabet := []string{"--healer", "--write", "--app", "agent:claude", "openai-compatible", "web", "--yes", "-v", "--app=x", "", "x", "--"}
	stored := make([]string, random.IntN(8))
	for index := range stored {
		stored[index] = alphabet[random.IntN(len(alphabet))]
	}
	return stored
}

func TestSerializeParseIsAFixedPoint(t *testing.T) {
	declared := sampleDeclared()
	random := rand.New(rand.NewPCG(1, 2))
	for iteration := 0; iteration < 5000; iteration++ {
		stored := randomStoredList(random)
		once := Serialize(declared, Parse(declared, stored))
		twice := Serialize(declared, Parse(declared, once))
		if !slices.Equal(once, twice) {
			t.Fatalf("not a fixed point for %q: once=%q twice=%q", stored, once, twice)
		}
		if reparsed, original := Parse(declared, once), Parse(declared, stored); !reflect.DeepEqual(reparsed, original) {
			t.Fatalf("parse(serialize(parse(%q))) = %#v, want %#v", stored, reparsed, original)
		}
	}
}

func randomLegalForm(random *rand.Rand, declared []formats.Argument) Form {
	form := Form{Values: map[string]string{}}
	for _, argument := range declared {
		if random.IntN(2) == 0 {
			continue
		}
		switch argument.Kind {
		case formats.ArgumentKindFlag:
			form.Values[argument.Flag] = ""
		case formats.ArgumentKindChoice:
			form.Values[argument.Flag] = argument.Options[random.IntN(len(argument.Options))].Value
		default:
			form.Values[argument.Flag] = []string{"web", "--dash", "a b", "x"}[random.IntN(4)]
		}
	}
	extras := []string{"--yes", "x", "", "--app=x", "-v"}
	for count := random.IntN(4); count > 0; count-- {
		form.Extra = append(form.Extra, extras[random.IntN(len(extras))])
	}
	return form
}

func TestParseSerializeRoundTripsLegalForms(t *testing.T) {
	declared := sampleDeclared()
	random := rand.New(rand.NewPCG(3, 4))
	for iteration := 0; iteration < 5000; iteration++ {
		form := randomLegalForm(random, declared)
		got := Parse(declared, Serialize(declared, form))
		if !reflect.DeepEqual(got, form) {
			t.Fatalf("round trip lost data: form=%#v serialized=%q parsed=%#v", form, Serialize(declared, form), got)
		}
	}
}

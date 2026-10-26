package main

import (
	"reflect"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

func TestExtractInputFlagsPullsNameValuePairsOutInOrder(t *testing.T) {
	cases := []struct {
		name          string
		arguments     []string
		wantPairs     []string
		wantRemaining []string
	}{
		{"none", []string{"checks", "--json"}, nil, []string{"checks", "--json"}},
		{"one", []string{"--input", "spec=/repo/x.yaml", "checks"}, []string{"spec=/repo/x.yaml"}, []string{"checks"}},
		{"two, interleaved", []string{"checks", "--input", "a=1", "--json", "--input", "b=2"},
			[]string{"a=1", "b=2"}, []string{"checks", "--json"}},
		{"trailing flag with nothing after it", []string{"checks", "--input"}, nil, []string{"checks", "--input"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gotPairs, gotRemaining := extractInputFlags(testCase.arguments)
			if !reflect.DeepEqual(gotPairs, testCase.wantPairs) {
				t.Fatalf("pairs = %v, want %v", gotPairs, testCase.wantPairs)
			}
			if !reflect.DeepEqual(gotRemaining, testCase.wantRemaining) {
				t.Fatalf("remaining = %v, want %v", gotRemaining, testCase.wantRemaining)
			}
		})
	}
}

func TestInputEnvironmentNameUppercasesAndReplacesNonAlphanumeric(t *testing.T) {
	cases := []struct{ name, want string }{
		{"spec", "FORMIC_INPUT_SPEC"},
		{"apiKey", "FORMIC_INPUT_APIKEY"},
		{"api-key", "FORMIC_INPUT_API_KEY"},
		{"api.key 2", "FORMIC_INPUT_API_KEY_2"},
	}
	for _, testCase := range cases {
		if got := inputEnvironmentName(testCase.name); got != testCase.want {
			t.Fatalf("inputEnvironmentName(%q) = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestParseInputArgumentsRefusesADuplicateNameAcrossFlagAndEnv(t *testing.T) {
	declared := []formats.Input{{Name: "spec"}}
	_, refused := parseInputArguments("checks", declared, []string{"spec=/a.yaml"}, []string{"FORMIC_INPUT_SPEC=/b.yaml"})
	if refused == nil {
		t.Fatal("want a refusal for the same name given via both --input and FORMIC_INPUT_SPEC")
	}
	if len(refused.Duplicate) != 1 || refused.Duplicate[0] != "spec" {
		t.Fatalf("refused.Duplicate = %v, want [\"spec\"]", refused.Duplicate)
	}
}

func TestParseInputArgumentsRefusesTheSameFlagGivenTwice(t *testing.T) {
	declared := []formats.Input{{Name: "spec"}}
	_, refused := parseInputArguments("checks", declared, []string{"spec=/a.yaml", "spec=/b.yaml"}, nil)
	if refused == nil {
		t.Fatal("want a refusal for --input spec given twice")
	}
	if len(refused.Duplicate) != 1 || refused.Duplicate[0] != "spec" {
		t.Fatalf("refused.Duplicate = %v, want [\"spec\"]", refused.Duplicate)
	}
}

func TestParseInputArgumentsRefusesASensitiveInputPassedViaFlag(t *testing.T) {
	declared := []formats.Input{{Name: "apiKey", Sensitive: true}}
	_, refused := parseInputArguments("checks", declared, []string{"apiKey=fake-secret"}, nil)
	if refused == nil {
		t.Fatal("want a refusal for a sensitive input given via --input")
	}
	if len(refused.Sensitive) != 1 || refused.Sensitive[0] != "apiKey" {
		t.Fatalf("refused.Sensitive = %v, want [\"apiKey\"]", refused.Sensitive)
	}
}

func TestParseInputArgumentsNeverEchoesTheValueInARefusal(t *testing.T) {
	declared := []formats.Input{{Name: "apiKey", Sensitive: true}}
	_, refused := parseInputArguments("checks", declared, []string{"apiKey=fake-secret-value-999"}, nil)
	if refused == nil {
		t.Fatal("want a refusal")
	}
	for _, name := range refused.Sensitive {
		if name == "fake-secret-value-999" {
			t.Fatal("refusal echoed the sensitive value instead of just its name")
		}
	}
}

func TestParseInputArgumentsAcceptsASensitiveInputViaEnvironmentOnly(t *testing.T) {
	declared := []formats.Input{{Name: "apiKey", Sensitive: true}}
	values, refused := parseInputArguments("checks", declared, nil, []string{"FORMIC_INPUT_APIKEY=fake-secret"})
	if refused != nil {
		t.Fatalf("unexpected refusal: %+v", refused)
	}
	if values["apiKey"] != "fake-secret" {
		t.Fatalf("values[apiKey] = %q, want the env-provided value", values["apiKey"])
	}
}

func TestParseInputArgumentsAcceptsANonSensitiveInputViaFlag(t *testing.T) {
	declared := []formats.Input{{Name: "spec"}}
	values, refused := parseInputArguments("checks", declared, []string{"spec=/repo/x.yaml"}, nil)
	if refused != nil {
		t.Fatalf("unexpected refusal: %+v", refused)
	}
	if values["spec"] != "/repo/x.yaml" {
		t.Fatalf("values[spec] = %q, want /repo/x.yaml", values["spec"])
	}
}

package toolspec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	toolspec "github.com/hive-controls/toolspec"
)

const (
	schemaPath  = "../schema/toolspec.schema.json"
	fixturesDir = "../fixtures/conformance"
)

// invalidExpectation records, per invalid/*.json fixture, exactly what
// fixtures/conformance/manifest.md documents the fixture tests: the failing instance
// location and keyword, a check of the structured jsonschema/v6 error data (never a loose
// substring match on the rendered message), and whether the generated struct's
// UnmarshalJSON also rejects it on its own. Schema validation runs unconditionally for
// every invalid fixture below — it is the sole source of truth for rejection, since the
// struct alone does not enforce every schema keyword (additionalProperties: false, for one).
type invalidExpectation struct {
	instanceLocation []string
	keyword          string
	unmarshalRejects bool
	verifyKind       func(t *testing.T, errorKind jsonschema.ErrorKind)
}

var expectedInvalid = map[string]invalidExpectation{
	// manifest.md: "omits the required launch block. Expected ajv error: instancePath ""
	// (root), must have required property 'launch'."
	"missing-required.json": {
		instanceLocation: []string{},
		keyword:          "required",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			required, ok := errorKind.(*kind.Required)
			if !ok {
				t.Fatalf("expected *kind.Required, got %T", errorKind)
			}
			if !slices.Contains(required.Missing, "launch") {
				t.Fatalf(`expected missing property "launch"; got %v`, required.Missing)
			}
		},
	},
	// manifest.md: "launch.args is a string instead of an array. Expected ajv error:
	// instancePath /launch/args, must be array."
	"wrong-type.json": {
		instanceLocation: []string{"launch", "args"},
		keyword:          "type",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			typeErr, ok := errorKind.(*kind.Type)
			if !ok {
				t.Fatalf("expected *kind.Type, got %T", errorKind)
			}
			if typeErr.Got != "string" {
				t.Fatalf("expected got=string; got %q", typeErr.Got)
			}
			if !slices.Contains(typeErr.Want, "array") {
				t.Fatalf("expected want to include array; got %v", typeErr.Want)
			}
		},
	},
	// manifest.md: "a top-level unknownField the schema doesn't declare. Expected ajv
	// error: instancePath "" (root), must NOT have additional properties (schema is
	// closed: additionalProperties: false at the top level)."
	"unknown-property.json": {
		instanceLocation: []string{},
		keyword:          "additionalProperties",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			extra, ok := errorKind.(*kind.AdditionalProperties)
			if !ok {
				t.Fatalf("expected *kind.AdditionalProperties, got %T", errorKind)
			}
			if !slices.Contains(extra.Properties, "unknownField") {
				t.Fatalf(`expected offending property "unknownField"; got %v`, extra.Properties)
			}
		},
	},
}

// validFixtureChecks records, per valid/*.json fixture, the specific claim
// fixtures/conformance/manifest.md makes about it.
var validFixtureChecks = map[string]func(t *testing.T, manifest toolspec.ToolManifest){
	// manifest.md: "the smallest manifest that satisfies the schema: toolspec, name,
	// launch.command only."
	"minimal.json": func(t *testing.T, manifest toolspec.ToolManifest) {
		t.Helper()
		if manifest.Config != nil {
			t.Errorf("minimal.json should carry no config; got %+v", manifest.Config)
		}
		if manifest.Title != nil {
			t.Errorf("minimal.json should carry no title; got %q", *manifest.Title)
		}
		if manifest.Description != nil {
			t.Errorf("minimal.json should carry no description; got %q", *manifest.Description)
		}
		if manifest.Status != nil {
			t.Errorf("minimal.json should carry no status; got %+v", manifest.Status)
		}
		if manifest.Health != nil {
			t.Errorf("minimal.json should carry no health; got %+v", manifest.Health)
		}
		if manifest.Pane != nil {
			t.Errorf("minimal.json should carry no pane; got %+v", manifest.Pane)
		}
		if len(manifest.Launch.Args) != 0 {
			t.Errorf("minimal.json's launch should carry no args; got %v", manifest.Launch.Args)
		}
		if manifest.Launch.Cwd != nil {
			t.Errorf("minimal.json's launch should carry no cwd; got %q", *manifest.Launch.Cwd)
		}
	},
	// manifest.md: "a full manifest exercising every top-level section (config, status,
	// health, pane), taken from the design doc's own example."
	"e2e-doctor.json": func(t *testing.T, manifest toolspec.ToolManifest) {
		t.Helper()
		if manifest.Config == nil {
			t.Error("e2e-doctor.json should exercise config")
		}
		if manifest.Status == nil {
			t.Error("e2e-doctor.json should exercise status")
		}
		if manifest.Health == nil {
			t.Error("e2e-doctor.json should exercise health")
		}
		if manifest.Pane == nil {
			t.Error("e2e-doctor.json should exercise pane")
		}
	},
	// manifest.md: "a full manifest with pane.interactive: true; the case a launcher must
	// route to a real TTY, never a captured pane."
	"interactive-tool.json": func(t *testing.T, manifest toolspec.ToolManifest) {
		t.Helper()
		if manifest.Pane == nil || manifest.Pane.Interactive == nil || !*manifest.Pane.Interactive {
			t.Fatalf("interactive-tool.json should set pane.interactive: true; got %+v", manifest.Pane)
		}
	},
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compiling %s: %v", schemaPath, err)
	}
	return schema
}

func listFixtureNames(t *testing.T, subdir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixturesDir, subdir))
	if err != nil {
		t.Fatalf("listing fixtures in %s: %v", subdir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func readFixture(t *testing.T, subdir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir, subdir, name))
	if err != nil {
		t.Fatalf("reading fixture %s/%s: %v", subdir, name, err)
	}
	return data
}

func validateAgainstSchema(t *testing.T, schema *jsonschema.Schema, data []byte) error {
	t.Helper()
	var decoded interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshalling fixture into interface{}: %v", err)
	}
	return schema.Validate(decoded)
}

// findCause searches a *jsonschema.ValidationError and its Causes, depth-first, for a node
// whose InstanceLocation and single-segment ErrorKind.KeywordPath() match exactly.
func findCause(validationErr *jsonschema.ValidationError, instanceLocation []string, keyword string) *jsonschema.ValidationError {
	path := validationErr.ErrorKind.KeywordPath()
	if len(path) == 1 && path[0] == keyword && slices.Equal(validationErr.InstanceLocation, instanceLocation) {
		return validationErr
	}
	for _, cause := range validationErr.Causes {
		if found := findCause(cause, instanceLocation, keyword); found != nil {
			return found
		}
	}
	return nil
}

// Every fixtures/conformance/valid/*.json fixture must unmarshal into the generated
// ToolManifest type, pass schema validation, and satisfy the specific claim
// fixtures/conformance/manifest.md makes about that fixture.
func TestValidFixturesUnmarshalAndValidate(t *testing.T) {
	schema := compileSchema(t)
	names := listFixtureNames(t, "valid")
	if len(names) == 0 {
		t.Fatal("expected at least one valid fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			check, ok := validFixtureChecks[name]
			if !ok {
				t.Fatalf("no per-fixture check registered for %s — add one and cross-check fixtures/conformance/manifest.md", name)
			}
			data := readFixture(t, "valid", name)

			var manifest toolspec.ToolManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatalf("%s should unmarshal into ToolManifest; got: %v", name, err)
			}

			if err := validateAgainstSchema(t, schema, data); err != nil {
				t.Fatalf("%s should validate against the schema; got: %v", name, err)
			}

			check(t, manifest)
		})
	}
}

// Every fixtures/conformance/invalid/*.json fixture is always schema-validated — regardless
// of whether the generated struct's UnmarshalJSON also rejects it — and the rejection must
// carry exactly the instance location, keyword, and structured detail
// fixtures/conformance/manifest.md documents for that fixture.
func TestInvalidFixturesAreRejected(t *testing.T) {
	schema := compileSchema(t)
	names := listFixtureNames(t, "invalid")
	if len(names) == 0 {
		t.Fatal("expected at least one invalid fixture")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			expected, ok := expectedInvalid[name]
			if !ok {
				t.Fatalf("no expected rejection registered for fixture %s — add one and cross-check fixtures/conformance/manifest.md", name)
			}
			data := readFixture(t, "invalid", name)

			var manifest toolspec.ToolManifest
			unmarshalErr := json.Unmarshal(data, &manifest)
			if (unmarshalErr != nil) != expected.unmarshalRejects {
				t.Fatalf("%s: expected struct-level rejection=%v; got err=%v", name, expected.unmarshalRejects, unmarshalErr)
			}

			schemaErr := validateAgainstSchema(t, schema, data)
			if schemaErr == nil {
				t.Fatalf("%s should be rejected by schema validation", name)
			}
			validationErr, ok := schemaErr.(*jsonschema.ValidationError)
			if !ok {
				t.Fatalf("%s: expected a *jsonschema.ValidationError; got %T: %v", name, schemaErr, schemaErr)
			}
			cause := findCause(validationErr, expected.instanceLocation, expected.keyword)
			if cause == nil {
				t.Fatalf("%s: expected a %q failure at instanceLocation %v per fixtures/conformance/manifest.md; got: %v", name, expected.keyword, expected.instanceLocation, schemaErr)
			}
			expected.verifyKind(t, cause.ErrorKind)
		})
	}
}

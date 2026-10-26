package formats_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/hive-controls/formic/internal/formats"
)

const (
	schemaPath  = "../../schema/manifest.schema.json"
	fixturesDir = "fixtures/conformance"
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
	// manifest.md: "an env[0].name that doesn't match ^[A-Z][A-Z0-9_]*$. Expected ajv
	// error: instancePath /env/0/name, must match pattern \"^[A-Z][A-Z0-9_]*$\"."
	"bad-env-name.json": {
		instanceLocation: []string{"env", "0", "name"},
		keyword:          "pattern",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			patternErr, ok := errorKind.(*kind.Pattern)
			if !ok {
				t.Fatalf("expected *kind.Pattern, got %T", errorKind)
			}
			if patternErr.Got != "not-a-valid-name" {
				t.Fatalf("expected got=not-a-valid-name; got %q", patternErr.Got)
			}
		},
	},
	// manifest.md: "a needs[0] of kind: secret missing its required name, matching none of
	// the four need shapes. Expected ajv error: instancePath /needs/0, must match exactly
	// one schema in oneOf." The generated Need type is `interface{}` (go-jsonschema does not
	// generate oneOf validation for it), so schema validation is the only thing that rejects
	// this fixture — unmarshalRejects is false.
	"bad-need-shape.json": {
		instanceLocation: []string{"needs", "0"},
		keyword:          "oneOf",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			if _, ok := errorKind.(*kind.OneOf); !ok {
				t.Fatalf("expected *kind.OneOf, got %T", errorKind)
			}
		},
	},
	// manifest.md: "an arguments[0].kind of switch, outside the flag | value | choice enum."
	"bad-argument-kind.json": {
		instanceLocation: []string{"arguments", "0", "kind"},
		keyword:          "enum",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			enumErr, ok := errorKind.(*kind.Enum)
			if !ok {
				t.Fatalf("expected *kind.Enum, got %T", errorKind)
			}
			if enumErr.Got != "switch" {
				t.Fatalf("expected got=switch; got %v", enumErr.Got)
			}
		},
	},
	// manifest.md: "an env[0].options[0] with no required value."
	"option-missing-value.json": {
		instanceLocation: []string{"env", "0", "options", "0"},
		keyword:          "required",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			required, ok := errorKind.(*kind.Required)
			if !ok {
				t.Fatalf("expected *kind.Required, got %T", errorKind)
			}
			if !slices.Contains(required.Missing, "value") {
				t.Fatalf(`expected missing property "value"; got %v`, required.Missing)
			}
		},
	},
	// manifest.md: "an actions[0].name of Start, outside ^[a-z][a-z0-9-]*$."
	"bad-action-name.json": {
		instanceLocation: []string{"actions", "0", "name"},
		keyword:          "pattern",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			patternErr, ok := errorKind.(*kind.Pattern)
			if !ok {
				t.Fatalf("expected *kind.Pattern, got %T", errorKind)
			}
			if patternErr.Got != "Start" {
				t.Fatalf("expected got=Start; got %q", patternErr.Got)
			}
		},
	},
	// manifest.md: "an actions[0] carrying a key the action does not declare."
	"action-unknown-property.json": {
		instanceLocation: []string{"actions", "0"},
		keyword:          "additionalProperties",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			extra, ok := errorKind.(*kind.AdditionalProperties)
			if !ok {
				t.Fatalf("expected *kind.AdditionalProperties, got %T", errorKind)
			}
			if !slices.Contains(extra.Properties, "mystery") {
				t.Fatalf(`expected offending property "mystery"; got %v`, extra.Properties)
			}
		},
	},
	// manifest.md: "an actions[0].confirm of the empty string."
	"empty-confirm.json": {
		instanceLocation: []string{"actions", "0", "confirm"},
		keyword:          "minLength",
		unmarshalRejects: true,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			if _, ok := errorKind.(*kind.MinLength); !ok {
				t.Fatalf("expected *kind.MinLength, got %T", errorKind)
			}
		},
	},
	// manifest.md: "a live.probe declaring both http and tcp, matching none of the three
	// probe shapes." Probe is a oneOf, so the generated type is interface{} and only schema
	// validation rejects it.
	"probe-two-kinds.json": {
		instanceLocation: []string{"live", "probe"},
		keyword:          "oneOf",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			if _, ok := errorKind.(*kind.OneOf); !ok {
				t.Fatalf("expected *kind.OneOf, got %T", errorKind)
			}
		},
	},
	// manifest.md: "a probe timeout of five seconds, outside the duration pattern."
	"probe-bad-duration.json": {
		instanceLocation: []string{"live", "probe", "timeout"},
		keyword:          "pattern",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			patternErr, ok := errorKind.(*kind.Pattern)
			if !ok {
				t.Fatalf("expected *kind.Pattern, got %T", errorKind)
			}
			if patternErr.Got != "five seconds" {
				t.Fatalf("expected got=%q; got %q", "five seconds", patternErr.Got)
			}
		},
	},
	// manifest.md: "a command probe with idleExit: 0, below the minimum of 1."
	"idle-exit-zero.json": {
		instanceLocation: []string{"live", "probe", "idleExit"},
		keyword:          "minimum",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			if _, ok := errorKind.(*kind.Minimum); !ok {
				t.Fatalf("expected *kind.Minimum, got %T", errorKind)
			}
		},
	},
	// manifest.md: "a needs[0] of kind: service with no tool, matching none of the five need
	// shapes." Need is a oneOf, so only schema validation rejects it.
	"bad-service-need.json": {
		instanceLocation: []string{"needs", "0"},
		keyword:          "oneOf",
		unmarshalRejects: false,
		verifyKind: func(t *testing.T, errorKind jsonschema.ErrorKind) {
			t.Helper()
			if _, ok := errorKind.(*kind.OneOf); !ok {
				t.Fatalf("expected *kind.OneOf, got %T", errorKind)
			}
		},
	},
}

// validFixtureChecks records, per valid/*.json fixture, the specific claim
// fixtures/conformance/manifest.md makes about it.
var validFixtureChecks = map[string]func(t *testing.T, manifest formats.ToolManifest){
	// manifest.md: "the smallest manifest that satisfies the schema: formic, name,
	// launch.command only."
	"minimal.json": func(t *testing.T, manifest formats.ToolManifest) {
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
	"e2e-doctor.json": func(t *testing.T, manifest formats.ToolManifest) {
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
	"interactive-tool.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if manifest.Pane == nil || manifest.Pane.Interactive == nil || !*manifest.Pane.Interactive {
			t.Fatalf("interactive-tool.json should set pane.interactive: true; got %+v", manifest.Pane)
		}
	},
	// manifest.md: "a v1.1 manifest exercising the three additive top-level fields:
	// env (a required secret and an optional one with a default), outputs.evidenceDir
	// naming the env entry that carries the evidence directory, and exit mapping codes
	// to meanings."
	"env-outputs-exit.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if len(manifest.Env) != 2 {
			t.Fatalf("env-outputs-exit.json should declare 2 env entries; got %d", len(manifest.Env))
		}
		if manifest.Env[0].Name != "RECIPE_API_KEY" || !manifest.Env[0].Secret || !manifest.Env[0].Required {
			t.Errorf("env-outputs-exit.json's first env entry should be a required secret named RECIPE_API_KEY; got %+v", manifest.Env[0])
		}
		if manifest.Env[1].Default == nil || *manifest.Env[1].Default != "evidence" {
			t.Errorf("env-outputs-exit.json's second env entry should default to \"evidence\"; got %+v", manifest.Env[1])
		}
		if manifest.Outputs == nil || manifest.Outputs.EvidenceDir == nil || *manifest.Outputs.EvidenceDir != "RECIPE_EVIDENCE_DIR" {
			t.Errorf("env-outputs-exit.json should set outputs.evidenceDir to RECIPE_EVIDENCE_DIR; got %+v", manifest.Outputs)
		}
		if len(manifest.Exit) != 3 {
			t.Errorf("env-outputs-exit.json should declare 3 exit codes; got %d", len(manifest.Exit))
		}
	},
	// manifest.md: "car 5's three additive fields at once: $schema (an editor reference,
	// previously rejected as an unknown property), needs (one entry of each of the four
	// kinds), and inputs (one input exercising label, kind, required, and sensitive)."
	"needs-and-inputs.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if manifest.Schema == nil || *manifest.Schema != "https://raw.githubusercontent.com/hive-controls/formic/main/schema/manifest.schema.json" {
			t.Errorf("needs-and-inputs.json should set $schema; got %+v", manifest.Schema)
		}
		if len(manifest.Needs) != 4 {
			t.Fatalf("needs-and-inputs.json should declare 4 needs; got %d", len(manifest.Needs))
		}
		if len(manifest.Inputs) != 1 {
			t.Fatalf("needs-and-inputs.json should declare 1 input; got %d", len(manifest.Inputs))
		}
		input := manifest.Inputs[0]
		if input.Name != "spec" || input.Label == nil || *input.Label != "Spec file" {
			t.Errorf("needs-and-inputs.json's input should be named spec with label \"Spec file\"; got %+v", input)
		}
		if !input.Required {
			t.Errorf("needs-and-inputs.json's input should be required; got %+v", input)
		}
	},
	// manifest.md: "N13's two additive fields: an env[] entry with options, and arguments[]
	// with one argument of each kind."
	"arguments-and-options.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if len(manifest.Env) != 1 || len(manifest.Env[0].Options) != 2 {
			t.Fatalf("arguments-and-options.json should declare one env entry with 2 options; got %+v", manifest.Env)
		}
		options := manifest.Env[0].Options
		if options[0].Value != "local" || options[0].Description == nil || options[1].Value != "remote" || options[1].Description != nil {
			t.Errorf("env options should be local (described) then remote (undescribed); got %+v", options)
		}
		if len(manifest.Arguments) != 3 {
			t.Fatalf("arguments-and-options.json should declare 3 arguments; got %d", len(manifest.Arguments))
		}
		wantKinds := []formats.ArgumentKind{formats.ArgumentKindChoice, formats.ArgumentKindValue, formats.ArgumentKindFlag}
		for index, want := range wantKinds {
			if manifest.Arguments[index].Kind != want {
				t.Errorf("argument %d kind = %q, want %q", index, manifest.Arguments[index].Kind, want)
			}
		}
		choice := manifest.Arguments[0]
		if choice.Default == nil || *choice.Default != "full" || len(choice.Options) != 2 || choice.Options[1].Value != "full" {
			t.Errorf("the choice argument should default to \"full\", one of its two options; got %+v", choice)
		}
	},
	// manifest.md: "a real consumer's manifest, key for key: three actions[] sharing one launch
	// prefix ..."
	"actions-docker-desktop.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if len(manifest.Actions) != 3 {
			t.Fatalf("actions-docker-desktop.json should declare 3 actions; got %d", len(manifest.Actions))
		}
		start, stop, restart := manifest.Actions[0], manifest.Actions[1], manifest.Actions[2]
		if start.Name != "start" || stop.Name != "stop" || restart.Name != "restart" {
			t.Errorf("actions should be start, stop, restart in order; got %s, %s, %s", start.Name, stop.Name, restart.Name)
		}
		if stop.Confirm == nil || *stop.Confirm == "" {
			t.Errorf("stop should carry a confirm sentence; got %+v", stop.Confirm)
		}
		if start.Confirm != nil {
			t.Errorf("start should carry no confirm; got %q", *start.Confirm)
		}
		if start.WhenUp == nil || *start.WhenUp != formats.ActionWhenUpSkip {
			t.Errorf("start should set whenUp: skip; got %+v", start.WhenUp)
		}
		if !slices.Equal(restart.Args, []string{"restart", "--timeout", "180"}) {
			t.Errorf("restart args = %v", restart.Args)
		}
		if restart.Exit["1"] == nil {
			t.Errorf("restart should map exit code 1; got %+v", restart.Exit)
		}
		if marshalled := marshalForCheck(t, manifest.Live); marshalled != `{"probe":{"command":["docker","info"],"timeout":"5s"}}` {
			t.Errorf("live = %s", marshalled)
		}
	},
	// manifest.md: "a real consumer's manifest: a service need naming another tool and its
	// start action ..."
	"actions-librechat.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if len(manifest.Needs) != 1 {
			t.Fatalf("actions-librechat.json should declare 1 need; got %d", len(manifest.Needs))
		}
		if marshalled := marshalForCheck(t, manifest.Needs[0]); marshalled != `{"action":"start","kind":"service","tool":"docker-desktop"}` {
			t.Errorf("needs[0] = %s", marshalled)
		}
		if len(manifest.Actions) != 3 || manifest.Actions[0].Ready == nil || manifest.Actions[0].Ready.Within != "120s" {
			t.Errorf("start should declare ready.within: 120s; got %+v", manifest.Actions)
		}
		if marshalled := marshalForCheck(t, manifest.Live); marshalled != `{"probe":{"http":"http://localhost:3080/"}}` {
			t.Errorf("live = %s", marshalled)
		}
	},
	// manifest.md: "a real consumer's manifest: an action with lifecycle: service, actions that
	// stand alone ..."
	"actions-llama-swap.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if len(manifest.Actions) != 3 {
			t.Fatalf("actions-llama-swap.json should declare 3 actions; got %d", len(manifest.Actions))
		}
		start, stop, tui := manifest.Actions[0], manifest.Actions[1], manifest.Actions[2]
		if start.Lifecycle == nil || *start.Lifecycle != formats.ActionLifecycleService {
			t.Errorf("start should set lifecycle: service; got %+v", start.Lifecycle)
		}
		if stop.Command == nil || *stop.Command != "node" || stop.Cwd == nil || *stop.Cwd != "{repo}" {
			t.Errorf("stop should stand alone as node in {repo}; got command=%v cwd=%v", stop.Command, stop.Cwd)
		}
		if tui.Interactive == nil || !*tui.Interactive || tui.Cwd == nil || *tui.Cwd != "{repo}" {
			t.Errorf("tui should be interactive in {repo}; got interactive=%v cwd=%v", tui.Interactive, tui.Cwd)
		}
	},
	// manifest.md: "a real consumer's manifest with no actions: a live.probe of kind http with
	// expect: any."
	"live-mcp-proxies.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if len(manifest.Actions) != 0 {
			t.Errorf("live-mcp-proxies.json should declare no actions; got %d", len(manifest.Actions))
		}
		if marshalled := marshalForCheck(t, manifest.Live); marshalled != `{"probe":{"expect":"any","http":"http://127.0.0.1:8081/mcp"}}` {
			t.Errorf("live = %s", marshalled)
		}
	},
	// manifest.md: "an http probe with an integer expect and a timeout."
	"live-probe-values.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if marshalled := marshalForCheck(t, manifest.Live); marshalled != `{"probe":{"expect":204,"http":"http://127.0.0.1:9000/ready","timeout":"5s"}}` {
			t.Errorf("live = %s", marshalled)
		}
	},
	// manifest.md: "a command probe with idleExit."
	"live-probe-command.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if marshalled := marshalForCheck(t, manifest.Live); marshalled != `{"probe":{"command":["sample","status"],"idleExit":3}}` {
			t.Errorf("live = %s", marshalled)
		}
	},
	// manifest.md: "a top-level lifecycle: service with a tcp probe, item-level whenUp and
	// ready, and a service need with no action."
	"service-lifecycle.json": func(t *testing.T, manifest formats.ToolManifest) {
		t.Helper()
		if manifest.Lifecycle == nil || *manifest.Lifecycle != formats.ToolManifestLifecycleService {
			t.Errorf("lifecycle should be service; got %+v", manifest.Lifecycle)
		}
		if manifest.Live == nil || manifest.Live.WhenUp == nil || *manifest.Live.WhenUp != formats.LiveWhenUpSkip {
			t.Fatalf("live.whenUp should be skip; got %+v", manifest.Live)
		}
		if manifest.Live.Ready == nil || manifest.Live.Ready.Within != "2m" {
			t.Errorf("live.ready.within should be 2m; got %+v", manifest.Live.Ready)
		}
		if marshalled := marshalForCheck(t, manifest.Needs[0]); marshalled != `{"kind":"service","tool":"sample-db"}` {
			t.Errorf("needs[0] = %s", marshalled)
		}
	},
}

// marshalForCheck renders a decoded oneOf-typed value (a probe, a need) back to JSON so a
// check can assert its shape without a typed accessor; encoding/json sorts map keys.
func marshalForCheck(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshalling %+v: %v", value, err)
	}
	return string(encoded)
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

			var manifest formats.ToolManifest
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

			var manifest formats.ToolManifest
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

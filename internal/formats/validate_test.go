package formats_test

import (
	"strings"
	"testing"

	"github.com/hive-controls/formic/internal/formats"
)

func TestValidateAcceptsValidManifest(t *testing.T) {
	data := []byte(`{"formic":1,"name":"sample","launch":{"command":"sample"}}`)
	if err := formats.Validate(data); err != nil {
		t.Fatalf("valid manifest should pass schema validation: %v", err)
	}
}

func TestValidateRejectsUnknownProperty(t *testing.T) {
	data := []byte(`{"formic":1,"name":"sample","launch":{"command":"sample"},"extra":true}`)
	err := formats.Validate(data)
	if err == nil {
		t.Fatal("manifest with an unknown property should fail schema validation")
	}
	if !strings.Contains(err.Error(), "additional properties") {
		t.Fatalf("expected additional-properties error, got: %v", err)
	}
}

func TestValidateRejectsMalformedJSON(t *testing.T) {
	err := formats.Validate([]byte(`{"formic":`))
	if err == nil || !strings.Contains(err.Error(), "decode manifest JSON") {
		t.Fatalf("expected malformed JSON error, got: %v", err)
	}
}

// TestValidateRejectsTheOldToolspecMarkerKey proves the hard cutover (§17 car 4): a manifest
// using the pre-cutover `toolspec: 1` marker key is rejected — `formic` is now required and
// `toolspec` is an unknown property under the schema's additionalProperties: false.
func TestValidateRejectsTheOldToolspecMarkerKey(t *testing.T) {
	data := []byte(`{"toolspec":1,"name":"sample","launch":{"command":"sample"}}`)
	if err := formats.Validate(data); err == nil {
		t.Fatal("manifest using the old toolspec marker key should fail schema validation")
	}
}

// TestValidateAcceptsSchemaKey proves car 5's §8 requirement: a manifest carrying a literal
// $schema key now validates. Before car 5 the schema's root had no $schema property, so
// additionalProperties: false rejected it (the same data would fail with an
// additional-properties error against the pre-car-5 schema).
func TestValidateAcceptsSchemaKey(t *testing.T) {
	data := []byte(`{"$schema":"https://raw.githubusercontent.com/hive-controls/formic/main/schema/manifest.schema.json","formic":1,"name":"sample","launch":{"command":"sample"}}`)
	if err := formats.Validate(data); err != nil {
		t.Fatalf("manifest carrying a $schema key should pass schema validation: %v", err)
	}
}

// TestValidateAcceptsNeedsAndInputs proves car 5's additive needs[]/inputs[] fields (§13/§14):
// a manifest declaring both now validates. Before car 5 either key was rejected with
// "additional properties 'needs'/'inputs' not allowed" under the same additionalProperties:
// false root that TestValidateRejectsUnknownProperty above still exercises for a truly
// unrecognized key.
func TestValidateAcceptsNeedsAndInputs(t *testing.T) {
	data := []byte(`{"formic":1,"name":"sample","launch":{"command":"sample"},` +
		`"needs":[{"kind":"secret","name":"SAMPLE_KEY"}],` +
		`"inputs":[{"name":"spec"}]}`)
	if err := formats.Validate(data); err != nil {
		t.Fatalf("manifest declaring needs and inputs should pass schema validation: %v", err)
	}
}

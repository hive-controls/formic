package toolspec_test

import (
	"strings"
	"testing"

	toolspec "github.com/hive-controls/toolspec"
)

func TestValidateAcceptsValidManifest(t *testing.T) {
	data := []byte(`{"toolspec":1,"name":"sample","launch":{"command":"sample"}}`)
	if err := toolspec.Validate(data); err != nil {
		t.Fatalf("valid manifest should pass schema validation: %v", err)
	}
}

func TestValidateRejectsUnknownProperty(t *testing.T) {
	data := []byte(`{"toolspec":1,"name":"sample","launch":{"command":"sample"},"extra":true}`)
	err := toolspec.Validate(data)
	if err == nil {
		t.Fatal("manifest with an unknown property should fail schema validation")
	}
	if !strings.Contains(err.Error(), "additional properties") {
		t.Fatalf("expected additional-properties error, got: %v", err)
	}
}

func TestValidateRejectsMalformedJSON(t *testing.T) {
	err := toolspec.Validate([]byte(`{"toolspec":`))
	if err == nil || !strings.Contains(err.Error(), "decode manifest JSON") {
		t.Fatalf("expected malformed JSON error, got: %v", err)
	}
}

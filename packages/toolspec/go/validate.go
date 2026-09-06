package toolspec

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const embeddedSchemaLocation = "https://hive-controls.dev/schemas/toolspec-1.json"

//go:embed toolspec.schema.json
var embeddedSchemaBytes []byte

var (
	compiledSchema    *jsonschema.Schema
	compiledSchemaErr error
	compileSchemaOnce sync.Once
)

// Validate checks a JSON-encoded manifest against the canonical toolspec schema.
func Validate(data []byte) error {
	schema, err := toolManifestSchema()
	if err != nil {
		return err
	}

	var manifestValue any
	if err := json.Unmarshal(data, &manifestValue); err != nil {
		return fmt.Errorf("decode manifest JSON: %w", err)
	}
	if err := schema.Validate(manifestValue); err != nil {
		return fmt.Errorf("validate manifest: %w", err)
	}
	return nil
}

func toolManifestSchema() (*jsonschema.Schema, error) {
	compileSchemaOnce.Do(func() {
		var schemaDocument any
		if err := json.Unmarshal(embeddedSchemaBytes, &schemaDocument); err != nil {
			compiledSchemaErr = fmt.Errorf("decode embedded toolspec schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(embeddedSchemaLocation, schemaDocument); err != nil {
			compiledSchemaErr = fmt.Errorf("load embedded toolspec schema: %w", err)
			return
		}
		compiledSchema, compiledSchemaErr = compiler.Compile(embeddedSchemaLocation)
	})
	if compiledSchemaErr != nil {
		return nil, fmt.Errorf("compile embedded toolspec schema: %w", compiledSchemaErr)
	}
	return compiledSchema, nil
}

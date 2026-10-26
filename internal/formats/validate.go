package formats

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	formatschema "github.com/hive-controls/formic/schema"
)

const (
	manifestSchemaLocation = "https://raw.githubusercontent.com/hive-controls/formic/main/schema/manifest.schema.json"
	toolkitSchemaLocation  = "https://raw.githubusercontent.com/hive-controls/formic/main/schema/toolkit.schema.json"
	catalogSchemaLocation  = "https://raw.githubusercontent.com/hive-controls/formic/main/schema/catalog.schema.json"
	equipSchemaLocation    = "https://raw.githubusercontent.com/hive-controls/formic/main/schema/equip.schema.json"
	missionSchemaLocation  = "https://raw.githubusercontent.com/hive-controls/formic/main/schema/mission.schema.json"
)

// compiledFormatSchema lazily compiles one embedded schema document and caches the result,
// so each format's Validate function compiles its schema exactly once.
type compiledFormatSchema struct {
	location string
	bytes    []byte

	once   sync.Once
	schema *jsonschema.Schema
	err    error
}

func (compiled *compiledFormatSchema) compile() (*jsonschema.Schema, error) {
	compiled.once.Do(func() {
		var schemaDocument any
		if err := json.Unmarshal(compiled.bytes, &schemaDocument); err != nil {
			compiled.err = fmt.Errorf("decode embedded schema %s: %w", compiled.location, err)
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(compiled.location, schemaDocument); err != nil {
			compiled.err = fmt.Errorf("load embedded schema %s: %w", compiled.location, err)
			return
		}
		compiled.schema, compiled.err = compiler.Compile(compiled.location)
	})
	if compiled.err != nil {
		return nil, fmt.Errorf("compile embedded schema %s: %w", compiled.location, compiled.err)
	}
	return compiled.schema, nil
}

var (
	manifestSchema = &compiledFormatSchema{location: manifestSchemaLocation, bytes: formatschema.ToolManifestBytes}
	toolkitSchema  = &compiledFormatSchema{location: toolkitSchemaLocation, bytes: formatschema.ToolkitBytes}
	catalogSchema  = &compiledFormatSchema{location: catalogSchemaLocation, bytes: formatschema.CatalogBytes}
	equipSchema    = &compiledFormatSchema{location: equipSchemaLocation, bytes: formatschema.EquipBytes}
	missionSchema  = &compiledFormatSchema{location: missionSchemaLocation, bytes: formatschema.MissionBytes}
)

// validateAgainst decodes data as JSON and checks it against compiled's schema, naming what
// in any decode/validation error so each Validate* function reports its own format's errors.
func validateAgainst(compiled *compiledFormatSchema, data []byte, what string) error {
	schema, err := compiled.compile()
	if err != nil {
		return err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode %s JSON: %w", what, err)
	}
	if err := schema.Validate(decoded); err != nil {
		return fmt.Errorf("validate %s: %w", what, err)
	}
	return nil
}

// Validate checks a JSON-encoded manifest against the canonical manifest schema.
func Validate(data []byte) error {
	return validateAgainst(manifestSchema, data, "manifest")
}

// ValidateToolkit checks a JSON-encoded toolkit descriptor against the canonical toolkit schema.
func ValidateToolkit(data []byte) error {
	return validateAgainst(toolkitSchema, data, "toolkit")
}

// ValidateCatalog checks a JSON-encoded catalog against the canonical catalog schema.
func ValidateCatalog(data []byte) error {
	return validateAgainst(catalogSchema, data, "catalog")
}

// ValidateEquip checks a JSON-encoded equip file against the canonical equip schema.
func ValidateEquip(data []byte) error {
	return validateAgainst(equipSchema, data, "equip")
}

// ValidateMission checks a JSON-encoded mission against the canonical mission schema.
func ValidateMission(data []byte) error {
	return validateAgainst(missionSchema, data, "mission")
}

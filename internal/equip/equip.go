// Package equip reads, writes, and resolves the machine's equip file — the record of how a
// tool's needs[] are met (design/specs/formic-platform-realignment.md §13, §17 car 7). The
// file holds references and provider choices only, never a secret's value.
package equip

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/hive-controls/formic/internal/formats"
	"gopkg.in/yaml.v3"
)

// DefaultPath returns <user-config>/formic/equip.yaml — the same root as catalog.yaml,
// honouring FORMIC_CONFIG_DIR through the caller's own discovery.DefaultUserConfigRoot.
func DefaultPath(userConfigRoot string) string {
	return filepath.Join(userConfigRoot, "formic", "equip.yaml")
}

// Load reads and validates the equip file at path. A file that does not exist reads as an
// empty equip file, not an error — the same "absent file = empty document" contract
// internal/catalog.Load follows for catalog.yaml.
func Load(path string) (formats.Equip, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return formats.Equip{Equip: 1}, nil
	}
	if err != nil {
		return formats.Equip{}, fmt.Errorf("read equip file %s: %w", path, err)
	}
	jsonData, err := yamlToJSON(data)
	if err != nil {
		return formats.Equip{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := formats.ValidateEquip(jsonData); err != nil {
		return formats.Equip{}, fmt.Errorf("%s: %w", path, err)
	}
	var document formats.Equip
	if err := json.Unmarshal(jsonData, &document); err != nil {
		return formats.Equip{}, fmt.Errorf("decode equip file %s: %w", path, err)
	}
	return document, nil
}

// Save validates document, then writes it atomically (temp file + rename, the same precedent
// as internal/runs.Store.Save) to path, creating its parent directory if needed. A document
// that fails validation is never written — the file on disk is left byte-identical.
func Save(path string, document formats.Equip) error {
	document.Equip = 1
	jsonData, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode equip file: %w", err)
	}
	if err := formats.ValidateEquip(jsonData); err != nil {
		return fmt.Errorf("validate equip file: %w", err)
	}
	yamlData, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode equip file as YAML: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create equip directory: %w", err)
	}
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, yamlData, 0o644); err != nil {
		return fmt.Errorf("write equip file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish equip file: %w", err)
	}
	return nil
}

// referencePattern mirrors equip.schema.json's own secrets.additionalProperties.pattern
// exactly: env.NAME is the implemented form; keychain:NAME and file:<path>#key are reserved,
// accepted here but not yet resolved by Resolve.
var referencePattern = regexp.MustCompile(`^(env\.[A-Z][A-Z0-9_]*|keychain:[^\s]+|file:[^\s#]+#[^\s#]+)$`)

// ErrLiteralValue is SetSecret's refusal when reference does not match a recognized reference
// form — the whole "a literal secret value" detection (§13, §15). Callers must report this
// error's own text and never the reference argument itself, so a literal secret is never
// echoed back.
var ErrLiteralValue = errors.New("a literal value; pass a reference such as env.NAME")

// SetSecret records secrets[name] = reference in the equip file at path. A reference that
// does not match a recognized form is refused with ErrLiteralValue and the file is left
// exactly as it was — Load is never even reached for a plainly-literal argument.
func SetSecret(path, name, reference string) (formats.Equip, error) {
	if !referencePattern.MatchString(reference) {
		return formats.Equip{}, ErrLiteralValue
	}
	document, err := Load(path)
	if err != nil {
		return formats.Equip{}, err
	}
	if document.Secrets == nil {
		document.Secrets = formats.EquipSecrets{}
	}
	document.Secrets[name] = reference
	if err := Save(path, document); err != nil {
		return formats.Equip{}, err
	}
	return document, nil
}

// SetProvider records providers[key] = provider in the equip file at path, where key is the
// full "<need kind>.<class>" form (e.g. "gate.browser"). Whether the armory actually accepts
// provider for that need class is the caller's decision (it has the catalog to check
// against) — SetProvider only writes what it is given.
func SetProvider(path, key, provider string) (formats.Equip, error) {
	document, err := Load(path)
	if err != nil {
		return formats.Equip{}, err
	}
	if document.Providers == nil {
		document.Providers = formats.EquipProviders{}
	}
	document.Providers[key] = provider
	if err := Save(path, document); err != nil {
		return formats.Equip{}, err
	}
	return document, nil
}

func yamlToJSON(data []byte) ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	jsonData, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode YAML as JSON: %w", err)
	}
	return jsonData, nil
}

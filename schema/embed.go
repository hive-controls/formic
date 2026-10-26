// Package schema embeds the tool-manifest JSON Schemas so internal/formats can validate
// against them without a second, hand-copied file — Go's embed cannot reach a parent
// folder, which is why this package sits beside the schema instead of formats embedding
// it directly.
package schema

import _ "embed"

//go:embed manifest.schema.json
var ToolManifestBytes []byte

//go:embed toolkit.schema.json
var ToolkitBytes []byte

//go:embed catalog.schema.json
var CatalogBytes []byte

//go:embed equip.schema.json
var EquipBytes []byte

//go:embed mission.schema.json
var MissionBytes []byte

// Package formats holds the Go half of the five file formats the platform ships: generated
// types for each schema in ../../schema/ (schema $id/title untouched by the car-3 move — see
// design/specs/formic-platform-realignment.md §8). One //go:generate line per schema, one
// <format>.gen.go per line — go-jsonschema is invoked once per schema file rather than once
// for all five, so a same-named sub-schema (e.g. the needs item, duplicated between
// manifest.schema.json and toolkit.schema.json) never collides across the generated files.
package formats

//go:generate go tool go-jsonschema -p formats -t ../../schema/manifest.schema.json -o manifest.gen.go

// toolkit.schema.json's needs item is a duplicate of manifest.schema.json's #/$defs/need under
// a distinct $defs name (toolkitNeed, not need): go-jsonschema is invoked once per schema file
// here, so a cross-file $ref is not resolved at generation time, and go-jsonschema names a
// generated type after the $defs key verbatim rather than prefixing it with the schema's title
// — an identical key in two schema files compiled into the same Go package collides.
//go:generate go tool go-jsonschema -p formats -t ../../schema/toolkit.schema.json -o toolkit.gen.go
//go:generate go tool go-jsonschema -p formats -t ../../schema/catalog.schema.json -o catalog.gen.go
//go:generate go tool go-jsonschema -p formats -t ../../schema/equip.schema.json -o equip.gen.go
//go:generate go tool go-jsonschema -p formats -t ../../schema/mission.schema.json -o mission.gen.go

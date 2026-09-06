// Package toolspec holds the Go half of the tool-manifest contract: generated types for
// ../schema/toolspec.schema.json, the single source of truth shared with the TypeScript
// half (../scripts/codegen.mts, ../src/types.generated.mts).
package toolspec

//go:generate go tool go-jsonschema -p toolspec -t ../schema/toolspec.schema.json -o toolspec.gen.go

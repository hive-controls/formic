package formats_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedTypesMatchSchema proves every committed <format>.gen.go is exactly what running
// generate.go's own //go:generate directives against ../../schema/<format>.schema.json produces
// right now. The directives are parsed from generate.go rather than re-typed here, so a flag
// change, a renamed file, or an added/removed schema can never go unnoticed by this test. Each
// generator's output is redirected to t.TempDir() — this test never deletes or rewrites a file
// in the source tree. On drift: run `go generate ./internal/formats` and commit the result.
func TestGeneratedTypesMatchSchema(t *testing.T) {
	directives := parseGenerateDirectives(t, "generate.go")
	if len(directives) == 0 {
		t.Fatal("generate.go: no //go:generate directives found")
	}

	for _, directive := range directives {
		directive := directive
		t.Run(filepath.Base(directive.originalOutput), func(t *testing.T) {
			commandArgs := directive.withOutput(filepath.Join(t.TempDir(), filepath.Base(directive.originalOutput)))

			command := exec.Command(commandArgs[0], commandArgs[1:]...)
			command.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("running generate.go's go:generate directive failed: %v\n%s", err, output)
			}

			generated, err := os.ReadFile(directive.outputValue)
			if err != nil {
				t.Fatalf("reading generated output: %v", err)
			}
			committedPath := filepath.Base(directive.originalOutput)
			committed, err := os.ReadFile(committedPath)
			if err != nil {
				t.Fatalf("reading committed %s: %v", committedPath, err)
			}
			if string(generated) != string(committed) {
				t.Fatalf("Drift detected: %s does not match its schema.\n"+
					"Run `go generate ./internal/formats` and commit the result.", committedPath)
			}
		})
	}
}

// generateDirective is one //go:generate line's argv, parsed once, with its -o value tracked
// separately so withOutput can report the redirected path back to the caller while
// originalOutput still names the committed file to diff against.
type generateDirective struct {
	args           []string
	outputIndex    int
	originalOutput string
	outputValue    string
}

// withOutput returns the directive's argv with its -o value replaced by path, and records
// path as outputValue for the caller to read the generated file back from.
func (directive *generateDirective) withOutput(path string) []string {
	replaced := append([]string{}, directive.args...)
	replaced[directive.outputIndex] = path
	directive.outputValue = path
	return replaced
}

// parseGenerateDirectives reads every //go:generate line from the named file and returns each
// one's command as argv (e.g. ["go", "tool", "go-jsonschema", "-p", "formats", "-t", "...", "-o", "..."]).
func parseGenerateDirectives(t *testing.T, path string) []*generateDirective {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	const prefix = "//go:generate "
	var directives []*generateDirective
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		args := strings.Fields(strings.TrimPrefix(line, prefix))
		found := false
		for index, arg := range args {
			if arg == "-o" && index+1 < len(args) {
				directives = append(directives, &generateDirective{
					args:           args,
					outputIndex:    index + 1,
					originalOutput: args[index+1],
				})
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: go:generate directive has no -o flag: %v", path, args)
		}
	}
	return directives
}

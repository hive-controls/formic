package discovery

import (
	"strings"
	"testing"
)

func TestNpmToolMarksInteractiveByCommandHeuristic(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"watch flag", "vitest --watch", true},
		{"ui flag", "cypress open --ui", true},
		{"interactive flag", "migrate --interactive", true},
		{"bare -i flag at the end", "jest -i", true},
		{"bare -i flag mid command", "node -i --experimental-vm-modules", true},
		{"repl token", "node repl.js", true},
		{"nodemon token", "nodemon server.js", true},
		{"plain build has no interactive marker", "tsc -p .", false},
		{"a hyphenated flag containing -i is not the bare flag", "eslint --fix-input", false},
		{"a word containing repl is not the repl token", "node replace-strings.mjs", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := packageDocument{Scripts: map[string]string{"run": testCase.command}}
			tool := npmTool(document, "package.json", "", "run")
			interactive := tool.Manifest.Pane != nil &&
				tool.Manifest.Pane.Interactive != nil &&
				*tool.Manifest.Pane.Interactive
			if interactive != testCase.want {
				t.Fatalf("command %q interactive = %v, want %v", testCase.command, interactive, testCase.want)
			}
		})
	}
}

// "test:unit" and "test-unit" both sanitize to the schema-legal name "test-unit" — a real
// collision this normalization scheme can produce within a single package.json.
func TestToolsFromPackageDisambiguatesCollidingScriptNames(t *testing.T) {
	document := packageDocument{Scripts: map[string]string{
		"test:unit": "vitest run unit",
		"test-unit": "jest unit",
	}}

	tools := toolsFromPackage(document, "package.json", "")
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2: %+v", len(tools), tools)
	}

	byOriginalScript := make(map[string]Tool, len(tools))
	seenNames := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if seenNames[tool.Name] {
			t.Fatalf("tool name %q is not unique across %+v", tool.Name, tools)
		}
		seenNames[tool.Name] = true
		if tool.Problem != "" {
			t.Fatalf("tool %q failed schema validation: %s", tool.Name, tool.Problem)
		}
		if len(tool.Manifest.Launch.Args) < 2 {
			t.Fatalf("tool %q launch args too short to name a script: %v", tool.Name, tool.Manifest.Launch.Args)
		}
		byOriginalScript[tool.Manifest.Launch.Args[1]] = tool
	}

	for originalScript, expectedCommand := range document.Scripts {
		tool, ok := byOriginalScript[originalScript]
		if !ok {
			t.Fatalf("no discovered tool launches the original script %q; got %+v", originalScript, tools)
		}
		if tool.Manifest.Launch.Args[0] != "run" || tool.Manifest.Launch.Args[1] != originalScript {
			t.Fatalf("tool for %q does not launch its own script: args = %v", originalScript, tool.Manifest.Launch.Args)
		}
		if !strings.Contains(tool.Title, originalScript) {
			t.Fatalf("title %q does not preserve the original script key %q for the user", tool.Title, originalScript)
		}
		if tool.Description != expectedCommand {
			t.Fatalf("description %q does not match the command for %q", tool.Description, originalScript)
		}
	}

	repeat := toolsFromPackage(document, "package.json", "")
	firstAssignment := make(map[string]string, len(tools))
	for _, tool := range tools {
		firstAssignment[tool.Manifest.Launch.Args[1]] = tool.Name
	}
	for _, tool := range repeat {
		if firstAssignment[tool.Manifest.Launch.Args[1]] != tool.Name {
			t.Fatalf("disambiguation is not deterministic across runs for %q: %q vs %q",
				tool.Manifest.Launch.Args[1], firstAssignment[tool.Manifest.Launch.Args[1]], tool.Name)
		}
	}
}

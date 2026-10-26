package formats_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const manifestPath = "fixtures/conformance/manifest.md"

// manifestFixtureBullet matches a manifest.md bullet naming one fixture, e.g.
// "- `minimal.json` — the smallest manifest that satisfies the schema: ...".
var manifestFixtureBullet = regexp.MustCompile("^- `([A-Za-z0-9_.-]+\\.json)`")

// parseManifestFixtureNames reads fixtures/conformance/manifest.md and returns the fixture
// filenames it documents under each "## valid/" / "## invalid/" heading.
func parseManifestFixtureNames(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading %s: %v", manifestPath, err)
	}

	documented := map[string][]string{"valid": nil, "invalid": nil}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "## valid/":
			section = "valid"
			continue
		case "## invalid/":
			section = "invalid"
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			section = ""
			continue
		}
		if section == "" {
			continue
		}
		if match := manifestFixtureBullet.FindStringSubmatch(trimmed); match != nil {
			documented[section] = append(documented[section], match[1])
		}
	}
	return documented
}

// TestManifestFixtureListingMatchesDisk asserts fixtures/conformance/manifest.md and the
// fixtures/conformance/{valid,invalid}/ directories name exactly the same files, in both
// directions: every fixture manifest.md documents must exist on disk (deleting a fixture file
// without updating the doc goes red here), and every fixture on disk must be documented (the
// per-fixture check-registration failures in TestValidFixturesUnmarshalAndValidate /
// TestInvalidFixturesAreRejected already catch that direction against the Go test's own
// tables, but not against manifest.md itself).
func TestManifestFixtureListingMatchesDisk(t *testing.T) {
	documented := parseManifestFixtureNames(t)
	for _, subdir := range []string{"valid", "invalid"} {
		subdir := subdir
		t.Run(subdir, func(t *testing.T) {
			documentedNames := documented[subdir]
			if len(documentedNames) == 0 {
				t.Fatalf("manifest.md lists no %s/ fixtures — the doc or this parser has drifted", subdir)
			}

			onDisk := listFixtureNames(t, subdir)
			onDiskSet := make(map[string]bool, len(onDisk))
			for _, name := range onDisk {
				onDiskSet[name] = true
			}
			documentedSet := make(map[string]bool, len(documentedNames))
			for _, name := range documentedNames {
				documentedSet[name] = true
			}

			for _, name := range documentedNames {
				if !onDiskSet[name] {
					t.Errorf("manifest.md documents %s/%s but it does not exist on disk", subdir, name)
				}
			}
			for _, name := range onDisk {
				if !documentedSet[name] {
					t.Errorf("%s/%s exists on disk but manifest.md does not document it", subdir, name)
				}
			}
		})
	}
}

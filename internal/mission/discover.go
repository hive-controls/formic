// Package mission discovers, validates, and pre-flights v1 missions — a saved, named launch of
// one catalogued tool (design/specs/formic-platform-realignment.md §9 N5, §17 car 9).
package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hive-controls/formic/internal/formats"
	"gopkg.in/yaml.v3"
)

// Source is one catalogued toolkit root, in catalog precedence order — the same list
// cmd/formic's loadCatalog() hands to discovery.Discover.
type Source struct {
	Path string
}

// Mission is one discovered, validated mission definition and where it came from.
type Mission struct {
	Definition formats.Mission
	Path       string
	SourceRoot string
	Personal   bool
	// ShadowedBy names, in precedence order, every later definition of the same name that
	// this one shadows (§9: "the first definition wins per name, and a shadowed one is shown
	// as shadowed").
	ShadowedBy []string
}

// InvalidMission is one discovered mission file that failed validation — reported by `doctor`
// (fatal, like an invalid manifest), never listed by `mission list` and never runnable.
type InvalidMission struct {
	Path  string
	Error string
}

// Set is every mission discovered across the personal root and every catalogued source, in
// precedence order.
type Set struct {
	Missions []Mission
	Invalid  []InvalidMission
}

// Find looks up a mission by name among the winning (non-shadowed) definitions.
func (set Set) Find(name string) (Mission, bool) {
	for _, mission := range set.Missions {
		if mission.Definition.Name == name {
			return mission, true
		}
	}
	return Mission{}, false
}

// Discover walks the personal missions root first (sorted by file name), then each catalogued
// source's `.formic/missions/*.yaml` in catalog order (§9: "The user's folder is read first,
// then toolkits in catalog order; the first definition wins per name, and a shadowed one is
// shown as shadowed"). Discovery is directory convention only — no toolkit schema field
// advertises a missions directory, and a source not present in sources never contributes
// missions (N8: "a mission in an uncatalogued directory never appears").
func Discover(userConfigRoot string, sources []Source) (Set, error) {
	set := Set{}
	winnerIndex := make(map[string]int)

	personalDirectory := filepath.Join(userConfigRoot, "formic", "missions")
	if err := discoverDirectory(personalDirectory, "", true, &set, winnerIndex); err != nil {
		return Set{}, err
	}
	for _, source := range sources {
		sourceDirectory := filepath.Join(source.Path, ".formic", "missions")
		if err := discoverDirectory(sourceDirectory, source.Path, false, &set, winnerIndex); err != nil {
			return Set{}, err
		}
	}
	return set, nil
}

func discoverDirectory(directory, sourceRoot string, personal bool, set *Set, winnerIndex map[string]int) error {
	paths, err := filepath.Glob(filepath.Join(directory, "*.yaml"))
	if err != nil {
		return fmt.Errorf("scan missions: %w", err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		definition, err := loadMission(path)
		if err != nil {
			set.Invalid = append(set.Invalid, InvalidMission{Path: path, Error: err.Error()})
			continue
		}
		candidate := Mission{Definition: definition, Path: path, SourceRoot: sourceRoot, Personal: personal}
		if index, exists := winnerIndex[definition.Name]; exists {
			winner := &set.Missions[index]
			winner.ShadowedBy = append(winner.ShadowedBy, candidate.Path)
			continue
		}
		winnerIndex[definition.Name] = len(set.Missions)
		set.Missions = append(set.Missions, candidate)
	}
	return nil
}

// loadMission YAML-decodes, then JSON-validates against the embedded mission-1 schema, exactly
// the way manifest.LoadFile and catalog.Load each convert their own YAML document before
// validating it — internal/manifest's own converter is unexported, so this mirrors that
// established per-package pattern rather than widening its public surface for one caller.
func loadMission(path string) (formats.Mission, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return formats.Mission{}, fmt.Errorf("read mission: %w", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return formats.Mission{}, fmt.Errorf("decode mission YAML: %w", err)
	}
	jsonData, err := json.Marshal(document)
	if err != nil {
		return formats.Mission{}, fmt.Errorf("encode mission as JSON: %w", err)
	}
	if err := formats.ValidateMission(jsonData); err != nil {
		return formats.Mission{}, err
	}
	var mission formats.Mission
	if err := json.Unmarshal(jsonData, &mission); err != nil {
		return formats.Mission{}, fmt.Errorf("decode mission: %w", err)
	}
	return mission, nil
}

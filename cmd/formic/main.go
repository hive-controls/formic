package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/doctor"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/repository"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/ui"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	if len(arguments) == 1 && arguments[0] == "version" {
		fmt.Printf("formic %s (%s %s)\n", version, commit, date)
		return 0
	}
	// __replay-row is the tab wrapper's own internal verb (§17 car 6): not a user-facing
	// command, invoked only by formic itself when it spawns a tab/window through the host
	// terminal (internal/launch.NewTabCommand's wrapperArgv).
	if len(arguments) > 0 && arguments[0] == "__replay-row" {
		return runWrapped(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "add" {
		return runAdd(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "list" {
		return runList(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "describe" {
		return runDescribe(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "launch" {
		return runLaunch(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "equip" {
		return runEquip(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "inventory" {
		return runInventory(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "probe" {
		return runProbeVerb(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "mission" {
		return runMission(arguments[1:])
	}
	repositoryRoot, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	if len(arguments) == 1 && arguments[0] == "doctor" {
		repositoryProfile, profileProblem := profile.Load(repositoryRoot)
		warnings := profile.UnknownToolWarnings(repositoryProfile, catalogToolNames(discoveredCatalog))
		discoveredMissions, missionErr := loadMissions()
		if missionErr != nil {
			fmt.Fprintf(os.Stderr, "formic: %v\n", missionErr)
			return 1
		}
		healthChecker := func(tool discovery.Tool) *launch.HealthResult {
			return launch.RunHealthCheck(tool, repositoryProfile.Tools[tool.Name], os.Environ(), repositoryRoot)
		}
		store, storeErr := runs.DefaultStore()
		if storeErr != nil {
			fmt.Fprintf(os.Stdout, "run recovery: %v\n", storeErr)
			return doctor.RunWithMissionsAndHealth(discoveredCatalog, discoveredMissions, profileProblem, warnings, healthChecker, os.Stdout)
		}
		return doctor.RecoverAndReportWithMissionsAndHealth(discoveredCatalog, discoveredMissions, profileProblem, warnings, healthChecker, store, time.Now(), os.Stdout)
	}
	if len(arguments) > 0 {
		fmt.Fprintf(os.Stderr, "unknown command %q\n", arguments[0])
		return 2
	}
	return runBrowser(discoveredCatalog, repositoryRoot)
}

func catalogToolNames(catalog discovery.Catalog) []string {
	names := make([]string, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func runWrapped(arguments []string) int {
	if len(arguments) != 1 {
		fmt.Fprintln(os.Stderr, "usage: formic __replay-row <run-row-path>")
		return 2
	}
	exitCode, err := launch.RunWrapper(arguments[0], time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
	}
	return exitCode
}

// loadCatalog no longer requires a Git repository as a startup precondition (§10 N6, §17
// car 6): formic starts in any directory. When no repository root is found above the
// working directory, the working directory itself stands in for it — repo-local discovery
// and the profile then simply find nothing there, rather than refusing to start at all.
// The armory itself is the admission rule (§12): only sources catalogued with `formic add`
// are ever scanned — nothing is seeded, nothing appears by accident.
func loadCatalog() (string, discovery.Catalog, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", discovery.Catalog{}, fmt.Errorf("resolve working directory: %w", err)
	}
	repositoryRoot, err := repository.FindRoot(workingDirectory)
	if err != nil {
		repositoryRoot = workingDirectory
	}
	sourcePaths, err := catalogSourcePaths()
	if err != nil {
		return "", discovery.Catalog{}, err
	}
	discoveredCatalog, err := discovery.Discover(sourcePaths)
	if err != nil {
		return "", discovery.Catalog{}, fmt.Errorf("discover tools: %w", err)
	}
	return repositoryRoot, discoveredCatalog, nil
}

// catalogSourcePaths resolves the user's catalog file and returns the paths of every
// source it admits, in precedence order.
func catalogSourcePaths() ([]string, error) {
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return nil, err
	}
	return catalog.SourcePaths(userConfigRoot)
}

// loadMissions discovers every mission across the personal root and every catalogued source, in
// precedence order (§9 N5) — the same catalog source list catalogSourcePaths hands to
// discovery.Discover.
func loadMissions() (mission.Set, error) {
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return mission.Set{}, err
	}
	sourcePaths, err := catalogSourcePaths()
	if err != nil {
		return mission.Set{}, err
	}
	sources := make([]mission.Source, 0, len(sourcePaths))
	for _, sourcePath := range sourcePaths {
		sources = append(sources, mission.Source{Path: sourcePath})
	}
	discovered, err := mission.Discover(userConfigRoot, sources)
	if err != nil {
		return mission.Set{}, fmt.Errorf("discover missions: %w", err)
	}
	return discovered, nil
}

// runAdd is `formic add <path>` (§12, §15): the path must exist and carry a
// `.formic/toolkit.yaml` that validates, or the catalog is left untouched and this
// refuses with the reason. On success the resulting catalog is printed.
func runAdd(arguments []string) int {
	jsonOutput := false
	var path string
	for _, argument := range arguments {
		if argument == "--json" {
			jsonOutput = true
			continue
		}
		path = argument
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "usage: formic add <path> [--json]")
		return 2
	}
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	document, err := catalog.Add(catalog.DefaultPath(userConfigRoot), path)
	if err != nil {
		if jsonOutput {
			printJSON(map[string]any{"refused": true, "reason": err.Error()})
		} else {
			fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		}
		return 1
	}
	if jsonOutput {
		printJSON(document)
		return 0
	}
	for _, source := range document.Sources {
		fmt.Println(source.Path)
	}
	return 0
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "formic: encode JSON: %v\n", err)
	}
}

// runBrowser starts the deck. The add-this-repo offer (N8 point 3) is attached best-effort: a
// user-config-root resolution failure, or a catalog read failure, simply means the deck starts
// with no offer showing rather than refusing to start at all.
func runBrowser(discoveredCatalog discovery.Catalog, repositoryRoot string) int {
	model := ui.New(discoveredCatalog, repositoryRoot)
	if userConfigRoot, err := discovery.DefaultUserConfigRoot(); err == nil {
		if sourcePaths, err := catalog.SourcePaths(userConfigRoot); err == nil {
			model = model.WithCatalogContext(userConfigRoot, catalog.UncataloguedAt(repositoryRoot, sourcePaths))
		}
	}
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	return 0
}

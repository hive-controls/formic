package main

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/doctor"
	"github.com/hive-controls/hivedeck/internal/launch"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/repository"
	"github.com/hive-controls/hivedeck/internal/ui"
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
		fmt.Printf("hivedeck %s (%s %s)\n", version, commit, date)
		return 0
	}
	if len(arguments) > 0 && arguments[0] == "run" {
		return runWrapped(arguments[1:])
	}
	repositoryRoot, catalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivedeck: %v\n", err)
		return 1
	}
	if len(arguments) == 1 && arguments[0] == "doctor" {
		repositoryProfile, profileProblem := profile.Load(repositoryRoot)
		warnings := profile.UnknownToolWarnings(repositoryProfile, catalogToolNames(catalog))
		return doctor.RunWithProfileWarnings(catalog, profileProblem, warnings, os.Stdout)
	}
	if len(arguments) > 0 {
		fmt.Fprintf(os.Stderr, "unknown command %q\n", arguments[0])
		return 2
	}
	return runBrowser(catalog, repositoryRoot)
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
		fmt.Fprintln(os.Stderr, "usage: hivedeck run <run-row-path>")
		return 2
	}
	exitCode, err := launch.RunWrapper(arguments[0], time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hivedeck: %v\n", err)
	}
	return exitCode
}

func loadCatalog() (string, discovery.Catalog, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", discovery.Catalog{}, fmt.Errorf("resolve working directory: %w", err)
	}
	repositoryRoot, err := repository.FindRoot(workingDirectory)
	if err != nil {
		return "", discovery.Catalog{}, err
	}
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return "", discovery.Catalog{}, err
	}
	catalog, err := discovery.Discover(repositoryRoot, userConfigRoot)
	if err != nil {
		return "", discovery.Catalog{}, fmt.Errorf("discover tools: %w", err)
	}
	return repositoryRoot, catalog, nil
}

func runBrowser(catalog discovery.Catalog, repositoryRoot string) int {
	program := tea.NewProgram(ui.New(catalog, repositoryRoot), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hivedeck: %v\n", err)
		return 1
	}
	return 0
}

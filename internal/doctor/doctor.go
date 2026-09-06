package doctor

import (
	"errors"
	"fmt"
	"io"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/profile"
)

// Run prints a doctor report and returns the process exit code. Per §4, the ONLY collision
// shape that is fatal (non-zero exit) is a repo-local definition shadowing a different tool
// of the same name from the user library — same name, different launch.command, the shape
// most likely to be an accident. Every other shadow (same command, or shadowing a built-in
// npm-script or shipped product manifest) is reported with the `↑` badge but stays exit 0.
// An invalid manifest is always fatal, independent of collisions.
func Run(catalog discovery.Catalog, output io.Writer) int {
	return RunWithProfile(catalog, nil, output)
}

func RunWithProfile(catalog discovery.Catalog, profileProblem error, output io.Writer) int {
	return RunWithProfileWarnings(catalog, profileProblem, nil, output)
}

func RunWithProfileWarnings(catalog discovery.Catalog, profileProblem error, profileWarnings []string, output io.Writer) int {
	fatalProblems := 0
	if profileProblem != nil {
		fatalProblems += printProfileProblems(output, profileProblem)
	}
	for _, warning := range profileWarnings {
		fmt.Fprintf(output, "profile warning: %s\n", warning)
	}
	for _, tool := range catalog.Tools {
		fmt.Fprintf(output, "tool %s %s\n", tool.Name, tool.Source.Path)
		if tool.Problem != "" {
			fmt.Fprintf(output, "invalid %s %s: %s\n", tool.Name, tool.Source.Path, tool.Problem)
			fatalProblems++
		}
		for _, shadowed := range tool.Shadowed {
			printShadow(output, tool, shadowed)
			if isFatalShadow(tool, shadowed) {
				fatalProblems++
			}
		}
	}
	if fatalProblems > 0 {
		fmt.Fprintf(output, "problems: %d across %d tools\n", fatalProblems, len(catalog.Tools))
		return 1
	}
	fmt.Fprintf(output, "ok: %d tools discovered, no problems\n", len(catalog.Tools))
	return 0
}

func printProfileProblems(output io.Writer, problem error) int {
	var validationError *profile.ValidationError
	if errors.As(problem, &validationError) {
		for _, message := range validationError.Problems {
			fmt.Fprintf(output, "profile %s\n", message)
		}
		return len(validationError.Problems)
	}
	fmt.Fprintf(output, "profile %s\n", problem)
	return 1
}

func printShadow(output io.Writer, tool discovery.Tool, shadowed discovery.Definition) {
	if isFatalShadow(tool, shadowed) {
		fmt.Fprintf(output, "collision %s: %s shadows a different user-library tool at %s\n", tool.Name, tool.Source.Path, shadowed.Source.Path)
		return
	}
	fmt.Fprintf(output, "↑ %s: %s shadows %s\n", tool.Name, tool.Source.Path, shadowed.Source.Path)
}

func isFatalShadow(tool discovery.Tool, shadowed discovery.Definition) bool {
	return tool.Source.Kind == discovery.SourceLocal &&
		shadowed.Source.Kind == discovery.SourceUser &&
		shadowed.Command != tool.Manifest.Launch.Command
}

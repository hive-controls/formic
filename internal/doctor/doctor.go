package doctor

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

// Run prints a doctor report and returns the process exit code. Per the admission rule
// (§12), shadowing is now the user's own chosen catalog precedence order, not an implicit
// repo-vs-user hierarchy — a shadow is always reported with the `↑` badge, informational,
// never fatal by itself. An invalid manifest is always fatal.
func Run(catalog discovery.Catalog, output io.Writer) int {
	return RunWithProfile(catalog, nil, output)
}

func RunWithProfile(catalog discovery.Catalog, profileProblem error, output io.Writer) int {
	return RunWithProfileWarnings(catalog, profileProblem, nil, output)
}

// RecoverAndReport runs the abandoned-foreground-run recovery pass (a row left "running" by a
// session that died mid-run) before producing the usual report, so `hivedeck doctor` surfaces
// and repairs that state too, not just discovery/profile problems. A recovery failure is
// reported like any other line, never fatal to the rest of the report.
func RecoverAndReport(catalog discovery.Catalog, profileProblem error, profileWarnings []string, store runs.Store, finishedAt time.Time, output io.Writer) int {
	if _, err := store.RecoverAbandonedForegroundRuns(finishedAt); err != nil {
		fmt.Fprintf(output, "run recovery: %v\n", err)
	}
	return RunWithProfileWarnings(catalog, profileProblem, profileWarnings, output)
}

func RunWithProfileWarnings(catalog discovery.Catalog, profileProblem error, profileWarnings []string, output io.Writer) int {
	return RunWithMissions(catalog, mission.Set{}, profileProblem, profileWarnings, output)
}

// RecoverAndReportWithMissions is RecoverAndReport extended with car 9's missions section (see
// RunWithMissions).
func RecoverAndReportWithMissions(catalog discovery.Catalog, missions mission.Set, profileProblem error, profileWarnings []string, store runs.Store, finishedAt time.Time, output io.Writer) int {
	return RecoverAndReportWithMissionsAndHealth(catalog, missions, profileProblem, profileWarnings, nil, store, finishedAt, output)
}

// RecoverAndReportWithMissionsAndHealth is RecoverAndReportWithMissions extended with §15 N11's
// per-tool health check (see RunWithMissionsAndHealth). healthChecker may be nil, in which case
// no health line is printed for any tool — the same as passing a checker that always returns
// nil.
func RecoverAndReportWithMissionsAndHealth(catalog discovery.Catalog, missions mission.Set, profileProblem error, profileWarnings []string, healthChecker func(discovery.Tool) *launch.HealthResult, store runs.Store, finishedAt time.Time, output io.Writer) int {
	if _, err := store.RecoverAbandonedForegroundRuns(finishedAt); err != nil {
		fmt.Fprintf(output, "run recovery: %v\n", err)
	}
	return RunWithMissionsAndHealth(catalog, missions, profileProblem, profileWarnings, healthChecker, output)
}

// RunWithMissions is RunWithProfileWarnings extended with car 9's missions section: an invalid
// mission file is reported and counted fatal, exactly like an invalid manifest (§9's discovery
// engine is the ENGINE side of a format `formic mission run` already validates); a shadowed
// mission is reported informationally, mirroring a shadowed tool.
func RunWithMissions(catalog discovery.Catalog, missions mission.Set, profileProblem error, profileWarnings []string, output io.Writer) int {
	return RunWithMissionsAndHealth(catalog, missions, profileProblem, profileWarnings, nil, output)
}

// RunWithMissionsAndHealth is RunWithMissions extended with §15 N11's per-tool health check:
// when healthChecker is non-nil, it is called once for every discovered tool and, when it
// reports a non-nil result (the manifest declared a health.check), that result is printed as a
// "health <tool>: ..." line right after the tool's own line — informational only, never fatal by
// itself, mirroring how a shadow is reported. healthChecker may be nil (no health reporting at
// all), matching every existing caller unchanged.
func RunWithMissionsAndHealth(catalog discovery.Catalog, missions mission.Set, profileProblem error, profileWarnings []string, healthChecker func(discovery.Tool) *launch.HealthResult, output io.Writer) int {
	fatalProblems := 0
	if profileProblem != nil {
		fatalProblems += printProfileProblems(output, profileProblem)
	}
	for _, warning := range profileWarnings {
		fmt.Fprintf(output, "profile warning: %s\n", warning)
	}
	for _, tool := range catalog.Tools {
		fmt.Fprintf(output, "tool %s %s\n", tool.Name, tool.Source.Path)
		if healthChecker != nil {
			if health := healthChecker(tool); health != nil {
				printHealth(output, tool, health)
			}
		}
		for _, shadowed := range tool.Shadowed {
			printShadow(output, tool, shadowed)
		}
	}
	for _, invalid := range catalog.Invalid {
		fmt.Fprintf(output, "invalid %s %s: %s\n", invalid.Name, invalid.Source.Path, invalid.Problem)
		fatalProblems++
	}
	for _, mission := range missions.Missions {
		fmt.Fprintf(output, "mission %s %s\n", mission.Definition.Name, mission.Path)
		for _, shadowed := range mission.ShadowedBy {
			fmt.Fprintf(output, "↑ %s: %s shadows %s\n", mission.Definition.Name, mission.Path, shadowed)
		}
	}
	for _, invalidMission := range missions.Invalid {
		fmt.Fprintf(output, "invalid mission %s: %s\n", invalidMission.Path, invalidMission.Error)
		fatalProblems++
	}
	totalTools := len(catalog.Tools) + len(catalog.Invalid)
	if fatalProblems > 0 {
		fmt.Fprintf(output, "problems: %d across %d tools\n", fatalProblems, totalTools)
		return 1
	}
	fmt.Fprintf(output, "ok: %d tools discovered, no problems\n", totalTools)
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
	fmt.Fprintf(output, "↑ %s: %s shadows %s\n", tool.Name, tool.Source.Path, shadowed.Source.Path)
}

// printHealth reports a single tool's health.check outcome (§15 N11) — informational, never
// fatal to the rest of the report by itself.
func printHealth(output io.Writer, tool discovery.Tool, health *launch.HealthResult) {
	if health.Error != "" {
		fmt.Fprintf(output, "health %s: ran=%v error=%q durationMs=%d\n", tool.Name, health.Ran, health.Error, health.DurationMs)
		return
	}
	exitCode := -1
	if health.ExitCode != nil {
		exitCode = *health.ExitCode
	}
	fmt.Fprintf(output, "health %s: ran=%v ok=%v exitCode=%d durationMs=%d\n", tool.Name, health.Ran, health.OK, exitCode, health.DurationMs)
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

// runMission is `formic mission <run|list>` (§17 car 9). Bare `formic mission` is a usage
// error, per every other verb's own bare-subcommand handling (see runEquip).
func runMission(arguments []string) int {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "usage: formic mission <run <name>|list> [--json]")
		return 2
	}
	switch arguments[0] {
	case "run":
		return runMissionRun(arguments[1:])
	case "list":
		return runMissionList(arguments[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown mission command %q\n", arguments[0])
		return 2
	}
}

// --- list ---

// listedMission is `formic mission list --json`'s sibling shape to listedTool (founder ruling
// 2): name, the mission FILE's own path (Source — mirroring listedTool.Source, which names a
// manifest file, not a source root), shadowed-by, the step's tool, evidence path, and the
// mission's own declared inputs.
type listedMission struct {
	Name       string   `json:"name"`
	Source     string   `json:"source"`
	ShadowedBy []string `json:"shadowedBy,omitempty"`
	Tool       string   `json:"tool"`
	Evidence   string   `json:"evidence"`
	Inputs     []string `json:"inputs,omitempty"`
}

// runMissionList is `formic mission list` (founder ruling 2): every winning (non-shadowed)
// mission. `formic list` stays tools-only; an invalid mission file never appears here — it is
// reported only by `doctor`.
func runMissionList(arguments []string) int {
	jsonOutput := hasJSONFlag(arguments)
	discoveredMissions, err := loadMissions()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	listed := make([]listedMission, 0, len(discoveredMissions.Missions))
	for _, discovered := range discoveredMissions.Missions {
		listed = append(listed, listedMission{
			Name: discovered.Definition.Name, Source: discovered.Path, ShadowedBy: discovered.ShadowedBy,
			Tool: discovered.Definition.Steps[0].Tool, Evidence: discovered.Definition.Evidence, Inputs: discovered.Definition.Inputs,
		})
	}
	if jsonOutput {
		printJSON(listed)
		return 0
	}
	for _, discovered := range listed {
		fmt.Printf("%s\t%s\t%s\n", discovered.Name, discovered.Tool, discovered.Source)
		for _, shadow := range discovered.ShadowedBy {
			fmt.Printf("  shadowed-by %s\n", shadow)
		}
	}
	return 0
}

// --- run ---

// preflightRefusal is `mission run`'s structured refusal (§9/§15: "a refusal lists every
// problem and runs nothing"), rendered the same "refused: true, reason, ..." shape every other
// verb's refusal uses.
type preflightRefusal struct {
	Refused  bool              `json:"refused"`
	Reason   string            `json:"reason"`
	Problems []mission.Problem `json:"problems"`
}

func refuseMission(jsonOutput bool, refused *mission.PreflightRefused) int {
	if jsonOutput {
		printJSON(preflightRefusal{Refused: true, Reason: "pre-flight", Problems: refused.Problems})
		return 1
	}
	fmt.Fprintln(os.Stderr, "formic: mission pre-flight refused")
	for _, problem := range refused.Problems {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", problem.Kind, problem.Detail)
	}
	return 1
}

// runMissionRun is `formic mission run <name>` (§9, §15). It discovers missions, resolves
// <name>, runs pre-flight (every problem, or none), then launches through the same
// launch.Prepare every other route shares — Prepare itself owns creating the evidence folder
// and wiring it into the child's environment (see internal/launch.Request.EvidenceRoot) — and
// writes the mission's own evidence-folder record on completion. Under --json the launched
// tool's own stdout and stderr are both redirected to this process's stderr, so stdout carries
// only the JSON summary (or refusal object); without --json, nothing changes.
func runMissionRun(arguments []string) int {
	inputPairs, remainingArguments := extractInputFlags(arguments)
	jsonOutput := hasJSONFlag(remainingArguments)
	name := positionalArgument(remainingArguments)
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: formic mission run <name> [--input name=value]... [--json]")
		return 2
	}
	_, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	discoveredMissions, err := loadMissions()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	found, ok := discoveredMissions.Find(name)
	if !ok {
		return refuse(jsonOutput, fmt.Sprintf("unknown mission %q", name))
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	baseEnvironment := os.Environ()
	plan, refused := mission.Preflight(found, discoveredCatalog, workingDirectory, mission.Values(inputPairs), baseEnvironment, jsonOutput)
	if refused != nil {
		return refuseMission(jsonOutput, refused)
	}

	repositoryRoot := plan.Tool.SourceRoot
	repositoryProfile, profileProblem := profile.Load(repositoryRoot)
	if profileProblem != nil {
		return refuse(jsonOutput, profileProblem.Error())
	}
	store, err := runs.DefaultStore()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	settings := repositoryProfile.Tools[plan.Tool.Name]
	request := launch.Request{
		Tool: plan.Tool, RepositoryRoot: repositoryRoot, Profile: repositoryProfile,
		Spec: settings.Spec, Inputs: plan.Inputs, Store: store, Environment: baseEnvironment,
		StartedAt: time.Now(), Mission: found.Definition.Name, MissionPath: found.Path, EvidenceRoot: plan.EvidenceRoot,
		Services: launch.CatalogServiceChecker(context.Background(), discoveredCatalog, repositoryRoot),
	}
	prepared, err := launch.Prepare(request, profile.ModeForeground)
	if err != nil {
		var needsRefused *launch.NeedsRefused
		if errors.As(err, &needsRefused) {
			return refuseNeeds(jsonOutput, needsRefused)
		}
		var inputsRefused *launch.InputsRefused
		if errors.As(err, &inputsRefused) {
			return refuseInputs(jsonOutput, inputsRefused)
		}
		return refuse(jsonOutput, err.Error())
	}

	prepared.Command.Stdin, prepared.Command.Stdout, prepared.Command.Stderr = os.Stdin, childStdout(jsonOutput), os.Stderr
	commandError := prepared.Command.Run()
	row, err := launch.Finish(store, prepared.Row.ID, commandError, time.Now())
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}

	result := launchResult{RunID: row.ID, Outcome: row.Outcome, ExitCode: row.ExitCode, Artifact: row.Artifact, Mission: row.Mission}
	if jsonOutput {
		printJSON(result)
	} else {
		fmt.Printf("mission %s run %s: %s (exit code %s) %s\n", found.Definition.Name, result.RunID, result.Outcome, formatExitCode(result.ExitCode), result.Artifact)
	}
	if row.ExitCode != nil {
		return *row.ExitCode
	}
	return 0
}

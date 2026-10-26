package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/launch"
)

// probeConcurrency caps how many probes `formic probe` runs at once.
const probeConcurrency = 4

// probeReport is one tool's live state. It is printed and dropped: never stored (§22 b).
type probeReport struct {
	Tool       string `json:"tool"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

type probeVerbResult struct {
	Probes []probeReport `json:"probes"`
}

func toProbeReport(toolName string, result launch.ProbeResult) probeReport {
	return probeReport{Tool: toolName, State: string(result.State), Detail: result.Detail, DurationMs: result.Duration.Milliseconds()}
}

// selectProbeTargets resolves the tools to probe: every admitted tool that declares live when no
// name is given, else each named tool (an unknown name is an error).
func selectProbeTargets(catalog discovery.Catalog, names []string) ([]discovery.Tool, error) {
	if len(names) == 0 {
		var targets []discovery.Tool
		for _, tool := range catalog.Tools {
			if tool.Manifest.Live != nil {
				targets = append(targets, tool)
			}
		}
		return targets, nil
	}
	targets := make([]discovery.Tool, 0, len(names))
	for _, name := range names {
		tool, found := catalog.Find(name)
		if !found {
			return nil, fmt.Errorf("unknown tool %q", name)
		}
		targets = append(targets, tool)
	}
	return targets, nil
}

// runProbeVerb is `formic probe [<tool>...] [--json]` (§22): one live-state probe per tool,
// in catalog order, at most probeConcurrency at once. It writes nothing and exits 0 for any
// report; only a refusal (an unknown tool) exits 1.
func runProbeVerb(arguments []string) int {
	jsonOutput := hasJSONFlag(arguments)
	repositoryRoot, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	targets, err := selectProbeTargets(discoveredCatalog, positionalArguments(arguments))
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	reports := make([]probeReport, len(targets))
	slots := make(chan struct{}, probeConcurrency)
	var group sync.WaitGroup
	for index, tool := range targets {
		group.Add(1)
		slots <- struct{}{}
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			reports[index] = toProbeReport(tool.Name, launch.Probe(context.Background(), tool, repositoryRoot))
		}()
	}
	group.Wait()
	if jsonOutput {
		printJSON(probeVerbResult{Probes: reports})
		return 0
	}
	for _, report := range reports {
		fmt.Printf("%s\t%s\t%s\t%dms\n", report.Tool, report.State, report.Detail, report.DurationMs)
	}
	return 0
}

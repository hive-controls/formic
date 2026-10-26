package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/pane"
)

type inventoryProvider struct {
	Class  string `json:"class"`
	Name   string `json:"name"`
	Chosen string `json:"chosen,omitempty"`
	Usable bool   `json:"usable"`
	Probe  string `json:"probe"`
}

type inventoryComponent struct {
	Name    string  `json:"name"`
	Present bool    `json:"present"`
	Version *string `json:"version,omitempty"`
}

type inventoryTerminalRoute struct {
	Route    string `json:"route"`
	Verified bool   `json:"verified"`
}

type inventoryTerminal struct {
	Detected bool                     `json:"detected"`
	Name     string                   `json:"name,omitempty"`
	Routes   []inventoryTerminalRoute `json:"routes"`
}

// terminalRoute is one (terminal, route) pair in the ledger below.
type terminalRoute struct {
	terminal launch.Terminal
	route    string
}

// verifiedTerminalRoutes is the per-terminal live-verification ledger (ruling 1): every
// host-terminal tab/split argv defaults to unverified until hand-exercised — only the pairs
// listed here as true have actually been run against the real terminal client. foreground and
// pane carry no host-terminal argv at all, so they are always verified below, rather than
// entered in this table.
var verifiedTerminalRoutes = map[terminalRoute]bool{
	{launch.TerminalWindows, "tab"}:   true, // hand-verified 2026-09-18
	{launch.TerminalWindows, "split"}: true, // hand-verified 2026-09-24
	// Observed 2026-10-15 on the built `formic launch --detach` binary: Ghostty 1.3.1 tab count
	// 1 -> 2 with the window count unchanged; Terminal.app 2.15 window count +1 (do script
	// opens a WINDOW, so this "tab" route lands as a new window there); cmux 0.64.23 gained a
	// new workspace in the current window with the requested cwd.
	{launch.TerminalGhostty, "tab"}: true,
	{launch.TerminalApple, "tab"}:   true,
	{launch.TerminalCmux, "tab"}:    true,
}

type inventoryResult struct {
	Providers []inventoryProvider  `json:"providers"`
	Agents    []inventoryComponent `json:"agents"`
	Runtimes  []inventoryComponent `json:"runtimes"`
	Terminal  inventoryTerminal    `json:"terminal"`
}

// inventoryAgentNames and inventoryRuntimeNames are the fixed names §15's `inventory` probes.
// Never health-checked at list time (N11 forbids it) and never written anywhere — probed
// fresh on every call, per §1 RULED's "detected live and never stored."
var inventoryAgentNames = []string{"claude", "codex", "kimi", "grok"}
var inventoryRuntimeNames = []string{"node", "go", "python", "python3", "git"}

const inventoryVersionProbeTimeout = 5 * time.Second

// runInventory is `formic inventory` (§15, N11): usable providers, installed agent CLIs,
// runtimes, and the host terminal and its routes — probed live on every call, never stored.
// TestInventoryWritesNoFile proves the "never stored" half.
func runInventory(arguments []string) int {
	jsonOutput := hasJSONFlag(arguments)
	_, discoveredCatalog, err := loadCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	equipPath, err := defaultEquipPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	equipFile, err := equip.Load(equipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "formic: %v\n", err)
		return 1
	}
	result := inventoryResult{
		Providers: inventoryProviders(discoveredCatalog, equipFile),
		Agents:    probeComponents(inventoryAgentNames),
		Runtimes:  probeComponents(inventoryRuntimeNames),
		Terminal:  inventoryTerminalReport(),
	}
	if jsonOutput {
		printJSON(result)
		return 0
	}
	for _, provider := range result.Providers {
		fmt.Printf("provider %s.%s chosen=%s usable=%v probe=%s\n", provider.Class, provider.Name, provider.Chosen, provider.Usable, provider.Probe)
	}
	for _, agent := range result.Agents {
		fmt.Printf("agent %s present=%v\n", agent.Name, agent.Present)
	}
	for _, runtimeComponent := range result.Runtimes {
		fmt.Printf("runtime %s present=%v\n", runtimeComponent.Name, runtimeComponent.Present)
	}
	fmt.Printf("terminal detected=%v name=%s routes=%s\n", result.Terminal.Detected, result.Terminal.Name, formatTerminalRoutes(result.Terminal.Routes))
	return 0
}

func formatTerminalRoutes(routes []inventoryTerminalRoute) string {
	rendered := make([]string, 0, len(routes))
	for _, route := range routes {
		rendered = append(rendered, fmt.Sprintf("%s(verified=%v)", route.Route, route.Verified))
	}
	return strings.Join(rendered, ",")
}

// inventoryProviders lists every provider name any admitted tool lists in a gate need. Car 7
// makes no network or vendor probe of whether a provider actually works — `usable` here means
// only "the equip file has chosen it, or at least one tool declares it," which is why probe is
// reported as "declared": a deeper, live capability probe is future work.
func inventoryProviders(discoveredCatalog discovery.Catalog, equipFile formats.Equip) []inventoryProvider {
	type providerKey struct{ class, name string }
	seen := make(map[providerKey]bool)
	var providers []inventoryProvider
	for _, tool := range discoveredCatalog.Tools {
		for _, need := range tool.Manifest.Needs {
			class, _, names, ok := equip.GateDetails(need)
			if !ok {
				continue
			}
			chosen := equipFile.Providers["gate."+class]
			for _, name := range names {
				key := providerKey{class, name}
				if seen[key] {
					continue
				}
				seen[key] = true
				providers = append(providers, inventoryProvider{
					Class: class, Name: name, Chosen: chosen,
					// Every entry here is already "listed by at least one tool" by
					// construction (that is how it entered this loop) — car 7's own
					// "usable" definition (§ inventory), not a live capability check.
					Usable: true,
					Probe:  "declared",
				})
			}
		}
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Class != providers[j].Class {
			return providers[i].Class < providers[j].Class
		}
		return providers[i].Name < providers[j].Name
	})
	return providers
}

func probeComponents(names []string) []inventoryComponent {
	components := make([]inventoryComponent, 0, len(names))
	for _, name := range names {
		components = append(components, probeComponent(name))
	}
	return components
}

func probeComponent(name string) inventoryComponent {
	if _, err := exec.LookPath(name); err != nil {
		return inventoryComponent{Name: name, Present: false}
	}
	return inventoryComponent{Name: name, Present: true, Version: probeVersion(name)}
}

// probeVersion runs `<name> --version`, bounded to inventoryVersionProbeTimeout: a probe that
// hangs must never hang `formic inventory` itself. A failure or timeout reports version=null
// rather than failing the whole verb — one slow/broken CLI must not take the others down.
func probeVersion(name string) *string {
	ctx, cancel := context.WithTimeout(context.Background(), inventoryVersionProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, "--version").CombinedOutput()
	if err != nil {
		return nil
	}
	version := strings.TrimSpace(string(output))
	return &version
}

// inventoryTerminalReport reuses internal/launch's existing terminal detection (never adds a
// terminal row of its own). foreground is always an available, verified route; pane's verified
// status instead follows pane.CanHostPane() — the SAME boolean internal/ui's mode chooser
// consults (paneUnavailableReason) to decide whether to offer pane at all (founder ruling: the
// pane route stays refused on Windows). Reporting pane verified=true on a
// platform where the deck itself refuses to offer it would claim a capability that does not
// exist there. tab is added only when a host terminal is actually detected, since
// NewTabCommand supports exactly the terminals DetectTerminal can return; split (N7) is added
// only when that same detected terminal also has a split command (HasSplitCommand). tab/split
// each carry whether they have actually been hand-exercised against the real terminal client
// (ruling 1, verifiedTerminalRoutes).
func inventoryTerminalReport() inventoryTerminal {
	terminal, detected := launch.DetectTerminal(os.Environ())
	routes := []inventoryTerminalRoute{
		{Route: "foreground", Verified: true},
		{Route: "pane", Verified: pane.CanHostPane()},
	}
	if detected {
		routes = append(routes, inventoryTerminalRoute{Route: "tab", Verified: verifiedTerminalRoutes[terminalRoute{terminal, "tab"}]})
		if launch.HasSplitCommand(terminal) {
			routes = append(routes, inventoryTerminalRoute{Route: "split", Verified: verifiedTerminalRoutes[terminalRoute{terminal, "split"}]})
		}
	}
	return inventoryTerminal{Detected: detected, Name: string(terminal), Routes: routes}
}

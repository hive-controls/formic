package launch

import (
	"context"
	"fmt"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
)

// NeedsRefused is returned by Prepare when a tool's needs[] are not all met against the
// machine's equip file — the resolution step §13 commits to "before every launch," so every
// caller of Prepare (the CLI verb and the deck) gets it, not just one. No run row is written
// and no process is started when this error is returned.
type NeedsRefused struct {
	Tool  string
	Needs []equip.NeedStatus
	Hint  string
}

func (refused *NeedsRefused) Error() string {
	return fmt.Sprintf("%s has unmet needs (hint: %s)", refused.Tool, refused.Hint)
}

// ResolveNeedStatuses loads the machine's equip file and resolves tool's merged needs against
// it, returning every status (met or not) — the one place that loads the equip file and calls
// equip.Resolve, shared by resolveNeedsForLaunch below and internal/mission.Preflight's own
// unmet-need pre-flight check.
func ResolveNeedStatuses(tool discovery.Tool, environment []string) ([]equip.NeedStatus, error) {
	return ResolveNeedStatusesWith(tool, environment, nil)
}

// ResolveNeedStatusesWith is ResolveNeedStatuses with a service checker (see
// CatalogServiceChecker); nil leaves a service need unchecked, which never blocks.
func ResolveNeedStatusesWith(tool discovery.Tool, environment []string, services equip.ServiceChecker) ([]equip.NeedStatus, error) {
	userConfigRoot, err := discovery.DefaultUserConfigRoot()
	if err != nil {
		return nil, err
	}
	equipFile, err := equip.Load(equip.DefaultPath(userConfigRoot))
	if err != nil {
		return nil, err
	}
	return equip.ResolveWith(tool.Manifest.Needs, tool.Manifest.Env, equipFile, environment, services), nil
}

// resolveNeedsForLaunch resolves tool's merged needs against the machine's equip file. On any
// unmet need it returns a *NeedsRefused naming every one of them, never just the first. On
// success it returns the <env>=<provider> environment entries every MET gate need's chosen
// provider delivers (§13: "the declared env[] entry that receives the chosen provider's name")
// — Prepare merges these into the child environment before its own env[] scoping runs.
func resolveNeedsForLaunch(tool discovery.Tool, environment []string, services equip.ServiceChecker) ([]string, error) {
	statuses, err := ResolveNeedStatusesWith(tool, environment, services)
	if err != nil {
		return nil, err
	}
	var unmet []equip.NeedStatus
	for _, status := range statuses {
		if status.Blocks() {
			unmet = append(unmet, status)
		}
	}
	if len(unmet) > 0 {
		return nil, &NeedsRefused{Tool: tool.Name, Needs: unmet, Hint: equip.Hint(unmet[0].Need)}
	}
	var gateAssignments []string
	for _, status := range statuses {
		_, env, _, ok := equip.GateDetails(status.Need)
		if !ok || env == "" {
			continue
		}
		gateAssignments = append(gateAssignments, env+"="+status.Reference)
	}
	return gateAssignments, nil
}

// CatalogServiceChecker answers a service need by probing the named tool from the catalog.
// A tool that is not in the catalog or declares no probe reports unknown, so the need is unmet.
func CatalogServiceChecker(ctx context.Context, catalog discovery.Catalog, repositoryRoot string) equip.ServiceChecker {
	return func(toolName string) (string, string) {
		target, found := catalog.Find(toolName)
		if !found {
			return string(ProbeUnknown), fmt.Sprintf("service tool %s is not in the catalog", toolName)
		}
		result := Probe(ctx, target, repositoryRoot)
		return string(result.State), serviceReason(toolName, result)
	}
}

// serviceReason is the one sentence both the CLI's catalog checker and the deck's held results
// use for a service that is not up, so the two surfaces refuse in the same words.
func serviceReason(toolName string, result ProbeResult) string {
	return fmt.Sprintf("service %s is %s (%s)", toolName, result.State, result.Detail)
}

// ServiceCheckerFrom answers a service need from probe results gathered earlier (the deck holds
// them across its off-thread pre-launch probe). It never probes: a service whose tool is
// missing from results is unknown, so its need is unmet.
func ServiceCheckerFrom(results map[string]ProbeResult) equip.ServiceChecker {
	return func(toolName string) (string, string) {
		result, held := results[toolName]
		if !held {
			return string(ProbeUnknown), fmt.Sprintf("service tool %s was not probed", toolName)
		}
		return string(result.State), serviceReason(toolName, result)
	}
}

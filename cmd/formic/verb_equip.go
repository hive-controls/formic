package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
)

type equipListResult struct {
	Needs []equip.ArmoryNeedStatus `json:"needs"`
}

// positionalArguments returns every argument that is not a recognized flag, in order.
func positionalArguments(arguments []string) []string {
	positional := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if argument != "--json" {
			positional = append(positional, argument)
		}
	}
	return positional
}

// runEquip is `formic equip` (§15): with no arguments, every need across the whole armory,
// grouped, with which tools declare it and whether it is met. With arguments, it records a
// secret reference (`equip secret <NAME> <reference>`) or a provider choice
// (`equip provider <kind>.<class> <provider>`) — ORCHESTRATOR RULING 2 (founder-confirmed
// 2026-09-23) for this argv shape.
func runEquip(arguments []string) int {
	jsonOutput := hasJSONFlag(arguments)
	positional := positionalArguments(arguments)
	switch {
	case len(positional) == 0:
		return runEquipList(jsonOutput)
	case positional[0] == "secret" && len(positional) == 3:
		return runEquipSecret(jsonOutput, positional[1], positional[2])
	case positional[0] == "provider" && len(positional) == 3:
		return runEquipProvider(jsonOutput, positional[1], positional[2])
	default:
		fmt.Fprintln(os.Stderr, "usage: formic equip [secret <NAME> <reference> | provider <kind>.<class> <provider>] [--json]")
		return 2
	}
}

func runEquipList(jsonOutput bool) int {
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
	result := equipListResult{Needs: equip.GroupNeedStatuses(discoveredCatalog.Tools, equipFile, os.Environ())}
	if jsonOutput {
		printJSON(result)
		return 0
	}
	for _, status := range result.Needs {
		fmt.Printf("%+v met=%v state=%s tools=%s\n", status.Need, status.Met, status.State, strings.Join(status.Tools, ","))
	}
	return 0
}

func runEquipSecret(jsonOutput bool, name, reference string) int {
	equipPath, err := defaultEquipPath()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	document, err := equip.SetSecret(equipPath, name, reference)
	if err != nil {
		if errors.Is(err, equip.ErrLiteralValue) {
			return refuse(jsonOutput, "refused: "+equip.ErrLiteralValue.Error())
		}
		return refuse(jsonOutput, err.Error())
	}
	if jsonOutput {
		printJSON(document)
	} else {
		fmt.Printf("secrets.%s = %s\n", name, reference)
	}
	return 0
}

func runEquipProvider(jsonOutput bool, key, provider string) int {
	_, discoveredCatalog, err := loadCatalog()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	accepted := acceptedProvidersForKey(discoveredCatalog, key)
	if !containsString(accepted, provider) {
		return refuse(jsonOutput, fmt.Sprintf("no admitted tool lists %q for %s; accepted providers: %s", provider, key, strings.Join(accepted, ", ")))
	}
	equipPath, err := defaultEquipPath()
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	document, err := equip.SetProvider(equipPath, key, provider)
	if err != nil {
		return refuse(jsonOutput, err.Error())
	}
	if jsonOutput {
		printJSON(document)
	} else {
		fmt.Printf("providers.%s = %s\n", key, provider)
	}
	return 0
}

// acceptedProvidersForKey collects every provider name any admitted tool lists for key's
// gate class (§13: "formic refuses a choice the tool does not list").
func acceptedProvidersForKey(discoveredCatalog discovery.Catalog, key string) []string {
	class := strings.TrimPrefix(key, "gate.")
	var accepted []string
	for _, tool := range discoveredCatalog.Tools {
		for _, need := range tool.Manifest.Needs {
			needClass, _, providers, ok := equip.GateDetails(need)
			if !ok || needClass != class {
				continue
			}
			accepted = append(accepted, providers...)
		}
	}
	return accepted
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

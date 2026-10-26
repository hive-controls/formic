package equip

import (
	"sort"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
)

// ArmoryNeedStatus is one need, grouped across every admitted tool that declares it — the shape
// both `formic equip`'s no-argument form and the deck's read-only equip view (N9, C2) share.
type ArmoryNeedStatus struct {
	Need formats.Need `json:"need"`
	Met  bool         `json:"met"`
	// State is the worst state across the declaring tools: unmet, else unchecked, else met.
	State string   `json:"state"`
	Tools []string `json:"tools"`
}

// GroupNeedStatuses resolves every tool's merged needs against equipFile and groups them by
// identity (NeedKey): a need is reported met only when it resolves met for EVERY tool that
// declares it (a gate need's "met" can differ per tool, since it also checks that tool's own
// env[] declaration). Hoisted from cmd/formic's own buildEquipListResult (§15) so the deck's
// read-only equip view (C2) can share the exact same computation.
func GroupNeedStatuses(tools []discovery.Tool, equipFile formats.Equip, environment []string) []ArmoryNeedStatus {
	order := make([]string, 0)
	grouped := make(map[string]*ArmoryNeedStatus)
	for _, tool := range tools {
		statuses := Resolve(tool.Manifest.Needs, tool.Manifest.Env, equipFile, environment)
		for _, status := range statuses {
			key := NeedKey(status.Need)
			if key == "" {
				continue
			}
			entry, exists := grouped[key]
			if !exists {
				entry = &ArmoryNeedStatus{Need: status.Need, Met: true, State: StateMet}
				grouped[key] = entry
				order = append(order, key)
			}
			entry.Tools = append(entry.Tools, tool.Name)
			entry.State = worseState(entry.State, status.State)
			entry.Met = entry.State == StateMet
		}
	}
	sort.Strings(order)
	needs := make([]ArmoryNeedStatus, 0, len(order))
	for _, key := range order {
		needs = append(needs, *grouped[key])
	}
	return needs
}

func worseState(current, candidate string) string {
	rank := map[string]int{StateMet: 0, StateUnchecked: 1, StateUnmet: 2}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}

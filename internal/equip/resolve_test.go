package equip_test

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
)

func need(t *testing.T, jsonText string) formats.Need {
	t.Helper()
	var decoded formats.Need
	if err := json.Unmarshal([]byte(jsonText), &decoded); err != nil {
		t.Fatalf("decode need fixture: %v", err)
	}
	return decoded
}

// TestResolveSecretMetWhenReferencedEnvVarIsSet proves the env.NAME happy path: a secret is
// met only when BOTH the equip file names a reference AND the launcher's environment carries
// that variable — presence only, never the value.
func TestResolveSecretMetWhenReferencedEnvVarIsSet(t *testing.T) {
	secretNeed := need(t, `{"kind":"secret","name":"NUMVERIFY_API_KEY"}`)
	equipFile := formats.Equip{Equip: 1, Secrets: formats.EquipSecrets{"NUMVERIFY_API_KEY": "env.NUMVERIFY_API_KEY"}}

	statuses := equip.Resolve([]formats.Need{secretNeed}, nil, equipFile, []string{"NUMVERIFY_API_KEY=irrelevant-value"})
	if len(statuses) != 1 || !statuses[0].Met {
		t.Fatalf("statuses = %+v, want the secret need met", statuses)
	}
	if statuses[0].Reference != "env.NUMVERIFY_API_KEY" {
		t.Fatalf("statuses[0].Reference = %q, want the reference echoed", statuses[0].Reference)
	}
}

// TestResolveSecretUnmetWhenEnvVarIsUnset is the other half: the reference is recorded, but
// the launcher's own environment does not carry the variable.
func TestResolveSecretUnmetWhenEnvVarIsUnset(t *testing.T) {
	secretNeed := need(t, `{"kind":"secret","name":"NUMVERIFY_API_KEY"}`)
	equipFile := formats.Equip{Equip: 1, Secrets: formats.EquipSecrets{"NUMVERIFY_API_KEY": "env.NUMVERIFY_API_KEY"}}

	statuses := equip.Resolve([]formats.Need{secretNeed}, nil, equipFile, nil)
	if len(statuses) != 1 || statuses[0].Met {
		t.Fatalf("statuses = %+v, want the secret need unmet", statuses)
	}
}

// TestResolveSecretUnmetWhenNoReferenceRecorded proves a secret with no equip-file entry at
// all is unmet, named by reason rather than crashing.
func TestResolveSecretUnmetWhenNoReferenceRecorded(t *testing.T) {
	secretNeed := need(t, `{"kind":"secret","name":"NUMVERIFY_API_KEY"}`)
	statuses := equip.Resolve([]formats.Need{secretNeed}, nil, formats.Equip{Equip: 1}, nil)
	if len(statuses) != 1 || statuses[0].Met || statuses[0].Reason == "" {
		t.Fatalf("statuses = %+v, want an unmet status naming why", statuses)
	}
}

// TestResolveSecretReservedFormsNeverMetButNeverRejected proves §13's reserved-forms rule:
// keychain:/file: references are accepted (never treated as unknown) but always report unmet
// with the exact "reference form not yet resolved" reason.
func TestResolveSecretReservedFormsNeverMetButNeverRejected(t *testing.T) {
	secretNeed := need(t, `{"kind":"secret","name":"VAULT_KEY"}`)
	equipFile := formats.Equip{Equip: 1, Secrets: formats.EquipSecrets{"VAULT_KEY": "keychain:VAULT_KEY"}}

	statuses := equip.Resolve([]formats.Need{secretNeed}, nil, equipFile, nil)
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v, want exactly one", statuses)
	}
	if statuses[0].Met {
		t.Fatal("a reserved keychain: reference must never report met (not resolved by the launcher yet)")
	}
	if statuses[0].Reason != "reference form not yet resolved" {
		t.Fatalf("statuses[0].Reason = %q, want the exact reserved-form reason", statuses[0].Reason)
	}
	if statuses[0].Reference != "keychain:VAULT_KEY" {
		t.Fatalf("statuses[0].Reference = %q, want the reserved reference echoed, not dropped", statuses[0].Reference)
	}
}

// TestResolveGateMetWhenChosenProviderIsAcceptedAndEnvIsDeclared proves the gate happy path:
// the equip file's choice, the tool's own accepted providers list, and the manifest's env[]
// declaration must all agree.
func TestResolveGateMetWhenChosenProviderIsAcceptedAndEnvIsDeclared(t *testing.T) {
	gateNeed := need(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["solari","local","cdp"]}`)
	equipFile := formats.Equip{Equip: 1, Providers: formats.EquipProviders{"gate.browser": "solari"}}
	declaredEnv := []formats.ToolManifestEnvElem{{Name: "E2E_DOCTOR_GATE"}}

	statuses := equip.Resolve([]formats.Need{gateNeed}, declaredEnv, equipFile, nil)
	if len(statuses) != 1 || !statuses[0].Met {
		t.Fatalf("statuses = %+v, want the gate need met", statuses)
	}
	if statuses[0].Reference != "solari" {
		t.Fatalf("statuses[0].Reference = %q, want the chosen provider", statuses[0].Reference)
	}
}

// TestResolveGateUnmetWhenChosenProviderIsNotAcceptedByThisTool proves formic refuses a
// choice the tool does not list (§13): the equip file's choice for the class exists, but this
// tool's own providers[] does not include it.
func TestResolveGateUnmetWhenChosenProviderIsNotAcceptedByThisTool(t *testing.T) {
	gateNeed := need(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["local","cdp"]}`)
	equipFile := formats.Equip{Equip: 1, Providers: formats.EquipProviders{"gate.browser": "solari"}}
	declaredEnv := []formats.ToolManifestEnvElem{{Name: "E2E_DOCTOR_GATE"}}

	statuses := equip.Resolve([]formats.Need{gateNeed}, declaredEnv, equipFile, nil)
	if len(statuses) != 1 || statuses[0].Met {
		t.Fatalf("statuses = %+v, want the gate need unmet (chosen provider not in this tool's list)", statuses)
	}
}

// TestResolveGateUnmetWhenManifestDoesNotDeclareTheEnvName proves the other half of §13's
// gate rule: even a chosen, accepted provider is unmet when the manifest never declares the
// env[] entry that would receive it.
func TestResolveGateUnmetWhenManifestDoesNotDeclareTheEnvName(t *testing.T) {
	gateNeed := need(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["solari"]}`)
	equipFile := formats.Equip{Equip: 1, Providers: formats.EquipProviders{"gate.browser": "solari"}}

	statuses := equip.Resolve([]formats.Need{gateNeed}, nil, equipFile, nil)
	if len(statuses) != 1 || statuses[0].Met {
		t.Fatalf("statuses = %+v, want the gate need unmet (env[] does not declare E2E_DOCTOR_GATE)", statuses)
	}
}

// TestResolveGateUnmetWhenNoProviderChosen proves a class with no equip-file entry at all is
// unmet.
func TestResolveGateUnmetWhenNoProviderChosen(t *testing.T) {
	gateNeed := need(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["solari"]}`)
	statuses := equip.Resolve([]formats.Need{gateNeed}, nil, formats.Equip{Equip: 1}, nil)
	if len(statuses) != 1 || statuses[0].Met {
		t.Fatalf("statuses = %+v, want the gate need unmet (nothing equipped)", statuses)
	}
}

// TestResolveRuntimeMetWhenOnPath and its unmet twin prove the runtime kind resolves against
// exec.LookPath directly — a real, present-on-every-CI executable ("go" — this suite's own
// toolchain) for the met case, a name that cannot exist for the unmet case.
func TestResolveRuntimeMetWhenOnPath(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH in this environment")
	}
	runtimeNeed := need(t, `{"kind":"runtime","name":"go"}`)
	statuses := equip.Resolve([]formats.Need{runtimeNeed}, nil, formats.Equip{Equip: 1}, nil)
	if len(statuses) != 1 || !statuses[0].Met {
		t.Fatalf("statuses = %+v, want the runtime need met", statuses)
	}
}

func TestResolveRuntimeUnmetWhenNotOnPath(t *testing.T) {
	runtimeNeed := need(t, `{"kind":"runtime","name":"definitely-not-a-real-executable-formic-car7"}`)
	statuses := equip.Resolve([]formats.Need{runtimeNeed}, nil, formats.Equip{Equip: 1}, nil)
	if len(statuses) != 1 || statuses[0].Met {
		t.Fatalf("statuses = %+v, want the runtime need unmet", statuses)
	}
}

// TestResolveNetworkIsUncheckedWithAnHonestReason: nothing enforces a network policy, so the
// need is reported unchecked (never met, never blocking) with a reason saying so.
func TestResolveNetworkIsUncheckedWithAnHonestReason(t *testing.T) {
	networkNeed := need(t, `{"kind":"network","policy":"privacy-vpn"}`)
	statuses := equip.Resolve([]formats.Need{networkNeed}, nil, formats.Equip{Equip: 1}, nil)
	if len(statuses) != 1 || statuses[0].State != equip.StateUnchecked || statuses[0].Blocks() {
		t.Fatalf("statuses = %+v, want the network need unchecked and not blocking", statuses)
	}
	if statuses[0].Reason != "policy not checked by the launcher" {
		t.Fatalf("statuses[0].Reason = %q, want the exact honest reason", statuses[0].Reason)
	}
}

// TestNeedKeyGroupsIdenticalNeedsAcrossTools proves NeedKey's grouping identity, used by
// `formic equip`'s armory-wide listing: two secret needs naming the same secret share a key;
// a differently-named one does not.
func TestNeedKeyGroupsIdenticalNeedsAcrossTools(t *testing.T) {
	first := need(t, `{"kind":"secret","name":"NUMVERIFY_API_KEY"}`)
	second := need(t, `{"kind":"secret","name":"NUMVERIFY_API_KEY"}`)
	different := need(t, `{"kind":"secret","name":"OTHER_KEY"}`)
	if equip.NeedKey(first) != equip.NeedKey(second) {
		t.Fatalf("NeedKey(first)=%q NeedKey(second)=%q, want the same key for the same secret", equip.NeedKey(first), equip.NeedKey(second))
	}
	if equip.NeedKey(first) == equip.NeedKey(different) {
		t.Fatal("NeedKey should not group two different secrets together")
	}
}

// TestGateDetailsDecodesAndRejectsOtherKinds proves GateDetails' contract directly.
func TestGateDetailsDecodesAndRejectsOtherKinds(t *testing.T) {
	gateNeed := need(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["solari","local"]}`)
	class, env, providers, ok := equip.GateDetails(gateNeed)
	if !ok || class != "browser" || env != "E2E_DOCTOR_GATE" || len(providers) != 2 {
		t.Fatalf("GateDetails = (%q, %q, %v, %v), want the decoded gate fields", class, env, providers, ok)
	}
	secretNeed := need(t, `{"kind":"secret","name":"X"}`)
	if _, _, _, ok := equip.GateDetails(secretNeed); ok {
		t.Fatal("GateDetails should report ok=false for a non-gate need")
	}
}

// TestHintNamesTheRightVerbFormPerKind proves Hint's contract for the two kinds `launch`'s
// refusal actually needs a hint for.
func TestHintNamesTheRightVerbFormPerKind(t *testing.T) {
	secretHint := equip.Hint(need(t, `{"kind":"secret","name":"NUMVERIFY_API_KEY"}`))
	if secretHint != "formic equip secret NUMVERIFY_API_KEY env.NUMVERIFY_API_KEY" {
		t.Fatalf("secret hint = %q, want the equip-secret invocation naming the need", secretHint)
	}
	gateHint := equip.Hint(need(t, `{"kind":"gate","class":"browser","env":"E2E_DOCTOR_GATE","providers":["solari"]}`))
	if gateHint != "formic equip provider gate.browser <provider>" {
		t.Fatalf("gate hint = %q, want the equip-provider invocation naming the class", gateHint)
	}
}

func TestResolveNetworkNeedIsUncheckedNotMet(t *testing.T) {
	statuses := equip.Resolve([]formats.Need{map[string]any{"kind": "network", "policy": "offline"}}, nil, formats.Equip{}, nil)
	if len(statuses) != 1 || statuses[0].State != equip.StateUnchecked || statuses[0].Met || statuses[0].Blocks() {
		t.Fatalf("statuses = %+v, want unchecked, not met, not blocking", statuses)
	}
}

func TestResolveServiceNeedStates(t *testing.T) {
	needs := []formats.Need{map[string]any{"kind": "service", "tool": "docker-desktop", "action": "start"}}
	cases := []struct {
		name    string
		checker equip.ServiceChecker
		state   string
	}{
		{"up", func(string) (string, string) { return "up", "" }, equip.StateMet},
		{"down", func(string) (string, string) { return "down", "" }, equip.StateUnmet},
		{"unknown", func(string) (string, string) { return "unknown", "" }, equip.StateUnmet},
		{"nil checker", nil, equip.StateUnchecked},
	}
	for _, tc := range cases {
		statuses := equip.ResolveWith(needs, nil, formats.Equip{}, nil, tc.checker)
		if statuses[0].State != tc.state {
			t.Errorf("%s: state = %q want %q", tc.name, statuses[0].State, tc.state)
		}
	}
	if hint := equip.Hint(needs[0]); hint != "formic launch docker-desktop start" {
		t.Errorf("hint = %q", hint)
	}
}

func TestServiceDetailsDecodesToolAndActionAndRejectsOtherKinds(t *testing.T) {
	tool, action, ok := equip.ServiceDetails(need(t, `{"kind":"service","tool":"docker-desktop","action":"start"}`))
	if !ok || tool != "docker-desktop" || action != "start" {
		t.Fatalf("ServiceDetails = (%q, %q, %v), want (docker-desktop, start, true)", tool, action, ok)
	}
	tool, action, ok = equip.ServiceDetails(need(t, `{"kind":"service","tool":"llama-swap"}`))
	if !ok || tool != "llama-swap" || action != "" {
		t.Fatalf("ServiceDetails without action = (%q, %q, %v)", tool, action, ok)
	}
	if _, _, ok := equip.ServiceDetails(need(t, `{"kind":"secret","name":"TOKEN"}`)); ok {
		t.Fatal("a secret need must not decode as a service need")
	}
	if _, _, ok := equip.ServiceDetails(need(t, `{"kind":"service"}`)); ok {
		t.Fatal("a service need with no tool must not decode")
	}
}

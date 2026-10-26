package equip

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/hive-controls/formic/internal/formats"
)

// NeedStatus is one need's resolution against the equip file — the shape both `describe` and
// `launch`'s refusal report (§13, §15).
type NeedStatus struct {
	Need formats.Need `json:"need"`
	Met  bool         `json:"met"`
	// State is met, unmet, or unchecked (a need the launcher does not evaluate here: a network
	// policy, or a service need resolved without a checker). Met is true only for met; only
	// unmet blocks (Blocks).
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// Need states.
const (
	StateMet       = "met"
	StateUnmet     = "unmet"
	StateUnchecked = "unchecked"
)

// Blocks reports whether this need refuses a launch: only unmet does.
func (status NeedStatus) Blocks() bool { return status.State == StateUnmet }

// ServiceChecker reports a service need's target tool state ("up" or "idle" meets the need) and
// a reason for any other answer. It is injected so equip never imports launch.
type ServiceChecker func(toolName string) (state string, reason string)

type serviceNeed struct {
	Kind   string `json:"kind"`
	Tool   string `json:"tool"`
	Action string `json:"action"`
}

type secretNeed struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type gateNeed struct {
	Kind      string   `json:"kind"`
	Class     string   `json:"class"`
	Env       string   `json:"env"`
	Providers []string `json:"providers"`
}

type runtimeNeed struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type networkNeed struct {
	Kind   string `json:"kind"`
	Policy string `json:"policy"`
}

// Resolve checks each of a tool's merged needs against the machine's equip file, per §13's
// four kinds. declaredEnv is the tool's own manifest env[] list — a gate need's chosen
// provider is only "met" when the manifest also declares the env[] entry that receives it.
// environment is the launcher's own environment, checked for a secret's PRESENCE only — its
// value is never read into a status, log, or error (§13).
func Resolve(needs []formats.Need, declaredEnv []formats.ToolManifestEnvElem, equipFile formats.Equip, environment []string) []NeedStatus {
	return ResolveWith(needs, declaredEnv, equipFile, environment, nil)
}

// ResolveWith is Resolve with a service checker; a nil checker leaves service needs unchecked.
func ResolveWith(needs []formats.Need, declaredEnv []formats.ToolManifestEnvElem, equipFile formats.Equip, environment []string, services ServiceChecker) []NeedStatus {
	statuses := make([]NeedStatus, 0, len(needs))
	for _, need := range needs {
		status := resolveOne(need, declaredEnv, equipFile, environment, services)
		if status.State == "" {
			status.State = StateUnmet
			if status.Met {
				status.State = StateMet
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func resolveOne(need formats.Need, declaredEnv []formats.ToolManifestEnvElem, equipFile formats.Equip, environment []string, services ServiceChecker) NeedStatus {
	kind, ok := needKind(need)
	if !ok {
		return NeedStatus{Need: need, Met: false, Reason: "need has no recognizable kind"}
	}
	switch kind {
	case "secret":
		return resolveSecret(need, equipFile, environment)
	case "gate":
		return resolveGate(need, declaredEnv, equipFile)
	case "runtime":
		return resolveRuntime(need)
	case "network":
		return resolveNetwork(need)
	case "service":
		return resolveService(need, services)
	default:
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("unrecognized need kind %q", kind)}
	}
}

func resolveSecret(need formats.Need, equipFile formats.Equip, environment []string) NeedStatus {
	var decoded secretNeed
	if decodeNeed(need, &decoded) != nil {
		return NeedStatus{Need: need, Met: false, Reason: "malformed secret need"}
	}
	reference, ok := equipFile.Secrets[decoded.Name]
	if !ok {
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("no secret equipped for %s", decoded.Name)}
	}
	if envName, isEnvForm := strings.CutPrefix(reference, "env."); isEnvForm {
		if environmentHasKey(environment, envName) {
			return NeedStatus{Need: need, Met: true, Reference: reference}
		}
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("environment variable %s is not set", envName), Reference: reference}
	}
	// keychain:NAME and file:<path>#key are reserved reference forms (§13): accepted and
	// round-tripped by the schema, but Resolve does not read them yet — never rejected as
	// unknown, always reported unmet with this exact reason.
	return NeedStatus{Need: need, Met: false, Reason: "reference form not yet resolved", Reference: reference}
}

func environmentHasKey(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func resolveGate(need formats.Need, declaredEnv []formats.ToolManifestEnvElem, equipFile formats.Equip) NeedStatus {
	var decoded gateNeed
	if decodeNeed(need, &decoded) != nil {
		return NeedStatus{Need: need, Met: false, Reason: "malformed gate need"}
	}
	providerKey := "gate." + decoded.Class
	chosen, ok := equipFile.Providers[providerKey]
	if !ok {
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("no provider equipped for %s", providerKey)}
	}
	if !containsString(decoded.Providers, chosen) {
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("equipped provider %s is not accepted by this tool for %s", chosen, providerKey), Reference: chosen}
	}
	if !declaresEnvName(declaredEnv, decoded.Env) {
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("%s is equipped as %s but env[] does not declare %s", providerKey, chosen, decoded.Env), Reference: chosen}
	}
	return NeedStatus{Need: need, Met: true, Reference: chosen}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func declaresEnvName(declaredEnv []formats.ToolManifestEnvElem, name string) bool {
	for _, entry := range declaredEnv {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func resolveRuntime(need formats.Need) NeedStatus {
	var decoded runtimeNeed
	if decodeNeed(need, &decoded) != nil {
		return NeedStatus{Need: need, Met: false, Reason: "malformed runtime need"}
	}
	// exec.LookPath is PATHEXT-aware on Windows in the standard library itself — no separate
	// Windows branch is needed here (unlike internal/launch's resolveExecutable, which must
	// resolve against an explicit environment slice rather than the calling process's own).
	path, err := exec.LookPath(decoded.Name)
	if err != nil {
		return NeedStatus{Need: need, Met: false, Reason: fmt.Sprintf("runtime %s not found on PATH", decoded.Name)}
	}
	return NeedStatus{Need: need, Met: true, Reference: path}
}

func resolveNetwork(need formats.Need) NeedStatus {
	var decoded networkNeed
	if decodeNeed(need, &decoded) != nil {
		return NeedStatus{Need: need, Met: false, Reason: "malformed network need"}
	}
	// No enforcement exists for a network policy: unchecked, never blocking (§22 e).
	return NeedStatus{Need: need, State: StateUnchecked, Reason: "policy not checked by the launcher", Reference: decoded.Policy}
}

func resolveService(need formats.Need, services ServiceChecker) NeedStatus {
	var decoded serviceNeed
	if decodeNeed(need, &decoded) != nil || decoded.Tool == "" {
		return NeedStatus{Need: need, Met: false, Reason: "malformed service need"}
	}
	if services == nil {
		return NeedStatus{Need: need, State: StateUnchecked, Reason: "service state not probed here", Reference: decoded.Tool}
	}
	state, reason := services(decoded.Tool)
	if state == "up" || state == "idle" {
		return NeedStatus{Need: need, Met: true, State: StateMet, Reference: decoded.Tool}
	}
	if reason == "" {
		reason = fmt.Sprintf("service %s is %s", decoded.Tool, state)
	}
	return NeedStatus{Need: need, State: StateUnmet, Reason: reason, Reference: decoded.Tool}
}

// needKind reads a need's "kind" discriminator generically, since formats.Need's concrete
// shape is not exported — the same round-trip-through-encoding/json pattern
// internal/discovery/catalog.go's own needKind uses (duplicated here on purpose: this
// package must not import internal/discovery, and the reverse dependency already runs the
// other way).
func needKind(need formats.Need) (string, bool) {
	data, err := json.Marshal(need)
	if err != nil {
		return "", false
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.Kind == "" {
		return "", false
	}
	return probe.Kind, true
}

func decodeNeed(need formats.Need, target any) error {
	data, err := json.Marshal(need)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// NeedKey returns a stable identity for a need — its kind plus whatever names it (a secret's
// name, a gate's class, a runtime's name, a network policy) — so the same need declared by
// more than one tool groups together. An empty string means the need could not be decoded.
func NeedKey(need formats.Need) string {
	kind, ok := needKind(need)
	if !ok {
		return ""
	}
	switch kind {
	case "secret":
		var decoded secretNeed
		if decodeNeed(need, &decoded) != nil {
			return ""
		}
		return "secret:" + decoded.Name
	case "gate":
		var decoded gateNeed
		if decodeNeed(need, &decoded) != nil {
			return ""
		}
		return "gate:" + decoded.Class
	case "runtime":
		var decoded runtimeNeed
		if decodeNeed(need, &decoded) != nil {
			return ""
		}
		return "runtime:" + decoded.Name
	case "network":
		var decoded networkNeed
		if decodeNeed(need, &decoded) != nil {
			return ""
		}
		return "network:" + decoded.Policy
	case "service":
		var decoded serviceNeed
		if decodeNeed(need, &decoded) != nil {
			return ""
		}
		return "service:" + decoded.Tool
	default:
		return ""
	}
}

// SecretDetails decodes a secret need's declared name. ok is false for every other need kind.
func SecretDetails(need formats.Need) (name string, ok bool) {
	kind, matched := needKind(need)
	if !matched || kind != "secret" {
		return "", false
	}
	var decoded secretNeed
	if decodeNeed(need, &decoded) != nil {
		return "", false
	}
	return decoded.Name, true
}

// ServiceDetails decodes a service need's target tool and the action that starts it (empty when
// the need names none). ok is false for every other need kind and for a service need with no tool.
func ServiceDetails(need formats.Need) (tool, action string, ok bool) {
	kind, matched := needKind(need)
	if !matched || kind != "service" {
		return "", "", false
	}
	var decoded serviceNeed
	if decodeNeed(need, &decoded) != nil || decoded.Tool == "" {
		return "", "", false
	}
	return decoded.Tool, decoded.Action, true
}

// GateDetails decodes a gate need's class, its declared env[] target, and the provider names
// it accepts. ok is false for every other need kind.
func GateDetails(need formats.Need) (class, env string, providers []string, ok bool) {
	kind, matched := needKind(need)
	if !matched || kind != "gate" {
		return "", "", nil, false
	}
	var decoded gateNeed
	if decodeNeed(need, &decoded) != nil {
		return "", "", nil, false
	}
	return decoded.Class, decoded.Env, decoded.Providers, true
}

// Hint suggests the `formic equip` invocation that would satisfy an unmet need (§13's
// refusal "hint" field) — the shape of the command only, never a value read from the need.
func Hint(need formats.Need) string {
	kind, ok := needKind(need)
	if !ok {
		return "formic equip"
	}
	switch kind {
	case "secret":
		var decoded secretNeed
		if decodeNeed(need, &decoded) == nil && decoded.Name != "" {
			return fmt.Sprintf("formic equip secret %s env.%s", decoded.Name, decoded.Name)
		}
	case "gate":
		var decoded gateNeed
		if decodeNeed(need, &decoded) == nil && decoded.Class != "" {
			return fmt.Sprintf("formic equip provider gate.%s <provider>", decoded.Class)
		}
	case "service":
		var decoded serviceNeed
		if decodeNeed(need, &decoded) == nil && decoded.Tool != "" {
			return strings.TrimSpace("formic launch " + decoded.Tool + " " + decoded.Action)
		}
	}
	return "formic equip"
}

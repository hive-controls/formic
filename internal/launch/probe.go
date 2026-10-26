package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/profile"
)

// ProbeState is a live probe's answer (§22): up, idle (a command probe's idleExit), down, or
// unknown when the probe could not run.
type ProbeState string

const (
	ProbeUp      ProbeState = "up"
	ProbeIdle    ProbeState = "idle"
	ProbeDown    ProbeState = "down"
	ProbeUnknown ProbeState = "unknown"
)

// Answering reports a probe that says the service is there: up, or up and idle.
func (state ProbeState) Answering() bool { return state == ProbeUp || state == ProbeIdle }

// DefaultProbeTimeout is the per-probe bound when the manifest declares none; a declared
// timeout is capped at HealthTimeout.
const DefaultProbeTimeout = 2 * time.Second

// ProbeResult is built fresh on every call and never persisted (§22 b): it carries no header,
// body, environment, or argv beyond what Detail names.
type ProbeResult struct {
	State    ProbeState    `json:"state"`
	Detail   string        `json:"detail,omitempty"`
	Duration time.Duration `json:"durationNs"`
}

// probeTimeoutOverride lets tests shorten the bound; never set outside tests.
var probeTimeoutOverride time.Duration

type probeSpec struct {
	HTTP     string   `json:"http"`
	TCP      string   `json:"tcp"`
	Command  []string `json:"command"`
	Expect   any      `json:"expect"`
	Timeout  string   `json:"timeout"`
	IdleExit int      `json:"idleExit"`
}

func decodeProbe(raw any) (probeSpec, error) {
	var spec probeSpec
	data, err := json.Marshal(raw)
	if err != nil {
		return spec, err
	}
	err = json.Unmarshal(data, &spec)
	return spec, err
}

func probeTimeout(spec probeSpec) time.Duration {
	if probeTimeoutOverride > 0 {
		return probeTimeoutOverride
	}
	timeout := DefaultProbeTimeout
	if declared, err := time.ParseDuration(spec.Timeout); err == nil && declared > 0 {
		timeout = declared
	}
	if timeout > HealthTimeout {
		timeout = HealthTimeout
	}
	return timeout
}

// Probe runs tool's declared live.probe once, bounded by its timeout and by ctx. It returns
// ProbeUnknown when the tool declares no probe or the probe could not run (§22 f). Nothing is
// written anywhere.
func Probe(ctx context.Context, tool discovery.Tool, repositoryRoot string) ProbeResult {
	start := time.Now()
	state, detail := runProbe(ctx, tool, repositoryRoot)
	return ProbeResult{State: state, Detail: detail, Duration: time.Since(start)}
}

func runProbe(ctx context.Context, tool discovery.Tool, repositoryRoot string) (ProbeState, string) {
	if tool.Manifest.Live == nil || tool.Manifest.Live.Probe == nil {
		return ProbeUnknown, "no probe declared"
	}
	spec, err := decodeProbe(tool.Manifest.Live.Probe)
	if err != nil {
		return ProbeUnknown, "malformed probe: " + err.Error()
	}
	root := tool.SourceRoot
	if root == "" {
		root = repositoryRoot
	}
	probeContext, cancel := context.WithTimeout(ctx, probeTimeout(spec))
	defer cancel()
	switch {
	case spec.HTTP != "":
		return probeHTTP(probeContext, spec, replaceRepo(spec.HTTP, root))
	case spec.TCP != "":
		return probeTCP(probeContext, replaceRepo(spec.TCP, root))
	case len(spec.Command) > 0:
		return probeCommand(probeContext, tool, spec, root, repositoryRoot)
	}
	return ProbeUnknown, "probe declares no http, tcp or command"
}

func replaceRepo(value, root string) string {
	return strings.ReplaceAll(value, "{repo}", root)
}

// failureState maps a transport error: a deadline or cancel is unknown, anything else down.
func failureState(probeContext context.Context, err error) (ProbeState, string) {
	if probeContext.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return ProbeUnknown, "probe timed out"
	}
	return ProbeDown, err.Error()
}

func probeHTTP(probeContext context.Context, spec probeSpec, target string) (ProbeState, string) {
	request, err := http.NewRequestWithContext(probeContext, http.MethodGet, target, nil)
	if err != nil {
		return ProbeUnknown, "malformed url: " + err.Error()
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	response, err := client.Do(request)
	if err != nil {
		return failureState(probeContext, err)
	}
	response.Body.Close() // never read (§22 f: no body matching)
	detail := fmt.Sprintf("http %d", response.StatusCode)
	if httpStatusMatches(spec.Expect, response.StatusCode) {
		return ProbeUp, detail
	}
	return ProbeDown, detail
}

// httpStatusMatches applies expect: any | 2xx (default) | an exact status code.
func httpStatusMatches(expect any, status int) bool {
	switch declared := expect.(type) {
	case string:
		if declared == "any" {
			return true
		}
	case float64:
		return status == int(declared)
	}
	return status >= 200 && status <= 299
}

func probeTCP(probeContext context.Context, target string) (ProbeState, string) {
	connection, err := (&net.Dialer{}).DialContext(probeContext, "tcp", target)
	if err != nil {
		return failureState(probeContext, err)
	}
	connection.Close()
	return ProbeUp, "tcp connect " + target
}

// probeCommand runs the argv through the same executable resolution and declared-env scoping as
// health; exit 0 is up, exit idleExit (when declared) is idle, any other exit is down, and a
// command that cannot run is unknown.
func probeCommand(probeContext context.Context, tool discovery.Tool, spec probeSpec, root, repositoryRoot string) (ProbeState, string) {
	settings := profile.Tool{}
	if loaded, err := profile.Load(repositoryRoot); err == nil {
		settings = loaded.Tools[tool.Name]
	}
	environment, err := resolveEnvironment(tool.Manifest, settings, os.Environ())
	if err != nil {
		return ProbeUnknown, err.Error()
	}
	argv := make([]string, len(spec.Command))
	for index, element := range spec.Command {
		argv[index] = replaceRepo(element, root)
	}
	executable, err := resolveExecutable(argv[0], environment)
	if err != nil {
		return ProbeUnknown, err.Error()
	}
	command := exec.CommandContext(probeContext, argv[0], argv[1:]...)
	command.Path, command.Dir, command.Env = executable, root, environment
	command.WaitDelay = time.Second
	runError := command.Run()
	if probeContext.Err() != nil {
		return ProbeUnknown, "probe timed out"
	}
	var exitError *exec.ExitError
	if runError != nil && !errors.As(runError, &exitError) {
		return ProbeUnknown, runError.Error()
	}
	code := ExitCode(runError)
	if code == 0 {
		return ProbeUp, "command exit 0"
	}
	if spec.IdleExit > 0 && code == spec.IdleExit {
		return ProbeIdle, fmt.Sprintf("command exit %d (idle)", code)
	}
	return ProbeDown, fmt.Sprintf("command exit %d", code)
}

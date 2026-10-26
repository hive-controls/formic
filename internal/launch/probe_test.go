package launch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/testfixture"
)

func probeTool(probe map[string]any) discovery.Tool {
	return discovery.Tool{Name: "svc", Manifest: formats.ToolManifest{Live: &formats.Live{Probe: probe}}}
}

func TestProbeUnknownWithoutDeclaration(t *testing.T) {
	result := Probe(context.Background(), discovery.Tool{Name: "x"}, t.TempDir())
	if result.State != ProbeUnknown {
		t.Fatalf("state = %q", result.State)
	}
}

func TestProbeHTTPStatusMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.ContentLength > 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected request shape: %s %d", r.Method, r.ContentLength)
		}
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
		case "/406":
			w.WriteHeader(406)
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	cases := []struct {
		name   string
		probe  map[string]any
		expect ProbeState
	}{
		{"200 default", map[string]any{"http": server.URL + "/ok"}, ProbeUp},
		{"500 default", map[string]any{"http": server.URL + "/bad"}, ProbeDown},
		{"406 any", map[string]any{"http": server.URL + "/406", "expect": "any"}, ProbeUp},
		{"406 exact", map[string]any{"http": server.URL + "/406", "expect": 406}, ProbeUp},
		{"406 default", map[string]any{"http": server.URL + "/406"}, ProbeDown},
	}
	for _, tc := range cases {
		if got := Probe(context.Background(), probeTool(tc.probe), t.TempDir()).State; got != tc.expect {
			t.Errorf("%s: state = %q want %q", tc.name, got, tc.expect)
		}
	}
}

func TestProbeHTTPDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	result := Probe(context.Background(), probeTool(map[string]any{"http": redirector.URL}), t.TempDir())
	if result.State != ProbeDown || hits.Load() != 0 {
		t.Fatalf("state = %q hits = %d", result.State, hits.Load())
	}
}

func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestProbeHTTPAndTCPDownOnRefusedPort(t *testing.T) {
	address := closedAddress(t)
	for _, probe := range []map[string]any{{"http": "http://" + address}, {"tcp": address}} {
		if got := Probe(context.Background(), probeTool(probe), t.TempDir()).State; got != ProbeDown {
			t.Errorf("%v: state = %q", probe, got)
		}
	}
}

func TestProbeTCPUp(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if got := Probe(context.Background(), probeTool(map[string]any{"tcp": listener.Addr().String()}), t.TempDir()).State; got != ProbeUp {
		t.Fatalf("state = %q", got)
	}
}

func TestProbeHTTPUnknownOnTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release); server.Close() })
	probeTimeoutOverride = 50 * time.Millisecond
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	if got := Probe(context.Background(), probeTool(map[string]any{"http": server.URL}), t.TempDir()).State; got != ProbeUnknown {
		t.Fatalf("state = %q", got)
	}
}

func TestProbeHonoursParentCancel(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release); server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	state := Probe(ctx, probeTool(map[string]any{"http": server.URL}), t.TempDir()).State
	if state != ProbeUnknown || time.Since(start) > time.Second {
		t.Fatalf("state = %q after %v", state, time.Since(start))
	}
}

func TestProbeCommand(t *testing.T) {
	fixture := testfixture.Tool(t)
	probeTimeoutOverride = HealthTimeout // a cold fixture exe can be slow under load
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	cases := []struct {
		name   string
		argv   []string
		expect ProbeState
	}{
		{"exit 0", []string{fixture, "fixture:exit", "0"}, ProbeUp},
		{"exit 3", []string{fixture, "fixture:exit", "3"}, ProbeDown},
		{"missing", []string{"definitely-not-a-real-binary-xyz"}, ProbeUnknown},
	}
	for _, tc := range cases {
		argv := make([]any, len(tc.argv))
		for i, v := range tc.argv {
			argv[i] = v
		}
		if got := Probe(context.Background(), probeTool(map[string]any{"command": argv}), t.TempDir()).State; got != tc.expect {
			t.Errorf("%s: state = %q want %q", tc.name, got, tc.expect)
		}
	}
}

func probeCommandState(t *testing.T, fixture string, code string, idleExit any) ProbeState {
	t.Helper()
	probe := map[string]any{"command": []any{fixture, "fixture:exit", code}}
	if idleExit != nil {
		probe["idleExit"] = idleExit
	}
	return Probe(context.Background(), probeTool(probe), t.TempDir()).State
}

func TestProbeCommandIdleExitReportsIdle(t *testing.T) {
	fixture := testfixture.Tool(t)
	probeTimeoutOverride = HealthTimeout
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	for code, expect := range map[string]ProbeState{"3": ProbeIdle, "0": ProbeUp, "4": ProbeDown} {
		if got := probeCommandState(t, fixture, code, 3); got != expect {
			t.Errorf("exit %s with idleExit 3: state = %q want %q", code, got, expect)
		}
	}
	result := Probe(context.Background(), probeTool(map[string]any{"command": []any{fixture, "fixture:exit", "3"}, "idleExit": 3}), t.TempDir())
	if result.Detail != "command exit 3 (idle)" {
		t.Errorf("detail = %q", result.Detail)
	}
}

func TestProbeCommandWithoutIdleExitNeverReportsIdle(t *testing.T) {
	fixture := testfixture.Tool(t)
	probeTimeoutOverride = HealthTimeout
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	if got := probeCommandState(t, fixture, "3", nil); got != ProbeDown {
		t.Fatalf("state = %q, want down", got)
	}
}

func TestProbeStateAnswering(t *testing.T) {
	for state, expect := range map[ProbeState]bool{ProbeUp: true, ProbeIdle: true, ProbeDown: false, ProbeUnknown: false} {
		if state.Answering() != expect {
			t.Errorf("%q.Answering() = %v", state, !expect)
		}
	}
}

func TestProbeCommandUnknownOnTimeout(t *testing.T) {
	fixture := testfixture.Tool(t)
	probeTimeoutOverride = 100 * time.Millisecond
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	start := time.Now()
	result := Probe(context.Background(), probeTool(map[string]any{"command": []any{fixture, "fixture:sleep", "5000"}}), t.TempDir())
	if result.State != ProbeUnknown || time.Since(start) > 3*time.Second {
		t.Fatalf("state = %q after %v", result.State, time.Since(start))
	}
}

func TestProbeCommandSubstitutesRepo(t *testing.T) {
	fixture := testfixture.Tool(t)
	probeTimeoutOverride = HealthTimeout
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	root := t.TempDir()
	marker := root + "/marker"
	tool := probeTool(map[string]any{"command": []any{fixture, "fixture:touch", "{repo}/marker"}})
	tool.SourceRoot = root
	if got := Probe(context.Background(), tool, t.TempDir()).State; got != ProbeUp {
		t.Fatalf("state = %q", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker not created: %v", err)
	}
}

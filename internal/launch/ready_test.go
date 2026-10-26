package launch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/testfixture"
)

func fastReadyPolling(t *testing.T) {
	t.Helper()
	ReadyPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { ReadyPollInterval = time.Second })
}

func TestReadyWindowReadsTheActionsWindow(t *testing.T) {
	declared := formats.ToolManifest{
		Live: &formats.Live{Probe: map[string]any{"tcp": "127.0.0.1:1"}},
		Actions: []formats.Action{
			{Name: "start", Ready: &formats.Ready{Within: "90s"}},
			{Name: "stop"},
		},
	}
	start, err := manifest.ForAction(declared, "start")
	if err != nil {
		t.Fatal(err)
	}
	if within, ok := ReadyWindow(start); !ok || within != 90*time.Second {
		t.Fatalf("start window = %v, %v", within, ok)
	}
	stop, err := manifest.ForAction(declared, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadyWindow(stop); ok {
		t.Fatal("an action without ready has a window")
	}
	noProbe := formats.ToolManifest{Live: &formats.Live{Ready: &formats.Ready{Within: "5s"}}}
	if _, ok := ReadyWindow(noProbe); ok {
		t.Fatal("a window without a probe")
	}
	unparsable := formats.ToolManifest{Live: &formats.Live{Probe: map[string]any{"tcp": "h:1"}, Ready: &formats.Ready{Within: "soon"}}}
	if _, ok := ReadyWindow(unparsable); ok {
		t.Fatal("an unparsable duration has a window")
	}
}

func TestWaitReadyReturnsOnFirstUp(t *testing.T) {
	fastReadyPolling(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	var observed []ProbeState
	result, ready := WaitReady(context.Background(), probeTool(map[string]any{"http": server.URL}), t.TempDir(), 5*time.Second,
		func(probe ProbeResult) { observed = append(observed, probe.State) })
	if !ready || result.State != ProbeUp || hits.Load() != 3 {
		t.Fatalf("ready = %v state = %q hits = %d", ready, result.State, hits.Load())
	}
	if len(observed) != 3 || observed[0] != ProbeDown || observed[2] != ProbeUp {
		t.Fatalf("observed = %v", observed)
	}
}

func TestWaitReadyTimesOutWithTheLastResult(t *testing.T) {
	fastReadyPolling(t)
	result, ready := WaitReady(context.Background(), probeTool(map[string]any{"tcp": closedAddress(t)}), t.TempDir(), 100*time.Millisecond, nil)
	if ready || result.State != ProbeDown {
		t.Fatalf("ready = %v state = %q, want not ready with the last result down", ready, result.State)
	}
}

func TestWaitReadyKeepsTheLastCompletedResultWhenTheWindowCutsAProbeOff(t *testing.T) {
	fastReadyPolling(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		<-r.Context().Done() // hold every later request open until the client gives up
	}))
	defer server.Close()
	var observed []ProbeState
	result, ready := WaitReady(context.Background(), probeTool(map[string]any{"http": server.URL}), t.TempDir(), 150*time.Millisecond,
		func(probe ProbeResult) { observed = append(observed, probe.State) })
	if ready || result.State != ProbeDown {
		t.Fatalf("ready = %v state = %q, want not ready with the last completed result down", ready, result.State)
	}
	if len(observed) != 1 || observed[0] != ProbeDown {
		t.Fatalf("observed = %v, want only the completed probe", observed)
	}
	if hits.Load() < 2 {
		t.Fatalf("server saw %d requests, want a second one cut off by the window", hits.Load())
	}
}

func TestWaitReadyCountsIdleAsReady(t *testing.T) {
	fastReadyPolling(t)
	probeTimeoutOverride = HealthTimeout
	t.Cleanup(func() { probeTimeoutOverride = 0 })
	tool := discovery.Tool{Name: "svc", Manifest: formats.ToolManifest{Live: &formats.Live{Probe: map[string]any{
		"command": []any{testfixture.Tool(t), "fixture:exit", "3"}, "idleExit": 3,
	}}}}
	result, ready := WaitReady(context.Background(), tool, t.TempDir(), 5*time.Second, nil)
	if !ready || result.State != ProbeIdle {
		t.Fatalf("ready = %v state = %q, want ready on idle", ready, result.State)
	}
}

func TestWaitReadyHonoursCancel(t *testing.T) {
	fastReadyPolling(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(40*time.Millisecond, cancel)
	start := time.Now()
	_, ready := WaitReady(ctx, probeTool(map[string]any{"tcp": closedAddress(t)}), t.TempDir(), time.Minute, nil)
	if ready || time.Since(start) > 2*time.Second {
		t.Fatalf("ready = %v after %v, want an early not-ready", ready, time.Since(start))
	}
}

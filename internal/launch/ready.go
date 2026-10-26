package launch

import (
	"context"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
)

// ReadyPollInterval is the gap between probes while waiting for a freshly started service;
// tests lower it.
var ReadyPollInterval = time.Second

// ReadyWindow is the effective manifest's live.ready.within; ok is false with no live.probe, no
// ready, or an unparsable or non-positive duration. effective is manifest.ForAction's projection.
func ReadyWindow(effective formats.ToolManifest) (time.Duration, bool) {
	if effective.Live == nil || effective.Live.Probe == nil || effective.Live.Ready == nil {
		return 0, false
	}
	within, err := time.ParseDuration(string(effective.Live.Ready.Within))
	if err != nil || within <= 0 {
		return 0, false
	}
	return within, true
}

// WaitReady probes tool (whose Manifest is the effective projection) now and every
// ReadyPollInterval until a result is Answering (it returns that result, true) or within or ctx
// ends (it returns the last completed result, false). observe, when non-nil, sees every result. It never
// writes anything: the wait is advisory and cannot change a run's outcome or exit code (§22 c).
func WaitReady(ctx context.Context, tool discovery.Tool, repositoryRoot string, within time.Duration, observe func(ProbeResult)) (ProbeResult, bool) {
	waitContext, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	deadline, _ := waitContext.Deadline()
	var last ProbeResult
	completed := false
	for {
		result := Probe(waitContext, tool, repositoryRoot)
		if result.State.Answering() {
			if observe != nil {
				observe(result)
			}
			return result, true
		}
		// A probe the window cut off is not an observation of the service: keep the last
		// completed result, and fall back to the cut-off one only when none ever completed.
		// The probe's own timer can fire a tick before waitContext reports Err, so the
		// deadline itself is checked too.
		cutOff := waitContext.Err() != nil || !time.Now().Before(deadline)
		if cutOff && completed {
			return last, false
		}
		last, completed = result, true
		if observe != nil {
			observe(last)
		}
		select {
		case <-waitContext.Done():
			return last, false
		case <-time.After(ReadyPollInterval):
		}
	}
}

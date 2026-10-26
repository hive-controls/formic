package launch

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
	"github.com/hive-controls/formic/internal/testfixture"
)

func stringPointer(value string) *string { return &value }

// actionFixtureTool is a two-action tool: "restart" (confirming, task) and "start" (service,
// optionally with a declared probe and an explicit whenUp).
func actionFixtureTool(t *testing.T) discovery.Tool {
	t.Helper()
	service := formats.ActionLifecycleService
	repositoryToken := "{repo}"
	return discovery.Tool{Name: "docker-desktop", Manifest: formats.ToolManifest{
		Launch: formats.ToolManifestLaunch{Command: testfixture.Tool(t), Args: []string{"fixture:status", "0", "done"}, Cwd: &repositoryToken},
		Actions: []formats.Action{
			{Name: "restart", Args: []string{"--timeout", "180"}, Confirm: stringPointer("Restart everything?")},
			{Name: "start", Lifecycle: &service},
		},
	}}
}

func withProbe(tool discovery.Tool) discovery.Tool {
	tool.Manifest.Live = &formats.Live{Probe: map[string]any{"tcp": "127.0.0.1:1"}}
	return tool
}

func newActionRequest(t *testing.T, tool discovery.Tool, action string) Request {
	t.Helper()
	t.Setenv("FORMIC_CONFIG_DIR", t.TempDir())
	return Request{
		Tool: tool, RepositoryRoot: "/repo", Action: action, Confirmed: true,
		Store: runs.Store{StateDirectory: t.TempDir()}, Environment: os.Environ(), StartedAt: time.Unix(100, 0),
	}
}

func rowCount(t *testing.T, store runs.Store) int {
	t.Helper()
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestPrepareRunsANamedActionWithItsProfileArgumentsAndRecordsIt(t *testing.T) {
	request := newActionRequest(t, actionFixtureTool(t), "restart")
	request.Profile = profile.File{Tools: map[string]profile.Tool{"docker-desktop": {
		Args:    []string{"--implicit-only"},
		Actions: map[string]profile.ActionSettings{"restart": {Args: []string{"--stored"}}},
	}}}
	prepared, err := Prepare(request, profile.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fixture:status", "0", "done", "--timeout", "180", "--stored"}
	if got := prepared.Command.Args[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv tail = %v, want %v (profile args for the ACTION, not the implicit args)", got, want)
	}
	if prepared.Row.Action != "restart" {
		t.Fatalf("Row.Action = %q", prepared.Row.Action)
	}
	saved, err := request.Store.Load(prepared.Row.ID)
	if err != nil || saved.Action != "restart" {
		t.Fatalf("persisted row action = %q, %v", saved.Action, err)
	}
}

func TestPrepareRefusesWithoutAnActionOnAMultiActionToolAndWritesNoRow(t *testing.T) {
	request := newActionRequest(t, actionFixtureTool(t), "")
	if _, err := Prepare(request, profile.ModeForeground); err == nil {
		t.Fatal("a multi-action tool with no action must be refused")
	}
	if count := rowCount(t, request.Store); count != 0 {
		t.Fatalf("%d rows written for a refused launch", count)
	}
}

func TestPrepareRefusesAConfirmingActionUntilConfirmed(t *testing.T) {
	request := newActionRequest(t, actionFixtureTool(t), "restart")
	request.Confirmed = false
	_, err := Prepare(request, profile.ModeForeground)
	var confirmation *ConfirmationRequired
	if !errors.As(err, &confirmation) || confirmation.Sentence != "Restart everything?" || confirmation.Action != "restart" {
		t.Fatalf("err = %v, want *ConfirmationRequired carrying the sentence", err)
	}
	if count := rowCount(t, request.Store); count != 0 {
		t.Fatalf("%d rows written before confirmation", count)
	}
	request.Confirmed = true
	if _, err := Prepare(request, profile.ModeForeground); err != nil {
		t.Fatalf("confirmed launch refused: %v", err)
	}
}

func TestPrepareCapturedModeWritesNoReplayFile(t *testing.T) {
	request := newActionRequest(t, actionFixtureTool(t), "restart")
	prepared, err := Prepare(request, profile.ModeCaptured)
	if err != nil {
		t.Fatal(err)
	}
	if _, statError := os.Stat(request.Store.ReplayPath(prepared.Row.ID)); !os.IsNotExist(statError) {
		t.Fatalf("captured mode wrote a replay file: %v", statError)
	}
	if prepared.Row.Mode != profile.ModeCaptured {
		t.Fatalf("row mode = %q", prepared.Row.Mode)
	}
}

func TestPrepareSkipsAServiceThatIsAlreadyUp(t *testing.T) {
	request := newActionRequest(t, withProbe(actionFixtureTool(t)), "start")
	request.Live = &ProbeResult{State: ProbeUp}
	_, err := Prepare(request, profile.ModeTab)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if count := rowCount(t, request.Store); count != 0 {
		t.Fatalf("%d rows written for a skipped launch", count)
	}
}

func TestPrepareSkipsAServiceThatIsIdle(t *testing.T) {
	request := newActionRequest(t, withProbe(actionFixtureTool(t)), "start")
	request.Live = &ProbeResult{State: ProbeIdle}
	_, err := Prepare(request, profile.ModeTab)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if count := rowCount(t, request.Store); count != 0 {
		t.Fatalf("%d rows written for a skipped launch", count)
	}
}

func TestPrepareAnywayLaunchesAServiceThatIsAlreadyUp(t *testing.T) {
	request := newActionRequest(t, withProbe(actionFixtureTool(t)), "start")
	request.Live = &ProbeResult{State: ProbeUp}
	request.Anyway = true
	prepared, err := Prepare(request, profile.ModeTab)
	if err != nil {
		t.Fatalf("Prepare with Anyway: %v", err)
	}
	if prepared.Command == nil || rowCount(t, request.Store) != 1 {
		t.Fatalf("command %v, rows %d, want a command and one row", prepared.Command, rowCount(t, request.Store))
	}
}

func TestPrepareLaunchesAServiceWhenTheProbeIsDownUnknownOrAbsent(t *testing.T) {
	for name, live := range map[string]*ProbeResult{
		"down": {State: ProbeDown}, "unknown": {State: ProbeUnknown}, "not probed": nil,
	} {
		request := newActionRequest(t, withProbe(actionFixtureTool(t)), "start")
		request.Live = live
		if _, err := Prepare(request, profile.ModeTab); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPrepareIgnoresALiveProbeForATaskOrAnExplicitWhenUpLaunch(t *testing.T) {
	task := newActionRequest(t, withProbe(actionFixtureTool(t)), "restart")
	task.Live = &ProbeResult{State: ProbeUp}
	if _, err := Prepare(task, profile.ModeForeground); err != nil {
		t.Fatalf("a task defaults to whenUp launch, got %v", err)
	}
	launchTool := withProbe(actionFixtureTool(t))
	launchWhenUp := formats.ActionWhenUpLaunch
	launchTool.Manifest.Actions[1].WhenUp = &launchWhenUp
	explicit := newActionRequest(t, launchTool, "start")
	explicit.Live = &ProbeResult{State: ProbeUp}
	if _, err := Prepare(explicit, profile.ModeTab); err != nil {
		t.Fatalf("explicit whenUp: launch must launch, got %v", err)
	}
	undeclared := newActionRequest(t, actionFixtureTool(t), "start")
	undeclared.Live = &ProbeResult{State: ProbeUp}
	if _, err := Prepare(undeclared, profile.ModeTab); err != nil {
		t.Fatalf("no live.probe means no skip, got %v", err)
	}
}

func TestPrepareNeedsDetachForAServiceOutsideTheTabRoute(t *testing.T) {
	request := newActionRequest(t, actionFixtureTool(t), "start")
	if _, err := Prepare(request, profile.ModeForeground); !errors.Is(err, ErrNeedsDetach) {
		t.Fatalf("foreground service without Detach: err = %v, want ErrNeedsDetach", err)
	}
	if count := rowCount(t, request.Store); count != 0 {
		t.Fatalf("%d rows written for a refused service launch", count)
	}
	request.Detach = true
	if _, err := Prepare(request, profile.ModeTab); err != nil {
		t.Fatalf("detached service: %v", err)
	}
}

func TestEffectiveLifecycleFallsBackToTheItemLevelValue(t *testing.T) {
	tool := actionFixtureTool(t).Manifest
	service := formats.ToolManifestLifecycleService
	tool.Lifecycle = &service
	tool.Actions = nil
	if got := effectiveLifecycle(tool, ""); got != "service" {
		t.Fatalf("item-level lifecycle = %q", got)
	}
	if _, found := manifest.ActionSpec(tool, "x"); found {
		t.Fatal("sanity")
	}
}

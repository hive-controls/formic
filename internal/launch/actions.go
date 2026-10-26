package launch

import (
	"errors"
	"fmt"

	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/profile"
)

// ErrAlreadyRunning is returned by Prepare when the effective action's whenUp is skip, the
// tool declares a live probe, and the caller-supplied Request.Live says it is up or idle. The caller
// reports it and exits 0; no run row is written.
var ErrAlreadyRunning = errors.New("already running")

// ErrNeedsDetach is returned by Prepare for a lifecycle: service action asked to run in the
// foreground or a captured route without Request.Detach — it would hold the caller forever.
// The deck never hits it (a service always routes to a tab); the CLI does without --detach.
var ErrNeedsDetach = errors.New("is a service and would hold this command forever; pass --detach")

// ConfirmationRequired is returned by Prepare for an action that declares confirm when the
// request is not Confirmed. No run row is written.
type ConfirmationRequired struct {
	Tool, Action, Sentence string
}

func (required *ConfirmationRequired) Error() string {
	return fmt.Sprintf("%s %s requires confirmation: %s", required.Tool, required.Action, required.Sentence)
}

// effectiveLifecycle is the action's lifecycle, falling back to the item-level lifecycle and
// then to "task".
func effectiveLifecycle(tool formats.ToolManifest, action string) string {
	if spec, found := manifest.ActionSpec(tool, action); found && spec.Lifecycle != nil {
		return string(*spec.Lifecycle)
	}
	if action == "" && tool.Lifecycle != nil {
		return string(*tool.Lifecycle)
	}
	return string(formats.ActionLifecycleTask)
}

// effectiveWhenUp is the action's whenUp, falling back to live.whenUp for the implicit action
// and then to the lifecycle default: skip for a service, launch for a task.
func effectiveWhenUp(tool formats.ToolManifest, action string) string {
	if spec, found := manifest.ActionSpec(tool, action); found && spec.WhenUp != nil {
		return string(*spec.WhenUp)
	}
	if action == "" && tool.Live != nil && tool.Live.WhenUp != nil {
		return string(*tool.Live.WhenUp)
	}
	if effectiveLifecycle(tool, action) == string(formats.ActionLifecycleService) {
		return string(formats.ActionWhenUpSkip)
	}
	return string(formats.ActionWhenUpLaunch)
}

// checkActionGates runs the refusals that need only the request and the manifest: the confirm
// gate, the already-up skip, and the service-needs-detach rule. All of them precede every
// side effect (row, replay file, process).
func checkActionGates(request Request, tool formats.ToolManifest, mode profile.LaunchMode) error {
	if spec, found := manifest.ActionSpec(tool, request.Action); found && spec.Confirm != nil && !request.Confirmed {
		return &ConfirmationRequired{Tool: request.Tool.Name, Action: request.Action, Sentence: *spec.Confirm}
	}
	if !request.Anyway && tool.Live != nil && request.Live != nil && request.Live.State.Answering() &&
		effectiveWhenUp(tool, request.Action) == string(formats.ActionWhenUpSkip) {
		return ErrAlreadyRunning
	}
	holdsTheCaller := mode == profile.ModeForeground || mode == profile.ModeCaptured
	if holdsTheCaller && !request.Detach && effectiveLifecycle(tool, request.Action) == string(formats.ActionLifecycleService) {
		return fmt.Errorf("%s %w", actionLabel(request.Tool.Name, request.Action), ErrNeedsDetach)
	}
	return nil
}

func actionLabel(tool, action string) string {
	if action == "" {
		return tool
	}
	return tool + " · " + action
}

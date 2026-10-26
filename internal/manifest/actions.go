package manifest

import (
	"fmt"
	"strings"

	"github.com/hive-controls/formic/internal/formats"
)

// ActionRequired is returned when a manifest declares actions[] and a launch named none: a
// launcher never guesses a default action.
type ActionRequired struct {
	Tool    string
	Actions []formats.Action
}

func (problem *ActionRequired) Error() string {
	return fmt.Sprintf("%s declares actions; name one of: %s", problem.Tool, strings.Join(namesOf(problem.Actions), ", "))
}

// UnknownAction is returned when a launch names an action the manifest does not declare.
type UnknownAction struct {
	Tool    string
	Action  string
	Actions []string
}

func (problem *UnknownAction) Error() string {
	return fmt.Sprintf("%s has no action %q; actions: %s", problem.Tool, problem.Action, strings.Join(problem.Actions, ", "))
}

// ActionNames lists the declared action names in order; nil for an action-less manifest.
func ActionNames(tool formats.ToolManifest) []string {
	if len(tool.Actions) == 0 {
		return nil
	}
	return namesOf(tool.Actions)
}

// ActionSpec looks up one declared action by name.
func ActionSpec(tool formats.ToolManifest, action string) (formats.Action, bool) {
	for _, candidate := range tool.Actions {
		if candidate.Name == action {
			return candidate, true
		}
	}
	return formats.Action{}, false
}

// ForAction returns the manifest one launch of `action` sees: launch rewritten to the action's
// argv, arguments = shared then the action's own, exit = the action's codes over the tool's,
// pane.interactive = the action's when set, and lifecycle/whenUp/ready carried from the action.
// The result declares no actions of its own (read confirm through ActionSpec), so projecting it
// again with "" is the identity. action "" on a manifest without actions[] returns it unchanged.
func ForAction(tool formats.ToolManifest, action string) (formats.ToolManifest, error) {
	if len(tool.Actions) == 0 {
		if action != "" {
			return formats.ToolManifest{}, fmt.Errorf("%s declares no actions", tool.Name)
		}
		return tool, nil
	}
	if action == "" {
		return formats.ToolManifest{}, &ActionRequired{Tool: tool.Name, Actions: tool.Actions}
	}
	spec, found := ActionSpec(tool, action)
	if !found {
		return formats.ToolManifest{}, &UnknownAction{Tool: tool.Name, Action: action, Actions: ActionNames(tool)}
	}
	effective := tool
	effective.Actions = nil
	effective.Launch = projectLaunch(tool.Launch, spec)
	effective.Arguments = append(append([]formats.Argument(nil), tool.Arguments...), spec.Arguments...)
	effective.Exit = overlayExit(tool.Exit, spec.Exit)
	effective.Pane = projectPane(tool.Pane, spec)
	effective.Lifecycle = projectLifecycle(tool.Lifecycle, spec)
	effective.Live = projectLive(tool.Live, spec)
	return effective, nil
}

func projectLaunch(launch formats.ToolManifestLaunch, spec formats.Action) formats.ToolManifestLaunch {
	projected := formats.ToolManifestLaunch{Command: launch.Command, Cwd: launch.Cwd}
	if spec.Command != nil {
		projected.Command = *spec.Command
		projected.Args = append([]string(nil), spec.Args...)
	} else {
		projected.Args = append(append([]string(nil), launch.Args...), spec.Args...)
	}
	if spec.Cwd != nil {
		projected.Cwd = spec.Cwd
	}
	return projected
}

func overlayExit(shared formats.ToolManifestExit, own formats.ActionExit) formats.ToolManifestExit {
	if shared == nil && own == nil {
		return nil
	}
	merged := make(formats.ToolManifestExit, len(shared)+len(own))
	for code, meaning := range shared {
		merged[code] = meaning
	}
	for code, meaning := range own {
		merged[code] = meaning
	}
	return merged
}

func projectPane(pane *formats.ToolManifestPane, spec formats.Action) *formats.ToolManifestPane {
	if spec.Interactive == nil {
		return pane
	}
	projected := formats.ToolManifestPane{}
	if pane != nil {
		projected = *pane
	}
	interactive := *spec.Interactive
	projected.Interactive = &interactive
	return &projected
}

func projectLifecycle(shared *formats.ToolManifestLifecycle, spec formats.Action) *formats.ToolManifestLifecycle {
	if spec.Lifecycle == nil {
		return shared
	}
	lifecycle := formats.ToolManifestLifecycle(*spec.Lifecycle)
	return &lifecycle
}

func projectLive(shared *formats.Live, spec formats.Action) *formats.Live {
	if spec.WhenUp == nil && spec.Ready == nil {
		return shared
	}
	projected := formats.Live{}
	if shared != nil {
		projected = *shared
	}
	if spec.WhenUp != nil {
		whenUp := formats.LiveWhenUp(*spec.WhenUp)
		projected.WhenUp = &whenUp
	}
	if spec.Ready != nil {
		ready := *spec.Ready
		projected.Ready = &ready
	}
	return &projected
}

func namesOf(actions []formats.Action) []string {
	names := make([]string, len(actions))
	for index, action := range actions {
		names[index] = action.Name
	}
	return names
}

// checkActions returns the first admission problem in a manifest's actions[] that the schema
// cannot express. An action-less manifest passes untouched.
func checkActions(tool formats.ToolManifest, repositoryRoot string) error {
	if len(tool.Actions) == 0 {
		return nil
	}
	if err := checkItemLevelLifecycle(tool); err != nil {
		return err
	}
	firstIndexByName := make(map[string]int, len(tool.Actions))
	sharedFlags := make(map[string]bool, len(tool.Arguments))
	for _, argument := range tool.Arguments {
		sharedFlags[argument.Flag] = true
	}
	for index, action := range tool.Actions {
		label := fmt.Sprintf("actions[%d] (%s)", index, action.Name)
		if earlier, seen := firstIndexByName[action.Name]; seen {
			return fmt.Errorf("%s: name is already used by actions[%d]", label, earlier)
		}
		firstIndexByName[action.Name] = index
		if err := checkActionArguments(tool, action, sharedFlags); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		effective, err := ForAction(tool, action.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if _, err := ResolveLaunch(effective, repositoryRoot, "{spec}", placeholderInputValues(tool.Inputs)); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func checkActionArguments(tool formats.ToolManifest, action formats.Action, sharedFlags map[string]bool) error {
	for _, argument := range action.Arguments {
		if sharedFlags[argument.Flag] {
			return fmt.Errorf("argument %s is already declared at manifest level", argument.Flag)
		}
	}
	merged := append(append([]formats.Argument(nil), tool.Arguments...), action.Arguments...)
	return checkDeclaredArguments(merged)
}

// checkItemLevelLifecycle refuses item-level lifecycle, live.whenUp and live.ready beside
// actions[]: which action they would govern is ambiguous, so each action declares its own.
func checkItemLevelLifecycle(tool formats.ToolManifest) error {
	if tool.Lifecycle != nil {
		return fmt.Errorf("lifecycle: declared beside actions[] is ambiguous; declare lifecycle on each action")
	}
	if tool.Live != nil && tool.Live.WhenUp != nil {
		return fmt.Errorf("live.whenUp: declared beside actions[] is ambiguous; declare whenUp on each action")
	}
	if tool.Live != nil && tool.Live.Ready != nil {
		return fmt.Errorf("live.ready: declared beside actions[] is ambiguous; declare ready on each action")
	}
	return nil
}

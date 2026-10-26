package main

import (
	"fmt"

	"github.com/hive-controls/formic/internal/formats"
)

// describedAction is one declared action as `formic describe` reports it.
type describedAction struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
	Interactive bool   `json:"interactive"`
	Lifecycle   string `json:"lifecycle,omitempty"`
}

func describeActions(declared []formats.Action) []describedAction {
	described := make([]describedAction, 0, len(declared))
	for _, action := range declared {
		entry := describedAction{Name: action.Name}
		if action.Title != nil {
			entry.Title = *action.Title
		}
		if action.Description != nil {
			entry.Description = *action.Description
		}
		if action.Confirm != nil {
			entry.Confirm = *action.Confirm
		}
		if action.Interactive != nil {
			entry.Interactive = *action.Interactive
		}
		if action.Lifecycle != nil {
			entry.Lifecycle = string(*action.Lifecycle)
		}
		described = append(described, entry)
	}
	return described
}

func printDescribedActions(actions []describedAction) {
	for _, action := range actions {
		line := "action: " + action.Name
		if action.Title != "" {
			line += " " + action.Title
		}
		if action.Interactive {
			line += " [interactive]"
		}
		if action.Lifecycle != "" {
			line += " [" + action.Lifecycle + "]"
		}
		if action.Confirm != "" {
			line += fmt.Sprintf(" confirm: %q", action.Confirm)
		}
		fmt.Println(line)
	}
}

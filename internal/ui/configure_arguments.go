package ui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/arguments"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
)

const (
	chooserNotSetLabel   = "(not set)"
	chooserOutsideSuffix = " (not among the options)"
)

// argumentRows is the customize screen's args step for a tool that declares arguments: one
// row per declared entry, then the Extra arguments row at index len(entries). It is empty for
// a tool that declares none, which keeps the free-text configureArgs field alone.
type argumentRows struct {
	entries     []formats.Argument
	index       int
	form        arguments.Form
	input       textinput.Model
	choiceIndex int
	choiceKept  string
}

// declaredArguments is the one place the customize screen reads a tool's declared launch
// arguments. For an action it is the projection's merged list (manifest-level rows first, then
// the action's own); everything downstream takes the list as a parameter.
func declaredArguments(tool discovery.Tool, action string) []formats.Argument {
	if action == "" {
		return tool.Manifest.Arguments
	}
	effective, err := manifest.ForAction(tool.Manifest, action)
	if err != nil {
		return nil
	}
	return effective.Arguments
}

// withArgumentRows loads the declared rows from the stored profile args. With nothing declared
// it leaves the free-text args field exactly as beginConfigure set it.
func (model Model) withArgumentRows(declared []formats.Argument, stored []string) Model {
	model.argumentRows.entries = declared
	model.argumentRows.index = 0
	model.argumentRows.form = arguments.Form{}
	if len(declared) == 0 {
		return model
	}
	form := arguments.Parse(declared, stored)
	model.argumentRows.form = form
	model.configureArgs.Prompt = "Extra arguments: "
	model.configureArgs.SetValue(strings.Join(form.Extra, ", "))
	return model.focusArgumentRow(0)
}

// onArgumentRow reports whether step 1 is showing a declared row, as opposed to the free-text
// field or the trailing Extra arguments row.
func (model Model) onArgumentRow() bool {
	return model.configureStep == 1 && model.argumentRows.index < len(model.argumentRows.entries)
}

// focusArgumentRow moves to a row and prefills it: the stored value wins, a declared default
// only fills a row with no stored value, and a flag ignores its default.
func (model Model) focusArgumentRow(index int) Model {
	model.argumentRows.index = index
	if index >= len(model.argumentRows.entries) {
		model.configureArgs.Focus()
		return model
	}
	entry := model.argumentRows.entries[index]
	values := model.argumentRows.form.Values
	stored, hasStored := values[entry.Flag]
	switch entry.Kind {
	case formats.ArgumentKindFlag:
		model.argumentRows.choiceIndex = 0
		if hasStored {
			model.argumentRows.choiceIndex = 1
		}
	case formats.ArgumentKindChoice:
		model.argumentRows.choiceKept = ""
		if hasStored && stored != "" && !slices.ContainsFunc(entry.Options, func(option formats.Option) bool { return option.Value == stored }) {
			model.argumentRows.choiceKept = stored
		}
		if !hasStored && entry.Default != nil {
			values[entry.Flag] = *entry.Default
		}
		model.argumentRows.choiceIndex = chooserIndexFor(entry, model.argumentRows.choiceKept, values[entry.Flag])
	default:
		if !hasStored && entry.Default != nil {
			values[entry.Flag] = *entry.Default
		}
		model.argumentRows.input = textinput.New()
		model.argumentRows.input.Prompt = entry.Flag + ": "
		model.argumentRows.input.SetValue(values[entry.Flag])
		model.argumentRows.input.Focus()
	}
	return model
}

// chooserIndexFor maps a current value onto a label position: 0 is "(not set)", then the
// options in order, then the kept out-of-options value when there is one.
func chooserIndexFor(entry formats.Argument, kept, current string) int {
	if current == "" {
		return 0
	}
	if kept != "" && current == kept {
		return len(entry.Options) + 1
	}
	for position, option := range entry.Options {
		if option.Value == current {
			return position + 1
		}
	}
	return 0
}

func argumentChooserLabels(entry formats.Argument, kept string) []string {
	if entry.Kind == formats.ArgumentKindFlag {
		return []string{"off", "on"}
	}
	labels := []string{chooserNotSetLabel}
	for _, option := range entry.Options {
		labels = append(labels, option.Value)
	}
	if kept != "" {
		labels = append(labels, kept+chooserOutsideSuffix)
	}
	return labels
}

// updateArgumentRowKey handles one key on a declared row. Choosers update the form as the
// selection moves, so the Args preview is always what a save would write.
func (model Model) updateArgumentRowKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	entry := model.argumentRows.entries[model.argumentRows.index]
	if key.String() == "enter" {
		return model.focusArgumentRow(model.argumentRows.index + 1), textinput.Blink
	}
	if entry.Kind == formats.ArgumentKindValue {
		updated, command := model.argumentRows.input.Update(key)
		model.argumentRows.input = updated
		model.storeArgumentValue(entry, strings.TrimSpace(updated.Value()))
		return model, command
	}
	switch key.String() {
	case "left":
		model = model.moveChooser(entry, -1)
	case "right", " ", "space":
		model = model.moveChooser(entry, 1)
	}
	return model, nil
}

func (model Model) moveChooser(entry formats.Argument, step int) Model {
	count := len(argumentChooserLabels(entry, model.argumentRows.choiceKept))
	model.argumentRows.choiceIndex = (model.argumentRows.choiceIndex + step + count) % count
	values := model.argumentRows.form.Values
	switch {
	case entry.Kind == formats.ArgumentKindFlag && model.argumentRows.choiceIndex == 1:
		values[entry.Flag] = ""
	case model.argumentRows.choiceIndex == 0 || entry.Kind == formats.ArgumentKindFlag:
		delete(values, entry.Flag)
	case model.argumentRows.choiceIndex <= len(entry.Options):
		values[entry.Flag] = entry.Options[model.argumentRows.choiceIndex-1].Value
	default:
		values[entry.Flag] = model.argumentRows.choiceKept
	}
	return model
}

func (model Model) storeArgumentValue(entry formats.Argument, typed string) {
	if typed == "" {
		delete(model.argumentRows.form.Values, entry.Flag)
		return
	}
	model.argumentRows.form.Values[entry.Flag] = typed
}

// serializedConfigureArguments is what a save writes into the profile's args list.
func (model Model) serializedConfigureArguments() []string {
	if len(model.argumentRows.entries) == 0 {
		return parseArguments(model.configureArgs.Value())
	}
	form := model.argumentRows.form
	form.Extra = parseArguments(model.configureArgs.Value())
	return arguments.Serialize(model.argumentRows.entries, form)
}

// configureArgumentsView renders step 1. A tool with no declared arguments keeps the
// single free-text field and its hint unchanged.
func configureArgumentsView(model Model, width int) string {
	if len(model.argumentRows.entries) == 0 {
		return model.configureArgs.View() + "\n" +
			mutedStyle.Render(truncate("Comma-separated argv · enter continue · esc cancel", width)) + "\n"
	}
	var content strings.Builder
	hint := "Comma-separated argv · enter continue · esc cancel"
	if model.onArgumentRow() {
		entry := model.argumentRows.entries[model.argumentRows.index]
		title := "Argument " + strconv.Itoa(model.argumentRows.index+1) + "/" + strconv.Itoa(len(model.argumentRows.entries))
		content.WriteString(mutedStyle.Render(truncate(title, width)) + "\n")
		if entry.Description != nil && *entry.Description != "" {
			content.WriteString(mutedStyle.Render(wrap(*entry.Description, width)) + "\n")
		}
		content.WriteString(argumentRowControl(model, entry) + "\n")
		hint = "←/→ choose · enter next · esc cancel"
		if entry.Kind == formats.ArgumentKindValue {
			hint = "type a value, blank leaves it unset · enter next · esc cancel"
		}
	} else {
		content.WriteString(mutedStyle.Render("Extra arguments") + "\n")
		content.WriteString(model.configureArgs.View() + "\n")
	}
	content.WriteString(mutedStyle.Render(truncate("Args: "+argumentsPreview(model), width)) + "\n")
	content.WriteString(mutedStyle.Render(truncate(hint, width)) + "\n")
	return content.String()
}

func argumentRowControl(model Model, entry formats.Argument) string {
	if entry.Kind == formats.ArgumentKindValue {
		return model.argumentRows.input.View()
	}
	labels := argumentChooserLabels(entry, model.argumentRows.choiceKept)
	return entry.Flag + ": " + chooserRow(labels, model.argumentRows.choiceIndex)
}

func chooserRow(labels []string, selected int) string {
	rendered := make([]string, 0, len(labels))
	for position, label := range labels {
		if position == selected {
			rendered = append(rendered, selectedStyle.Render(" "+label+" "))
			continue
		}
		rendered = append(rendered, mutedStyle.Render(" "+label+" "))
	}
	return strings.Join(rendered, " ")
}

func argumentsPreview(model Model) string {
	preview := model.serializedConfigureArguments()
	if len(preview) == 0 {
		return "none"
	}
	return encodeArgv(preview)
}

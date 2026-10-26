package ui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/formats"
)

// envChooser is the env step's row state for a declared env[] entry that lists options: the
// row cycles a fixed set of labels instead of taking typed text. It is inactive for an entry
// with no options and for a secret entry, whose masked source-name input is unchanged.
type envChooser struct {
	active   bool
	required bool
	options  []formats.Option
	index    int
	kept     string
}

// newEnvChooser builds the chooser for one entry. The stored value wins, then the declared
// default, then "(not set)"; a required entry has no "(not set)" label, since the launch
// refuses a missing required key, so it falls back to the first option. A value outside the
// options is kept as a trailing label, so saving without touching the row never drops it.
func newEnvChooser(entry formats.ToolManifestEnvElem, existingEnv map[string]string) envChooser {
	if entry.Secret || len(entry.Options) == 0 {
		return envChooser{}
	}
	chooser := envChooser{active: true, required: entry.Required, options: entry.Options}
	current := envRowValue(entry, existingEnv)
	if current == "" {
		return chooser
	}
	chooser.index = slices.Index(chooser.values(), current)
	if chooser.index < 0 {
		chooser.kept = current
		chooser.index = len(chooser.values()) - 1
	}
	return chooser
}

// values are the stored values behind each label, in label order: "" for "(not set)".
func (chooser envChooser) values() []string {
	var values []string
	if !chooser.required {
		values = append(values, "")
	}
	for _, option := range chooser.options {
		values = append(values, option.Value)
	}
	if chooser.kept != "" {
		values = append(values, chooser.kept)
	}
	return values
}

func (chooser envChooser) labels() []string {
	labels := []string{}
	if !chooser.required {
		labels = append(labels, chooserNotSetLabel)
	}
	for _, option := range chooser.options {
		labels = append(labels, option.Value)
	}
	if chooser.kept != "" {
		labels = append(labels, chooser.kept+chooserOutsideSuffix)
	}
	return labels
}

// selectedValue is what the row stores: "" for "(not set)", else the chosen option or kept value.
func (chooser envChooser) selectedValue() string {
	return chooser.values()[chooser.index]
}

// onEnvChooserRow reports whether the env step is showing a chooser row.
func (model Model) onEnvChooserRow() bool {
	return model.configureStep == 2 && model.envChooser.active
}

// updateEnvChooserKey cycles the chooser on left, right or space; every other key is ignored
// because a chooser row takes no typed text.
func (model Model) updateEnvChooserKey(key tea.KeyMsg) Model {
	count := len(model.envChooser.labels())
	switch key.String() {
	case "left":
		model.envChooser.index = (model.envChooser.index - 1 + count) % count
	case "right", " ", "space":
		model.envChooser.index = (model.envChooser.index + 1) % count
	}
	return model
}

func envChooserView(model Model, entry formats.ToolManifestEnvElem) string {
	return envRowPrompt(entry) + chooserRow(model.envChooser.labels(), model.envChooser.index)
}

// configureEnvControl is the env row's control: the chooser for an option-bearing row, else
// the text input.
func configureEnvControl(model Model) string {
	if model.envChooser.active {
		return envChooserView(model, model.configureEnvEntries[model.configureEnvIndex])
	}
	return model.configureEnv.View()
}

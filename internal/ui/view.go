package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/hivedeck/internal/discovery"
	"github.com/hive-controls/hivedeck/internal/manifest"
	"github.com/hive-controls/hivedeck/internal/profile"
	"github.com/hive-controls/hivedeck/internal/runs"
)

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	selectedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	problemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	collisionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
)

// Column widths for a tool row. The source kind is the longest of "repo"/"user"/"npm"/
// "product", and the state cell holds the longest of "unsupported"/"invalid" — both fixed, so
// every row's columns line up and only the title flexes with the terminal.
const (
	kindColumnWidth  = 7
	stateColumnWidth = 11
	badgeColumnWidth = 1
	// Everything a tool row spends on something other than the title, in render order:
	// the two-column cursor prefix, a two-column gap, the kind cell, a two-column gap, the
	// badge cell, one space, and the state cell.
	toolRowFixedWidth = 2 + 2 + kindColumnWidth + 2 + badgeColumnWidth + 1 + stateColumnWidth
	minimumTitleWidth = 12
	// The detail view's field labels are padded to one width so every VALUE starts in the
	// same column. Ragged labels put "e2e-doctor.schema.json" and "/tmp/.../repo" two columns
	// apart, and a reader scanning values down the screen has to re-find the column each line.
	// Sized to the longest label the view emits, "Config schema: ".
	detailLabelWidth = 15
)

func (model Model) View() string {
	if model.configuring {
		return model.configureView()
	}
	if model.promptingSpec {
		return model.specPromptView()
	}
	if model.showPanes {
		return model.paneView()
	}
	if model.showRuns {
		return model.runListView()
	}
	if model.detail {
		return model.detailView()
	}
	return model.listView()
}

func (model Model) specPromptView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate("Launch "+model.pendingTool.Title, width)))
	content.WriteString("\n\n")
	content.WriteString(model.specInput.View() + "\n")
	content.WriteString(model.specArgvPreview(width))
	content.WriteString("\n")
	content.WriteString(mutedStyle.Render("enter launch · esc cancel") + "\n")
	appendNotice(&content, model.notice, width)
	return content.String()
}

// specArgvPreview shows the command the typed path is being substituted into. Asking for a
// path with nothing but a tool title above it gives no way to tell WHERE the path lands —
// which argument, of which command — until after the launch has already happened.
func (model Model) specArgvPreview(width int) string {
	resolved, err := manifest.ResolveLaunch(model.pendingTool.Manifest, model.repositoryRoot, model.specInput.Value())
	if err != nil {
		return ""
	}
	preview := encodeArgv(append([]string{resolved.Command}, resolved.Args...))
	return mutedStyle.Render(pad("Runs: ", detailLabelWidth)+truncatePath(preview, width-detailLabelWidth)) + "\n"
}

func (model Model) runListView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render("Runs"))
	content.WriteString("\n\n")
	if len(model.runRows) == 0 {
		content.WriteString(mutedStyle.Render("No runs recorded.") + "\n")
	}
	// title, blank, rows, hint, blank, notice, help.
	capacity := listCapacity(model.layoutHeight(), 6+noticeRows(model.notice))
	start, end := visibleWindow(model.runCursor, len(model.runRows), capacity)
	columns := model.runColumnWidths(model.runRows[start:end])
	for index := start; index < end; index++ {
		line := model.runRow(model.runRows[index], columns, width-2, index == model.runCursor)
		if index == model.runCursor {
			line = selectedStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		content.WriteString(line + "\n")
	}
	appendScrollHint(&content, start, end, len(model.runRows))
	content.WriteString("\n")
	appendNotice(&content, model.notice, width)
	content.WriteString(mutedStyle.Render(truncate("↑/↓ move · enter open artifact · r/esc back · q quit", width)))
	return content.String()
}

// runColumns holds the content-derived width of each variable run-list column. Without them
// every field starts wherever the previous field happened to end, so "checks tab" and
// "web--storybook foreground" push the timestamp, exit code and outcome to different columns
// on adjacent rows and nothing can be scanned down the screen.
type runColumns struct {
	id      int
	tool    int
	mode    int
	exit    int
	outcome int
}

const startedColumnWidth = len("2006-01-02 15:04:05")

func (model Model) runColumnWidths(rows []runs.Row) runColumns {
	columns := runColumns{}
	for _, row := range rows {
		columns.id = max(columns.id, lipgloss.Width(row.ID))
		columns.tool = max(columns.tool, lipgloss.Width(row.Tool))
		columns.mode = max(columns.mode, lipgloss.Width(string(row.Mode)))
		columns.exit = max(columns.exit, lipgloss.Width(model.runExitCode(row)))
		columns.outcome = max(columns.outcome, lipgloss.Width(row.Outcome))
	}
	return columns
}

// runExitCode renders the code alone, or — when the row's tool declares an `exit` table for
// that code (toolspec v1.1) — the code with its declared meaning right beside it, so a person
// scanning the run list never has to go read the manifest to know what a code means.
func (model Model) runExitCode(row runs.Row) string {
	if row.ExitCode == nil {
		return "exit=—"
	}
	code := fmt.Sprintf("exit=%d", *row.ExitCode)
	if meaning := model.exitMeaning(row); meaning != "" {
		code += " (" + meaning + ")"
	}
	return code
}

func (model Model) exitMeaning(row runs.Row) string {
	if row.ExitCode == nil {
		return ""
	}
	tool, ok := model.toolByName(row.Tool)
	if !ok || tool.Manifest.Exit == nil {
		return ""
	}
	meaning, ok := tool.Manifest.Exit[strconv.Itoa(*row.ExitCode)]
	if !ok {
		return ""
	}
	text, ok := meaning.(string)
	if !ok {
		return ""
	}
	return text
}

// outcomeStyle colours the one field a person scans this list for. "failed" is the reason the
// run list exists; leaving it the same weight as "ok" makes the reader parse every row.
func outcomeStyle(outcome string) lipgloss.Style {
	switch outcome {
	case "failed":
		return problemStyle
	// Every live state is highlighted, not just the initial one: a row moving through
	// started -> progress is the whole point of watching a run that has not finished.
	case "running", "started", "progress", "needs-human":
		return collisionStyle
	default:
		return mutedStyle
	}
}

// cellStyler decides whether a row's individual cells carry their own colour. They must NOT
// when the row itself is styled: lipgloss closes an inner style with a full reset, which also
// closes the outer one, so a selected row's highlight ends at the first coloured cell instead
// of running the width of the row.
func cellStyler(rowIsStyled bool) func(lipgloss.Style, string) string {
	if rowIsStyled {
		return func(_ lipgloss.Style, text string) string { return text }
	}
	return func(style lipgloss.Style, text string) string { return style.Render(text) }
}

func (model Model) runRow(row runs.Row, columns runColumns, width int, selected bool) string {
	artifact := model.runArtifact(row)
	if artifact == "" {
		artifact = "—"
	}
	cell := cellStyler(selected)
	exitCode := model.runExitCode(row)
	spent := columns.id + columns.tool + columns.mode + startedColumnWidth +
		columns.exit + columns.outcome + 12
	return cell(mutedStyle, pad(row.ID, columns.id)) + "  " +
		pad(row.Tool, columns.tool) + "  " +
		cell(mutedStyle, pad(string(row.Mode), columns.mode)) + "  " +
		cell(mutedStyle, row.StartedAt.Local().Format("2006-01-02 15:04:05")) + "  " +
		cell(mutedStyle, pad(exitCode, columns.exit)) + "  " +
		cell(outcomeStyle(row.Outcome), pad(row.Outcome, columns.outcome)) + "  " +
		truncatePath(artifact, width-spent)
}

// runArtifact is what the run list shows AND what the existing "enter" open affordance
// (openSelectedArtifact) opens: the artifact the run's own status stream reported, or — when
// nothing was reported and the manifest declares `outputs.evidenceDir` (toolspec v1.1) — the
// resolved evidence directory read back out of the run's own persisted environment.
func (model Model) runArtifact(row runs.Row) string {
	if row.Artifact != "" {
		return row.Artifact
	}
	tool, ok := model.toolByName(row.Tool)
	if !ok || tool.Manifest.Outputs == nil || tool.Manifest.Outputs.EvidenceDir == nil {
		return ""
	}
	return environmentValue(row.Env, *tool.Manifest.Outputs.EvidenceDir)
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if value, found := strings.CutPrefix(entry, prefix); found {
			return value
		}
	}
	return ""
}

func (model Model) listView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render("hivedeck"))
	content.WriteString("\n")
	content.WriteString(mutedStyle.Render(truncatePath(model.repositoryRoot, width)))
	content.WriteString("\n\n")
	content.WriteString(model.filter.View())
	content.WriteString("\n\n")
	tools := model.visibleTools()
	if len(tools) == 0 {
		content.WriteString(mutedStyle.Render("No tools match this filter."))
		content.WriteString("\n")
	}
	// title, root, blank, filter, blank, rows, hint, blank, notice, help.
	capacity := listCapacity(model.layoutHeight(), 8+noticeRows(model.notice))
	start, end := visibleWindow(model.cursor, len(tools), capacity)
	titleWidth := titleColumnWidth(tools[start:end], width)
	for index := start; index < end; index++ {
		content.WriteString(model.toolRow(tools[index], index == model.cursor, titleWidth))
		content.WriteString("\n")
	}
	appendScrollHint(&content, start, end, len(tools))
	content.WriteString("\n")
	appendNotice(&content, model.notice, width)
	content.WriteString(mutedStyle.Render(truncate(model.helpText(), width)))
	return content.String()
}

// titleColumnWidth sizes the title column to the widest title actually on screen rather than
// to the terminal. A title column stretched to a 120-column terminal strands the source-kind
// and state columns against the right edge, sixty columns of nothing away from the title they
// describe; sizing to the content keeps them beside it at any width.
func titleColumnWidth(visible []discovery.Tool, width int) int {
	available := width - toolRowFixedWidth
	widest := minimumTitleWidth
	for _, tool := range visible {
		if titleWidth := lipgloss.Width(tool.Title); titleWidth > widest {
			widest = titleWidth
		}
	}
	if widest > available {
		widest = available
	}
	if widest < minimumTitleWidth {
		widest = minimumTitleWidth
	}
	return widest
}

func (model Model) toolRow(tool discovery.Tool, selected bool, titleWidth int) string {
	state, styleState := "", mutedStyle
	switch {
	case manifest.IsUnsupportedManifestVersion(tool.Problem):
		state = "unsupported"
	case tool.Problem != "":
		state, styleState = "invalid", problemStyle
	}
	rowIsStyled := selected || tool.Problem != ""
	cell := cellStyler(rowIsStyled)
	badge := " "
	if len(tool.Shadowed) > 0 {
		badge = cell(collisionStyle, "↑")
	}
	row := pad(tool.Title, titleWidth) + "  " + cell(mutedStyle, pad(string(tool.Source.Kind), kindColumnWidth)) +
		"  " + badge + " " + cell(styleState, pad(state, stateColumnWidth))
	if selected {
		return selectedStyle.Render("> " + row)
	}
	if tool.Problem != "" {
		return mutedStyle.Render("  " + row)
	}
	return "  " + row
}

func (model Model) detailView() string {
	tools := model.visibleTools()
	if len(tools) == 0 {
		return model.listView()
	}
	width := model.layoutWidth()
	tool := tools[clampCursor(model.cursor, len(tools))]
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate(tool.Title, width)))
	content.WriteString("\n")
	content.WriteString(mutedStyle.Render(truncate(tool.Name+" · "+string(tool.Source.Kind), width)))
	content.WriteString("\n\n")
	var body strings.Builder
	appendDetailSections(&body, tool, model.repositoryRoot, width)
	interactive := toolInteractive(tool)
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, interactive)
	body.WriteString(field("Launch mode: ", string(mode)))
	if model.profileProblem != "" {
		body.WriteString(problemStyle.Render(truncate("Profile: "+model.profileProblem, width)) + "\n")
	}
	// title, name, blank, body, blank, notice, help.
	capacity := listCapacity(model.layoutHeight(), 5+noticeRows(model.notice))
	lines := clipLines(strings.Split(strings.TrimRight(body.String(), "\n"), "\n"), capacity)
	content.WriteString(strings.Join(lines, "\n") + "\n")
	content.WriteString("\n")
	appendNotice(&content, model.notice, width)
	content.WriteString(mutedStyle.Render(truncate(model.helpText(), width)))
	return content.String()
}

func (model Model) configureView() string {
	tool, ok := model.currentTool()
	if !ok {
		return model.listView()
	}
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate("Configure "+tool.Title, width)))
	content.WriteString("\n\n")
	content.WriteString(field("Profile: ", truncatePath(".hivedeck/"+profile.FileName, width-9)))
	content.WriteString(field("Mode: ", modeChooser(model.configureMode)))
	content.WriteString("\n")
	if model.configureStep == 0 {
		content.WriteString(mutedStyle.Render("←/→ choose · enter continue · esc cancel") + "\n")
	} else if model.configureStep == 1 {
		content.WriteString(model.configureArgs.View() + "\n")
		content.WriteString(mutedStyle.Render(truncate("Comma-separated argv · enter continue · esc cancel", width)) + "\n")
	} else {
		content.WriteString(configureEnvDescription(model, width))
		content.WriteString(model.configureEnv.View() + "\n")
		content.WriteString(mutedStyle.Render(truncate(configureEnvHint(model), width)) + "\n")
	}
	appendNotice(&content, model.notice, width)
	return content.String()
}

// configureEnvDescription shows the current declared env[] row's description, if it has one —
// blank for the free-text form (no declared entries to describe).
func configureEnvDescription(model Model, width int) string {
	if len(model.configureEnvEntries) == 0 {
		return ""
	}
	entry := model.configureEnvEntries[model.configureEnvIndex]
	if entry.Description == nil || *entry.Description == "" {
		return ""
	}
	return mutedStyle.Render(wrap(*entry.Description, width)) + "\n"
}

func configureEnvHint(model Model) string {
	if len(model.configureEnvEntries) == 0 {
		return "Use env.NAME for secret values · enter save · esc cancel"
	}
	more := model.configureEnvIndex+1 < len(model.configureEnvEntries)
	if model.configureEnvEntries[model.configureEnvIndex].Secret {
		if more {
			return "Name of the environment variable that holds this secret · enter next · esc cancel"
		}
		return "Name of the environment variable that holds this secret · enter save · esc cancel"
	}
	if more {
		return "enter next · esc cancel"
	}
	return "enter save · esc cancel"
}

// modeChooser shows every launch mode, not just the current one. "←/→ choose" over a single
// highlighted value gives a person nothing to choose between — they have to press the key to
// discover that the other options exist, and press it twice more to find their way back.
func modeChooser(current profile.LaunchMode) string {
	rendered := make([]string, 0, 3)
	for _, mode := range []profile.LaunchMode{profile.ModeForeground, profile.ModeTab, profile.ModePane} {
		if mode == current {
			rendered = append(rendered, selectedStyle.Render(" "+string(mode)+" "))
			continue
		}
		rendered = append(rendered, mutedStyle.Render(" "+string(mode)+" "))
	}
	return strings.Join(rendered, " ")
}

func appendDetailSections(content *strings.Builder, tool discovery.Tool, repositoryRoot string, width int) {
	if tool.Description != "" {
		content.WriteString(wrap(tool.Description, width) + "\n\n")
	}
	content.WriteString(labelledPath("Source: ", tool.Source.Path, width))
	switch {
	case manifest.IsUnsupportedManifestVersion(tool.Problem):
		content.WriteString(mutedStyle.Render(wrap(tool.Problem, width)) + "\n")
	case tool.Problem != "":
		content.WriteString(problemStyle.Render(wrap("Validation: "+tool.Problem, width)) + "\n")
	default:
		appendLaunch(content, tool, repositoryRoot, width)
		appendConfig(content, tool, width)
		appendStatus(content, tool)
	}
	appendCollisions(content, tool, width)
}

// labelledPath keeps the label intact and spends the remaining columns on the path's tail,
// so "Source:" never scrolls off while the filename does. The label is muted and the value is
// not: eight declaration lines in one weight read as a wall, and the values are what is being
// read — the labels only say which value is which.
func labelledPath(label, value string, width int) string {
	return mutedStyle.Render(pad(label, detailLabelWidth)) + truncatePath(value, width-detailLabelWidth) + "\n"
}

func field(label, value string) string {
	return mutedStyle.Render(pad(label, detailLabelWidth)) + value + "\n"
}

func appendLaunch(content *strings.Builder, tool discovery.Tool, repositoryRoot string, width int) {
	resolved, err := manifest.ResolveLaunch(tool.Manifest, repositoryRoot, "<prompt>")
	if err != nil {
		content.WriteString(problemStyle.Render(wrap("Launch: "+err.Error(), width)) + "\n")
		return
	}
	argumentVector := append([]string{resolved.Command}, resolved.Args...)
	content.WriteString(labelledPath("Launch argv: ", encodeArgv(argumentVector), width))
	content.WriteString(labelledPath("Launch cwd: ", resolved.Cwd, width))
}

// encodeArgv renders argv as JSON WITHOUT encoding/json's default HTML escaping. That escaping
// exists for JSON embedded in a web page, and there is no web page here: it turned the very
// common `{spec}` placeholder into `"<prompt>"` on screen, so the one line a person
// reads to check what will actually run was unreadable exactly where it mattered.
func encodeArgv(argumentVector []string) string {
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(argumentVector); err != nil {
		return fmt.Sprintf("%q", argumentVector)
	}
	return strings.TrimRight(encoded.String(), "\n")
}

func appendConfig(content *strings.Builder, tool discovery.Tool, width int) {
	if tool.Manifest.Config == nil {
		content.WriteString(field("Config: ", "none"))
		return
	}
	if tool.Manifest.Config.Schema != nil {
		content.WriteString(labelledPath("Config schema: ", *tool.Manifest.Config.Schema, width))
	}
	if len(tool.Manifest.Config.Files) > 0 {
		content.WriteString(labelledPath("Config files: ", strings.Join(tool.Manifest.Config.Files, ", "), width))
	}
}

func appendStatus(content *strings.Builder, tool discovery.Tool) {
	if tool.Manifest.Status == nil {
		content.WriteString(field("Status: ", "exit-code (fallback)"))
		return
	}
	status := string(tool.Manifest.Status.Kind)
	if tool.Manifest.Status.Fd != nil {
		status += " on " + string(*tool.Manifest.Status.Fd)
	}
	content.WriteString(field("Status: ", status))
}

func appendCollisions(content *strings.Builder, tool discovery.Tool, width int) {
	if len(tool.Shadowed) == 0 {
		return
	}
	content.WriteString(collisionStyle.Render("↑ Shadowed definitions") + "\n")
	for _, shadowed := range tool.Shadowed {
		content.WriteString("  " + truncatePath(shadowed.Source.Path, width-2) + "\n")
	}
}

// wrap reflows prose to the terminal width on word boundaries. Prose is the one thing in this
// UI that should NOT be truncated — a description exists to be read — but letting the terminal
// hard-wrap it mid-word is what made long descriptions look broken.
func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		lines = append(lines, wrapParagraph(paragraph, width)...)
	}
	return strings.Join(lines, "\n")
}

func wrapParagraph(paragraph string, width int) []string {
	words := strings.Fields(paragraph)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := words[0]
	for _, word := range words[1:] {
		if lipgloss.Width(current)+1+lipgloss.Width(word) > width {
			lines = append(lines, current)
			current = word
			continue
		}
		current += " " + word
	}
	return append(lines, current)
}

func (model Model) helpText() string {
	if model.showHelp {
		return "type: filter · backspace: edit · esc: clear/back · ↑/↓: move · enter: details/launch · c: configure · r: runs · q: quit"
	}
	if model.detail {
		return "enter launch · c configure · esc back · ? help · q quit"
	}
	return "↑/↓ move · enter details · r runs · ? help · q quit"
}

func appendScrollHint(content *strings.Builder, start, end, count int) {
	hint := scrollHint(start, end, count)
	if hint == "" {
		return
	}
	content.WriteString("  " + mutedStyle.Render(hint) + "\n")
}

func appendNotice(content *strings.Builder, notice string, width int) {
	if notice == "" {
		return
	}
	content.WriteString(collisionStyle.Render(wrap(notice, width)) + "\n")
}

func noticeRows(notice string) int {
	if notice == "" {
		return 0
	}
	return 1
}

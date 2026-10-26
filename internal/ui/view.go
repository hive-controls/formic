package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/hive-controls/formic/internal/catalog"
	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/formats"
	"github.com/hive-controls/formic/internal/manifest"
	"github.com/hive-controls/formic/internal/mission"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	selectedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	problemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	collisionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	abortedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))
)

// Column widths for a tool row. The source kind is the longest of "repo"/"user"/"npm"/
// "product", and the state cell holds the longest of "unsupported"/"invalid" — both fixed, so
// every row's columns line up and only the title flexes with the terminal.
const (
	kindColumnWidth  = 7
	stateColumnWidth = 11
	// 8 holds the longest state word, "starting".
	liveColumnWidth  = 8
	badgeColumnWidth = 1
	// Everything a tool row spends on something other than the title, in render order:
	// the two-column cursor prefix, a two-column gap, the kind cell, a two-column gap, the
	// badge cell, one space, and the state cell.
	toolRowFixedWidth = 2 + 2 + kindColumnWidth + 2 + badgeColumnWidth + 1 + stateColumnWidth
	// The live column (two-column gap plus the cell) is dropped on a terminal too narrow to keep
	// the title column at its minimum beside it.
	liveColumnExtraWidth = 2 + liveColumnWidth
	minimumTitleWidth    = 12
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
	if model.promptingEquip {
		return model.equipPromptView()
	}
	if model.promptingInputs {
		return model.inputPromptView()
	}
	if model.promptingSpec {
		return model.specPromptView()
	}
	if model.showEquipView {
		return model.equipView()
	}
	if model.showMissions {
		return model.missionsView()
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

// equipPromptView renders C1's launch-time equip prompt: the current step's secret text field
// (pre-filled env.NAME, masked with nothing — a reference is not a secret) or the gate branch's
// provider cursor list, populated from exactly the need's OWN declared providers.
func (model Model) equipPromptView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate("Launch "+model.pendingTool.Title, width)))
	content.WriteString("\n\n")
	if len(model.equipNeeds) == 0 {
		model.renderNotice(&content, width)
		return content.String()
	}
	status := model.equipNeeds[model.equipStep]
	content.WriteString(mutedStyle.Render(fmt.Sprintf("Need %d/%d", model.equipStep+1, len(model.equipNeeds))) + "\n")
	if name, ok := equip.SecretDetails(status.Need); ok {
		content.WriteString(mutedStyle.Render("secret: "+name) + "\n")
		content.WriteString(model.equipField.View() + "\n")
		content.WriteString(mutedStyle.Render("enter save · esc cancel") + "\n")
	} else if class, _, providers, ok := equip.GateDetails(status.Need); ok {
		content.WriteString(mutedStyle.Render("gate: "+class) + "\n")
		for index, provider := range providers {
			if index == model.equipProviderCursor {
				content.WriteString(selectedStyle.Render("> "+provider) + "\n")
			} else {
				content.WriteString("  " + provider + "\n")
			}
		}
		content.WriteString(mutedStyle.Render("↑/↓ choose · enter select · esc cancel") + "\n")
	}
	model.renderNotice(&content, width)
	return content.String()
}

// equipView renders C2's read-only equip view: every need across the whole armory, grouped by
// identity, met/unmet, and which tools declare it — never a value, since equipViewNeeds itself
// only ever carries a reference or a provider name (equip.ArmoryNeedStatus), not a secret.
func (model Model) equipView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render("Equip"))
	content.WriteString("\n\n")
	if len(model.equipViewNeeds) == 0 {
		content.WriteString(mutedStyle.Render("No needs declared across the armory.") + "\n")
	}
	for _, status := range model.equipViewNeeds {
		content.WriteString(equipViewRow(status) + "\n")
	}
	content.WriteString("\n")
	content.WriteString(mutedStyle.Render(truncate("esc/left back · q quit", width)))
	return content.String()
}

// equipViewRow renders one grouped need's identity (secret name or gate class — the two kinds
// C1 can prompt for; runtime/network needs still group and render, labelled generically, since
// they can appear in the armory even though C1 never prompts for them), met/unmet, and the
// declaring tools.
func equipViewRow(status equip.ArmoryNeedStatus) string {
	identity := equipNeedIdentity(status.Need)
	metStyle := problemStyle
	metText := "unmet"
	switch status.State {
	case equip.StateMet:
		metStyle, metText = mutedStyle, "met"
	case equip.StateUnchecked:
		metStyle, metText = mutedStyle, "unchecked"
	}
	return identity + "  " + metStyle.Render(metText) + "  " + mutedStyle.Render("tools: "+strings.Join(status.Tools, ", "))
}

func equipNeedIdentity(need formats.Need) string {
	if name, ok := equip.SecretDetails(need); ok {
		return "secret: " + name
	}
	if class, _, _, ok := equip.GateDetails(need); ok {
		return "gate: " + class
	}
	return "need"
}

// missionsView renders E2a's missions section: every discovered mission, one row each, an
// explicit empty-state line when there are none.
func (model Model) missionsView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render("Missions"))
	content.WriteString("\n\n")
	if len(model.missionRows) == 0 {
		content.WriteString(mutedStyle.Render("No missions discovered.") + "\n")
	}
	for index, discoveredMission := range model.missionRows {
		line := missionRow(discoveredMission)
		if index == model.missionCursor {
			line = selectedStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		content.WriteString(line + "\n")
	}
	content.WriteString("\n")
	model.renderNotice(&content, width)
	content.WriteString(mutedStyle.Render(truncate("↑/↓ move · enter launch · esc/m back · q quit", width)))
	return content.String()
}

// missionRow shows the mission's name, its step tool, and its source — personal, or the
// catalogued toolkit's own name (best-effort via catalog.DescriptorAt; falls back to the source
// root path when the descriptor cannot be read) — plus the shadowed badge when a later
// definition of the same name was shadowed by this one (§9).
func missionRow(discoveredMission mission.Mission) string {
	source := "personal"
	if !discoveredMission.Personal {
		source = discoveredMission.SourceRoot
		if toolkit, err := catalog.DescriptorAt(discoveredMission.SourceRoot); err == nil {
			source = toolkit.Name
		}
	}
	row := discoveredMission.Definition.Name + "  " +
		mutedStyle.Render("tool: "+discoveredMission.Definition.Steps[0].Tool+" · source: "+source)
	if len(discoveredMission.ShadowedBy) > 0 {
		row += "  " + collisionStyle.Render("↑ shadowed")
	}
	return row
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
	model.renderNotice(&content, width)
	return content.String()
}

// specArgvPreview shows the command the typed path is being substituted into. Asking for a
// path with nothing but a tool title above it gives no way to tell WHERE the path lands —
// which argument, of which command — until after the launch has already happened.
func (model Model) specArgvPreview(width int) string {
	resolved, err := manifest.ResolveLaunch(model.pendingTool.Manifest, model.repositoryRoot, model.specInput.Value(), nil)
	if err != nil {
		return ""
	}
	resolved.Args = append(resolved.Args, model.repositoryProfile.Tools[model.pendingTool.Name].Args...)
	preview := encodeArgv(append([]string{resolved.Command}, resolved.Args...))
	return mutedStyle.Render(pad("Runs: ", detailLabelWidth)+truncatePath(preview, width-detailLabelWidth)) + "\n"
}

// inputPromptView renders the current step of the generalized sequential input prompt (N10):
// title, a "N/total" position hint, the input's declared kind (when the manifest gives one), the
// masked-or-plain textinput field itself, and the enter/esc hint. A sensitive input's typed value
// is never rendered here beyond textinput's own EchoPassword masking, set once at
// focusCurrentInputField.
func (model Model) inputPromptView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate("Launch "+model.pendingTool.Title, width)))
	content.WriteString("\n\n")
	if len(model.inputSchema) > 0 {
		entry := model.inputSchema[model.inputStep]
		content.WriteString(mutedStyle.Render(fmt.Sprintf("Input %d/%d", model.inputStep+1, len(model.inputSchema))) + "\n")
		if hint := inputKindHint(entry); hint != "" {
			content.WriteString(mutedStyle.Render(hint) + "\n")
		}
	}
	content.WriteString(model.inputField.View() + "\n")
	content.WriteString(mutedStyle.Render("enter next · esc cancel") + "\n")
	model.renderNotice(&content, width)
	return content.String()
}

func inputKindHint(input formats.Input) string {
	if input.Kind == nil || *input.Kind == "" {
		return ""
	}
	return "kind: " + *input.Kind
}

func (model Model) runListView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render("Runs"))
	content.WriteString("\n\n")
	if len(model.runRows) == 0 {
		content.WriteString(mutedStyle.Render("No runs recorded.") + "\n")
	}
	start, end := model.runListWindowBounds()
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
	model.renderNotice(&content, width)
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
	// live is true when any row in the window belongs to a catalog tool that declares a probe;
	// only then does the run list carry a live column.
	live bool
}

const startedColumnWidth = len("2006-01-02 15:04:05")

// runArtifactCell is the artifact column text: the artifact, or a dash.
func (model Model) runArtifactCell(row runs.Row) string {
	if artifact := model.runArtifact(row); artifact != "" {
		return artifact
	}
	return "—"
}

func (model Model) runColumnWidths(rows []runs.Row) runColumns {
	columns := runColumns{}
	for _, row := range rows {
		columns.id = max(columns.id, lipgloss.Width(row.ID))
		columns.tool = max(columns.tool, lipgloss.Width(runToolLabel(row)))
		columns.mode = max(columns.mode, lipgloss.Width(string(row.Mode)))
		columns.exit = max(columns.exit, lipgloss.Width(model.runExitCode(row)))
		columns.outcome = max(columns.outcome, lipgloss.Width(row.Outcome))
		if tool, ok := model.toolByName(row.Tool); ok && declaresProbe(tool) {
			columns.live = true
		}
	}
	return columns
}

// runToolLabel is the run list's tool cell (E2c: "the run list shows Row.Mission when set — a
// short column or suffix"): the tool name alone for an ordinary run, or with a mission suffix
// for one launched through `formic mission run`/the deck's own missions section.
func runToolLabel(row runs.Row) string {
	label := row.Tool
	if row.Action != "" {
		label += " · " + row.Action
	}
	if row.Mission == "" {
		return label
	}
	return label + " · mission: " + row.Mission
}

// runExitCode renders the code alone, or — when the row's tool declares an `exit` table for
// that code (manifest v1.1) — the code with its declared meaning right beside it, so a person
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
	if !ok {
		return ""
	}
	return exitMeaningFor(tool, row.Action, *row.ExitCode)
}

// outcomeStyle colours the one field a person scans this list for. "failed" is the reason the
// run list exists; leaving it the same weight as "ok" makes the reader parse every row.
func outcomeStyle(outcome string) lipgloss.Style {
	switch outcome {
	case "failed":
		return problemStyle
	// "aborted" means the tool never finished and produced no exit code: neither a pass nor
	// the tool's own failure, so it gets a warn colour distinct from both.
	case "aborted":
		return abortedStyle
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
	artifact := model.runArtifactCell(row)
	cell := cellStyler(selected)
	exitCode := model.runExitCode(row)
	spent := columns.id + columns.tool + columns.mode + startedColumnWidth +
		columns.exit + columns.outcome + 12
	liveColumn := ""
	if columns.live {
		spent += liveColumnExtraWidth
		liveColumn = "  " + model.runLiveCell(row, cell)
	}
	return cell(mutedStyle, pad(row.ID, columns.id)) + "  " +
		pad(runToolLabel(row), columns.tool) +
		liveColumn + "  " +
		cell(mutedStyle, pad(string(row.Mode), columns.mode)) + "  " +
		cell(mutedStyle, row.StartedAt.Local().Format("2006-01-02 15:04:05")) + "  " +
		cell(mutedStyle, pad(exitCode, columns.exit)) + "  " +
		cell(outcomeStyle(row.Outcome), pad(row.Outcome, columns.outcome)) + "  " +
		truncatePath(artifact, width-spent)
}

// runArtifact is what the run list shows AND what the existing "enter" open affordance
// (openSelectedArtifact) opens: the artifact the run's own status stream reported, or — when
// nothing was reported and the manifest declares `outputs.evidenceDir` (manifest v1.1) — the
// resolved evidence directory, resolved once at launch time and stored on the row directly
// (N10: the durable row's environment is redacted to names only, so this can no longer be
// read back out of it).
func (model Model) runArtifact(row runs.Row) string {
	if row.Artifact != "" {
		return row.Artifact
	}
	return row.EvidenceDir
}

func (model Model) listView() string {
	width := model.layoutWidth()
	var content strings.Builder
	content.WriteString(titleStyle.Render("formic"))
	content.WriteString("\n")
	content.WriteString(mutedStyle.Render(truncatePath(model.repositoryRoot, width)))
	content.WriteString("\n")
	appendUncataloguedOffer(&content, model.uncatalogued, width)
	content.WriteString("\n")
	content.WriteString(model.filter.View())
	content.WriteString("\n\n")
	tools := model.visibleTools()
	if len(tools) == 0 {
		content.WriteString(mutedStyle.Render("No tools match this filter."))
		content.WriteString("\n")
	}
	// title, root, offer, blank, filter, blank, rows, hint, blank, notice, help.
	start, end := model.listWindowBounds(len(tools))
	titleWidth := titleColumnWidth(tools[start:end], width-model.liveColumnSpend())
	for index := start; index < end; index++ {
		content.WriteString(model.toolRow(tools[index], index == model.cursor, titleWidth))
		content.WriteString("\n")
	}
	appendScrollHint(&content, start, end, len(tools))
	content.WriteString("\n")
	model.renderNotice(&content, width)
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
		"  " + badge + " " + cell(styleState, pad(state, stateColumnWidth)) + model.liveSuffix(tool, cell)
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
	if toolHasActions(tool) {
		return model.actionMenuView(tool)
	}
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate(tool.Title, width)))
	content.WriteString("\n")
	content.WriteString(mutedStyle.Render(truncate(tool.Name+" · "+string(tool.Source.Kind), width)))
	content.WriteString("\n\n")
	var body strings.Builder
	appendDetailSections(&body, tool, model.repositoryRoot, model.repositoryProfile.Tools[tool.Name].Args, width)
	interactive := toolInteractive(tool)
	mode := profile.ResolveMode(model.repositoryProfile, tool.Name, interactive)
	body.WriteString(field("Launch mode: ", string(mode)))
	if model.profileProblem != "" {
		body.WriteString(problemStyle.Render(truncate("Profile: "+model.profileProblem, width)) + "\n")
	}
	// title, name, blank, body, blank, notice, help.
	capacity := listCapacity(model.layoutHeight(), 5+model.noticeLineCount())
	lines := clipLines(strings.Split(strings.TrimRight(body.String(), "\n"), "\n"), capacity)
	content.WriteString(strings.Join(lines, "\n") + "\n")
	content.WriteString("\n")
	model.renderNotice(&content, width)
	content.WriteString(mutedStyle.Render(truncate(model.helpText(), width)))
	return content.String()
}

func (model Model) configureView() string {
	tool, ok := model.currentTool()
	if !ok {
		return model.listView()
	}
	width := model.layoutWidth()
	paneReason := paneUnavailableReason(tool)
	splitAvailable := model.configureSplitAvailable()
	var content strings.Builder
	content.WriteString(titleStyle.Render(truncate(configureTitle(tool, model.configureAction), width)))
	content.WriteString("\n\n")
	content.WriteString(field("Profile: ", truncatePath(".formic/"+profile.FileName, width-9)))
	content.WriteString(field("Mode: ", modeChooser(model.configureMode, splitAvailable, paneReason)))
	if paneReason != "" {
		content.WriteString(mutedStyle.Render(truncate(paneReason, width)) + "\n")
	}
	content.WriteString("\n")
	if model.configureStep == 0 {
		content.WriteString(mutedStyle.Render("←/→ choose · enter continue · esc cancel") + "\n")
	} else if model.configureStep == 1 {
		content.WriteString(configureArgumentsView(model, width))
	} else {
		content.WriteString(configureEnvDescription(model, width))
		content.WriteString(configureEnvControl(model) + "\n")
		content.WriteString(mutedStyle.Render(truncate(configureEnvHint(model), width)) + "\n")
	}
	model.renderNotice(&content, width)
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
	if model.envChooser.active {
		if more {
			return "←/→ choose · enter next · esc cancel"
		}
		return "←/→ choose · enter save · esc cancel"
	}
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

// modeChooser shows every mode that can actually apply, not just the current one. "←/→ choose"
// over a single highlighted value gives a person nothing to choose between — they have to press
// the key to discover that the other options exist, and press it twice more to find their way
// back. A withheld mode (pane, when paneReason is non-empty) is left out of the row entirely
// rather than offered and refused; configureView prints paneReason as its own line.
func modeChooser(current profile.LaunchMode, splitAvailable bool, paneReason string) string {
	modes := []profile.LaunchMode{profile.ModeForeground, profile.ModeTab}
	if splitAvailable {
		modes = append(modes, profile.ModeSplit)
	}
	if paneReason == "" {
		modes = append(modes, profile.ModePane)
	}
	rendered := make([]string, 0, len(modes))
	for _, mode := range modes {
		if mode == current {
			rendered = append(rendered, selectedStyle.Render(" "+string(mode)+" "))
			continue
		}
		rendered = append(rendered, mutedStyle.Render(" "+string(mode)+" "))
	}
	return strings.Join(rendered, " ")
}

func appendDetailSections(content *strings.Builder, tool discovery.Tool, repositoryRoot string, profileArguments []string, width int) {
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
		appendLaunch(content, tool, repositoryRoot, profileArguments, width)
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

func appendLaunch(content *strings.Builder, tool discovery.Tool, repositoryRoot string, profileArguments []string, width int) {
	resolved, err := manifest.ResolveLaunch(tool.Manifest, repositoryRoot, "<prompt>", nil)
	if err != nil {
		content.WriteString(problemStyle.Render(wrap("Launch: "+err.Error(), width)) + "\n")
		return
	}
	argumentVector := append(append([]string{resolved.Command}, resolved.Args...), profileArguments...)
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
		return "type: filter · backspace: edit · esc: clear/back · ↑/↓: move · enter: details/launch · c: configure · r: runs · m: missions · e: equip · q: quit"
	}
	if model.confirmingAction {
		return "y confirm · any other key cancels"
	}
	if tool, ok := model.currentTool(); ok && model.detail && toolHasActions(tool) {
		return "↑/↓ action · enter run · c configure · esc back · ? help · q quit"
	}
	if model.detail {
		return "enter launch · c configure · esc back · ? help · q quit"
	}
	return "↑/↓ move · enter details · r runs · m missions · e equip · ? help · q quit"
}

func appendScrollHint(content *strings.Builder, start, end, count int) {
	hint := scrollHint(start, end, count)
	if hint == "" {
		return
	}
	content.WriteString("  " + mutedStyle.Render(hint) + "\n")
}

// appendUncataloguedOffer renders N8 point 3's one-line add-this-repo offer, or nothing when no
// offer is pending — the offer is never a blocking prompt, just a line the list view carries
// alongside everything else until the person presses "a" or the deck exits.
func appendUncataloguedOffer(content *strings.Builder, uncatalogued *catalog.Uncatalogued, width int) {
	if uncatalogued == nil {
		return
	}
	line := fmt.Sprintf("this repo has a toolkit (%s) — add it? press a", uncatalogued.Name)
	content.WriteString(collisionStyle.Render(truncate(line, width)) + "\n")
}

func uncataloguedOfferRows(uncatalogued *catalog.Uncatalogued) int {
	if uncatalogued == nil {
		return 0
	}
	return 1
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

// renderNotice writes E2b's multi-line mission pre-flight refusal panel (model.noticeProblems —
// ruling 3: "a refusal is shown as the FULL problem list", one line per problem, never a single
// wrapped line that could truncate) when one is pending, or the plain single-line notice
// otherwise — never both, so a stale problem list from an earlier refusal never bleeds into an
// unrelated later notice.
func (model Model) renderNotice(content *strings.Builder, width int) {
	if len(model.noticeProblems) > 0 {
		appendProblemNotice(content, model.noticeProblems, width)
		return
	}
	appendNotice(content, model.notice, width)
}

// appendProblemNotice renders one line per problem, each its own line — the "multi-line notice
// panel" a single truncated/wrapped line would fail to be.
func appendProblemNotice(content *strings.Builder, problems []string, width int) {
	for _, problem := range problems {
		content.WriteString(collisionStyle.Render(truncate(problem, width)) + "\n")
	}
}

// noticeLineCount is the layout-capacity counterpart to renderNotice: the number of lines the
// pending notice/problem panel actually occupies, so a view's row-capacity math reserves room
// for every problem line, not just one.
func (model Model) noticeLineCount() int {
	if len(model.noticeProblems) > 0 {
		return len(model.noticeProblems)
	}
	return noticeRows(model.notice)
}

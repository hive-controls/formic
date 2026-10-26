package ui

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/equip"
	"github.com/hive-controls/formic/internal/launch"
	"github.com/hive-controls/formic/internal/manifest"
)

// serviceOffer is the offer made when a launch is refused for an unmet service need (spec §22e):
// run the needed tool's action. It is the first unmet service need only, and accepting it never
// resumes the refused launch (R8) — the person launches it again once the service is up.
type serviceOffer struct {
	forTool string
	target  discovery.Tool
	action  string
	reason  string
}

// noteLaunchRefusal records why a launch was refused: the plain notice, upgraded to an offer
// when the refusal is an unmet service need whose tool the catalog can run.
func (model Model) noteLaunchRefusal(tool discovery.Tool, err error) Model {
	model.serviceOffer = nil
	model.notice = describeLaunchError(tool, err)
	var refused *launch.NeedsRefused
	if !errors.As(err, &refused) {
		return model
	}
	if offer := model.offerFor(refused); offer != nil {
		model.serviceOffer = offer
		model.notice = model.serviceOfferLine()
	}
	return model
}

// offerFor builds the offer for the first unmet service need in refused, or nil when there is
// nothing safe to offer: the target must be in the catalog and valid, and a need's action must be
// one the target declares (an action-less target takes its ordinary launch, so names none).
func (model Model) offerFor(refused *launch.NeedsRefused) *serviceOffer {
	for _, status := range refused.Needs {
		name, action, isService := equip.ServiceDetails(status.Need)
		if !isService {
			continue
		}
		target, found := model.catalog.Find(name)
		if !found || target.Problem != "" || status.Reason == "" {
			return nil
		}
		if toolHasActions(target) && action != "" {
			if _, declared := manifest.ActionSpec(target.Manifest, action); !declared {
				return nil
			}
		}
		if !toolHasActions(target) && action != "" {
			return nil
		}
		return &serviceOffer{forTool: refused.Tool, target: target, action: action, reason: status.Reason}
	}
	return nil
}

// refusalNotice is the plain refusal the offer was made from.
func (model Model) refusalNotice() string {
	offer := model.serviceOffer
	title := offer.forTool
	if tool, found := model.toolByName(offer.forTool); found {
		title = tool.Title
	}
	return title + " cannot start: " + offer.reason
}

// serviceOfferLine is the notice that carries the offer, or "" when none is pending.
func (model Model) serviceOfferLine() string {
	offer := model.serviceOffer
	if offer == nil {
		return ""
	}
	line := model.refusalNotice() + ". "
	switch {
	case offer.action == "" && toolHasActions(offer.target):
		return line + "Open " + offer.target.Title + " · actions? [y/N]"
	case offer.action == "":
		return line + "Run " + offer.target.Title + "? [y/N]"
	}
	title := offer.action
	if spec, declared := manifest.ActionSpec(offer.target.Manifest, offer.action); declared {
		title = actionTitle(spec)
	}
	return line + "Run " + offer.target.Title + " · " + title + "? [y/N]"
}

// serviceOfferShowing is true while the offer's own line is still the notice, in a view that
// renders it and is not taking typed input. Otherwise the offer is not visible (another notice
// replaced it, or the person moved to the equip view, a prompt or the customize form while the
// probe ran) and must not answer keys.
func (model Model) serviceOfferShowing() bool {
	if model.serviceOffer == nil || model.notice != model.serviceOfferLine() {
		return false
	}
	typing := model.configuring || model.promptingEquip || model.promptingInputs || model.promptingSpec
	return !typing && !model.showEquipView
}

// updateServiceOfferKey answers the offer: y accepts, any other key dismisses and changes
// nothing else.
func (model Model) updateServiceOfferKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	offer := model.serviceOffer
	if keyName := key.String(); keyName != "y" && keyName != "Y" {
		model.notice = model.refusalNotice()
		model.serviceOffer = nil
		return model, nil
	}
	model.serviceOffer = nil
	model.missionInFlight, model.missionEvidenceRoot, model.noticeProblems = nil, "", nil
	model.notice = ""
	model = model.focusTool(offer.target)
	if offer.action == "" && toolHasActions(offer.target) {
		model.actionInFlight, model.actionConfirmed, model.actionResult = "", false, actionResult{}
		return model, nil
	}
	if offer.action == "" {
		return model.beginLaunch()
	}
	return model.beginActionLaunch(offer.target, offer.action)
}

// focusTool puts the cursor and detail view on tool, leaving every other view and clearing the
// list filter when it hides the tool.
func (model Model) focusTool(tool discovery.Tool) Model {
	model.showRuns, model.showMissions, model.showEquipView, model.showPanes = false, false, false, false
	model.detail, model.actionCursor = true, 0
	if indexOfTool(model.visibleTools(), tool.Name) < 0 {
		model.filter.SetValue("")
		model.searching = false
	}
	model.cursor = max(indexOfTool(model.visibleTools(), tool.Name), 0)
	return model
}

func indexOfTool(tools []discovery.Tool, name string) int {
	for index, tool := range tools {
		if tool.Name == name {
			return index
		}
	}
	return -1
}

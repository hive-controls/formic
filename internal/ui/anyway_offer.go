package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/profile"
)

// anywayOffer is the hint left by a launch skipped because the service already answers (spec §22
// c): everything that launch was called with, so one key can run it again past the skip. It is a
// hint, not a question — any key but "a" drops it and is handled normally.
type anywayOffer struct {
	tool      discovery.Tool
	spec      string
	mode      profile.LaunchMode
	inputs    map[string]string
	action    string
	confirmed bool
}

// dropPendingOffers drops both offers when a key is not answering them. A notice that is still an
// offer's own line would keep promising a key that no longer does that, so it reverts to the plain
// sentence the offer was made from; whatever the key's own handling then sets wins.
func (model Model) dropPendingOffers() Model {
	if model.serviceOffer != nil && model.notice == model.serviceOfferLine() {
		model.notice = model.refusalNotice()
	}
	if model.anywayOffer != nil && model.notice == model.anywayOfferLine() {
		model.notice = model.anywayOffer.tool.Title + " is already running."
	}
	model.serviceOffer, model.anywayOffer = nil, nil
	return model
}

// noteAlreadyRunning records the skip: the plain notice, upgraded to the launch-anyway hint unless
// the skipped launch belongs to a mission (a mission step has no key to answer for).
func (model Model) noteAlreadyRunning(tool discovery.Tool, spec string, mode profile.LaunchMode, inputs map[string]string) Model {
	model.serviceOffer, model.anywayOffer = nil, nil
	model.notice = tool.Title + " is already running."
	if model.missionInFlight != nil {
		return model
	}
	model.anywayOffer = &anywayOffer{
		tool: tool, spec: spec, mode: mode, inputs: inputs,
		action: model.actionInFlight, confirmed: model.actionConfirmed,
	}
	model.notice = model.anywayOfferLine()
	return model
}

// anywayOfferLine is the notice that carries the hint, or "" when none is pending.
func (model Model) anywayOfferLine() string {
	if model.anywayOffer == nil {
		return ""
	}
	return model.anywayOffer.tool.Title + " is already running. Press a to launch anyway."
}

// anywayOfferShowing is true while the hint's own line is still the notice, in a view that renders
// it and is not taking typed input — the same visibility rule as serviceOfferShowing.
func (model Model) anywayOfferShowing() bool {
	if model.anywayOffer == nil || model.notice != model.anywayOfferLine() {
		return false
	}
	typing := model.configuring || model.promptingEquip || model.promptingInputs || model.promptingSpec
	return !typing && !model.showEquipView
}

// launchAnywayNow runs the skipped launch again with Request.Anyway for exactly that one launch.
// The action state the first attempt had answered (in flight, confirmed) is restored from the
// offer, so nothing is asked again and nothing the first attempt had not satisfied is set. Service
// needs are still probed by the launch's own hold and still refuse.
func (model Model) launchAnywayNow() (tea.Model, tea.Cmd) {
	offer := model.anywayOffer
	model.anywayOffer = nil
	model.notice = ""
	model.actionInFlight, model.actionConfirmed = offer.action, offer.confirmed
	model.launchAnyway = true
	resumed, command := model.startLaunch(offer.tool, offer.spec, offer.mode, offer.inputs)
	if typed, ok := resumed.(Model); ok {
		typed.launchAnyway = false
		return typed, command
	}
	return resumed, command
}

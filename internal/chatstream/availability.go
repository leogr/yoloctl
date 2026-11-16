package chatstream

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (d *channelDriver) availabilityAction(action Action) error {
	refuse := func(message string) error {
		d.emit(Event{Kind: "control", ID: action.ID, Action: action.Kind, State: "refused", Err: errors.New(message)})
		return nil
	}
	if !validReference(action.ID, 80) || action.Channel != "" || action.Topic != nil || action.Catalog != nil {
		return refuse("invalid availability action")
	}
	if action.Kind == "away" && (action.Reason == nil || !validAwayReason(*action.Reason)) || action.Kind == "back" && action.Reason != nil {
		return refuse("invalid away reason")
	}
	if d.control != nil || d.pendingSend() {
		return refuse("another send or action is pending")
	}
	if d.setupPending() {
		return refuse("identity setup is pending; availability was not sent")
	}
	if action.Reason != nil {
		reason := *action.Reason
		action.Reason = &reason
	}
	// Capture the server buffer's latest cursor before submission. A numeric
	// buffered before this explicit action cannot confirm it, even if unread.
	if err := d.readOne(); err != nil {
		return err
	}
	if d.setupPending() {
		return refuse("identity setup is pending; availability was not sent")
	}
	request := Request{ID: action.ID, Op: action.Kind, Reason: action.Reason}
	raw, err := json.Marshal(request)
	if err != nil {
		return refuse("invalid availability request")
	}
	if _, err := decodeChannelRequest(raw, func(string) bool { return false }, false); err != nil {
		return refuse(err.Error())
	}

	d.control = &pendingChannelAction{Action: action, until: time.Now().Add(20 * time.Second), after: d.latest}
	reply, err := d.wire.exchange(request)
	if err != nil {
		return err
	}
	var queued struct {
		Status string `json:"status"`
	}
	if !reply.OK || json.Unmarshal(reply.Result, &queued) != nil || queued.Status != "queued" {
		return errors.New("availability queue outcome uncertain; reconnect before another action")
	}
	return nil
}

func (d *channelDriver) availabilityNumeric(r record) error {
	pending := d.control
	if pending == nil || r.Cursor <= pending.after {
		return nil
	}
	if r.Command == "306" && pending.Kind == "away" {
		d.ownAway = true
		d.ownReason = *pending.Reason
	} else if r.Command == "305" && pending.Kind == "back" {
		d.ownAway = false
		d.ownReason = ""
	} else {
		return nil
	}
	d.availabilityKnown = true
	d.emitAvailability()
	for _, channel := range d.channels {
		if channel.membership == "joined" {
			channel.adapter.members()
		}
	}
	d.finishControl("confirmed", nil)
	return nil
}

func (d *channelDriver) emitAvailability() {
	state := "unknown"
	if d.availabilityKnown {
		state = "back"
		if d.ownAway {
			state = "away"
		}
	}
	reason := ""
	if state == "away" {
		reason = d.ownReason
	}
	d.emit(Event{Kind: "availability", State: state, AwayReason: reason})
}

func (d *channelDriver) patchSelfMembers(channel *channelState, members []Member) {
	if !d.availabilityKnown {
		return
	}
	for i := range members {
		if members[i].Account == d.wire.header.SelfAccount && members[i].Nick == d.wire.header.Nick {
			members[i].Away = d.ownAway
			members[i].AwayReason = d.ownReason
			channel.adapter.roster[strings.ToLower(members[i].Nick)] = members[i]
		}
	}
}

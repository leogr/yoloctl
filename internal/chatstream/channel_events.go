package chatstream

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func (d *channelDriver) serverPrefix() string { return "yoloctl-" + d.wire.header.RoomID[:12] }
func (d *channelDriver) selfPrefix(prefix string) bool {
	return prefix == d.wire.header.Nick+"!"+d.wire.header.SelfAccount+"@airc"
}

func (d *channelDriver) finishControl(state string, err error) {
	d.finishControlCode(state, "", err)
}

func (d *channelDriver) finishControlCode(state, code string, err error) {
	if d.control == nil {
		return
	}
	action := d.control.Action
	d.control = nil
	d.emit(Event{Kind: "control", Channel: action.Channel, ID: action.ID, Action: action.Kind, State: state, Code: code, Err: err})
}

func (d *channelDriver) action(action Action) error {
	refuseCode := func(code, message string) error {
		d.emit(Event{Kind: "control", Channel: action.Channel, ID: action.ID, Action: action.Kind, State: "refused", Code: code, Err: errors.New(message)})
		return nil
	}
	refuse := func(message string) error { return refuseCode("", message) }
	if !validReference(action.ID, 80) || managedchat.ValidateChannelName(action.Channel) != nil {
		return refuse("invalid managed channel/action identity")
	}
	if action.Catalog != nil {
		if action.Kind != "join" || action.Topic != nil {
			return refuse("only a join without topic text accepts a trusted catalog")
		}
		if err := d.refreshCatalog(*action.Catalog); err != nil {
			return refuse("channel catalog refused before JOIN: " + err.Error())
		}
		action.Catalog = nil // pending controls retain only their original action
	}
	channel := d.channels[action.Channel]
	if channel == nil {
		if action.Kind == "join" {
			return refuseCode("unknown-channel", "unknown managed channel; create it first with yoloctl chat channel create "+strings.TrimPrefix(action.Channel, "#"))
		}
		return refuse("invalid managed channel/action identity")
	}
	if channel.deleted {
		return refuseCode("deleted-channel", errChannelDeleted.Error())
	}
	if d.control != nil || d.pendingSend() {
		return refuse("another send or channel action is pending")
	}
	if channel.membership == "unknown" || channel.membership == "joining" {
		return refuse("channel membership is unknown; reconnect explicitly before acting")
	}
	if action.Kind != "topic-set" && action.Topic != nil {
		return refuse("unexpected topic field")
	}
	request := Request{ID: action.ID, Channel: action.Channel}
	switch action.Kind {
	case "join":
		if channel.membership == "joined" {
			d.emit(Event{Kind: "membership", Channel: action.Channel, ID: action.ID, State: "joined"})
			d.emit(Event{Kind: "control", Channel: action.Channel, ID: action.ID, Action: action.Kind, State: "confirmed"})
			return nil
		}
		request.Op = "join"
	case "part":
		if channel.membership == "left" || channel.membership == "kicked" {
			channel.membership = "left"
			d.emit(Event{Kind: "membership", Channel: action.Channel, ID: action.ID, State: "left"})
			d.emit(Event{Kind: "control", Channel: action.Channel, ID: action.ID, Action: action.Kind, State: "confirmed"})
			return nil
		}
		request.Op = "part"
	case "topic-read", "topic-set", "topic-clear":
		if channel.membership != "joined" {
			return refuse("join this channel before using its topic")
		}
		if action.Kind == "topic-read" && channel.bootstrap != "ready" {
			return refuse("topic query was not sent: JOIN bootstrap 366 is unresolved; reconnect if it is unavailable")
		}
		request.Op = "topic"
		switch action.Kind {
		case "topic-set":
			if action.Topic == nil {
				return refuse("topic-set needs text")
			}
			text := *action.Topic // capture input before the caller can mutate it
			request.Topic, action.Topic = &text, &text
		case "topic-clear":
			empty := ""
			request.Topic = &empty
		}
	default:
		return refuse("unsupported managed channel action")
	}
	if d.setupPending() {
		return refuseCode(SetupPendingCode, "channel bootstrap/identity setup is pending; action was not sent")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return refuse("invalid channel action")
	}
	if _, err := decodeChannelRequest(raw, func(name string) bool { return d.channels[name] != nil }, false); err != nil {
		return refuse(err.Error())
	}
	d.control = &pendingChannelAction{Action: action, until: time.Now().Add(20 * time.Second)}
	reply, err := d.wire.exchange(request)
	if err != nil {
		return err
	}
	if !reply.OK {
		// A queued operation is acknowledged separately from IRC outcome. An
		// unexpected negative JSONL reply cannot prove no network side effect.
		return errors.New("channel action queue outcome is uncertain; inspect before retrying")
	}
	var queued struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(reply.Result, &queued) != nil || queued.Status != "queued" {
		return errors.New("invalid channel action queue acknowledgment")
	}
	if action.Kind == "join" {
		channel.membership = "joining"
		d.emit(Event{Kind: "membership", Channel: action.Channel, ID: action.ID, State: "joining"})
	}
	return nil
}

func numeric(command string) bool {
	return len(command) == 3 && command[0] >= '0' && command[0] <= '9' && command[1] >= '0' && command[1] <= '9' && command[2] >= '0' && command[2] <= '9'
}

func (d *channelDriver) record(r record) error {
	if r.Event == "receipt" {
		for name, channel := range d.channels {
			if p := channel.adapter.pending; p != nil && p.Receipt == r.Receipt {
				if r.Target != name {
					return errors.New("receipt target differs from its original channel")
				}
				return channel.adapter.record(r)
			}
		}
		return nil
	}
	if r.Event != "" {
		return errors.New("unsupported AIRC logical event")
	}
	if numeric(r.Command) && (r.Prefix != d.serverPrefix() || len(r.Params) == 0 || r.Params[0] != d.wire.header.Nick) {
		return errors.New("AIRC numeric does not match the current recipient/server")
	}
	if r.Command == "422" {
		// AIRC deliberately has no MOTD and preserves this welcome numeric in
		// its startup buffer. It is not a channel/history operation refusal.
		return nil
	}
	if (r.Command == "BATCH" || r.Command == "FAIL") && r.Prefix != d.serverPrefix() {
		return errors.New("AIRC history control lacks the expected server origin")
	}
	if handled, err := d.historyRecord(r); handled {
		return err
	}
	if r.Command == "ERROR" {
		if len(r.Params) > 0 && r.Params[0] == TakenOverReason {
			return ErrTakenOver
		}
		return errors.New("AIRC closed this membership; reconnect only after inspecting state")
	}
	if r.Command == "QUIT" || r.Command == "NICK" || r.Command == "AWAY" {
		if r.Command == "NICK" && d.selfPrefix(r.Prefix) {
			return errors.New("human nickname changed in this epoch; reconnect explicitly")
		}
		for _, channel := range d.channels {
			if err := channel.adapter.record(r); err != nil {
				return err
			}
		}
		return nil
	}
	if numeric(r.Command) && (r.Command[0] == '4' || r.Command[0] == '5') {
		return d.refusal(r)
	}
	name := eventChannel(r)
	channel := d.channels[name]
	if channel == nil {
		if d.selfPrefix(r.Prefix) && r.Command == "JOIN" {
			return errors.New("client joined a channel outside the trusted catalog")
		}
		return nil
	}
	if batch := r.Tags["batch"]; batch != "" {
		owner := d.batchOwner[batch]
		if owner == "" && d.historyLost {
			return nil
		}
		if owner != name || r.Command != "PRIVMSG" && r.Command != "NOTICE" {
			return errors.New("history child escaped its channel/batch")
		}
	}
	if err := d.membershipEvent(name, channel, r); err != nil {
		return err
	}
	if r.Command == "331" || r.Command == "332" || r.Command == "TOPIC" {
		return d.topicEvent(name, channel, r)
	}
	if r.Command == "366" && channel.bootstrap == "pending" && r.Cursor > channel.bootstrapAt {
		channel.bootstrap, channel.until, channel.setup = "ready", time.Time{}, true
	}
	if r.Command == "315" && channel.whoPending {
		channel.whoPending, channel.until = false, time.Time{}
	}
	if d.rejectIncompleteRoster(name, channel, r) {
		return nil
	}
	if err := channel.adapter.record(r); err != nil {
		return err
	}
	if batch := channel.adapter.batches[r.Tags["batch"]]; batch != nil && !batch.discard && d.history != nil {
		limit := 50
		if d.history.latest {
			limit = 20
		}
		if batch.messages > limit {
			return errors.New("AIRC history exceeded its requested page bound")
		}
	}
	d.boundRosters(name)
	return nil
}

func eventChannel(r record) string {
	switch r.Command {
	case "353":
		if len(r.Params) >= 3 {
			return r.Params[2]
		}
	case "331", "332", "352", "315", "366":
		if len(r.Params) >= 2 {
			return r.Params[1]
		}
	case "JOIN", "PART", "KICK", "MODE", "TOPIC", "PRIVMSG", "NOTICE":
		if len(r.Params) > 0 {
			return r.Params[0]
		}
	}
	return ""
}

func (d *channelDriver) membershipEvent(name string, channel *channelState, r record) error {
	state := ""
	if r.Command == "JOIN" && d.selfPrefix(r.Prefix) {
		channel.membership, channel.bootstrap, channel.bootstrapAt = "joined", "pending", r.Cursor
		channel.until = time.Now().Add(10 * time.Second)
		channel.topicNewer, channel.rosterInvalid = false, false
		d.initialHistory(name)
		state = "joined"
	} else if r.Command == "PART" && d.selfPrefix(r.Prefix) {
		state = "left"
	} else if r.Command == "KICK" && len(r.Params) >= 2 && r.Params[1] == d.wire.header.Nick {
		state = "kicked"
	}
	if state == "" {
		return nil
	}
	if channel.deleted {
		// A deleted channel stays deleted: its server KICK only ends local
		// state. Nothing rejoins, and a pending action on it has no
		// knowable outcome; other channels and the connection continue.
		channel.membership, channel.bootstrap = "left", "unknown"
		channel.until, channel.setup, channel.whoPending = time.Time{}, false, false
		channel.adapter.roster, channel.adapter.names = map[string]Member{}, nil
		d.cancelChannelHistory(name)
		if d.control != nil && d.control.Channel == name {
			d.finishControl("uncertain", errors.New("channel was deleted while this action was pending; inspect before acting again"))
		}
		return nil
	}
	if state != "joined" {
		channel.membership, channel.bootstrap = state, "unknown"
		channel.until, channel.setup, channel.whoPending = time.Time{}, false, false
		channel.adapter.roster, channel.adapter.names = map[string]Member{}, nil
		d.cancelChannelHistory(name)
		d.emit(Event{Kind: "topic", Channel: name, State: "unknown"})
	}
	id := ""
	matching := d.control != nil && d.control.Channel == name && ((state == "joined" && d.control.Kind == "join") || (state == "left" && d.control.Kind == "part"))
	if matching {
		id = d.control.ID
	}
	d.emit(Event{Kind: "membership", Channel: name, ID: id, State: state})
	if matching {
		d.finishControl("confirmed", nil)
	} else if state == "kicked" && d.control != nil && d.control.Channel == name {
		// A removal (an ACL change, or a deletion whose catalog has not
		// arrived yet) leaves the pending action's outcome unknown. It
		// finishes once as uncertain and is never replayed; the connection
		// and other channels continue.
		d.finishControl("uncertain", errors.New("removed from the channel while this action was pending; inspect before acting again"))
	}
	if state == "kicked" {
		d.emit(Event{Kind: "gap", Channel: name, Err: errors.New("removed from channel; room enrollment may remain, no automatic rejoin")})
	}
	return nil
}

func (d *channelDriver) topicEvent(name string, channel *channelState, r record) error {
	if channel.membership != "joined" {
		return nil
	}
	text, by, live := "", "", r.Command == "TOPIC"
	if live {
		if len(r.Params) != 2 {
			return errors.New("invalid topic event")
		}
		text = r.Params[1]
		by, _ = prefixIdentity(r.Prefix)
	} else if r.Command == "332" {
		if len(r.Params) != 3 {
			return errors.New("invalid topic numeric")
		}
		text = r.Params[2]
	} else if len(r.Params) != 3 {
		return errors.New("invalid empty-topic numeric")
	}
	if len(text) > 200 || strings.ContainsAny(text, "\x00\r\n") {
		return errors.New("invalid bounded channel topic")
	}
	id, matched := "", false
	if pending := d.control; pending != nil && pending.Channel == name {
		if !live && pending.Kind == "topic-read" && channel.bootstrap == "ready" {
			matched = true
		} else if live && d.selfPrefix(r.Prefix) {
			matched = pending.Kind == "topic-clear" && text == "" || pending.Kind == "topic-set" && pending.Topic != nil && text == *pending.Topic
		}
		if matched {
			id = pending.ID
		}
	}
	if !live && !matched && (channel.bootstrap != "pending" || channel.topicNewer) {
		return nil // stale bootstrap or unrequested numeric is not new topic proof
	}
	if live || matched {
		channel.topicNewer = true
	}
	d.emit(Event{Kind: "topic", Channel: name, ID: id, State: "known", Topic: text, TopicBy: by, TopicLive: live})
	if matched {
		d.finishControl("confirmed", nil)
	}
	return nil
}

func (d *channelDriver) refusal(r record) error {
	if d.control != nil {
		if len(r.Params) < 2 || r.Params[1] != d.control.Channel {
			return errors.New("uncorrelated IRC refusal while channel action is pending")
		}
		name := d.control.Channel
		channel := d.channels[name]
		if d.control.Kind == "join" || r.Command == "442" {
			channel.membership, channel.bootstrap = "left", "unknown"
			d.emit(Event{Kind: "membership", Channel: name, ID: d.control.ID, State: "left"})
		}
		code := ""
		if d.control.Kind == "join" && r.Command == "403" {
			code = "acl-denied"
		}
		d.finishControlCode("refused", code, errors.New("AIRC refused the channel action ("+r.Command+")"))
		return nil
	}
	if len(r.Params) >= 2 {
		if channel := d.channels[r.Params[1]]; channel != nil && channel.whoPending {
			channel.whoPending, channel.until = false, time.Time{}
			d.emit(Event{Kind: "gap", Channel: r.Params[1], Err: errors.New("channel identity query was refused; roster is unavailable")})
			return nil
		}
	}
	// JSONL v2 separately publishes send receipts. A raw numeric by itself
	// must never decide the outcome of an unrelated send or history page.
	d.emit(Event{Kind: "gap", Err: errors.New("AIRC reported an unassigned operation refusal (" + r.Command + ")")})
	return nil
}

func (d *channelDriver) boundRosters(changed string) {
	count := 0
	for _, channel := range d.channels {
		count += len(channel.adapter.roster) + len(channel.adapter.names)
	}
	if count <= maxRoster {
		return
	}
	channel := d.channels[changed]
	channel.rosterInvalid = true
	channel.adapter.roster, channel.adapter.names = map[string]Member{}, nil
	d.emit(Event{Kind: "gap", Channel: changed, Err: errors.New("aggregate roster bound reached; this roster is unavailable")})
}

func (d *channelDriver) rejectIncompleteRoster(name string, channel *channelState, r record) bool {
	if channel.rosterInvalid && (r.Command == "353" || r.Command == "366" || r.Command == "352" || r.Command == "315") {
		return true
	}
	if r.Command != "353" || len(r.Params) != 4 {
		return false
	}
	count := len(strings.Fields(r.Params[3]))
	for _, other := range d.channels {
		count += len(other.adapter.roster) + len(other.adapter.names)
	}
	if count <= maxRoster {
		return false
	}
	channel.rosterInvalid = true
	channel.adapter.roster, channel.adapter.names = map[string]Member{}, nil
	d.emit(Event{Kind: "gap", Channel: name, Err: errors.New("aggregate roster bound reached; partial NAMES is not a complete roster")})
	return true
}

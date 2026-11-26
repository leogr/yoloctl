package chatstream

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

type channelHistory struct {
	HistoryRequest
	latest bool
	batch  string
	until  time.Time
	found  Message
	wrong  bool
	rows   []Message // a window page, collected until its batch closes
}

// emitLatest reports how a channel's initial LATEST page ended: Err is nil
// once its batch closed. The view ignores it; an export waits for it.
func (d *channelDriver) emitLatest(channel, id string, err error) {
	d.emit(Event{Kind: "history-latest", Channel: channel, ID: id, Err: err})
}

func (d *channelDriver) requestHistory(request HistoryRequest, latest bool) {
	pending := &channelHistory{HistoryRequest: request, latest: latest}
	channel := d.channels[request.Channel]
	refused := func() {
		d.failHistory(pending, errors.New("history request was not sent: membership, continuity or another page is pending"))
	}
	if channel != nil && channel.deleted {
		d.failHistory(pending, errChannelDeleted)
		return
	}
	anchor, anchors := "", 0
	for _, a := range []string{request.Before, request.Lookup, request.Around, request.After} {
		if a != "" {
			anchor, anchors = a, anchors+1
		}
	}
	window := request.Around != "" || request.After != ""
	if channel == nil || channel.membership != "joined" || d.historyLost || !validReference(request.ID, 80) ||
		(!latest && !validReference(anchor, 128)) || anchors > 1 ||
		window && !request.Window || request.Window && request.Lookup != "" ||
		len(d.queue) >= managedchat.MaxChannels {
		refused()
		return
	}
	if d.history != nil && d.history.Channel == request.Channel {
		refused()
		return
	}
	for _, queued := range d.queue {
		if queued.Channel == request.Channel {
			refused()
			return
		}
	}
	d.queue = append(d.queue, pending)
}

func (h *channelHistory) wireRequest() Request {
	r := Request{Op: "history", Channel: h.Channel}
	switch {
	case h.latest:
		r.Direction, r.Reference, r.Limit = "LATEST", "*", 20
	case h.Lookup != "":
		r.Direction, r.Reference, r.Limit = "AROUND", "msgid="+h.Lookup, 1
	case h.Around != "":
		r.Direction, r.Reference, r.Limit = "AROUND", "msgid="+h.Around, 50
	case h.After != "":
		r.Direction, r.Reference, r.Limit = "AFTER", "msgid="+h.After, 50
	default:
		r.Direction, r.Reference, r.Limit = "BEFORE", "msgid="+h.Before, 50
	}
	return r
}

// advance runs one bounded setup/history request between event reads. WHO and
// controls/sends do not overlap: generic unlabelled errors must not confirm or
// refuse a different in-flight operation.
func (d *channelDriver) advance() error {
	if d.control != nil || d.pendingSend() {
		return nil
	}
	for _, channel := range d.channels {
		if channel.whoPending {
			return nil
		}
	}
	for _, entry := range d.wire.header.Channels {
		channel := d.channels[entry.Name]
		if !channel.setup {
			continue
		}
		channel.setup, channel.whoPending = false, true
		channel.until = deadlineAfter(d.partialTimeout)
		reply, err := d.wire.exchange(Request{Op: "who", Channel: entry.Name})
		if err != nil {
			return err
		}
		if !reply.OK {
			return errors.New("channel identity setup was not queued; reconnect explicitly")
		}
		return nil
	}
	if d.history != nil || len(d.queue) == 0 || d.historyLost {
		return nil
	}
	d.history, d.queue = d.queue[0], d.queue[1:]
	history := d.history
	history.until = deadlineAfter(d.partialTimeout)
	channel := d.channels[history.Channel]
	if channel == nil || channel.membership != "joined" {
		d.finishHistory(false, errors.New("channel left before history request"))
		return nil
	}
	reply, err := d.wire.exchange(history.wireRequest())
	if err != nil {
		return err
	}
	if !reply.OK {
		d.historyGap(errors.New("history request was not confirmed queued"))
		return nil
	}
	channel.adapter.initialHistory = history.latest
	if !history.latest && history.Lookup == "" && !history.Window {
		channel.adapter.olderRequests = 1
	}
	return nil
}

func (d *channelDriver) initialHistory(channel string) {
	d.requestHistory(HistoryRequest{ID: fmt.Sprintf("initial-%s-%d", channel, d.wire.serial), Channel: channel}, true)
}

func (d *channelDriver) failHistory(request *channelHistory, err error) {
	if err == nil && (request.latest || request.Lookup != "" || request.Window) {
		err = errors.New("history request ended without its batch; continuity is unknown")
	}
	switch {
	case request.latest:
		d.emitLatest(request.Channel, request.ID, err)
	case request.Window:
		d.emit(Event{Kind: "history-window", Channel: request.Channel, ID: request.ID, Err: err})
	case request.Lookup != "":
		d.emit(Event{Kind: "history-lookup", Channel: request.Channel, ID: request.ID, Err: err})
	default:
		d.emit(Event{Kind: "history-page", Channel: request.Channel, ID: request.ID, Count: -1, Err: err})
	}
}

func (d *channelDriver) finishHistory(success bool, err error) {
	if d.history == nil {
		return
	}
	request := d.history
	switch {
	case !success:
		d.failHistory(request, err)
	case request.latest:
		d.emitLatest(request.Channel, request.ID, nil)
	case request.Lookup != "":
		if request.wrong {
			d.failHistory(request, errors.New("AIRC returned a different message ID"))
		} else {
			d.emit(Event{Kind: "history-lookup", Channel: request.Channel, ID: request.ID, Message: request.found})
		}
	case request.Window:
		d.emit(Event{Kind: "history-window", Channel: request.Channel, ID: request.ID, Messages: request.rows})
	}
	if err != nil && request.Lookup == "" && !request.Window {
		d.emit(Event{Kind: "gap", Channel: request.Channel, Err: err})
	}
	if channel := d.channels[request.Channel]; channel != nil {
		channel.adapter.initialHistory, channel.adapter.olderRequests = false, 0
	}
	d.history = nil
}

func (d *channelDriver) historyGap(err error) {
	if d.history != nil && (d.history.Lookup != "" || d.history.Window) {
		d.finishHistory(false, err)
	} else {
		d.finishHistory(false, nil)
	}
	for _, request := range d.queue {
		d.failHistory(request, err)
	}
	d.queue, d.historyLost = nil, true
	for _, channel := range d.channels {
		channel.adapter.historyLost = true
		channel.adapter.initialHistory, channel.adapter.olderRequests = false, 0
		for _, batch := range channel.adapter.batches {
			batch.gapped, batch.discard = true, true
		}
	}
	d.emit(Event{Kind: "gap", Err: err})
}

func (d *channelDriver) cancelChannelHistory(channel string) {
	if d.history != nil && d.history.Channel == channel {
		// Without a labelled cancellation acknowledgment an in-flight batch
		// could still arrive; quarantine global paging instead of misrouting it.
		d.historyGap(errors.New("channel left during history; further paging requires explicit reconnect"))
		return
	}
	kept := d.queue[:0]
	for _, request := range d.queue {
		switch {
		case request.Channel != channel:
			kept = append(kept, request)
		default:
			d.failHistory(request, errors.New("channel left before its history was read"))
		}
	}
	d.queue = kept
}

func (d *channelDriver) historyRecord(r record) (bool, error) {
	if r.Command == "FAIL" && len(r.Params) > 0 && r.Params[0] == "CHATHISTORY" {
		if len(r.Params) < 3 {
			return true, errors.New("history failure lacks channel attribution")
		}
		if d.historyLost {
			return true, nil
		}
		if d.history == nil || r.Params[2] != d.history.Channel {
			return true, errors.New("history failure targets another request/channel")
		}
		if d.history.Lookup != "" || d.history.Window {
			if d.history.batch != "" {
				return true, errors.New("AIRC refused a history lookup inside its batch")
			}
			switch {
			case r.Params[1] != "HISTORY_LOST":
			case d.history.Lookup != "" || d.history.Around != "":
				d.finishHistory(true, nil) // the message itself is gone: a definite miss
				return true, nil
			default:
				// An edge page's anchor is gone: nothing is known of either side.
				d.finishHistory(false, errors.New("the message this page continues from is no longer retained"))
				return true, nil
			}
		}
		d.finishHistory(false, errors.New("AIRC refused that channel history request"))
		return true, nil
	}
	if r.Command != "BATCH" {
		return false, nil
	}
	if len(r.Params) == 0 || len(r.Params[0]) < 2 || !validReference(r.Params[0][1:], 128) {
		return true, errors.New("invalid history batch identity")
	}
	id := r.Params[0][1:]
	if strings.HasPrefix(r.Params[0], "-") {
		owner := d.batchOwner[id]
		if owner == "" {
			if d.historyLost {
				return true, nil
			}
			return true, errors.New("unowned history batch close")
		}
		if err := d.channels[owner].adapter.record(r); err != nil {
			return true, err
		}
		delete(d.batchOwner, id)
		if d.history != nil && d.history.batch == id {
			d.finishHistory(true, nil)
		}
		return true, nil
	}
	if !strings.HasPrefix(r.Params[0], "+") || len(r.Params) != 3 || r.Params[1] != "chathistory" || r.Tags["batch"] != "" {
		// JSONL v2 assembles nested multiline before delivery and retains the
		// parent chathistory tag on its complete logical message. Never parse
		// fallback multiline fragments as a second IRC implementation here.
		return true, errors.New("unexpected unassembled or nested history batch")
	}
	owner := r.Params[2]
	if d.channels[owner] == nil || d.batchOwner[id] != "" || len(d.batchOwner) >= managedchat.MaxChannels {
		return true, errors.New("invalid or unbounded history batch owner")
	}
	if !d.historyLost && (d.history == nil || d.history.Channel != owner || d.history.batch != "") {
		return true, errors.New("history batch does not match its requested channel")
	}
	d.batchOwner[id] = owner
	if d.history != nil {
		d.history.batch = id
	}
	return true, d.channels[owner].adapter.record(r)
}

package chatstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type channelState struct {
	adapter       *driver
	membership    string
	bootstrap     string // pending, ready, unknown
	bootstrapAt   uint64
	until         time.Time
	setup         bool
	whoPending    bool
	topicNewer    bool // live TOPIC/explicit result since this JOIN beats bootstrap context
	rosterInvalid bool
	// deleted: the name was deleted after this connection opened; it stays
	// unusable here even if a channel with a new ID reuses the name.
	deleted bool
}

type pendingChannelAction struct {
	Action
	until time.Time
}

// channelDriver shares the existing logical AIRC decoder for each channel,
// while owning exactly one wire connection, cursor and send/control slot.
type channelDriver struct {
	wire        *driver
	catalog     Header // last accepted desired metadata; wire.header.Nick is live
	channels    map[string]*channelState
	emit        func(Event)
	control     *pendingChannelAction
	history     *channelHistory
	queue       []*channelHistory
	batchOwner  map[string]string
	historyLost bool
	// origin is the room generation this connection opened at; the relay
	// refuses names deleted after it, and the driver mirrors that before Write.
	origin uint64
}

// RunChannels owns a single confined controller stream. Closed UI queues never
// cause a reconnect/replay. Every operation captures its target and caller ID.
func RunChannels(ctx context.Context, in io.ReadCloser, out io.WriteCloser, outgoing <-chan Send, older <-chan HistoryRequest, actions <-chan Action, catalogs <-chan Catalog, emit func(Event)) error {
	return newChannelDriver(in, out, emit).run(ctx, outgoing, older, actions, catalogs)
}

func newChannelDriver(in io.ReadCloser, out io.WriteCloser, emit func(Event)) *channelDriver {
	return &channelDriver{wire: &driver{in: in, out: out, read: bufio.NewReaderSize(in, ResponseLimit+1)}, channels: map[string]*channelState{}, batchOwner: map[string]string{}, emit: emit}
}

func (d *channelDriver) run(ctx context.Context, outgoing <-chan Send, older <-chan HistoryRequest, actions <-chan Action, catalogs <-chan Catalog) (err error) {
	in, out, emit := d.wire.in, d.wire.out, d.emit
	closeStream := func() { _ = in.Close(); _ = out.Close() }
	stop := context.AfterFunc(ctx, func() { d.wire.closeFor("was closed by this view", closeStream) })
	defer stop()
	defer closeStream()
	defer func() {
		d.endPending(errors.New("connection ended before confirmation; inspect state before another action"))
		emit(Event{Kind: "connection", Err: err})
	}()
	timeout := streamReplyTimeout // read once, before any callback can run
	initial := time.AfterFunc(timeout, func() {
		d.wire.closeFor(fmt.Sprintf("sent no room header and client handshake within %v", timeout), closeStream)
	})
	defer initial.Stop()
	if err := d.wire.frame(&d.wire.header); err != nil {
		return err
	}
	if d.wire.header.Event == NickHeldEvent {
		return ErrNickHeld
	}
	if err := validateHeader(d.wire.header); err != nil {
		return err
	}
	d.wire.header = normalizedHeader(d.wire.header)
	// The wire header names this connection's live nickname; the desired
	// catalog carries the saved one, as every catalog read does.
	human, _ := headerHuman(d.wire.header) // validated above
	d.catalog = normalizedHeader(d.wire.header)
	d.catalog.Nick = human.Nick
	var ready envelope
	if err := d.wire.frame(&ready); err != nil {
		return err
	}
	var limits Limits
	h := d.wire.header
	if ready.Event != "ready" || ready.Version != 2 || !validReference(ready.Epoch, 80) || ready.Account != h.SelfAccount || ready.Nick != h.Nick || json.Unmarshal(ready.Multiline, &limits) != nil || limits.MaxBytes <= 0 || limits.MaxLines <= 0 {
		return errors.New("AIRC did not establish the expected managed JSONL v2 identity")
	}
	initial.Stop()
	d.wire.epoch = ready.Epoch
	d.origin = h.Generation
	d.wire.allowedChannel = func(channel string) bool { return d.channels[channel] != nil && !d.channels[channel].deleted }
	for _, channel := range h.Channels {
		d.addChannel(channel.Name, "left")
	}
	// Only #space is configured for startup; queued self JOIN/bootstrap must
	// still be consumed before asserting membership or query readiness.
	d.channels[h.Channel].membership = "unknown"
	d.channels[h.Channel].until = time.Now().Add(15 * time.Second)
	limits.MaxBytes = min(limits.MaxBytes, MaxMessageBytes)
	emit(Event{Kind: "connection", Connected: true, Limits: limits, Attachments: d.wire.header.Attachments})
	// The header establishes identity, not whether the server applied the
	// desired catalog. Keep it unobserved until a selected catalog read arrives.
	emit(Event{Kind: "catalog", Catalog: &Catalog{Header: normalizedHeader(d.catalog), RuntimeUnobserved: true}})
	for {
		if err := d.deadlines(); err != nil {
			return err
		}
		if err := d.advance(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case send, ok := <-outgoing:
			if !ok {
				return nil
			}
			if err := d.send(send); err != nil {
				return err
			}
		case request, ok := <-older:
			if !ok {
				older = nil
			} else {
				d.requestHistory(request, false)
			}
		case action, ok := <-actions:
			if !ok {
				actions = nil
			} else if err := d.action(action); err != nil {
				return err
			}
		case catalog, ok := <-catalogs:
			if !ok {
				catalogs = nil
			} else {
				d.receiveCatalog(catalog)
			}
		default:
		}
		if err := d.readOne(); err != nil {
			return err
		}
	}
}

func (d *channelDriver) addChannel(name, membership string) {
	h := d.wire.header
	h.Channel = name
	state := &channelState{membership: membership, bootstrap: "unknown"}
	state.adapter = &driver{header: h, batches: map[string]*historyBatch{}, roster: map[string]Member{}, exchangeFn: d.wire.exchange}
	state.adapter.emit = func(event Event) {
		event.Channel = name
		if event.Kind == "message" {
			event.Message.Channel = name
		}
		if event.Kind == "history-page" && d.history != nil && d.history.Channel == name {
			event.ID = d.history.ID
		}
		d.emit(event)
	}
	d.channels[name] = state
}

func (d *channelDriver) readOne() error {
	reply, err := d.wire.exchange(Request{Op: "read", After: d.wire.cursor, Limit: 1, WaitMS: 250})
	if err != nil {
		return err
	}
	if !reply.OK {
		return errors.New("AIRC event read failed; reconnect explicitly")
	}
	var page struct {
		Epoch        string   `json:"epoch"`
		Events       []record `json:"events"`
		Next, Latest uint64
		Gap          bool `json:"gap"`
	}
	if json.Unmarshal(reply.Result, &page) != nil || page.Epoch != d.wire.epoch || len(page.Events) > 1 || page.Next < d.wire.cursor || page.Next > page.Latest {
		return errors.New("invalid AIRC read cursor or epoch")
	}
	if page.Gap {
		d.gap()
		if d.control != nil {
			return errors.New("control outcome uncertain after event loss; reconnect explicitly")
		}
	}
	if len(page.Events) == 0 && page.Next != d.wire.cursor {
		return errors.New("AIRC advanced an empty read")
	}
	for _, event := range page.Events {
		if event.Cursor <= d.wire.cursor || event.Cursor != page.Next {
			return errors.New("AIRC read event cursor mismatch")
		}
		if err := d.record(event); err != nil {
			return err
		}
	}
	d.wire.cursor = page.Next // never skip to latest
	return nil
}

func (d *channelDriver) pendingSend() bool {
	for _, channel := range d.channels {
		if channel.adapter.pending != nil {
			return true
		}
	}
	return false
}

func (d *channelDriver) setupPending() bool {
	for _, channel := range d.channels {
		if channel.bootstrap == "pending" || channel.setup || channel.whoPending {
			return true
		}
	}
	return false
}

func (d *channelDriver) send(send Send) error {
	channel := d.channels[send.Channel]
	if channel != nil && channel.deleted {
		d.emit(Event{Kind: "delivery", Channel: send.Channel, ID: send.ID, State: "rejected", Err: fmt.Errorf("message was not sent: %w", errChannelDeleted)})
		return nil
	}
	if channel == nil || channel.membership != "joined" || d.control != nil || d.pendingSend() || d.setupPending() {
		d.emit(Event{Kind: "delivery", Channel: send.Channel, ID: send.ID, State: "rejected", Err: errors.New("message was not sent: channel membership/setup is unconfirmed or another action is pending")})
		return nil
	}
	return channel.adapter.send(send)
}

func (d *channelDriver) endPending(err error) {
	for name, channel := range d.channels {
		if pending := channel.adapter.pending; pending != nil {
			d.emit(Event{Kind: "delivery", Channel: name, ID: pending.ID, State: "uncertain", Err: err})
			channel.adapter.pending = nil
		}
	}
	if d.control != nil {
		d.finishControl("uncertain", err)
	}
	for _, request := range append([]*channelHistory{d.history}, d.queue...) {
		switch {
		case request == nil:
		case request.latest:
			d.emitLatest(request.Channel, request.ID, err)
		default:
			d.emit(Event{Kind: "history-page", Channel: request.Channel, ID: request.ID, Count: -1, Err: err})
		}
	}
}

func (d *channelDriver) deadlines() error {
	now := time.Now()
	if d.control != nil && now.After(d.control.until) {
		return errors.New("channel action acknowledgment timed out; outcome uncertain")
	}
	for name, channel := range d.channels {
		if channel.until.IsZero() || !now.After(channel.until) {
			continue
		}
		channel.until, channel.setup, channel.whoPending = time.Time{}, false, false
		channel.bootstrap = "unknown"
		d.emit(Event{Kind: "gap", Channel: name, Err: errors.New("channel bootstrap/roster timed out; reconnect before relying on its state")})
		if channel.membership == "unknown" || channel.membership == "joining" {
			return errors.New("startup channel membership was not confirmed")
		}
		// Missing 366 never makes a topic query eligible. Known membership may
		// remain, but this channel's topic/roster evidence stays unavailable.
	}
	if d.history != nil && now.After(d.history.until) {
		d.historyGap(errors.New("history response timed out; continuity is unknown"))
	}
	return nil
}

func (d *channelDriver) gap() {
	d.historyGap(errors.New("AIRC event buffer has a gap; missing history is not proof of failed delivery"))
	for name, channel := range d.channels {
		channel.membership, channel.bootstrap = "unknown", "unknown"
		channel.setup, channel.whoPending, channel.until = false, false, time.Time{}
		channel.adapter.roster, channel.adapter.names = map[string]Member{}, nil
		d.emit(Event{Kind: "membership", Channel: name, State: "unknown"})
		d.emit(Event{Kind: "topic", Channel: name, State: "unknown"})
	}
}

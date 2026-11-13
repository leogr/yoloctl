package chatstream

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ExportResult is one channel's export: the messages the server returned,
// oldest first in the server's order, or why that channel failed.
type ExportResult struct {
	Channel  string
	Messages []Message
	// Joined reports that the export joined the channel for the read, and
	// PartErr why leaving it again failed, if it did.
	Joined  bool
	PartErr error
	Err     error
}

// SetupPendingCode refuses a channel action while a channel's startup or
// join setup is still running; the action was not sent and may be repeated.
const SetupPendingCode = "setup-pending"

// setupRetry is the pause before repeating an action refused only because
// setup was pending. The driver's own setup deadline bounds the repetition.
const setupRetry = 100 * time.Millisecond

// errExportConnection ends every channel not yet exported.
var errExportConnection = errors.New("the chat connection ended")

// Export reads the retained history of each channel over a running
// RunChannels stream whose only consumer it is: events are that stream's
// events, and older and actions its request queues. defaultChannel is the
// room's default channel, which the stream joins at startup.
//
// A channel's history is its initial LATEST page followed by BEFORE pages
// from the oldest message received, until the server answers with an empty
// page: nothing older is retained. Live messages are not part of it. A
// channel the session has not joined is joined for the read and parted
// afterwards. A gap, a refused or failed page, or losing the channel ends that
// channel's export with the reason; other channels continue. done receives
// each channel's result in order. The returned error is the connection's:
// it ended or never opened (ErrNickHeld when another session holds the
// nickname).
func Export(ctx context.Context, events <-chan Event, older chan<- HistoryRequest, actions chan<- Action, defaultChannel string, channels []string, done func(ExportResult)) error {
	x := &exporter{ctx: ctx, events: events, older: older, actions: actions, membership: map[string]string{defaultChannel: "unknown"}, latest: map[string]*latestPage{}}
	finishRest := func(from int, err error) {
		for _, channel := range channels[from:] {
			done(ExportResult{Channel: channel, Err: err})
		}
	}
	if err := x.connect(defaultChannel); err != nil {
		finishRest(0, err)
		return err
	}
	for i, channel := range channels {
		result := x.channel(channel)
		done(result)
		if x.connErr != nil {
			finishRest(i+1, errExportConnection)
			return x.connErr
		}
		if err := ctx.Err(); err != nil {
			finishRest(i+1, err)
			return err
		}
	}
	return nil
}

type latestPage struct {
	messages []Message
	done     bool
	err      error
}

type exporter struct {
	ctx        context.Context
	events     <-chan Event
	older      chan<- HistoryRequest
	actions    chan<- Action
	membership map[string]string
	latest     map[string]*latestPage
	connErr    error
	serial     int
	active     string // the channel being read; its gaps end it
	activeErr  error
	page       []Message // the open older page of active
	pageID     string
	connected  bool
}

func (x *exporter) id(kind string) string {
	x.serial++
	return fmt.Sprintf("export-%s-%d", kind, x.serial)
}

// observe records one event's effect on the export's state.
func (x *exporter) observe(e Event) {
	switch e.Kind {
	case "connection":
		if e.Connected {
			x.connected = true
			return
		}
		x.connErr = e.Err
		if x.connErr == nil {
			x.connErr = errExportConnection
		}
	case "membership":
		x.membership[e.Channel] = e.State
		if e.Channel == x.active && e.State != "joined" && e.State != "joining" && x.activeErr == nil {
			x.activeErr = fmt.Errorf("the channel was %s during the export", e.State)
		}
	case "gap":
		// After a lost continuity the driver refuses further pages itself.
		if (e.Channel == "" || e.Channel == x.active) && x.active != "" && x.activeErr == nil {
			x.activeErr = errors.New("history continuity was lost")
			if e.Err != nil {
				x.activeErr = fmt.Errorf("history continuity was lost: %w", e.Err)
			}
		}
	case "history-latest":
		page := x.latestFor(e.Channel)
		page.done, page.err = true, e.Err
	case "message":
		m := e.Message
		switch {
		case m.Older && m.Channel == x.active && x.pageID != "":
			x.page = append(x.page, m)
		case m.Replay && !m.Older:
			if page := x.latestFor(m.Channel); !page.done {
				page.messages = append(page.messages, m)
			}
		}
	}
}

func (x *exporter) latestFor(channel string) *latestPage {
	page := x.latest[channel]
	if page == nil {
		page = &latestPage{}
		x.latest[channel] = page
	}
	return page
}

// wait observes events until match accepts one or the connection ends.
func (x *exporter) wait(match func(Event) bool) (Event, error) {
	for {
		if x.connErr != nil {
			return Event{}, x.connErr
		}
		select {
		case <-x.ctx.Done():
			return Event{}, x.ctx.Err()
		case e, ok := <-x.events:
			if !ok {
				x.connErr = errExportConnection
				return Event{}, x.connErr
			}
			x.observe(e)
			if x.connErr != nil {
				return Event{}, x.connErr
			}
			if match(e) {
				return e, nil
			}
		}
	}
}

// waitUntil observes events until cond holds after one of them.
func (x *exporter) waitUntil(cond func() bool) error {
	if cond() {
		return nil
	}
	_, err := x.wait(func(Event) bool { return cond() })
	return err
}

// connect waits for the stream and for the default channel's startup join
// and its initial page, which also finishes the startup setup that would
// refuse a join of another channel.
func (x *exporter) connect(defaultChannel string) error {
	if err := x.waitUntil(func() bool { return x.connected }); err != nil {
		return err
	}
	return x.waitUntil(func() bool {
		state := x.membership[defaultChannel]
		return state != "unknown" && state != "joining" && (state != "joined" || x.latestFor(defaultChannel).done)
	})
}

// send queues a request unless the connection or context ends first.
func send[T any](x *exporter, queue chan<- T, request T) error {
	select {
	case queue <- request:
		return nil
	case <-x.ctx.Done():
		return x.ctx.Err()
	}
}

// control runs one channel action and waits for its outcome. An action
// refused only because setup was pending was not sent; it is repeated.
func (x *exporter) control(kind, channel string) error {
	for {
		id := x.id(kind)
		if err := send(x, x.actions, Action{ID: id, Kind: kind, Channel: channel}); err != nil {
			return err
		}
		e, err := x.wait(func(e Event) bool { return e.Kind == "control" && e.ID == id })
		if err != nil {
			return err
		}
		if e.State == "refused" && e.Code == SetupPendingCode {
			if err := x.pause(setupRetry); err != nil {
				return err
			}
			continue
		}
		return controlOutcome(kind, channel, e)
	}
}

// pause waits for d while still observing events.
func (x *exporter) pause(d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-x.ctx.Done():
			return x.ctx.Err()
		case <-timer.C:
			return nil
		case e, ok := <-x.events:
			if !ok {
				x.connErr = errExportConnection
				return x.connErr
			}
			if x.observe(e); x.connErr != nil {
				return x.connErr
			}
		}
	}
}

func controlOutcome(kind, channel string, e Event) error {
	if e.State != "confirmed" {
		reason := e.Err
		if reason == nil {
			reason = errors.New(e.State)
		}
		return fmt.Errorf("%s %s was %s: %w", kind, channel, e.State, reason)
	}
	return nil
}

// channel exports one channel.
func (x *exporter) channel(channel string) (result ExportResult) {
	result.Channel = channel
	if x.membership[channel] != "joined" {
		delete(x.latest, channel) // a page from an earlier membership is not this read
		if err := x.control("join", channel); err != nil {
			result.Err = err
			return result
		}
		result.Joined = true
		defer func() {
			if x.connErr == nil {
				result.PartErr = x.control("part", channel)
			}
		}()
	}
	x.active, x.activeErr = channel, nil
	defer func() { x.active, x.page, x.pageID = "", nil, "" }()
	result.Messages, result.Err = x.read(channel)
	return result
}

// read collects the initial page and then every older page, newest last.
func (x *exporter) read(channel string) ([]Message, error) {
	latest := x.latestFor(channel)
	if err := x.waitUntil(func() bool { return latest.done || x.activeErr != nil }); err != nil {
		return nil, err
	}
	if x.activeErr != nil {
		return nil, x.activeErr
	}
	if latest.err != nil {
		return nil, latest.err
	}
	pages := [][]Message{latest.messages}
	seen := map[string]bool{}
	for _, m := range latest.messages {
		if seen[m.ID] {
			return nil, fmt.Errorf("message %s was returned twice", m.ID)
		}
		seen[m.ID] = true
	}
	for len(pages[len(pages)-1]) > 0 {
		before := pages[len(pages)-1][0].ID // pages hold their messages oldest first
		x.page, x.pageID = nil, x.id("page")
		if err := send(x, x.older, HistoryRequest{ID: x.pageID, Channel: channel, Before: before}); err != nil {
			return nil, err
		}
		id := x.pageID
		e, err := x.wait(func(e Event) bool { return e.Kind == "history-page" && e.ID == id })
		if err != nil {
			return nil, err
		}
		if x.activeErr != nil {
			return nil, x.activeErr
		}
		if e.Count < 0 {
			if e.Err != nil {
				return nil, fmt.Errorf("a history page failed: %w", e.Err)
			}
			return nil, errors.New("a history page failed")
		}
		if e.Count != len(x.page) {
			return nil, fmt.Errorf("a history page reported %d messages but delivered %d", e.Count, len(x.page))
		}
		for _, m := range x.page {
			if seen[m.ID] {
				return nil, fmt.Errorf("message %s was returned twice", m.ID)
			}
			seen[m.ID] = true
		}
		pages = append(pages, x.page)
	}
	var out []Message
	for i := len(pages) - 1; i >= 0; i-- {
		out = append(out, pages[i]...)
	}
	return out, nil
}

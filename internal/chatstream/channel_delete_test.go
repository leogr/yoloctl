package chatstream

import (
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/managedchat"
)

// deletionFixture is a joined #space/#build driver at its stream's original
// generation, with the relay's allowlist as RunChannels configures it.
func deletionFixture(t *testing.T) (*channelDriver, *[]Event, *[]Request) {
	t.Helper()
	d, events, requests := channelDriverFixture(t)
	d.origin = d.wire.header.Generation
	d.wire.allowedChannel = func(channel string) bool { return d.channels[channel] != nil && !d.channels[channel].deleted }
	return d, events, requests
}

// deletedCatalog is the next generation without #build and with its record.
func deletedCatalog(d *channelDriver) Catalog {
	next := normalizedHeader(d.catalog)
	next.Generation++
	build := next.Channels[1]
	next.Channels = next.Channels[:1]
	next.DeletedChannels = append(next.DeletedChannels, managedchat.DeletedChannel{ID: build.ID, Name: build.Name, Generation: next.Generation})
	return Catalog{Header: next}
}

func lastEvent(events []Event, kind string) (Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == kind {
			return events[i], true
		}
	}
	return Event{}, false
}

func TestChannelsCatalogRemovesOnlyAnExplicitlyDeletedIdentity(t *testing.T) {
	d, events, requests := deletionFixture(t)
	unexplained := normalizedHeader(d.catalog)
	unexplained.Generation++
	unexplained.Channels = unexplained.Channels[:1]
	if err := d.refreshCatalog(Catalog{Header: unexplained}); err == nil {
		t.Fatal("unexplained disappearance accepted")
	}
	deleted := deletedCatalog(d)
	foreign := deleted
	foreign.Header = normalizedHeader(deleted.Header)
	foreign.Header.DeletedChannels[0].ID = managedchat.NewID()
	if err := d.refreshCatalog(foreign); err == nil {
		t.Fatal("removal proved by another identity's record accepted")
	}
	if err := d.refreshCatalog(deleted); err != nil {
		t.Fatal(err)
	}
	membership, _ := lastEvent(*events, "membership")
	if !d.channels["#build"].deleted || membership.Channel != "#build" || membership.State != "deleted" || !errors.Is(membership.Err, errChannelDeleted) {
		t.Fatal("deletion not reported distinctly from PART/KICK", membership)
	}
	// The ledger only grows.
	dropped := normalizedHeader(deleted.Header)
	dropped.Generation++
	dropped.DeletedChannels = nil
	if err := d.refreshCatalog(Catalog{Header: dropped}); err == nil {
		t.Fatal("catalog dropping a deletion record accepted")
	}
	// A same-name channel with a new ID is catalog information only: it does
	// not revive the deleted channel on this connection.
	recreated := normalizedHeader(deleted.Header)
	recreated.Generation++
	recreated.Channels = append(recreated.Channels, managedchat.Channel{ID: managedchat.NewID(), Name: "#build", Members: recreated.Channels[0].Members})
	if err := d.refreshCatalog(Catalog{Header: recreated}); err != nil {
		t.Fatal(err)
	}
	if !d.channels["#build"].deleted || d.wire.allowedChannel("#build") {
		t.Fatal("recreated name revived the deleted channel on an old connection")
	}
	// Every request for it is refused before Write; #space still sends.
	sent := len(*requests)
	if err := d.send(Send{ID: "late", Channel: "#build", Text: "late"}); err != nil {
		t.Fatal(err)
	}
	delivery, _ := lastEvent(*events, "delivery")
	if delivery.ID != "late" || delivery.Channel != "#build" || delivery.State != "rejected" || !errors.Is(delivery.Err, errChannelDeleted) {
		t.Fatal("send to a deleted channel was not a definite local refusal", delivery)
	}
	for _, kind := range []string{"join", "part", "topic-read"} {
		if err := d.action(Action{ID: kind, Kind: kind, Channel: "#build"}); err != nil {
			t.Fatal(err)
		}
		control, _ := lastEvent(*events, "control")
		if control.ID != kind || control.State != "refused" || control.Code != "deleted-channel" {
			t.Fatal("control on a deleted channel was not refused", control)
		}
	}
	d.requestHistory(HistoryRequest{ID: "page", Channel: "#build", Before: "old"}, false)
	page, _ := lastEvent(*events, "history-page")
	if page.ID != "page" || page.Count != -1 || !errors.Is(page.Err, errChannelDeleted) || len(d.queue) != 0 {
		t.Fatal("history for a deleted channel was queued", page)
	}
	if len(*requests) != sent {
		t.Fatal("a refused request reached the wire", (*requests)[sent:])
	}
	if err := d.send(Send{ID: "space", Channel: "#space", Text: "still here"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != sent+1 || (*requests)[sent].Target != "#space" || !d.pendingSend() {
		t.Fatal("deletion stopped another channel's send", *requests)
	}
}

func TestChannelsPendingWorkOnADeletedChannelKeepsItsGenuineOutcome(t *testing.T) {
	d, events, _ := deletionFixture(t)
	if err := d.send(Send{ID: "inflight", Channel: "#build", Text: "before deletion"}); err != nil || !d.pendingSend() {
		t.Fatal("send was not in flight", err)
	}
	if err := d.refreshCatalog(deletedCatalog(d)); err != nil {
		t.Fatal(err)
	}
	// The send keeps its server outcome, reported once; nothing is replayed.
	receipt := record{Event: "receipt", Receipt: "send-one", Target: "#build", State: "rejected", Reason: "channel deleted"}
	for range 2 {
		if err := d.record(receipt); err != nil {
			t.Fatal(err)
		}
	}
	var outcomes []Event
	for _, event := range *events {
		if event.Kind == "delivery" && event.ID == "inflight" {
			outcomes = append(outcomes, event)
		}
	}
	if len(outcomes) != 1 || outcomes[0].State != "rejected" || d.pendingSend() {
		t.Fatal("in-flight send outcome was lost, invented or repeated", outcomes)
	}
	// A control pending when the server's deletion KICK arrives finishes as
	// uncertain without ending the connection or reporting an ordinary KICK.
	d2, events2, _ := deletionFixture(t)
	topic := "pending topic"
	if err := d2.action(Action{ID: "topic", Kind: "topic-set", Channel: "#build", Topic: &topic}); err != nil || d2.control == nil {
		t.Fatal("topic action was not pending", err)
	}
	if err := d2.refreshCatalog(deletedCatalog(d2)); err != nil {
		t.Fatal(err)
	}
	kick := record{Command: "KICK", Prefix: d2.serverPrefix(), Params: []string{"#build", d2.wire.header.Nick, "Channel deleted"}}
	if err := d2.record(kick); err != nil {
		t.Fatal("deletion KICK with a pending control ended the connection", err)
	}
	control, _ := lastEvent(*events2, "control")
	membership, _ := lastEvent(*events2, "membership")
	if d2.control != nil || control.ID != "topic" || control.State != "uncertain" || membership.State != "deleted" {
		t.Fatal("pending control on a deleted channel not finished once as uncertain", control, membership)
	}
	for _, event := range *events2 {
		if event.Kind == "membership" && event.State == "kicked" {
			t.Fatal("deletion reported as an ordinary KICK")
		}
	}
}

func TestChannelsNameDeletedAndRecreatedBetweenRefreshesIsUnusable(t *testing.T) {
	d, events, requests := deletionFixture(t)
	// #ops appears for the first time already recreated: its name was deleted
	// after this connection's original generation.
	next := normalizedHeader(d.catalog)
	next.Generation += 3
	next.Channels = append(next.Channels, managedchat.Channel{ID: managedchat.NewID(), Name: "#ops", Members: next.Channels[0].Members})
	next.DeletedChannels = []managedchat.DeletedChannel{{ID: managedchat.NewID(), Name: "#ops", Generation: next.Generation - 1}}
	if err := d.refreshCatalog(Catalog{Header: next}); err != nil {
		t.Fatal(err)
	}
	sent := len(*requests)
	if err := d.action(Action{ID: "join", Kind: "join", Channel: "#ops"}); err != nil {
		t.Fatal(err)
	}
	control, _ := lastEvent(*events, "control")
	if control.Code != "deleted-channel" || len(*requests) != sent || !strings.Contains(control.Err.Error(), "open a new chat connection") {
		t.Fatal("old connection may join a name recreated after it opened", control)
	}
	// Records no newer than the original generation never block a name.
	fresh, _, _ := deletionFixture(t)
	old := normalizedHeader(fresh.catalog)
	old.DeletedChannels = []managedchat.DeletedChannel{{ID: managedchat.NewID(), Name: "#ops", Generation: old.Generation}}
	old.Generation++
	old.Channels = append(old.Channels, managedchat.Channel{ID: managedchat.NewID(), Name: "#ops", Members: old.Channels[0].Members})
	if err := fresh.refreshCatalog(Catalog{Header: old}); err != nil || fresh.channels["#ops"].deleted {
		t.Fatal("a deletion before this connection opened blocked its name", err)
	}
}

func TestChannelsHeaderDeletionRecordsAreValidated(t *testing.T) {
	d, _, _ := deletionFixture(t)
	valid := normalizedHeader(d.catalog)
	valid.DeletedChannels = []managedchat.DeletedChannel{{ID: managedchat.NewID(), Name: "#old", Generation: valid.Generation}}
	if err := validateHeader(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Header){
		"active id":  func(h *Header) { h.DeletedChannels[0].ID = h.Channels[1].ID },
		"default":    func(h *Header) { h.DeletedChannels[0].Name = managedchat.DefaultChannel },
		"future":     func(h *Header) { h.DeletedChannels[0].Generation = h.Generation + 1 },
		"zero":       func(h *Header) { h.DeletedChannels[0].Generation = 0 },
		"bad name":   func(h *Header) { h.DeletedChannels[0].Name = "#Old" },
		"legacy hdr": func(h *Header) { h.Schema, h.Channels, h.DefaultChannel = 1, nil, "" },
	} {
		h := normalizedHeader(valid)
		mutate(&h)
		if err := validateHeader(h); err == nil {
			t.Fatal("invalid deletion records accepted", name)
		}
	}
}

// confirmSpace sends to #space and confirms it with its receipt: the other
// channel still completes a send after a removal elsewhere.
func confirmSpace(t *testing.T, d *channelDriver, events *[]Event) {
	t.Helper()
	if err := d.send(Send{ID: "space", Channel: "#space", Text: "still here"}); err != nil {
		t.Fatal(err)
	}
	if err := d.record(record{Event: "receipt", Receipt: "send-one", Target: "#space", State: "confirmed", MsgID: "m-space"}); err != nil {
		t.Fatal(err)
	}
	if delivery, _ := lastEvent(*events, "delivery"); delivery.ID != "space" || delivery.State != "confirmed" {
		t.Fatal("another channel's send was not confirmed", delivery)
	}
}

// A self-KICK while an action is pending on that channel (an ACL change, or
// a deletion whose catalog arrives later or earlier) finishes the action once
// as uncertain, never replays it and keeps the connection and other channels.
func TestChannelsKickDuringAPendingActionKeepsTheStream(t *testing.T) {
	topic := "pending topic"
	for _, test := range []struct {
		name          string
		catalogFirst  bool
		reason, wantM string
	}{
		{"deletion KICK before the catalog", false, "Channel deleted", "deleted"},
		{"deletion KICK after the catalog", true, "Channel deleted", "deleted"},
		{"ACL removal", false, "Membership revoked", "kicked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, events, requests := deletionFixture(t)
			if err := d.action(Action{ID: "topic", Kind: "topic-set", Channel: "#build", Topic: &topic}); err != nil || d.control == nil {
				t.Fatal("topic action was not pending", err)
			}
			if test.catalogFirst {
				if err := d.refreshCatalog(deletedCatalog(d)); err != nil {
					t.Fatal(err)
				}
			}
			written := len(*requests)
			kick := record{Command: "KICK", Prefix: d.serverPrefix(), Params: []string{"#build", d.wire.header.Nick, test.reason}}
			if err := d.record(kick); err != nil {
				t.Fatal("a KICK during a pending action ended the connection", err)
			}
			if test.wantM == "deleted" && !test.catalogFirst {
				if err := d.refreshCatalog(deletedCatalog(d)); err != nil {
					t.Fatal(err)
				}
			}
			var controls []Event
			for _, event := range *events {
				if event.Kind == "control" {
					controls = append(controls, event)
				}
			}
			if d.control != nil || len(controls) != 1 || controls[0].ID != "topic" || controls[0].State != "uncertain" || len(*requests) != written {
				t.Fatal("pending action not finished exactly once as uncertain, or replayed", controls)
			}
			if membership, _ := lastEvent(*events, "membership"); membership.Channel != "#build" || membership.State != test.wantM {
				t.Fatal("membership after the removal", membership)
			}
			confirmSpace(t, d, events)
		})
	}
}

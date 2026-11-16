package chatstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func availabilityFixture(t *testing.T) (*channelDriver, *[]Event, *[]Request) {
	t.Helper()
	d, events, requests := channelDriverFixture(t)
	d.availabilityKnown = true
	d.wire.exchangeFn = func(r Request) (envelope, error) {
		*requests = append(*requests, r)
		if r.Op == "read" {
			return envelope{OK: true, Result: json.RawMessage(`{"epoch":"epoch-one","events":[],"next":0,"latest":9}`)}, nil
		}
		return envelope{OK: true, Result: json.RawMessage(`{"status":"queued"}`)}, nil
	}
	return d, events, requests
}
func awayReason(s string) *string { return &s }

func TestAvailabilityFilterBounds(t *testing.T) {
	for _, line := range []string{`{"id":"a","op":"away","reason":"lunch"}`, `{"id":"b","op":"back"}`, `{"id":"a","op":"away","reason":"` + strings.Repeat("é", 100) + `"}`} {
		var out bytes.Buffer
		if err := FilterChannels(strings.NewReader(line+"\n"), &out, func(string) error { t.Fatal("connection action acquired a channel grant"); return nil }, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range []string{`{"id":"a","op":"away"}`, `{"id":"a","op":"away","reason":""}`, `{"id":"a","op":"back","reason":""}`, `{"id":"a","op":"away","reason":"x","channel":"#space"}`, `{"id":"a","op":"away","reason":"x","target":"#space"}`, `{"id":"a","op":"away","reason":"x","topic":"x"}`, `{"id":"a","op":"away","reason":"x\nJOIN"}`, `{"id":"a","op":"away","reason":"x\u0000"}`, `{"id":"a","op":"away","reason":"` + strings.Repeat("é", 101) + `"}`} {
		if _, err := DecodeRequest([]byte(line), "#space"); err == nil {
			t.Fatalf("accepted %s", line)
		}
	}
}

func TestAvailabilityNumericFenceAndSelfRoster(t *testing.T) {
	d, events, requests := availabilityFixture(t)
	d.channels["#space"].deleted = true // no selected-channel dependency
	for _, c := range d.channels {
		self := Member{Nick: d.wire.header.Nick, Account: d.wire.header.SelfAccount}
		c.adapter.roster[strings.ToLower(self.Nick)] = self
		c.adapter.roster["other-session"] = Member{Nick: "other-session", Account: self.Account}
	}
	if err := d.action(Action{ID: "away", Kind: "away", Reason: awayReason("lunch")}); err != nil {
		t.Fatal(err)
	}
	if d.control == nil || d.ownAway {
		t.Fatal("queue treated as confirmation")
	}
	for _, item := range []struct {
		code   string
		cursor uint64
	}{{"306", 8}, {"305", 10}} {
		r := numericRecord(d, item.code, "ok")
		r.Cursor = item.cursor
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
		if d.control == nil || d.ownAway {
			t.Fatal("old/wrong numeric confirmed away")
		}
	}
	r := numericRecord(d, "306", "away")
	r.Cursor = 11
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	if d.control != nil || !d.ownAway || d.ownReason != "lunch" {
		t.Fatal("matching numeric not applied")
	}
	for _, c := range d.channels {
		// A later WHO update with G but no reason must retain the confirmed reason.
		c.adapter.roster[strings.ToLower(d.wire.header.Nick)] = Member{Nick: d.wire.header.Nick, Account: d.wire.header.SelfAccount}
		c.adapter.members()
		if m := c.adapter.roster[strings.ToLower(d.wire.header.Nick)]; !m.Away || m.AwayReason != "lunch" {
			t.Fatalf("WHO lost self state %+v", m)
		}
		if c.adapter.roster["other-session"].Away {
			t.Fatal("account-wide away leaked")
		}
	}
	if len(*requests) != 2 || len(*events) == 0 {
		t.Fatal("missing bounded exchange/events")
	}
}

func TestAvailabilityRefusalAndUncertainty(t *testing.T) {
	d, _, requests := availabilityFixture(t)
	d.ownAway = true
	d.ownReason = "old"
	if err := d.action(Action{ID: "bad", Kind: "back", Reason: awayReason("no")}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 || !d.ownAway {
		t.Fatal("local refusal changed presence")
	}
	if err := d.action(Action{ID: "back", Kind: "back"}); err != nil {
		t.Fatal(err)
	}
	if err := d.action(Action{ID: "second", Kind: "back"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatal("concurrent action crossed slot")
	}
	d.control.until = time.Now().Add(-time.Second)
	if d.deadlines() == nil {
		t.Fatal("control timeout ignored")
	}
	d.endPending(errors.New("lost"))
	if d.control != nil {
		t.Fatal("uncertain control survived ended stream")
	}
}

func TestAvailabilityWrongRecipientAndPeerNeverConfirm(t *testing.T) {
	for _, wrong := range []string{"server", "recipient", "peer"} {
		d, _, _ := availabilityFixture(t)
		if err := d.action(Action{ID: "away", Kind: "away", Reason: awayReason("x")}); err != nil {
			t.Fatal(err)
		}
		r := numericRecord(d, "306", "ok")
		r.Cursor = 10
		if wrong == "server" {
			r.Prefix = "elsewhere"
		}
		if wrong == "recipient" {
			r.Params[0] = "other"
		}
		if wrong == "peer" {
			r = record{Command: "AWAY", Prefix: "peer!peer@airc", Params: []string{"x"}, Cursor: 10}
		}
		err := d.record(r)
		if wrong != "peer" && err == nil {
			t.Fatal("untrusted numeric accepted")
		}
		if d.control == nil || d.ownAway {
			t.Fatal("untrusted event confirmed")
		}
	}
}

func TestAvailabilityBufferedNumericCannotConfirmNewAction(t *testing.T) {
	d, _, requests := availabilityFixture(t)
	d.wire.exchangeFn = func(request Request) (envelope, error) {
		*requests = append(*requests, request)
		if request.Op == "read" {
			old := numericRecord(d, "306", "old away")
			old.Cursor = 5
			page := struct {
				Epoch        string   `json:"epoch"`
				Events       []record `json:"events"`
				Next, Latest uint64
			}{"epoch-one", []record{old}, 5, 9}
			raw, _ := json.Marshal(page)
			return envelope{OK: true, Result: raw}, nil
		}
		return envelope{OK: true, Result: json.RawMessage(`{"status":"queued"}`)}, nil
	}
	if err := d.action(Action{ID: "new", Kind: "away", Reason: awayReason("new")}); err != nil {
		t.Fatal(err)
	}
	if d.control == nil || d.ownAway || d.control.after != 9 {
		t.Fatal("pre-submission numeric confirmed new action")
	}
}

func TestAvailabilityQueueFailureAndGapNeverReplay(t *testing.T) {
	for _, mode := range []string{"failed-reply", "lost-reply", "gap"} {
		d, events, requests := availabilityFixture(t)
		exchange := d.wire.exchangeFn
		d.wire.exchangeFn = func(r Request) (envelope, error) {
			if r.Op != "away" {
				return exchange(r)
			}
			*requests = append(*requests, r)
			if mode == "lost-reply" {
				return envelope{}, errors.New("lost queue reply")
			}
			if mode == "failed-reply" {
				return envelope{OK: false}, nil
			}
			return envelope{OK: true, Result: json.RawMessage(`{"status":"queued"}`)}, nil
		}
		err := d.action(Action{ID: "away", Kind: "away", Reason: awayReason("x")})
		if mode != "gap" && err == nil {
			t.Fatal("uncertain queue accepted")
		}
		if mode == "gap" {
			d.gap()
			err = errors.New("event gap")
		}
		d.endPending(err)
		if len(*requests) != 2 || d.control != nil || d.ownAway {
			t.Fatal("failed action replayed/confirmed")
		}
		seen := false
		for _, e := range *events {
			if e.Kind == "control" && e.ID == "away" && e.State == "uncertain" {
				seen = true
			}
		}
		if !seen {
			t.Fatal("uncertain outcome not retained")
		}
	}
}

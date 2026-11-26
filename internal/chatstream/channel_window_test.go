package chatstream

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// A window page reaches the view whole, once its batch closes: its rows are
// not live messages, not an older page, and keep their reply and fields;
// interleaved live rows stay on the ordinary path.
func TestChannelWindowPageArrivesWholeAtBatchClose(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	r := HistoryRequest{ID: "window", Channel: "#build", Around: "target", Window: true}
	d.requestHistory(r, false)
	if err := d.advance(); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 {
		t.Fatalf("wire requests: %+v", *requests)
	}
	if wire := (*requests)[0]; wire.Op != "history" || wire.Channel != "#build" || wire.Direction != "AROUND" || wire.Reference != "msgid=target" || wire.Limit != 50 {
		t.Fatalf("window request: %+v", wire)
	}
	for _, rec := range []record{
		{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+h1", "chathistory", "#build"}},
		{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "before"}, Tags: map[string]string{"batch": "h1", "msgid": "before", "account": "peer-account"}},
		{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "live"}, Tags: map[string]string{"msgid": "live"}},
		{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "the target"}, Tags: map[string]string{
			"batch": "h1", "msgid": "target", "account": "peer-account", "+reply": "before", "time": "2026-11-10T15:04:05Z",
		}},
		{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "after"}, Tags: map[string]string{"batch": "h1", "msgid": "after"}},
	} {
		if err := d.record(rec); err != nil {
			t.Fatal(err)
		}
	}
	if len(*events) != 1 || (*events)[0].Kind != "message" || (*events)[0].Message.ID != "live" {
		t.Fatalf("a page row reached the stream before its batch closed: %+v", *events)
	}
	if err := d.record(record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-h1"}}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 2 {
		t.Fatalf("the page did not complete exactly once: %+v", *events)
	}
	page := (*events)[1]
	ids := []string{}
	for _, m := range page.Messages {
		ids = append(ids, m.ID)
	}
	if page.Kind != "history-window" || page.ID != r.ID || page.Channel != "#build" || page.Err != nil ||
		!slices.Equal(ids, []string{"before", "target", "after"}) {
		t.Fatalf("window page: %+v", page)
	}
	if target := page.Messages[1]; target.ReplyTo != "before" || target.Account != "peer-account" || target.Time.IsZero() || target.Text != "the target" || target.Older {
		t.Fatalf("page row lost its fields: %+v", target)
	}
}

// After and before pages are window pages too: never an older page of the
// live list, so no history-page event and no older classification.
func TestChannelWindowEdgeDirections(t *testing.T) {
	for _, test := range []struct {
		request   HistoryRequest
		direction string
	}{
		{HistoryRequest{ID: "after", Channel: "#build", After: "anchor", Window: true}, "AFTER"},
		{HistoryRequest{ID: "before", Channel: "#build", Before: "anchor", Window: true}, "BEFORE"},
	} {
		t.Run(test.direction, func(t *testing.T) {
			d, events, requests := channelDriverFixture(t)
			d.requestHistory(test.request, false)
			if err := d.advance(); err != nil {
				t.Fatal(err)
			}
			if wire := (*requests)[0]; wire.Direction != test.direction || wire.Reference != "msgid=anchor" || wire.Limit != 50 {
				t.Fatalf("edge request: %+v", wire)
			}
			for _, rec := range []record{
				{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+h1", "chathistory", "#build"}},
				{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "row"}, Tags: map[string]string{"batch": "h1", "msgid": "row"}},
				{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-h1"}},
			} {
				if err := d.record(rec); err != nil {
					t.Fatal(err)
				}
			}
			if len(*events) != 1 || (*events)[0].Kind != "history-window" || len((*events)[0].Messages) != 1 || (*events)[0].Messages[0].ID != "row" {
				t.Fatalf("edge page: %+v", *events)
			}
		})
	}
}

func TestChannelWindowRequestShapes(t *testing.T) {
	for _, r := range []HistoryRequest{
		{ID: "a", Channel: "#build", Around: "x"},                           // a page needs Window
		{ID: "b", Channel: "#build", After: "x"},                            // likewise
		{ID: "c", Channel: "#build", Lookup: "x", Window: true},             // a lookup is one record
		{ID: "d", Channel: "#build", Around: "x", After: "y", Window: true}, // one anchor
		{ID: "e", Channel: "#build", Window: true},                          // no anchor
	} {
		d, events, requests := channelDriverFixture(t)
		d.requestHistory(r, false)
		if err := d.advance(); err != nil {
			t.Fatal(err)
		}
		if len(*requests) != 0 || len(*events) != 1 || (*events)[0].Err == nil {
			t.Fatalf("%s: malformed request was not refused: %+v %+v", r.ID, *requests, *events)
		}
	}
}

// A window page that cannot complete says so; a retention miss is an empty
// page. Neither marks a gap in the live list.
func TestChannelWindowMissAndFailure(t *testing.T) {
	for _, test := range []struct {
		name, code, failure string
	}{
		{name: "retention miss", code: "HISTORY_LOST"},
		{name: "refused", code: "INVALID_PARAMS", failure: "refused"},
		{name: "lost connection", failure: "connection ended"},
		{name: "deadline", failure: "timed out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, events, _ := channelDriverFixture(t)
			d.requestHistory(HistoryRequest{ID: "window", Channel: "#build", Around: "target", Window: true}, false)
			if err := d.advance(); err != nil {
				t.Fatal(err)
			}
			switch {
			case test.code != "":
				if err := d.record(record{Command: "FAIL", Prefix: d.serverPrefix(), Params: []string{"CHATHISTORY", test.code, "#build", "detail"}}); err != nil {
					t.Fatal(err)
				}
			case test.name == "lost connection":
				d.endPending(errors.New("connection ended"))
			default:
				d.historyGap(errors.New("history response timed out"))
			}
			i := slices.IndexFunc(*events, func(e Event) bool { return e.Kind == "history-window" })
			if i < 0 {
				t.Fatalf("no page completion: %+v", *events)
			}
			result := (*events)[i]
			if result.ID != "window" || len(result.Messages) != 0 || (test.failure == "") != (result.Err == nil) ||
				result.Err != nil && !strings.Contains(result.Err.Error(), test.failure) {
				t.Fatalf("unexpected disposition: %+v", result)
			}
			if test.code != "" && slices.ContainsFunc(*events, func(e Event) bool { return e.Kind == "gap" && e.Channel == "#build" }) {
				t.Fatalf("a window failure marked a channel gap: %+v", *events)
			}
		})
	}
}

// A lost edge cursor says nothing about either side: a failed read, unlike
// an empty completed page.
func TestChannelWindowLostEdgeCursorFails(t *testing.T) {
	for _, r := range []HistoryRequest{
		{ID: "after", Channel: "#build", After: "gone", Window: true},
		{ID: "before", Channel: "#build", Before: "gone", Window: true},
	} {
		for _, lost := range []bool{true, false} {
			d, events, _ := channelDriverFixture(t)
			d.requestHistory(r, false)
			if err := d.advance(); err != nil {
				t.Fatal(err)
			}
			recs := []record{{Command: "FAIL", Prefix: d.serverPrefix(), Params: []string{"CHATHISTORY", "HISTORY_LOST", "#build", "detail"}}}
			if !lost {
				recs = []record{
					{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+h1", "chathistory", "#build"}},
					{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-h1"}},
				}
			}
			for _, rec := range recs {
				if err := d.record(rec); err != nil {
					t.Fatal(err)
				}
			}
			i := slices.IndexFunc(*events, func(e Event) bool { return e.Kind == "history-window" })
			if i < 0 || (*events)[i].ID != r.ID || len((*events)[i].Messages) != 0 || lost != ((*events)[i].Err != nil) {
				t.Fatalf("%s lost=%v: %+v", r.ID, lost, *events)
			}
		}
	}
}

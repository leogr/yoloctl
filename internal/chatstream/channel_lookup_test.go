package chatstream

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestChannelLookupUsesOneExactRecordFromCompletedBatch(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	r := HistoryRequest{ID: "lookup", Channel: "#build", Lookup: "target"}
	d.requestHistory(r, false)
	if err := d.advance(); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 {
		t.Fatalf("wire requests: %+v", *requests)
	}
	wire := (*requests)[0]
	if wire.Op != "history" || wire.Channel != r.Channel || wire.Direction != "AROUND" || wire.Reference != "msgid=target" || wire.Limit != 1 {
		t.Fatalf("lookup request: %+v", wire)
	}
	for _, record := range []record{
		{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+h1", "chathistory", "#build"}},
		{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "live"}, Tags: map[string]string{"msgid": "live"}},
		{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "one\nassembled\nmessage"}, Tags: map[string]string{
			"batch": "h1", "msgid": r.Lookup, "account": "peer-account", "time": "2026-11-10T15:04:05Z",
		}},
	} {
		if err := d.record(record); err != nil {
			t.Fatal(err)
		}
	}
	if len(*events) != 1 || (*events)[0].Kind != "message" || (*events)[0].Message.ID != "live" {
		t.Fatalf("lookup streamed or swallowed a record before batch close: %+v", *events)
	}
	if err := d.record(record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-h1"}}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 2 {
		t.Fatalf("batch did not complete exactly once: %+v", *events)
	}
	result := (*events)[1]
	msg := result.Message
	if result.Kind != "history-lookup" || result.ID != r.ID || result.Channel != r.Channel || result.Err != nil ||
		msg.ID != r.Lookup || msg.Channel != r.Channel || msg.Account != "peer-account" || msg.Time.IsZero() || msg.Text != "one\nassembled\nmessage" {
		t.Fatalf("lookup result: %+v", result)
	}
}

func TestChannelLookupDistinguishesMissAndFailure(t *testing.T) {
	for _, test := range []struct {
		name, code, wrong, failure string
	}{
		{name: "empty batch"},
		{name: "retention miss", code: "HISTORY_LOST"},
		{name: "refused", code: "INVALID_PARAMS", failure: "refused"},
		{name: "wrong ID", wrong: "other", failure: "different message ID"},
		{name: "lost connection", failure: "connection ended"},
		{name: "deadline", failure: "timed out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, events, _ := channelDriverFixture(t)
			d.requestHistory(HistoryRequest{ID: "lookup", Channel: "#build", Lookup: "target"}, false)
			if err := d.advance(); err != nil {
				t.Fatal(err)
			}
			feed := func(r record) {
				t.Helper()
				if err := d.record(r); err != nil {
					t.Fatal(err)
				}
			}
			switch {
			case test.code != "":
				feed(record{Command: "FAIL", Prefix: d.serverPrefix(), Params: []string{"CHATHISTORY", test.code, "#build", "detail"}})
			case test.name == "lost connection":
				d.endPending(errors.New("connection ended"))
			case test.name == "deadline":
				d.historyGap(errors.New("history response timed out"))
			default:
				feed(record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+h1", "chathistory", "#build"}})
				if test.wrong != "" {
					feed(record{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "wrong"}, Tags: map[string]string{"batch": "h1", "msgid": test.wrong}})
				}
				feed(record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-h1"}})
			}
			if slices.ContainsFunc(*events, func(e Event) bool { return e.Kind == "message" || e.Kind == "history-page" }) {
				t.Fatalf("lookup entered the transcript/page stream: %+v", *events)
			}
			i := slices.IndexFunc(*events, func(e Event) bool { return e.Kind == "history-lookup" })
			if i < 0 {
				t.Fatalf("no lookup completion: %+v", *events)
			}
			result := (*events)[i]
			if result.ID != "lookup" || result.Channel != "#build" || result.Message.ID != "" ||
				(test.failure == "") != (result.Err == nil) ||
				(result.Err != nil && !strings.Contains(result.Err.Error(), test.failure)) {
				t.Fatalf("unexpected disposition: %+v", result)
			}
		})
	}
}

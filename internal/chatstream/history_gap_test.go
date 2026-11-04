package chatstream

import "testing"

func TestHistoryGapWithLostBatchOpenerDoesNotEmitLiveHistory(t *testing.T) {
	var events []Event
	d := &driver{header: testHeader(t), batches: map[string]*historyBatch{},
		roster: map[string]Member{}, olderRequests: 1,
		emit: func(e Event) { events = append(events, e) }}
	d.historyGap()
	events = nil
	// The unread-buffer gap consumed the batch opener. Its remaining body is
	// history with an unknown parent, never a newly received live message.
	r := record{Command: "PRIVMSG", Params: []string{"#space", "old history"},
		Prefix: "opus!mopus@airc", Tags: map[string]string{
			"msgid": "history-tail", "account": "mopus", "batch": "lost-opener"}}
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "message" {
			t.Fatalf("orphan history emitted after gap: %+v", e.Message)
		}
	}
	// A new unbatched message remains live; the connection stays usable.
	r.Params[1], r.Tags["msgid"] = "new live message", "live-after-gap"
	delete(r.Tags, "batch")
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Kind == "message" {
			count++
			if e.Message.ID != "live-after-gap" || e.Message.Replay {
				t.Fatalf("wrong live message: %+v", e.Message)
			}
		}
	}
	if count != 1 {
		t.Fatalf("want one live message, got %d", count)
	}
}

package chatstream

import (
	"slices"
	"strings"
	"testing"
)

func TestRosterTracksNamesJoinsRolesAndObservedAccounts(t *testing.T) {
	var snapshots [][]Member
	d := &driver{header: testHeader(t), batches: map[string]*historyBatch{}, roster: map[string]Member{}, emit: func(e Event) {
		if e.Kind == "members" {
			snapshots = append(snapshots, e.Members)
		}
	}}
	for _, r := range []record{
		{Command: "353", Params: []string{"human", "=", "#space", "@opus +sol qa"}},
		{Command: "353", Params: []string{"human", "=", "#other", "ignored"}},
		{Command: "366", Params: []string{"human", "#space", "End of NAMES"}},
	} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	got := snapshots[len(snapshots)-1]
	if len(got) != 3 || got[0].Nick != "opus" || !got[0].Op || got[2].Nick != "sol" || !got[2].Voice || got[1].Op || got[1].Voice {
		t.Fatalf("NAMES roster: %+v", got)
	}
	for _, r := range []record{
		{Command: "JOIN", Params: []string{"#space"}, Prefix: "agy!magy@airc"},
		{Command: "MODE", Params: []string{"#space", "+v", "agy"}},
		{Command: "PART", Params: []string{"#space"}, Prefix: "qa!mqa@airc"},
		{Command: "NICK", Params: []string{"sol2"}, Prefix: "sol!msol@airc"},
		{Command: "PRIVMSG", Params: []string{"#space", "hi"}, Prefix: "opus!mopus@airc", Tags: map[string]string{"msgid": "m1", "account": "mopus"}},
	} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	got = snapshots[len(snapshots)-1]
	byNick := map[string]Member{}
	for _, m := range got {
		byNick[m.Nick] = m
	}
	if _, ok := byNick["qa"]; ok || byNick["agy"].Account != "magy" || !byNick["agy"].Voice || byNick["sol2"].Nick != "sol2" || byNick["opus"].Account != "mopus" {
		t.Fatalf("roster updates: %+v", got)
	}
	// WHO rows reconcile authenticated accounts and away state; AWAY updates it.
	for _, r := range []record{
		{Command: "352", Params: []string{"human", "#space", "msol", "airc", "airc.local", "sol2", "G+", "0 AIRC participant"}},
		{Command: "352", Params: []string{"human", "#other", "mx", "airc", "airc.local", "opus", "H", "0 x"}},
		{Command: "315", Params: []string{"human", "#space", "End of WHO"}},
		{Command: "AWAY", Params: []string{"lunch"}, Prefix: "opus!mopus@airc"},
	} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	byNick = map[string]Member{}
	for _, m := range snapshots[len(snapshots)-1] {
		byNick[m.Nick] = m
	}
	if sol := byNick["sol2"]; sol.Account != "msol" || !sol.Away || !sol.Voice {
		t.Fatalf("WHO reconciliation: %+v", sol)
	}
	if opus := byNick["opus"]; !opus.Away || opus.AwayReason != "lunch" || opus.Account != "mopus" {
		t.Fatalf("AWAY update or foreign WHO row: %+v", opus)
	}
	if err := d.record(record{Command: "AWAY", Prefix: "opus!mopus@airc"}); err != nil || byNickAfter(snapshots)["opus"].Away {
		t.Fatal("AWAY without a reason must mark the member back", err)
	}
	if err := d.record(record{Command: "QUIT", Params: []string{"bye"}, Prefix: "agy!magy@airc"}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots[len(snapshots)-1]) != 2 {
		t.Fatal("QUIT keeps a departed member")
	}
}

func TestOlderPagesAreMarkedCountedAndEnded(t *testing.T) {
	var events []Event
	d := &driver{header: testHeader(t), batches: map[string]*historyBatch{}, roster: map[string]Member{}, olderRequests: 1, emit: func(e Event) { events = append(events, e) }}
	for _, r := range []record{
		{Command: "BATCH", Params: []string{"+h2", "chathistory", "#space"}},
		{Command: "PRIVMSG", Params: []string{"#space", "old\nmultiline"}, Prefix: "opus!mopus@airc", Tags: map[string]string{"msgid": "o1", "account": "mopus", "batch": "h2"}},
		{Command: "BATCH", Params: []string{"-h2"}},
		// A later live-history batch is ordinary replay, not an older page.
		{Command: "BATCH", Params: []string{"+h3", "chathistory", "#space"}},
		{Command: "BATCH", Params: []string{"-h3"}},
	} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 2 || !events[0].Message.Older || !events[0].Message.Replay || events[0].Message.Text != "old\nmultiline" {
		t.Fatalf("older message: %+v", events)
	}
	if events[1].Kind != "history-page" || events[1].Count != 1 {
		t.Fatalf("older page completion: %+v", events[1])
	}
	d.olderRequests = 1
	events = nil
	_ = d.record(record{Command: "BATCH", Params: []string{"+h4", "chathistory", "#space"}})
	_ = d.record(record{Command: "BATCH", Params: []string{"-h4"}})
	if len(events) != 1 || events[0].Kind != "history-page" || events[0].Count != 0 {
		t.Fatalf("empty older page must end paging: %+v", events)
	}
}

func TestHistoryRefusalsAndGapsResolvePendingRequests(t *testing.T) {
	var events []Event
	d := &driver{header: testHeader(t), batches: map[string]*historyBatch{}, roster: map[string]Member{}, initialHistory: true, olderRequests: 1, emit: func(e Event) { events = append(events, e) }}
	pages := func() (counts []int) {
		for _, e := range events {
			if e.Kind == "history-page" {
				counts = append(counts, e.Count)
			}
		}
		return counts
	}
	refusal := record{Command: "FAIL", Params: []string{"CHATHISTORY", "HISTORY_LOST", "#space", "history reference no longer retained"}}
	for _, r := range []record{
		// The initial LATEST request was sent first, so its batch resolves first.
		{Command: "BATCH", Params: []string{"+h1", "chathistory", "#space"}},
		{Command: "PRIVMSG", Params: []string{"#space", "recent"}, Prefix: "opus!mopus@airc", Tags: map[string]string{"msgid": "r1", "account": "mopus", "batch": "h1"}},
		{Command: "BATCH", Params: []string{"-h1"}},
		// Another operation's failure resolves no history request.
		{Command: "FAIL", Params: []string{"PRIVMSG", "INVALID_TARGET", "#other", "no such channel"}},
		// The older request is refused asynchronously after its queued reply.
		refusal,
	} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	if events[0].Kind != "message" || events[0].Message.Older || !events[0].Message.Replay {
		t.Fatalf("initial history is replay, not an older page: %+v", events[0])
	}
	if got := pages(); len(got) != 1 || got[0] != -1 || d.olderRequests != 0 || d.initialHistory {
		t.Fatalf("a history refusal must finish the pending older page once: %v requests=%d", got, d.olderRequests)
	}
	if err := d.record(refusal); err != nil || len(pages()) != 1 {
		t.Fatalf("a refusal with nothing pending finished a page: %v %v", err, pages())
	}
	// A read gap may have lost a queued or open older page; it finishes as
	// unavailable, the rest of an interrupted page is dropped, and its late
	// close adds nothing.
	tail := func(batch string) record {
		return record{Command: "PRIVMSG", Params: []string{"#space", "tail"}, Prefix: "opus!mopus@airc", Tags: map[string]string{"msgid": "t-" + batch, "account": "mopus", "batch": batch}}
	}
	message := func(e Event) bool { return e.Kind == "message" }
	fresh := func() *driver {
		events = nil
		return &driver{header: testHeader(t), batches: map[string]*historyBatch{}, roster: map[string]Member{}, olderRequests: 1, emit: func(e Event) { events = append(events, e) }}
	}
	for _, open := range []bool{false, true} {
		d := fresh()
		if open {
			_ = d.record(record{Command: "BATCH", Params: []string{"+h2", "chathistory", "#space"}})
		}
		d.historyGap()
		if open {
			_ = d.record(tail("h2"))
		}
		_ = d.record(record{Command: "BATCH", Params: []string{"-h2"}})
		if got := pages(); len(got) != 1 || got[0] != -1 || d.olderRequests != 0 || len(d.batches) != 0 || slices.ContainsFunc(events, message) {
			t.Fatalf("gap with an open batch=%v: pages=%v requests=%d events=%+v", open, got, d.olderRequests, events)
		}
	}
	// Initial replay interrupted by a gap continues as replay.
	d = fresh()
	d.initialHistory, d.olderRequests = true, 0
	_ = d.record(record{Command: "BATCH", Params: []string{"+h3", "chathistory", "#space"}})
	d.historyGap()
	_ = d.record(tail("h3"))
	if i := slices.IndexFunc(events, message); i < 0 || !events[i].Message.Replay || events[i].Message.Older || len(pages()) != 0 {
		t.Fatalf("replay interrupted by a gap must continue as replay: %+v", events)
	}
	// After the gap, history matches no known request: a late batch is
	// discarded and paging stays off without sending a request (no stream).
	events = nil
	for _, r := range []record{{Command: "BATCH", Params: []string{"+h4", "chathistory", "#space"}}, tail("h4"), {Command: "BATCH", Params: []string{"-h4"}}} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.loadOlder("o1"); err != nil || slices.ContainsFunc(events, message) || len(pages()) != 1 || pages()[0] != -1 || d.olderRequests != 0 {
		t.Fatalf("history after a gap must be discarded and paging refused: %v %+v", err, events)
	}
	// A later gap only reports itself and forgets a batch whose close was lost.
	events = nil
	if d.historyGap(); len(events) != 1 || events[0].Kind != "gap" || len(d.batches) != 0 {
		t.Fatalf("a later gap only reports itself and forgets stale batches: %+v batches=%d", events, len(d.batches))
	}
}

func byNickAfter(snapshots [][]Member) map[string]Member {
	out := map[string]Member{}
	for _, m := range snapshots[len(snapshots)-1] {
		out[m.Nick] = m
	}
	return out
}

// The topic arrives on joining (331/332/333) and as live TOPIC changes; other
// channels and oversized values are ignored.
func TestTopicEvents(t *testing.T) {
	var topics []Event
	d := &driver{header: testHeader(t), batches: map[string]*historyBatch{}, roster: map[string]Member{}, emit: func(e Event) {
		if e.Kind == "topic" {
			topics = append(topics, e)
		}
	}}
	for _, r := range []record{
		{Command: "332", Params: []string{"human", "#space", "Ship the chat UX"}},
		{Command: "333", Params: []string{"human", "#space", "opus!mopus@airc", "1700000000"}},
		{Command: "332", Params: []string{"human", "#other", "elsewhere"}},
		{Command: "TOPIC", Params: []string{"#space", strings.Repeat("x", maxTopicBytes+1)}, Prefix: "qa!mqa@airc"},
		{Command: "TOPIC", Params: []string{"#space", ""}, Prefix: "qa!mqa@airc"},
	} {
		if err := d.record(r); err != nil {
			t.Fatal(err)
		}
	}
	if len(topics) != 3 || topics[0].Topic != "Ship the chat UX" || topics[0].TopicLive || topics[1].TopicBy != "opus" || topics[1].Topic != "Ship the chat UX" {
		t.Fatalf("joined topic: %+v", topics)
	}
	if last := topics[2]; !last.TopicLive || last.Topic != "" || last.TopicBy != "qa" {
		t.Fatalf("live clear: %+v", last)
	}
}

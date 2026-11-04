package chatstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func channelDriverFixture(t *testing.T) (*channelDriver, *[]Event, *[]Request) {
	t.Helper()
	header := normalizedHeader(testHeader(t))
	header.Channels = append(header.Channels, managedchat.Channel{ID: managedchat.NewID(), Name: "#build", Members: append([]string(nil), header.Channels[0].Members...)})
	events, requests := []Event{}, []Request{}
	d := &channelDriver{wire: &driver{header: header, epoch: "epoch-one"}, catalog: normalizedHeader(header), channels: map[string]*channelState{}, batchOwner: map[string]string{}, emit: func(e Event) { events = append(events, e) }}
	d.wire.exchangeFn = func(request Request) (envelope, error) {
		requests = append(requests, request)
		result := json.RawMessage(`{"status":"queued"}`)
		if request.Op == "send" {
			result = json.RawMessage(`{"receipt":"send-one","state":"pending"}`)
		}
		return envelope{OK: true, Result: result}, nil
	}
	for _, channel := range header.Channels {
		d.addChannel(channel.Name, "joined")
		d.channels[channel.Name].bootstrap = "ready"
	}
	return d, &events, &requests
}

func selfRecord(d *channelDriver, command string, params ...string) record {
	return record{Command: command, Prefix: d.wire.header.Nick + "!" + d.wire.header.SelfAccount + "@airc", Params: params}
}

func numericRecord(d *channelDriver, command string, params ...string) record {
	return record{Command: command, Prefix: d.serverPrefix(), Params: append([]string{d.wire.header.Nick}, params...)}
}

func TestChannelsJoinBootstrapCannotConfirmTopicQuery(t *testing.T) {
	for _, initial := range []string{"", "old topic"} {
		t.Run(initial, func(t *testing.T) {
			d, events, requests := channelDriverFixture(t)
			d.channels["#build"].membership = "left"
			if err := d.action(Action{ID: "join", Kind: "join", Channel: "#build"}); err != nil {
				t.Fatal(err)
			}
			joined := selfRecord(d, "JOIN", "#build")
			joined.Cursor = 10
			if err := d.record(joined); err != nil {
				t.Fatal(err)
			}
			if d.control != nil || d.channels["#build"].membership != "joined" {
				t.Fatal("authentic self JOIN did not confirm membership")
			}
			if err := d.action(Action{ID: "early-query", Kind: "topic-read", Channel: "#build"}); err != nil {
				t.Fatal(err)
			}
			if len(*requests) != 1 || d.control != nil {
				t.Fatal("topic query crossed unresolved JOIN bootstrap")
			}
			// A newer live topic must survive delayed initial 331/332 context.
			if err := d.record(record{Command: "TOPIC", Prefix: "other!other-account@airc", Params: []string{"#build", "new topic"}}); err != nil {
				t.Fatal(err)
			}
			code := "332"
			if initial == "" {
				code = "331"
			}
			if err := d.record(numericRecord(d, code, "#build", initial)); err != nil {
				t.Fatal(err)
			}
			for _, event := range *events {
				if event.Kind == "topic" && event.Topic != "new topic" {
					t.Fatal("old JOIN numeric overwrote newer topic")
				}
			}
			end := numericRecord(d, "366", "#build", "end")
			end.Cursor = 12
			if err := d.record(end); err != nil {
				t.Fatal(err)
			}
			if err := d.advance(); err != nil {
				t.Fatal(err)
			} // bounded WHO setup
			if err := d.record(numericRecord(d, "315", "#build", "end")); err != nil {
				t.Fatal(err)
			}
			if err := d.action(Action{ID: "query", Kind: "topic-read", Channel: "#build"}); err != nil {
				t.Fatal(err)
			}
			if d.control == nil {
				t.Fatal("query was not admitted after proven setup")
			}
			if err := d.record(numericRecord(d, "332", "#space", "unrelated")); err != nil {
				t.Fatal(err)
			}
			if d.control == nil {
				t.Fatal("foreign channel completed topic query")
			}
			if err := d.record(numericRecord(d, "332", "#build", "new topic")); err != nil {
				t.Fatal(err)
			}
			if d.control != nil {
				t.Fatal("matching query remained pending")
			}
			found := false
			for _, event := range *events {
				if event.Kind == "control" && event.ID == "query" && event.State == "confirmed" && event.Channel == "#build" {
					found = true
				}
			}
			if !found {
				t.Fatal("query completion lost original ID/channel")
			}
		})
	}
}

func TestChannelsBootstrapTimeoutLate366AndIdempotence(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	if err := d.action(Action{ID: "again", Kind: "join", Channel: "#build"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 || len(d.queue) != 0 {
		t.Fatal("already-joined action sent/refetched")
	}
	d.channels["#build"].membership = "left"
	if err := d.action(Action{ID: "left", Kind: "part", Channel: "#build"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 {
		t.Fatal("already-absent part sent")
	}
	joined := selfRecord(d, "JOIN", "#build")
	joined.Cursor = 20
	if err := d.record(joined); err != nil {
		t.Fatal(err)
	}
	d.channels["#build"].until = time.Now().Add(-time.Second)
	if err := d.deadlines(); err != nil {
		t.Fatal(err)
	}
	late := numericRecord(d, "366", "#build", "end")
	late.Cursor = 21
	if err := d.record(late); err != nil {
		t.Fatal(err)
	}
	if err := d.action(Action{ID: "late-query", Kind: "topic-read", Channel: "#build"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 || d.control != nil || d.channels["#build"].bootstrap != "unknown" {
		t.Fatal("late bootstrap terminator lifted quarantine")
	}
	if (*events)[len(*events)-1].State != "refused" {
		t.Fatal("local query refusal is not explicit")
	}
}

func TestChannelsControlUsesExactSelfIdentityAndOneGlobalSlot(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	text := "new topic"
	if err := d.action(Action{ID: "topic", Kind: "topic-set", Channel: "#build", Topic: &text}); err != nil {
		t.Fatal(err)
	}
	if err := d.send(Send{ID: "other", Channel: "#space", Text: "must remain draft"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 {
		t.Fatal("send overlapped unlabelled control")
	}
	if err := d.record(record{Command: "TOPIC", Prefix: "another-nick!" + d.wire.header.SelfAccount + "@airc", Params: []string{"#build", text}}); err != nil {
		t.Fatal(err)
	}
	if d.control == nil {
		t.Fatal("another connection sharing an account confirmed this action")
	}
	if err := d.record(selfRecord(d, "TOPIC", "#space", text)); err != nil {
		t.Fatal(err)
	}
	if d.control == nil {
		t.Fatal("wrong channel confirmed action")
	}
	if err := d.record(selfRecord(d, "TOPIC", "#build", text)); err != nil {
		t.Fatal(err)
	}
	if d.control != nil {
		t.Fatal("self topic echo did not confirm")
	}
	if err := d.send(Send{ID: "send", Channel: "#space", Text: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := d.action(Action{ID: "part", Kind: "part", Channel: "#build"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatal("control overlapped another channel's send")
	}
	if err := d.record(record{Event: "receipt", Receipt: "send-one", Target: "#build", State: "confirmed", MsgID: "m1"}); err == nil {
		t.Fatal("foreign receipt accepted")
	}
	if err := d.record(record{Event: "receipt", Receipt: "send-one", Target: "#space", State: "confirmed", MsgID: "m1"}); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Kind != "delivery" || last.ID != "send" || last.Channel != "#space" {
		t.Fatal("delivery lost captured target")
	}
}

func TestChannelsHistoryIsGloballySerializedAndChannelCorrelated(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	d.requestHistory(HistoryRequest{ID: "older-build", Channel: "#build", Before: "anchor"}, false)
	d.requestHistory(HistoryRequest{ID: "older-space", Channel: "#space", Before: "anchor2"}, false)
	if err := d.advance(); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 || (*requests)[0].Channel != "#build" {
		t.Fatal("history was not serialized")
	}
	open := record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+h1", "chathistory", "#build"}}
	if err := d.record(open); err != nil {
		t.Fatal(err)
	}
	message := record{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#build", "line\n\n```go\ncode\n```"}, Tags: map[string]string{"batch": "h1", "msgid": "old", "account": "peer-account"}}
	if err := d.record(record{Command: "PRIVMSG", Prefix: "peer!peer-account@airc", Params: []string{"#space", "live"}, Tags: map[string]string{"msgid": "live", "account": "peer-account"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.record(message); err != nil {
		t.Fatal(err)
	}
	foreign := message
	foreign.Params = []string{"#space", "wrong"}
	if err := d.record(foreign); err == nil {
		t.Fatal("history child escaped parent channel")
	}
	if err := d.record(record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-h1"}}); err != nil {
		t.Fatal(err)
	}
	var page Event
	var live, replay Message
	for _, event := range *events {
		if event.Kind == "history-page" {
			page = event
		}
		if event.Kind == "message" && event.Message.ID == "live" {
			live = event.Message
		}
		if event.Kind == "message" && event.Message.ID == "old" {
			replay = event.Message
		}
	}
	if page.ID != "older-build" || page.Channel != "#build" || page.Count != 1 || live.Channel != "#space" || live.Replay || replay.Channel != "#build" || !replay.Replay || !replay.Older || replay.Text != message.Params[1] {
		t.Fatal("interleaved history/live/assembled multiline attribution failed")
	}
	if err := d.advance(); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 || (*requests)[1].Channel != "#space" {
		t.Fatal("next page did not retain its captured target")
	}
	if err := d.record(record{Command: "FAIL", Prefix: d.serverPrefix(), Params: []string{"CHATHISTORY", "HISTORY_LOST", "#build", "no"}}); err == nil {
		t.Fatal("foreign history FAIL settled a pending page")
	}
}

func TestChannelsGapAndKickPreserveUncertaintyWithoutAutojoin(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	d.requestHistory(HistoryRequest{ID: "page", Channel: "#build", Before: "anchor"}, false)
	if err := d.advance(); err != nil {
		t.Fatal(err)
	}
	if err := d.record(record{Command: "KICK", Prefix: d.serverPrefix(), Params: []string{"#build", d.wire.header.Nick, "Membership revoked"}}); err != nil {
		t.Fatal(err)
	}
	if d.channels["#build"].membership != "kicked" || d.channels["#space"].membership != "joined" || len(*requests) != 1 {
		t.Fatal("channel kick changed other membership or autojoined")
	}
	if err := d.action(Action{ID: "forget", Kind: "part", Channel: "#build"}); err != nil || len(*requests) != 1 {
		t.Fatal("explicit leave of kicked channel should be local", err)
	}
	d.gap()
	for _, state := range d.channels {
		if state.membership != "unknown" || state.bootstrap != "unknown" {
			t.Fatal("gap retained positive membership/setup")
		}
	}
	before := len(*events)
	if err := d.record(record{Command: "PRIVMSG", Params: []string{"#space", "orphan replay"}, Tags: map[string]string{"batch": "lost", "msgid": "old"}}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != before {
		t.Fatal("orphan replay after gap became live/unread")
	}
}

func TestChannelsCatalogRefreshRejectsConflictsAndNeverJoins(t *testing.T) {
	d, _, requests := channelDriverFixture(t)
	next := normalizedHeader(d.wire.header)
	next.Generation++
	next.Channels = append(next.Channels, managedchat.Channel{ID: managedchat.NewID(), Name: "#zeta", Members: append([]string(nil), next.Channels[0].Members...)})
	if err := d.refreshCatalog(Catalog{Header: next, RuntimeApplied: true}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 || d.channels["#zeta"].membership != "left" {
		t.Fatal("catalog refresh joined or followed a channel")
	}
	if err := d.refreshCatalog(Catalog{Header: next, RuntimeApplied: false, Pending: []string{"observation changed"}}); err != nil {
		t.Fatal("observed pending state was treated as declaration conflict", err)
	}
	conflict := normalizedHeader(next)
	conflict.Channels[1].ID = managedchat.NewID()
	if err := d.refreshCatalog(Catalog{Header: conflict}); err == nil {
		t.Fatal("same generation changed immutable catalog")
	}
	foreign := normalizedHeader(next)
	foreign.DiskID = managedchat.NewID()
	foreign.Generation++
	if err := d.refreshCatalog(Catalog{Header: foreign}); err == nil {
		t.Fatal("refresh crossed disk identity")
	}
	// The accepted snapshot owns its slices, not the caller's mutable buffer.
	next.Channels[0].Members[0] = managedchat.NewID()
	if reflect.DeepEqual(next.Channels, d.wire.header.Channels) {
		t.Fatal("accepted catalog aliases caller storage")
	}
}

func TestChannelsCatalogRefreshPreservesConnectionCapabilities(t *testing.T) {
	for _, attachments := range []bool{false, true} {
		d, events, requests := channelDriverFixture(t)
		d.wire.header.Attachments = attachments
		d.catalog.Attachments = attachments
		next := normalizedHeader(d.catalog)
		// Catalog metadata cannot enable or disable a capability negotiated
		// by the connection, at either the same or a newer generation.
		next.Attachments = !attachments
		for range 2 {
			if err := d.refreshCatalog(Catalog{Header: next}); err != nil {
				t.Fatalf("attachments=%v: %v", attachments, err)
			}
			if d.wire.header.Attachments != attachments || d.catalog.Attachments != attachments || (*events)[len(*events)-1].Catalog.Header.Attachments != attachments {
				t.Fatal("catalog changed the connection's attachment capability")
			}
			next.Generation++
		}
		if len(*requests) != 0 {
			t.Fatal("catalog refresh issued a wire request")
		}
	}
}

func TestChannelsSavedNicknameRefreshPreservesLiveCorrelation(t *testing.T) {
	d, events, _ := channelDriverFixture(t)
	liveNick, account := d.wire.header.Nick, d.wire.header.SelfAccount
	next := normalizedHeader(d.catalog)
	next.Generation++
	next.Nick = "saved-next"
	for i := range next.Identities {
		if next.Identities[i].Account == account {
			next.Identities[i].Nick = next.Nick
		}
	}
	for range 2 { // Repeat accepted generation must compare desired metadata.
		if err := d.refreshCatalog(Catalog{Header: next}); err != nil {
			t.Fatal("saved preference blocked identity refresh", err)
		}
	}
	if d.wire.header.Nick != liveNick || d.catalog.Nick != next.Nick || (*events)[len(*events)-1].Catalog.Header.Nick != next.Nick {
		t.Fatal("saved preference replaced live protocol nick or was not published")
	}
	conflict := normalizedHeader(next)
	conflict.Nick = "same-generation-conflict"
	if err := d.refreshCatalog(Catalog{Header: conflict}); err == nil {
		t.Fatal("nickname preference bypassed equal-generation conflict checks")
	}
	if err := d.send(Send{ID: "after-save", Channel: "#space", Text: "still live"}); err != nil {
		t.Fatal(err)
	}
	livePrefix := liveNick + "!" + account + "@airc"
	if err := d.record(record{Command: "PRIVMSG", Prefix: livePrefix, Params: []string{"#space", "still live"}, Tags: map[string]string{"msgid": "after-save-msg", "account": account, "+airc-send": "send-one"}}); err != nil {
		t.Fatal(err)
	}
	confirmed := false
	for _, event := range *events {
		confirmed = confirmed || event.Kind == "delivery" && event.ID == "after-save" && event.State == "confirmed"
	}
	if !confirmed || d.pendingSend() {
		t.Fatal("old live nickname's authenticated echo no longer confirms delivery")
	}
	text := "current topic"
	if err := d.action(Action{ID: "topic-save", Kind: "topic-set", Channel: "#build", Topic: &text}); err != nil {
		t.Fatal(err)
	}
	if err := d.record(record{Command: "TOPIC", Prefix: next.Nick + "!" + account + "@airc", Params: []string{"#build", text}}); err != nil || d.control == nil {
		t.Fatal("desired nickname impersonated the live connection", err)
	}
	if err := d.record(record{Command: "TOPIC", Prefix: livePrefix, Params: []string{"#build", text}}); err != nil || d.control != nil {
		t.Fatal("live TOPIC no longer confirms", err)
	}
	d.channels["#build"].membership = "left"
	if err := d.action(Action{ID: "join-after-save", Kind: "join", Channel: "#build"}); err != nil {
		t.Fatal(err)
	}
	if err := d.record(record{Command: "JOIN", Prefix: livePrefix, Params: []string{"#build"}, Cursor: 10}); err != nil {
		t.Fatal(err)
	}
	end := record{Command: "366", Prefix: d.serverPrefix(), Params: []string{liveNick, "#build", "end"}, Cursor: 11}
	if err := d.record(end); err != nil || d.channels["#build"].bootstrap != "ready" {
		t.Fatal("live recipient no longer completes JOIN bootstrap", err)
	}
	if err := d.advance(); err != nil {
		t.Fatal(err)
	}
	if err := d.record(record{Command: "315", Prefix: d.serverPrefix(), Params: []string{liveNick, "#build", "end"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.action(Action{ID: "query-after-save", Kind: "topic-read", Channel: "#build"}); err != nil || d.control == nil {
		t.Fatal("topic query refused after live 366", err)
	}
	if err := d.record(record{Command: "332", Prefix: d.serverPrefix(), Params: []string{liveNick, "#build", text}}); err != nil || d.control != nil {
		t.Fatal("live recipient no longer confirms topic query", err)
	}
}

func TestChannelsFilterStrictTopicAndChangingAuthorization(t *testing.T) {
	for _, valid := range []string{`{"id":"t","op":"topic","channel":"#build"}`, `{"id":"t","op":"topic","channel":"#build","text":""}`, `{"id":"t","op":"topic","channel":"#build","text":"value"}`} {
		var out bytes.Buffer
		if err := FilterChannels(strings.NewReader(valid+"\n"), &out, func(channel string) error {
			if channel != "#build" {
				return errors.New("foreign")
			}
			return nil
		}, false); err != nil {
			t.Fatal(err)
		}
		var before, after map[string]any
		_ = json.Unmarshal([]byte(valid), &before)
		_ = json.Unmarshal(out.Bytes(), &after)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("topic query/clear/set shape changed")
		}
	}
	for _, invalid := range []string{`{"id":"t","op":"topic","channel":"#build","text":null}`, `{"id":"t","op":"topic","channel":"#build","text":"x\ny"}`, `{"id":"t","op":"topic","channel":"#build","raw":"TOPIC"}`, `{"id":"t","op":"join","channel":"#space,#build"}`, `{"id":"t","op":"part","channel":"#build","reason":"x"}`, `{"id":"t","op":"topic","channel":"#Build"}`} {
		var out bytes.Buffer
		if err := FilterChannels(strings.NewReader(invalid+"\n"), &out, func(string) error { return nil }, false); err == nil || out.Len() != 0 {
			t.Fatal("invalid topic/membership request forwarded")
		}
	}
	reads := 0
	var out bytes.Buffer
	err := FilterChannels(strings.NewReader("{\"id\":\"a\",\"op\":\"join\",\"channel\":\"#build\"}\n{\"id\":\"b\",\"op\":\"who\",\"channel\":\"#build\"}\n"), &out, func(string) error {
		reads++
		if reads > 1 {
			return errors.New("permission changed")
		}
		return nil
	}, false)
	if err == nil || reads != 2 || strings.Count(out.String(), "\n") != 1 {
		t.Fatal("filter retained a stale startup-only allowlist")
	}
}

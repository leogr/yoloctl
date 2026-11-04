package chatstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func testHeader(t *testing.T) Header {
	t.Helper()
	r := &managedchat.Room{ID: managedchat.NewID(), DiskID: managedchat.NewID(), Generation: 3, Channel: "#space", Human: managedchat.Member{ID: managedchat.NewID(), Name: "human"}}
	h, err := RoomHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLogicalReceiptAndEchoIdentity(t *testing.T) {
	h := testHeader(t)
	var events []Event
	p := &pendingSend{Send: Send{ID: "local", Text: "first\n```go\ncode\n```", ReplyTo: "parent"}, Receipt: "receipt"}
	d := &driver{header: h, pending: p, batches: map[string]*historyBatch{"history": {}}, emit: func(e Event) { events = append(events, e) }}
	message := record{Command: "PRIVMSG", Params: []string{"#space", p.Text}, Prefix: "human!user@server", Tags: map[string]string{"msgid": "stable", "account": "impostor", "+airc-send": "receipt", "+reply": "parent"}}
	if err := d.record(message); err != nil {
		t.Fatal(err)
	}
	if events[0].Message.ClientID != "" || d.pending == nil {
		t.Fatal("tag/nick impersonation confirmed a send")
	}
	message.Tags["account"] = h.SelfAccount
	message.Params[1] = "partial"
	if err := d.record(message); err != nil {
		t.Fatal(err)
	}
	if events[1].Message.ClientID != "" {
		t.Fatal("partial echo correlated")
	}
	if err := d.record(record{Event: "receipt", Receipt: "receipt", State: "confirmed", Target: "#space", MsgID: "stable"}); err != nil {
		t.Fatal(err)
	}
	if events[2].State != "confirmed" || events[2].ID != "local" || d.pending != nil {
		t.Fatal(events)
	}
	message.Params[1] = p.Text
	message.Tags["batch"] = "history"
	if err := d.record(message); err != nil {
		t.Fatal(err)
	}
	last := events[3].Message
	if last.ClientID != "local" || !last.Replay || last.Text != p.Text {
		t.Fatal(last)
	}
}

func TestReceiptWithoutStableIDNeverConfirms(t *testing.T) {
	d := &driver{header: testHeader(t), pending: &pendingSend{Send: Send{ID: "local"}, Receipt: "r"}, emit: func(Event) { t.Fatal("emitted confirmation") }}
	if err := d.record(record{Event: "receipt", Receipt: "r", Target: "#space", State: "confirmed"}); err == nil || d.pending == nil {
		t.Fatal("invalid receipt cleared pending")
	}
}

func TestLogicalFullEchoResolvesMissingReceipt(t *testing.T) {
	h := testHeader(t)
	p := &pendingSend{Send: Send{ID: "local", Text: "full\nbody"}, Receipt: "r"}
	var events []Event
	d := &driver{header: h, pending: p, batches: map[string]*historyBatch{}, emit: func(e Event) { events = append(events, e) }}
	err := d.record(record{Command: "PRIVMSG", Prefix: h.Nick + "!user@server", Params: []string{"#space", p.Text}, Tags: map[string]string{"msgid": "stable", "account": h.SelfAccount, "+airc-send": "r"}})
	if err != nil || d.pending != nil || len(events) != 2 || events[0].State != "confirmed" || events[1].Message.ClientID != "local" {
		t.Fatal("full echo did not resolve adapter and UI together", err, events)
	}
	if err := d.record(record{Event: "receipt", Receipt: "r", Target: "#space", State: "confirmed", MsgID: "stable"}); err != nil || len(events) != 2 {
		t.Fatal("late duplicate receipt changed terminal outcome", err, events)
	}
}

func TestDriverPreservesCursorAndMarksLostSendUncertain(t *testing.T) {
	h := testHeader(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	serverRead, hostWrite := io.Pipe()
	hostRead, serverWrite := io.Pipe()
	defer func() { _ = serverRead.Close(); _ = serverWrite.Close() }()
	outgoing := make(chan Send, 1)
	outgoing <- Send{ID: "local", Text: "one\ntwo"}
	served := make(chan error, 1)
	go func() {
		defer func() { _ = serverWrite.Close() }()
		enc := json.NewEncoder(serverWrite)
		_ = enc.Encode(h)
		_ = enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "epoch-1", "account": h.SelfAccount, "nick": h.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1024}})
		read := bufio.NewReader(serverRead)
		for n := 0; n < 6; n++ {
			line, err := read.ReadBytes('\n')
			if err != nil {
				served <- err
				return
			}
			var q Request
			if err := json.Unmarshal(line, &q); err != nil {
				served <- err
				return
			}
			var result any
			switch n {
			case 0:
				if q.Op != "history" {
					served <- errors.New("missing initial history")
					return
				}
				result = map[string]string{"status": "queued"}
			case 1:
				if q.Op != "members" || q.Channel != "#space" {
					served <- errors.New("missing roster request")
					return
				}
				result = map[string]string{"status": "queued"}
			case 2:
				if q.Op != "who" || q.Channel != "#space" {
					served <- errors.New("missing roster identity request")
					return
				}
				result = map[string]string{"status": "queued"}
			case 3:
				if q.Op != "send" || q.Text != "one\ntwo" {
					served <- errors.New("send changed")
					return
				}
				result = map[string]string{"receipt": "receipt-1", "state": "pending"}
			case 4:
				if q.Op != "read" || q.After != 0 {
					served <- errors.New("first read cursor changed")
					return
				}
				result = map[string]any{"epoch": "epoch-1", "next": 4, "latest": 9, "gap": true, "events": []any{map[string]any{"cursor": 4, "command": "PING", "params": []string{"x"}}}}
			case 5:
				if q.Op != "read" || q.After != 4 {
					served <- errors.New("driver skipped to latest")
					return
				}
				served <- nil
				return // dropped reply, no resend
			}
			if err := enc.Encode(map[string]any{"id": q.ID, "ok": true, "result": result}); err != nil {
				served <- err
				return
			}
		}
	}()
	var events []Event
	if err := Run(ctx, hostRead, hostWrite, outgoing, nil, func(e Event) { events = append(events, e) }); err == nil {
		t.Fatal("lost connection succeeded")
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	uncertain, gap, confirmed := 0, 0, 0
	for _, e := range events {
		if e.State == "uncertain" {
			uncertain++
		}
		if e.Kind == "gap" {
			gap++
		}
		if e.State == "confirmed" {
			confirmed++
		}
	}
	if uncertain != 1 || gap != 1 || confirmed != 0 {
		t.Fatal(events)
	}
}

type writeCloser struct{ bytes.Buffer }

func (*writeCloser) Close() error { return nil }

func TestDriverRejectsEpochOrFrameBeforeSend(t *testing.T) {
	h := testHeader(t)
	for _, bad := range []string{"partial", strings.Repeat("x", ResponseLimit) + "\n"} {
		var output writeCloser
		if err := Run(t.Context(), io.NopCloser(strings.NewReader(bad)), &output, nil, nil, func(Event) {}); err == nil || output.Len() != 0 {
			t.Fatal("bad framing allowed request", err)
		}
	}
	h.HumanAccount = "impostor"
	if validateHeader(h) == nil {
		t.Fatal("untrusted identity accepted")
	}
}

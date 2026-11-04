package chatstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sendTap records each send request the host writes and can fail that Write.
type sendTap struct {
	io.WriteCloser
	sends  []string
	fail   func(w io.Writer, p []byte) (int, error)
	closed atomic.Bool
}

func (w *sendTap) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"op":"send"`)) {
		w.sends = append(w.sends, string(p))
		if w.fail != nil {
			return w.fail(w.WriteCloser, p)
		}
	}
	return w.WriteCloser.Write(p)
}

func (w *sendTap) Close() error {
	w.closed.Store(true)
	return w.WriteCloser.Close()
}

// peerReply is how the synthetic peer answers one complete send request.
type peerReply struct {
	reply map[string]any // its "id" defaults to the request's
	queue []record       // events published after the reply
	raw   string         // written verbatim instead of a reply
	drop  bool           // end the stream without replying
}

// servePeer is a bounded synthetic JSONL peer, not live AIRC: it joins
// #space, answers WHO and the latest-history page, and lets send answer each
// send request. It returns when the host closes the stream or a send drops it.
func servePeer(fixture *channelDriver, header Header, read io.ReadCloser, write io.WriteCloser, send func(Request) peerReply) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer func() { _ = read.Close(); _ = write.Close() }()
		enc := json.NewEncoder(write)
		if err := enc.Encode(header); err != nil {
			done <- err
			return
		}
		if err := enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "peer-epoch", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
			done <- err
			return
		}
		queue := []record{selfRecord(fixture, "JOIN", "#space"), numericRecord(fixture, "331", "#space", "No topic"), numericRecord(fixture, "366", "#space", "end")}
		cursor, serial := uint64(0), 0
		reader := bufio.NewReader(read)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- nil // the host closed the stream
				return
			}
			var request Request
			if err := json.Unmarshal(line, &request); err != nil {
				done <- err
				return
			}
			reply := map[string]any{"ok": true, "result": map[string]string{"status": "queued"}}
			switch request.Op {
			case "read":
				var page []record
				if len(queue) > 0 {
					cursor++
					event := queue[0]
					queue = queue[1:]
					event.Cursor = cursor
					page = []record{event}
				}
				reply["result"] = map[string]any{"epoch": "peer-epoch", "events": page, "next": cursor, "latest": cursor + uint64(len(queue))}
			case "who":
				queue = append(queue, numericRecord(fixture, "315", request.Channel, "end"))
			case "history":
				serial++
				batch := fmt.Sprintf("h%d", serial)
				queue = append(queue, record{Command: "BATCH", Prefix: fixture.serverPrefix(), Params: []string{"+" + batch, "chathistory", request.Channel}}, record{Command: "BATCH", Prefix: fixture.serverPrefix(), Params: []string{"-" + batch}})
			case "send":
				answer := send(request)
				queue = append(queue, answer.queue...)
				if answer.drop {
					done <- nil
					return
				}
				if answer.raw != "" {
					if _, err := io.WriteString(write, answer.raw); err != nil {
						done <- nil
						return
					}
					continue
				}
				reply = answer.reply
			default:
				done <- fmt.Errorf("unexpected op %s", request.Op)
				return
			}
			if _, ok := reply["id"]; !ok {
				reply["id"] = request.ID
			}
			if err := enc.Encode(reply); err != nil {
				done <- nil
				return
			}
		}
	}()
	return done
}

// imageSend admits an image only when the stream has the attachment feature.
var imageSend = Send{ID: "image-send", Channel: "#space", Text: "é see [Image #1]", ReplyTo: "parent-msgid", Attach: []Attach{{ID: attachA, At: 7}}}

// The adapters keep the probed attachment capability while the shared wire
// has lost it, the mismatch an earlier catalog refresh caused (since fixed).
// The adapter admits the image send; the wire's own allowlist refuses it
// before Write. That is a definite rejection on the same live stream, and an
// explicit later send on it is written once and confirmed by its echo.
func TestChannelsPreWriteRejectionKeepsTheStream(t *testing.T) {
	fixture, _, _ := channelDriverFixture(t)
	header := fixture.wire.header
	header.Attachments = true
	hostRead, serverWrite := io.Pipe()
	serverRead, hostWrite := io.Pipe()
	tap := &sendTap{WriteCloser: hostWrite}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	plain := Send{ID: "text-send", Channel: "#space", Text: "explicit follow-up", ReplyTo: "parent-msgid"}
	var peerSends []Request
	done := servePeer(fixture, header, serverRead, serverWrite, func(request Request) peerReply {
		peerSends = append(peerSends, request)
		echo := selfRecord(fixture, "PRIVMSG", "#space", request.Text)
		echo.Tags = map[string]string{"msgid": "m-plain", "account": header.SelfAccount, "+airc-send": "r-plain", "+reply": request.ReplyTo}
		return peerReply{
			reply: map[string]any{"ok": true, "result": map[string]string{"receipt": "r-plain", "state": "pending"}},
			queue: []record{echo, {Event: "receipt", Receipt: "r-plain", Target: "#space", State: "confirmed", MsgID: "m-plain"}},
		}
	})
	outgoing := make(chan Send, 1)
	var events []Event
	var d *channelDriver
	queued, rejected, confirmed := false, -1, -1
	pendingAtRejection, closedAtRejection, adapterAttachments := true, true, false
	d = newChannelDriver(hostRead, tap, func(event Event) {
		events = append(events, event)
		switch {
		case event.Kind == "connection" && event.Connected:
			// Inject the mismatch after the handshake built the adapters.
			d.wire.header.Attachments = false
			adapterAttachments = d.channels["#space"].adapter.header.Attachments
		case event.Kind == "members" && event.Channel == "#space" && !d.setupPending() && !queued:
			queued = true
			outgoing <- imageSend
		case event.Kind == "delivery" && event.ID == imageSend.ID:
			rejected = len(events) - 1
			pendingAtRejection, closedAtRejection = d.pendingSend(), tap.closed.Load()
			outgoing <- plain
		case event.Kind == "delivery" && event.ID == plain.ID && event.State == "confirmed":
			confirmed = len(events) - 1
			cancel()
		}
	})
	err := d.run(ctx, outgoing, nil, nil, nil)
	if !errors.Is(err, context.Canceled) && (err == nil || !strings.Contains(err.Error(), "was closed by this view")) {
		t.Fatalf("the run ended for another reason than the test's cancel: %v", err)
	}
	if peerErr := <-done; peerErr != nil {
		t.Fatal(peerErr)
	}
	if !adapterAttachments {
		t.Fatal("the injected mismatch needs adapters that still admit attachments")
	}
	if rejected < 0 || confirmed < 0 {
		t.Fatalf("the refusal or the explicit follow-up did not complete: rejected=%d confirmed=%d events=%+v", rejected, confirmed, events)
	}
	refusal := events[rejected]
	if refusal.State != "rejected" || refusal.Channel != "#space" || refusal.Err == nil || !strings.HasPrefix(refusal.Err.Error(), "message was not sent: ") {
		t.Fatalf("want one definite rejection for the original ID and channel: %+v", refusal)
	}
	if pendingAtRejection || closedAtRejection {
		t.Fatalf("the rejection must clear the pending send and keep the stream open: pending=%v closed=%v", pendingAtRejection, closedAtRejection)
	}
	if len(tap.sends) != 1 || strings.Contains(tap.sends[0], `"attach"`) || len(peerSends) != 1 || peerSends[0].Text != plain.Text || peerSends[0].ReplyTo != plain.ReplyTo || len(peerSends[0].Attach) != 0 {
		t.Fatalf("only the explicit follow-up may be written, once: tap=%q peer=%+v", tap.sends, peerSends)
	}
	var connections, deliveries []int
	echoed := false
	for i, event := range events {
		switch event.Kind {
		case "connection":
			connections = append(connections, i)
		case "delivery":
			deliveries = append(deliveries, i)
		case "message":
			echoed = echoed || event.Message.ClientID == plain.ID && event.Message.ReplyTo == plain.ReplyTo
		}
	}
	if len(connections) != 2 || !events[connections[0]].Connected || connections[1] < confirmed {
		t.Fatalf("the refusal must not end the connection: connection events at %v, confirmation at %d", connections, confirmed)
	}
	if !slices.Equal(deliveries, []int{rejected, confirmed}) || !echoed {
		t.Fatalf("want exactly the rejection and the echo-confirmed follow-up, no uncertain outcome: %v echoed=%v", deliveries, echoed)
	}
}

// Once Write was invoked the peer may hold the request, whatever the byte
// count or the error's text: the send stays uncertain, ends the stream, and
// is never written again. Only exchange's typed pre-write refusal rejects.
func TestChannelsWrittenSendFailuresStayUncertain(t *testing.T) {
	_, invalid := decodeChannelRequest([]byte("{}"), func(string) bool { return false }, false)
	validation := invalid.Error()
	for _, c := range []struct {
		name   string
		fail   func(w io.Writer, p []byte) (int, error)
		answer peerReply
	}{
		{name: "zero bytes", fail: func(io.Writer, []byte) (int, error) { return 0, errors.New("write failed") }},
		{name: "zero bytes with the local validation text", fail: func(io.Writer, []byte) (int, error) { return 0, errors.New(validation) }},
		{name: "partial bytes", fail: func(w io.Writer, p []byte) (int, error) {
			n, _ := w.Write(p[:len(p)/2])
			return n, io.ErrShortWrite
		}},
		{name: "full write then a lost reply", answer: peerReply{drop: true}},
		{name: "full write then a malformed reply", answer: peerReply{raw: "not json\n"}},
		{name: "full write then an uncorrelated reply", answer: peerReply{reply: map[string]any{"id": "q999", "ok": true, "result": map[string]string{"receipt": "r", "state": "pending"}}}},
		{name: "full write then a peer error with the local validation text", answer: peerReply{reply: map[string]any{"ok": false, "error": validation}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture, _, _ := channelDriverFixture(t)
			header := fixture.wire.header
			header.Attachments = true
			hostRead, serverWrite := io.Pipe()
			serverRead, hostWrite := io.Pipe()
			tap := &sendTap{WriteCloser: hostWrite, fail: c.fail}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			peerSends := 0
			done := servePeer(fixture, header, serverRead, serverWrite, func(Request) peerReply {
				peerSends++
				return c.answer
			})
			outgoing := make(chan Send, 1)
			var events []Event
			var d *channelDriver
			queued := false
			d = newChannelDriver(hostRead, tap, func(event Event) {
				events = append(events, event)
				if event.Kind == "members" && event.Channel == "#space" && !d.setupPending() && !queued {
					queued = true
					outgoing <- imageSend
				}
			})
			err := d.run(ctx, outgoing, nil, nil, nil)
			if err == nil || ctx.Err() != nil {
				t.Fatalf("the written send must end the stream on its own: err=%v ctx=%v", err, ctx.Err())
			}
			if peerErr := <-done; peerErr != nil {
				t.Fatal(peerErr)
			}
			var deliveries []Event
			for _, event := range events {
				if event.Kind == "delivery" {
					deliveries = append(deliveries, event)
				}
			}
			if len(deliveries) != 1 || deliveries[0].ID != imageSend.ID || deliveries[0].Channel != "#space" || deliveries[0].State != "uncertain" {
				t.Fatalf("want exactly one uncertain outcome for the original send: %+v", deliveries)
			}
			if len(tap.sends) != 1 || peerSends > 1 {
				t.Fatalf("the send was attempted %d times (peer saw %d)", len(tap.sends), peerSends)
			}
			var refused *localRejection
			if errors.As(err, &refused) {
				t.Fatalf("a failure after Write must never be a local rejection: %v", err)
			}
		})
	}
}

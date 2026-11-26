package chatstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestChatCommandWaitsForAHeaderPastTheOldLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		header := testHeader(t)
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		r, w := io.Pipe()
		go func() {
			defer func() { _ = w.Close() }()
			time.Sleep(31 * time.Second)
			enc := json.NewEncoder(w)
			if enc.Encode(header) != nil {
				return
			}
			_ = enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "slow-startup", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}})
		}()
		connected := false
		err := RunChannels(ctx, r, nopWriteCloser{io.Discard}, nil, nil, nil, nil, func(e Event) {
			if e.Kind == "connection" && e.Connected {
				connected = true
				cancel()
			}
		})
		if !connected || !errors.Is(err, context.Canceled) {
			t.Fatalf("slow command stream connected %t, returned %v", connected, err)
		}
	})
}

func TestChatViewFailSafeForSilentHeader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		defer func() { _ = w.Close() }()
		start := time.Now()
		err := RunViewChannels(t.Context(), r, nopWriteCloser{io.Discard}, nil, nil, nil, nil, func(Event) {})
		if err == nil || !strings.Contains(err.Error(), "sent no room header") || time.Since(start) != 2*time.Minute {
			t.Fatalf("silent view ended after %v: %v", time.Since(start), err)
		}
	})
}

func TestChatViewStalledReadUsesPendingControlDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		defer func() { _ = w.Close() }()
		d := newChannelDriver(r, nopWriteCloser{io.Discard}, func(Event) {})
		d.wire.replyTimeout = 2 * time.Minute
		d.wire.controlDeadline = d.controlDeadline
		d.control = &pendingChannelAction{until: time.Now().Add(15 * time.Second)}
		start := time.Now()
		err := d.readOne()
		if err == nil || !strings.Contains(err.Error(), "before the pending control deadline") || time.Since(start) != 15*time.Second {
			t.Fatalf("silent read ended after %v: %v", time.Since(start), err)
		}
	})
}

func TestChatViewKeepsLiveMessagesAfterMissingTerminator(t *testing.T) {
	for _, phase := range []string{"who", "history"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { testChatViewMissingTerminator(t, phase) })
		})
	}
}

func testChatViewMissingTerminator(t *testing.T, phase string) {
	t.Helper()
	initial, _, _ := channelDriverFixture(t)
	header := initial.wire.header
	const budget = 1100 * time.Millisecond // several responsive polls before expiry
	hostRead, peerWrite := io.Pipe()
	peerRead, hostWrite := io.Pipe()
	peerDone := make(chan error, 1)
	go func() {
		defer func() { _ = peerRead.Close(); _ = peerWrite.Close() }()
		enc := json.NewEncoder(peerWrite)
		if err := enc.Encode(header); err != nil {
			peerDone <- err
			return
		}
		if err := enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "responsive", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
			peerDone <- err
			return
		}
		reader := bufio.NewReader(peerRead)
		queue := []record{selfRecord(initial, "JOIN", "#space"), numericRecord(initial, "366", "#space", "end")}
		var cursor uint64
		var pendingSince time.Time
		liveQueued := false
		for {
			line, err := reader.ReadBytes('\n')
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				peerDone <- nil
				return
			}
			var request Request
			if err != nil || json.Unmarshal(line, &request) != nil {
				peerDone <- fmt.Errorf("read malformed request %q: %v", line, err)
				return
			}
			var result any = map[string]string{"status": "queued"}
			switch request.Op {
			case "who":
				if phase == "who" {
					pendingSince = time.Now()
				} else {
					queue = append(queue, numericRecord(initial, "315", "#space", "end"))
				}
			case "history":
				if phase == "history" {
					pendingSince = time.Now()
					queue = append(queue, record{Command: "BATCH", Prefix: initial.serverPrefix(), Params: []string{"+missing", "chathistory", "#space"}})
				}
			case "read":
				var page []record
				if len(queue) > 0 {
					cursor++
					event := queue[0]
					queue = queue[1:]
					event.Cursor = cursor
					page = []record{event}
				} else {
					// A normal poll straddles the request deadline and still replies.
					time.Sleep(250 * time.Millisecond)
					if !liveQueued && !pendingSince.IsZero() && time.Since(pendingSince) > budget {
						live := selfRecord(initial, "PRIVMSG", "#space", "after the gap")
						live.Tags = map[string]string{"msgid": "live-after-gap", "account": header.SelfAccount}
						queue = append(queue, live)
						liveQueued = true
					}
				}
				result = map[string]any{"epoch": "responsive", "events": page, "next": cursor, "latest": cursor + uint64(len(queue))}
			default:
				peerDone <- fmt.Errorf("unexpected op %q", request.Op)
				return
			}
			if err := enc.Encode(map[string]any{"id": request.ID, "ok": true, "result": result}); err != nil {
				peerDone <- nil
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	wantGap := "channel bootstrap/roster timed out"
	if phase == "history" {
		wantGap = "history response timed out"
	}
	gap, live := false, false
	d := newChannelDriver(hostRead, hostWrite, func(e Event) {
		if e.Kind == "gap" && e.Err != nil && strings.Contains(e.Err.Error(), wantGap) {
			gap = true
		}
		if e.Kind == "message" && e.Message.ID == "live-after-gap" {
			live = gap
			cancel()
		}
	})
	d.wire.replyTimeout = 2 * time.Minute
	d.partialTimeout = budget
	d.wire.controlDeadline = d.controlDeadline
	err := d.run(ctx, nil, nil, nil, nil)
	if peerErr := <-peerDone; peerErr != nil {
		t.Fatal(peerErr)
	}
	if !gap || !live || !errors.Is(err, context.Canceled) {
		t.Fatalf("responsive peer lost: gap %t, live message after gap %t, error %v", gap, live, err)
	}
}

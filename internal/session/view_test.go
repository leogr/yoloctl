package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
)

func TestPrivateRelayPreservesBufferedInputAndEOF(t *testing.T) {
	server, remote := net.Pipe()
	private, frontend := net.Pipe()
	defer func() { _ = server.Close(); _ = frontend.Close() }()
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	want := bytes.Repeat([]byte("typeahead\x00\r\n"), 6000)
	relayDone, viewDone := make(chan error, 1), make(chan error, 1)
	go func() { relayDone <- Relay(context.Background(), remote, private, bytes.NewReader(want)) }()
	var live []byte
	go func() {
		viewDone <- Attach(context.Background(), frontend, AttachIO{OnEvent: func(e ViewEvent) error { live = append(live, e.Data...); return nil }})
	}()
	if err := writeFrame(server, frameHello, []byte{protocolVersion}); err != nil {
		t.Fatal(err)
	}
	f, err := readFrame(server)
	if err != nil || f.Type != frameSubscribe {
		t.Fatalf("input preceded subscription: %v %v", f, err)
	}
	var sub subscription
	if err := decodeJSON(f.Payload, &sub); err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(viewState{Bundle: sub.Bundle, Core: sub.Core, Session: "fixture", Client: "client", SnapshotBytes: 1})
	for _, f := range []frame{{frameState, p}, {frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}} {
		if err := writeFrame(server, f.Type, f.Payload); err != nil {
			t.Fatal(err)
		}
	}
	var got []byte
	for len(got) < len(want) {
		f, err := readFrame(server)
		if err != nil || f.Type != frameData {
			t.Fatalf("stdin=%v %v", f, err)
		}
		got = append(got, f.Payload...)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("buffered stdin changed or reordered")
	}
	// Exhausted command stdin is not a detach: live output and status still arrive.
	if err := writeFrame(server, frameData, sequenced(1, []byte("live-after-eof"))); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(server, frameExit, encodeExit(17)); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{relayDone, viewDone} {
		select {
		case err := <-done:
			var status *ExitError
			if !errors.As(err, &status) || status.Status != 17 {
				t.Fatalf("exit=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("relay did not finish")
		}
	}
	if string(live) != "live-after-eof" {
		t.Fatalf("live output=%q", live)
	}
}

func TestManagedViewRejectsInvalidStateAndEventOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     func(*viewState)
		frames    []frame
		want      string
		published int
	}{
		{name: "bundle", state: func(s *viewState) { s.Bundle = "old-build" }, want: "incompatible session state"},
		{name: "core", state: func(s *viewState) { s.Core = "old-core" }, want: "incompatible session state"},
		{name: "missing incarnation", state: func(s *viewState) { s.Session = "" }, want: "incompatible session state"},
		{name: "snapshot limit", state: func(s *viewState) { s.SnapshotBytes = terminal.MaxSnapshotBytes + 1 }, want: "incompatible session state"},
		{name: "snapshot overflow", frames: []frame{{frameSnapshot, []byte("xx")}}, want: "exceeds declared size"},
		{name: "incomplete snapshot", frames: []frame{{frameSnapshotEnd, nil}}, want: "incomplete session snapshot"},
		{name: "data before snapshot", frames: []frame{{frameData, sequenced(42, []byte("bad"))}}, want: "expected complete session snapshot"},
		{name: "gap", frames: []frame{{frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}, {frameData, sequenced(43, []byte("bad"))}}, want: "session event gap", published: 1},
		{name: "duplicate", frames: []frame{{frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}, {frameData, sequenced(42, []byte("once"))}, {frameData, sequenced(42, []byte("twice"))}}, want: "session event gap", published: 2},
		{name: "broken resize", frames: []frame{{frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}, {frameResize, sequenced(42, []byte{1})}}, want: "resize payload", published: 1},
		{name: "activity without bytes", frames: []frame{{frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}, {frameActivity, sequenced(42, []byte(`{"sequence":1,"source":"peer","bytes":0}`))}}, want: "invalid input activity", published: 1},
		{name: "unknown event", frames: []frame{{frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}, {'?', sequenced(42, []byte("bad"))}}, want: "unexpected session event", published: 1},
		{name: "clipboard interleaving", frames: []frame{{frameSnapshot, []byte("x")}, {frameSnapshotEnd, nil}, {frameClipboard, sequenced(42, []byte("{"))}, {frameData, sequenced(43, []byte("bad"))}}, want: "interrupted clipboard event", published: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer func() { _ = server.Close(); _ = client.Close() }()
			_ = server.SetDeadline(time.Now().Add(3 * time.Second))
			published := 0
			done := make(chan error, 1)
			go func() {
				done <- Attach(context.Background(), client, AttachIO{OnEvent: func(ViewEvent) error { published++; return nil }})
			}()
			if err := writeFrame(server, frameHello, []byte{protocolVersion}); err != nil {
				t.Fatal(err)
			}
			f, err := readFrame(server)
			if err != nil || f.Type != frameSubscribe {
				t.Fatalf("subscribe=%v %v", f, err)
			}
			var sub subscription
			if err := decodeJSON(f.Payload, &sub); err != nil {
				t.Fatal(err)
			}
			state := viewState{Bundle: sub.Bundle, Core: sub.Core, Session: "fixture", Client: "client", Sequence: 41, SnapshotBytes: 1}
			if tc.state != nil {
				tc.state(&state)
			}
			p, _ := json.Marshal(state)
			if err := writeFrame(server, frameState, p); err != nil {
				t.Fatal(err)
			}
			for _, frame := range tc.frames {
				if err := writeFrame(server, frame.Type, frame.Payload); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), tc.want) || published != tc.published {
					t.Fatalf("error=%v, published=%d; want %q, %d", err, published, tc.want, tc.published)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("invalid session stream left attachment waiting")
			}
		})
	}
}

func TestRelayPrivateChannelClosureDetachesIdleSession(t *testing.T) {
	holder, remote := net.Pipe()
	private, frontend := net.Pipe()
	defer func() { _ = holder.Close() }()
	defer func() { _ = frontend.Close() }()
	done := make(chan error, 1)
	go func() { done <- Relay(context.Background(), remote, private, nil) }()
	_ = frontend.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("detach=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle relay survived private channel closure")
	}
	_ = holder.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := holder.Read(b[:]); err != io.EOF {
		t.Fatalf("holder client socket left open: %v", err)
	}
}

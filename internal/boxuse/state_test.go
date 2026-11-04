package boxuse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
)

func testState(t *testing.T, initial string) *state {
	t.Helper()
	vt, err := terminal.New(6, 40, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	if _, err := vt.Write([]byte(initial)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	s := newState()
	t.Cleanup(s.close)
	if err := s.apply(context.Background(), session.ViewEvent{Snapshot: snapshot, SessionID: "session", ClientID: "self"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func applyText(t *testing.T, s *state, p string) {
	t.Helper()
	if err := s.apply(context.Background(), session.ViewEvent{Data: []byte(p)}); err != nil {
		t.Fatal(err)
	}
}

func TestScreenRestoresAndDiffsWithoutRedrawNoise(t *testing.T) {
	s := testState(t, "\x1b[2J\x1b[Hheader\r\n漢字 👩‍💻\r\nold")
	first, base := s.observe(0, observation{})
	if !first.Full || first.Lines[1] != "漢字 👩‍💻" || first.Rows != 6 || first.Cols != 40 {
		t.Fatalf("screen: %+v", first)
	}
	redraw := "\x1b[1;1Hheader\x1b[3;4H"
	for range 1000 {
		applyText(t, s, redraw)
	}
	stable, _ := s.observe(first.Revision, base)
	if stable.Revision != first.Revision || stable.Full || len(stable.Changed) != 0 {
		t.Fatalf("redraw changed observation: %+v", stable)
	}
	compact, err := json.Marshal(stable)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact)*10 >= len(redraw)*1000 {
		t.Fatal("observation did not bound redraw output")
	}
	t.Logf("Redraw fixture: %d raw bytes -> %d observation JSON bytes (byte comparison, not a token benchmark)", len(redraw)*1000, len(compact))
	applyText(t, s, "\x1b[3;1H\x1b[2K")
	diff, latest := s.observe(first.Revision, base)
	if diff.Full || len(diff.Changed) != 1 || diff.Changed[0] != (rowChange{Row: 2, Text: ""}) {
		t.Fatalf("erase diff: %+v", diff)
	}
	stale, _ := s.observe(first.Revision, latest)
	if !stale.Full {
		t.Fatal("stale baseline must fall back to full screen")
	}
	applyText(t, s, "\x1b[?1049h\x1b[HALTERNATE")
	alt, _ := s.observe(latest.Revision, latest)
	if !alt.Full || !alt.Alternate || alt.Lines[0] != "ALTERNATE" {
		t.Fatalf("alternate screen: %+v", alt)
	}
	if err := s.apply(context.Background(), session.ViewEvent{Size: terminal.Geometry{Rows: 8, Cols: 60}}); err != nil {
		t.Fatal(err)
	}
	resized, _ := s.observe(alt.Revision, alt)
	if !resized.Full || resized.Rows != 8 || resized.Cols != 60 {
		t.Fatalf("geometry: %+v", resized)
	}
}

func TestHoldAndPeerActivityAreAdvisory(t *testing.T) {
	s := testState(t, "before")
	first, _ := s.observe(0, observation{})
	applyText(t, s, "\x1b[?2026h\x1b[Hafter\x1b[K")
	held, _ := s.observe(0, observation{})
	if !held.Holding || held.Lines[0] != first.Lines[0] {
		t.Fatalf("published held update: %+v", held)
	}
	if err := s.apply(context.Background(), session.ViewEvent{EndHold: true}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []session.InputActivity{{Sequence: 1, Source: "peer", Bytes: 4}, {Sequence: 2, Source: "self", Bytes: 1}} {
		if err := s.apply(context.Background(), session.ViewEvent{Input: &a}); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.observe(0, observation{})
	if after.Holding || after.Lines[0] != "after" || after.Input.Sequence != 2 || after.Input.PeerRevision != 1 || after.Input.LastPeer != "peer" {
		t.Fatalf("observation: %+v", after)
	}
	if _, err := s.encode(request{Op: "text", Text: "still interactive"}); err != nil {
		t.Fatal("peer activity blocked input", err)
	}
}

func TestWaitChangeTimeoutCancellationAndEnd(t *testing.T) {
	s := testState(t, "ready")
	view, _ := s.observe(0, observation{})
	if timedOut, err := s.wait(context.Background(), view.Revision, time.Millisecond, 0); err != nil || !timedOut {
		t.Fatalf("timeout: %v %v", timedOut, err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.wait(context.Background(), view.Revision, time.Second, 0); done <- err }()
	applyText(t, s, " changed")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait missed change")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	current, _ := s.observe(0, observation{})
	if _, err := s.wait(ctx, current.Revision, time.Second, 0); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	go func() { _, err := s.wait(context.Background(), current.Revision, time.Minute, 0); done <- err }()
	s.end()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session end stranded wait")
	}
}

// Acceptance is this attachment's cumulative holder-queued count. Peer counts,
// the global sequence and partial own progress cannot satisfy it.
func TestInputAcceptanceCountsOnlyThisAttachment(t *testing.T) {
	s := testState(t, "ready")
	accept := func(a session.InputActivity) error {
		return s.apply(context.Background(), session.ViewEvent{Input: &a})
	}
	for _, a := range []session.InputActivity{{Sequence: 1, Source: "peer", Bytes: 64}, {Sequence: 2, Source: "self", Bytes: 2}} {
		if err := accept(a); err != nil {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.awaitInput(canceled, 5); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("peer or partial progress confirmed input: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.awaitInput(context.Background(), 5) }()
	if err := accept(session.InputActivity{Sequence: 3, Source: "self", Bytes: 5}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("acceptance did not release input")
	}
	if err := s.awaitInput(context.Background(), 4); err == nil {
		t.Fatal("accepted more input than was submitted")
	}
	if err := accept(session.InputActivity{Sequence: 4, Source: "self", Bytes: 5}); err == nil {
		t.Fatal("own acceptance must grow")
	}
	ended := testState(t, "ready")
	go func() { done <- ended.awaitInput(context.Background(), 1) }()
	ended.end()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("session end confirmed input")
		}
	case <-time.After(time.Second):
		t.Fatal("session end stranded input")
	}
}

func TestInputUsesRestoredModesAndExplicitSubmit(t *testing.T) {
	s := testState(t, "")
	for _, tc := range []struct {
		r    request
		want string
	}{
		{request{Op: "text", Text: "hello"}, "hello"},
		{request{Op: "key", Key: "enter"}, "\r"},
		{request{Op: "key", Key: "key_c", Modifiers: []string{"ctrl"}}, "\x03"},
	} {
		p, err := s.encode(tc.r)
		if err != nil || string(p) != tc.want {
			t.Fatalf("%+v = %q %v", tc.r, p, err)
		}
	}
	if _, err := s.encode(request{Op: "text", Text: "one\ntwo"}); err == nil {
		t.Fatal("unbracketed multiline text could submit commands")
	}
	applyText(t, s, "\x1b[?2004h\x1b[>31u")
	p, err := s.encode(request{Op: "text", Text: "one\ntwo"})
	if err != nil || string(p) != "\x1b[200~one\ntwo\x1b[201~" {
		t.Fatalf("paste: %q %v", p, err)
	}
	p, err = s.encode(request{Op: "key", Key: "enter"})
	if err != nil || !strings.Contains(string(p), "\x1b[13u") || !strings.Contains(string(p), ":3u") {
		t.Fatalf("negotiated press/release: %q %v", p, err)
	}
	for _, text := range []string{"\x1b[201~", "a\x00b", strings.Repeat("x", maxInputBytes+1)} {
		if _, err := s.encode(request{Op: "text", Text: text}); err == nil {
			t.Fatal("accepted invalid text")
		}
	}
}

func TestHistoryAndOutputBounds(t *testing.T) {
	s := testState(t, strings.Repeat("line\r\n", 40))
	h, err := s.history(request{Limit: 3, Offset: 1})
	if err != nil || len(h.Lines) != 3 || h.Start != h.Total-4 || !h.Truncated {
		t.Fatalf("history: %+v %v", h, err)
	}
	if _, err := s.history(request{Offset: 1000}); err == nil {
		t.Fatal("accepted unavailable history")
	}
	lines, truncated := boundLines([]string{strings.Repeat("漢", maxTextBytes)})
	if !truncated || len(lines) != 1 || len(lines[0]) >= maxTextBytes || strings.ContainsRune(lines[0], '\ufffd') {
		t.Fatal("invalid UTF-8 truncation")
	}
}

func TestRequestValidation(t *testing.T) {
	for _, p := range []string{
		`{"id":"1","op":"screen","text":"hidden mutation"}`,
		`{"id":"1","op":"wait","timeout_ms":60001}`,
		`{"id":"1","op":"screen","unknown":true}`,
		`{"id":"1","op":"detach"} {}`,
		`{"op":"text","text":"no id"}`,
		`{"id":"1","op":"screen","op":"detach"}`,
		`{"id":"1","op":"wait","timeout_ms":null}`,
		`{"id":"1","op":"history","limit":0}`,
	} {
		if _, err := parseRequest([]byte(p)); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
}

func TestBlockedOutputCanBeCanceled(t *testing.T) {
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	encode := responseEncoder(ctx, w)
	done := make(chan error, 1)
	go func() { done <- encode(response{Type: "ready"}) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("blocked output cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked output prevented cancellation")
	}
}

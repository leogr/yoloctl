package chatstream

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// A refusing stream's single nick-held frame ends the view with the typed
// refusal before any header validation, client handshake or request.
func TestChannelsNickHeldFrameIsTheTypedRefusal(t *testing.T) {
	var events []Event
	err := RunChannels(context.Background(), io.NopCloser(strings.NewReader(`{"event":"nick-held"}`+"\n")), nopWriteCloser{io.Discard}, nil, nil, nil, nil, func(e Event) { events = append(events, e) })
	if !errors.Is(err, ErrNickHeld) {
		t.Fatalf("got %v, want ErrNickHeld", err)
	}
	if len(events) != 1 || events[0].Kind != "connection" || !errors.Is(events[0].Err, ErrNickHeld) {
		t.Fatalf("the view must learn the typed refusal: %+v", events)
	}
}

// AIRC's "Session taken over" ends a replaced view as ErrTakenOver, whether
// the ERROR record or the client's closed event reaches the driver first.
// Any other ERROR keeps the generic diagnostic.
func TestTakenOverSessionEndsWithTheTypedError(t *testing.T) {
	d, _, _ := channelDriverFixture(t)
	if err := d.record(record{Command: "ERROR", Params: []string{TakenOverReason}}); !errors.Is(err, ErrTakenOver) {
		t.Fatalf("ERROR %q: %v", TakenOverReason, err)
	}
	if err := d.record(record{Command: "ERROR", Params: []string{"Membership revoked"}}); err == nil || errors.Is(err, ErrTakenOver) {
		t.Fatalf("another ERROR is not a take-over: %v", err)
	}
	for _, tc := range []struct {
		closed string
		want   bool
	}{
		{`{"event":"closed","error":"IRC disconnected; reconnect explicitly","uncertain":[],"server_error":"Session taken over"}`, true},
		{`{"event":"closed","error":"IRC disconnected; reconnect explicitly","uncertain":[]}`, false},
	} {
		w := &driver{header: normalizedHeader(testHeader(t)), in: io.NopCloser(strings.NewReader("")), out: nopWriteCloser{io.Discard}, read: bufio.NewReader(strings.NewReader(tc.closed + "\n"))}
		_, err := w.exchange(Request{ID: "r1", Op: "read", Limit: 1})
		if err == nil || errors.Is(err, ErrTakenOver) != tc.want {
			t.Fatalf("closed event %s: %v", tc.closed, err)
		}
	}
}

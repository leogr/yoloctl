package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

// v030Request and v030Frame are the wire types of the released v0.3.0
// controller (protocol 2, internal/controller/protocol.go at v0.3.0). That
// controller decodes every frame strictly into them.
type v030Request struct {
	ID       string              `json:"id"`
	Method   string              `json:"method"`
	Space    string              `json:"space"`
	Args     []string            `json:"args,omitempty"`
	Flags    map[string][]string `json:"flags,omitempty"`
	Cwd      string              `json:"cwd,omitempty"`
	Terminal bool                `json:"terminal,omitempty"`
	View     bool                `json:"view,omitempty"`
	Cols     uint16              `json:"cols,omitempty"`
	Rows     uint16              `json:"rows,omitempty"`
	Term     string              `json:"term,omitempty"`
	Payload  json.RawMessage     `json:"payload,omitempty"`
	Session  *json.RawMessage    `json:"session,omitempty"`
	Dash     int                 `json:"dash"`
}

type v030Frame struct {
	ImageRetryJob string       `json:"image_retry_job,omitempty"`
	Type          string       `json:"type"`
	Hello         *Hello       `json:"hello,omitempty"`
	Request       *v030Request `json:"request,omitempty"`
	Data          []byte       `json:"data,omitempty"`
	Status        int          `json:"status,omitempty"`
	Error         string       `json:"error,omitempty"`
	Cols          uint16       `json:"cols,omitempty"`
	Rows          uint16       `json:"rows,omitempty"`
	Signal        int          `json:"signal,omitempty"`
	Owned         bool         `json:"owned,omitempty"`
}

func readV030Frame(r io.Reader) (v030Frame, error) {
	var f v030Frame
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return f, err
	}
	raw := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(r, raw); err != nil {
		return f, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return f, fmt.Errorf("invalid controller frame: %w", err)
	}
	return f, nil
}

// v030Controller answers one connection as the v0.3.0 controller does
// (server.go at v0.3.0): exit 2 on any hello but its own, its hello, then for
// control.drain one exit frame after the drain.
type v030Controller struct {
	hello   Hello
	got     *v030Request
	decode  error
	drained bool
}

func (c *v030Controller) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	wire := NewWire(conn, conn)
	fail := func(err error) { _ = wire.Write(Frame{Type: "exit", Status: 2, Error: err.Error()}) }
	frame, err := readV030Frame(conn)
	if err != nil {
		c.decode = err
		return
	}
	if frame.Type != "hello" || frame.Hello == nil {
		fail(errors.New("controller handshake required"))
		return
	}
	if err := v030MatchHello(*frame.Hello, c.hello); err != nil {
		fail(err)
		return
	}
	if err := wire.Write(Frame{Type: "hello", Hello: &c.hello}); err != nil {
		return
	}
	frame, err = readV030Frame(conn)
	if err != nil {
		c.decode = err
		return
	}
	c.got = frame.Request
	if frame.Type != "request" || frame.Request == nil || frame.Request.Method != "control.drain" {
		fail(errors.New("controller request required"))
		return
	}
	if err := (Request{ID: c.got.ID, Method: c.got.Method, Space: c.got.Space}).Validate(); err != nil || c.got.Space != c.hello.Space {
		fail(fmt.Errorf("invalid request: %v", err))
		return
	}
	c.drained = true
	_ = wire.Write(Frame{Type: "exit"})
}

// v030MatchHello is MatchHello at v0.3.0, whose ProtocolVersion was 2.
func v030MatchHello(local, remote Hello) error {
	if local.Protocol != 2 || remote.Protocol != 2 {
		return fmt.Errorf("controller protocol mismatch (client %d, controller %d)", local.Protocol, remote.Protocol)
	}
	if local.Space == "" || local.Space != remote.Space {
		return errors.New("controller space identity mismatch")
	}
	if local.Bundle == "" || local.Bundle != remote.Bundle {
		return fmt.Errorf("controller bundle mismatch (client %s, controller %s); use the selected host yoloctl build to update the controller", local.Bundle, remote.Bundle)
	}
	if local.Generation != "" && local.Generation != remote.Generation {
		return errors.New("controller generation changed; reconnect before retrying")
	}
	return nil
}

func olderHello() Hello {
	return Hello{Protocol: OlderProtocol, Bundle: "v0.3.0-bundle", Space: "work", Generation: "g1"}
}

func callV030(t *testing.T, c *v030Controller, call func(context.Context, net.Conn) error) error {
	t.Helper()
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { c.serve(right); close(done) }()
	err := call(ctx, left)
	<-done
	return err
}

// The pre-deploy drain of a v0.3.0 controller: this build's hello is refused
// before any request, then the drain in protocol 2 is decoded strictly and
// acknowledged after the drain. A debug log level never reaches that request.
func TestUpgradeDrainsAReleasedProtocol2Controller(t *testing.T) {
	c := &v030Controller{hello: olderHello()}
	current := Hello{Protocol: ProtocolVersion, Bundle: "v0.3.0-bundle", Space: "work", Generation: "g1"}
	err := callV030(t, c, func(ctx context.Context, conn net.Conn) error {
		return Call(ctx, conn, current, Request{ID: "first-request-0001", Method: "control.drain", Space: "work", Dash: -1, Log: LogDebug}, ClientIO{Out: io.Discard, Err: io.Discard})
	})
	if !RefusedAsOlder(err) || c.got != nil || c.drained || c.decode != nil {
		t.Fatalf("current hello: err=%v request=%+v drained=%v decode=%v", err, c.got, c.drained, c.decode)
	}
	c = &v030Controller{hello: olderHello()}
	var stderr bytes.Buffer
	err = callV030(t, c, func(ctx context.Context, conn net.Conn) error {
		return DrainOlder(ctx, conn, current, "drain-request-0001", &stderr)
	})
	if err != nil || c.decode != nil || !c.drained {
		t.Fatalf("older drain: err=%v decode=%v drained=%v stderr=%q", err, c.decode, c.drained, stderr.String())
	}
	if want := (v030Request{ID: "drain-request-0001", Method: "control.drain", Space: "work", Dash: -1}); !reflect.DeepEqual(*c.got, want) {
		t.Fatalf("drain request = %+v, want %+v", *c.got, want)
	}
}

// The older protocol changes only the protocol number: bundle, space and
// generation keep the exact check, a refusal stays an exit, and a controller
// that answers with another protocol is not drained.
func TestOlderDrainKeepsTheExactHandshake(t *testing.T) {
	for name, tc := range map[string]struct {
		local, remote Hello
		want          string
	}{
		"generation": {olderHello(), Hello{Protocol: OlderProtocol, Bundle: "v0.3.0-bundle", Space: "work", Generation: "g2"}, "controller generation changed"},
		"bundle":     {olderHello(), Hello{Protocol: OlderProtocol, Bundle: "other", Space: "work", Generation: "g1"}, "controller bundle mismatch"},
		"space":      {olderHello(), Hello{Protocol: OlderProtocol, Bundle: "v0.3.0-bundle", Space: "other", Generation: "g1"}, "controller space identity mismatch"},
	} {
		t.Run(name, func(t *testing.T) {
			c := &v030Controller{hello: tc.remote}
			err := callV030(t, c, func(ctx context.Context, conn net.Conn) error {
				return DrainOlder(ctx, conn, tc.local, "drain-request-0001", io.Discard)
			})
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Status != 2 || !strings.Contains(exit.Message, tc.want) || c.got != nil || c.drained {
				t.Fatalf("err=%v request=%+v drained=%v", err, c.got, c.drained)
			}
		})
	}
	// A controller that answers the protocol-2 hello with its own hello at
	// another protocol gets no request.
	left, right := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = right.Close() }()
		wire := NewWire(right, right)
		if _, err := wire.Read(); err != nil {
			return
		}
		h := olderHello()
		h.Protocol = ProtocolVersion
		_ = wire.Write(Frame{Type: "hello", Hello: &h})
		if f, err := wire.Read(); err == nil {
			t.Errorf("request sent after a protocol %d hello: %+v", h.Protocol, f)
		}
	}()
	err := DrainOlder(context.Background(), left, olderHello(), "drain-request-0001", io.Discard)
	<-done
	if err == nil || !strings.Contains(err.Error(), "controller protocol mismatch") {
		t.Fatalf("err=%v", err)
	}
}

// Only the released controller's own refusal of this build's hello selects
// the older drain; every other failure keeps the drain's ordinary handling.
func TestRefusedAsOlderMatchesOnlyTheReleasedRefusal(t *testing.T) {
	refusal := fmt.Sprintf("controller protocol mismatch (client %d, controller 2)", ProtocolVersion)
	for err, want := range map[error]bool{
		&ExitError{Status: 2, Message: refusal}:                                                 true,
		fmt.Errorf("drain: %w", &ExitError{Status: 2, Message: refusal}):                        true,
		&ExitError{Status: 1, Message: refusal}:                                                 false,
		&ExitError{Status: 2, Message: "controller protocol mismatch (client 3, controller 1)"}: false,
		&ExitError{Status: 2, Message: "controller is in maintenance"}:                          false,
		errors.New(refusal): false, // the client's own check of a controller hello
		nil:                 false,
	} {
		if got := RefusedAsOlder(err); got != want {
			t.Errorf("RefusedAsOlder(%v) = %v, want %v", err, got, want)
		}
	}
}

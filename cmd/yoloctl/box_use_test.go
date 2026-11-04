package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
)

// A framed managed peer exercises the real frontend, session decoder and
// controller routing without requiring a VM. The holder itself is covered by
// session's mixed-view tests; this fixture also sends forged ordinary stdout.
func boxUsePacket(w io.Writer, kind byte, p []byte) error {
	header := []byte{kind, 0, 0}
	binary.BigEndian.PutUint16(header[1:], uint16(len(p)))
	_, err := w.Write(append(header, p...))
	return err
}

func boxUseReadPacket(r io.Reader) ([]byte, error) {
	var header [3]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	p := make([]byte, binary.BigEndian.Uint16(header[1:]))
	_, err := io.ReadFull(r, p)
	return p, err
}

func boxUseBootstrap(stream io.ReadWriter, snapshot []byte) error {
	if err := boxUsePacket(stream, 'h', []byte{6}); err != nil {
		return err
	}
	p, err := boxUseReadPacket(stream)
	if err != nil {
		return err
	}
	var sub struct{ Bundle, Core string }
	if err := json.Unmarshal(p, &sub); err != nil {
		return err
	}
	state, _ := json.Marshal(map[string]any{"Bundle": sub.Bundle, "Core": sub.Core, "Session": "fixture", "Client": "self", "SnapshotBytes": len(snapshot)})
	if err := boxUsePacket(stream, 'b', state); err != nil {
		return err
	}
	for len(snapshot) > 0 {
		n := min(len(snapshot), 32768)
		if err := boxUsePacket(stream, 's', snapshot[:n]); err != nil {
			return err
		}
		snapshot = snapshot[n:]
	}
	return boxUsePacket(stream, 'S', nil)
}

// boxUseEmitter sequences live holder events after bootstrap. Its activity
// uses the holder's shape: a global input sequence and the source's cumulative
// queued bytes. Only one goroutine may own an emitter.
type boxUseEmitter struct {
	view           io.Writer
	frames, inputs uint64
}

func (e *boxUseEmitter) emit(kind byte, p []byte) error {
	e.frames++
	return boxUsePacket(e.view, kind, append(binary.BigEndian.AppendUint64(nil, e.frames), p...))
}

func (e *boxUseEmitter) accept(source string, bytes uint64) error {
	e.inputs++
	p, _ := json.Marshal(session.InputActivity{Sequence: e.inputs, Source: source, Bytes: bytes})
	return e.emit('a', p)
}

// boxUseAdmit reads the attachment's original input route and reports each
// chunk as queued for "self", as the holder does after accepting DATA frames.
func boxUseAdmit(in io.Reader, events chan<- func(*boxUseEmitter) error, received chan<- string) {
	var total uint64
	buf := make([]byte, 64)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			total += uint64(n)
			accepted := total
			received <- string(buf[:n])
			events <- func(e *boxUseEmitter) error { return e.accept("self", accepted) }
		}
		if err != nil {
			return
		}
	}
}

func TestBoxUseControllerUsesPrivateStateAndOriginalInput(t *testing.T) {
	vt, err := terminal.New(6, 60, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	_, _ = vt.Write([]byte("SHARED_SCREEN\r\n> "))
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	requests, input := io.Pipe()
	output, replies := io.Pipe()
	t.Cleanup(func() { _ = requests.Close(); _ = input.Close(); _ = output.Close(); _ = replies.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := newAgentboxAttachCmd()
	cmd.SetContext(ctx)
	cmd.SetIn(requests)
	cmd.SetOut(replies)
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	if err := cmd.Flags().Set("box-use", "true"); err != nil {
		t.Fatal(err)
	}
	events := make(chan func(*boxUseEmitter) error, 4)
	gotInput := make(chan string, 8)
	callErr := make(chan error, 1)
	target := controllerTarget{Operator: true, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
		if !req.View || req.Terminal || streams.Resize != nil || streams.View == nil {
			return errors.New("box-use selected raw/PTY routing")
		}
		_, _ = io.WriteString(streams.Out, "FAKE_SCREEN_FROM_STDOUT\n")
		if err := boxUseBootstrap(streams.View, snapshot); err != nil {
			return err
		}
		go boxUseAdmit(streams.In, events, gotInput)
		emitter := &boxUseEmitter{view: streams.View}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-streams.Signals:
				// Final output must be drained so a deliberate detach still gets
				// its operation receipt after the frontend closes its view.
				_, err := streams.View.Write([]byte("trailing-view-bytes"))
				return err
			case event := <-events:
				if err := event(emitter); err != nil {
					return err
				}
			}
		}
	}}
	go func() {
		callErr <- callControllerTerminal(cmd, target, controller.Request{Method: "box.attach", Space: "fixture", Args: []string{"box"}, Flags: map[string][]string{}})
		_ = replies.Close()
	}()
	parsed := make(chan map[string]any, 8)
	go func() {
		defer close(parsed)
		d := json.NewDecoder(output)
		for {
			var response map[string]any
			if d.Decode(&response) != nil {
				return
			}
			parsed <- response
		}
	}()
	// Drain decoded replies before observing exit: a successful detach
	// may finish while its final response is still queued by the decoder.
	next := func(kind string) map[string]any {
		t.Helper()
		select {
		case response, ok := <-parsed:
			if !ok {
				t.Fatalf("frontend output ended before %s", kind)
			}
			if response["type"] != kind {
				t.Fatalf("response = %+v, want %s", response, kind)
			}
			return response
		case <-ctx.Done():
			t.Fatal("response timed out")
		}
		return nil
	}
	send := func(p string) {
		t.Helper()
		if _, err := io.WriteString(input, p+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	next("ready")
	send(`{"id":"1","op":"screen"}`)
	first := next("screen")
	encoded, _ := json.Marshal(first)
	if !bytes.Contains(encoded, []byte("SHARED_SCREEN")) || bytes.Contains(encoded, []byte("FAKE_SCREEN")) {
		t.Fatalf("wrong provenance: %s", encoded)
	}
	send(`{"id":"bad","op":"text","text":"should not send","unknown":true}`)
	next("error")
	send(`{"id":"2","op":"text","text":"hello"}`)
	next("sent")
	send(`{"id":"3","op":"key","key":"enter"}`)
	next("sent")
	var delivered string
	for delivered != "hello\r" {
		select {
		case p := <-gotInput:
			delivered += p
		case <-ctx.Done():
			t.Fatalf("input = %q", delivered)
		}
	}
	// Own acceptance advances this attachment's revision; wait from after it.
	send(`{"id":"3b","op":"screen"}`)
	revision := uint64(next("screen")["screen"].(map[string]any)["revision"].(float64))
	events <- func(e *boxUseEmitter) error { return e.accept("another-agent", 17) }
	events <- func(e *boxUseEmitter) error { return e.emit('d', []byte("\x1b[2;1H> peer steering")) }
	send(fmt.Sprintf(`{"id":"4","op":"wait","since":%d,"timeout_ms":1000,"settle_ms":10}`, revision))
	wait := next("wait")
	encoded, _ = json.Marshal(wait)
	if !bytes.Contains(encoded, []byte("another-agent")) || !bytes.Contains(encoded, []byte("peer steering")) {
		t.Fatalf("missing collaboration: %s", encoded)
	}
	send(`{"id":"5","op":"detach"}`)
	next("detached")
	select {
	case err := <-callErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("detach did not finish receipt")
	}
	if !strings.Contains(diagnostics.String(), "FAKE_SCREEN_FROM_STDOUT") {
		t.Fatal("setup stdout did not reach diagnostics")
	}
}

func TestBoxUseModeIsExclusiveAndAvailableThroughAlias(t *testing.T) {
	for _, args := range [][]string{{"attach"}, {"box", "attach"}} {
		root := newRootCmd()
		cmd, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Flags().Lookup("box-use") == nil {
			t.Fatal("missing box-use flag")
		}
		if err := cmd.Flags().Parse([]string{"--box-use", "--passthrough"}); err != nil {
			t.Fatal(err)
		}
		if err := cmd.ValidateFlagGroups(); err == nil {
			t.Fatal("conflicting frontends accepted")
		}
	}
}

func TestBoxUseSessionExitAndUncertainSetup(t *testing.T) {
	vt, err := terminal.New(2, 20, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	_, _ = vt.Write([]byte("FINAL_SCREEN"))
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"exit", "unknown", "unavailable"} {
		t.Run(tc, func(t *testing.T) {
			cmd := newAgentboxAttachCmd()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd.SetContext(ctx)
			input, writer := io.Pipe()
			defer func() { _ = input.Close(); _ = writer.Close() }()
			cmd.SetIn(input)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(io.Discard)
			target := controllerTarget{Operator: true, Call: func(_ context.Context, _ controller.Request, streams controller.ClientIO) error {
				if tc == "unknown" {
					return &controller.OutcomeUnknown{ID: "inspect-this", Cause: io.ErrUnexpectedEOF}
				}
				if tc == "unavailable" {
					return &controller.ExitError{Status: 2, Message: "matching controller required"}
				}
				if err := boxUseBootstrap(streams.View, snapshot); err != nil {
					return err
				}
				return boxUsePacket(streams.View, 'e', []byte{0, 0, 0, 7})
			}}
			err := callControllerBoxUse(cmd, target, controller.Request{Method: "box.attach"})
			if tc == "exit" {
				if classifyError(err) != 7 || !strings.Contains(output.String(), `"status":7`) || !strings.Contains(output.String(), "FINAL_SCREEN") {
					t.Fatalf("exit = %v, output = %s", err, output.String())
				}
			} else if tc == "unknown" {
				var unknown *controller.OutcomeUnknown
				if !errors.As(err, &unknown) || unknown.ID != "inspect-this" {
					t.Fatalf("lost uncertain receipt: %v", err)
				}
			} else if err == nil || err.Error() != "matching controller required" {
				t.Fatalf("lost setup failure: %v", err)
			}
		})
	}
}

// A finite invocation's io.Pipe write returns before the controller relays it.
// sent, and therefore detach, waits until the holder has queued every byte of
// this attachment; peer and partial own progress cannot release it. Without
// that acceptance the input is uncertain and is never resent.
func TestBoxUseSentWaitsForHolderAcceptance(t *testing.T) {
	vt, err := terminal.New(3, 40, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	_, _ = vt.Write([]byte("$ "))
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"accepted", "disconnected", "canceled"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			batchCtx, stopBatch := context.WithCancel(ctx)
			defer stopBatch()
			batch, err := prepareBoxUseBatch(newAgentboxUseCmd(), []string{"coder", "text", "hello"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			delivered := make(chan string, 1)
			var out bytes.Buffer
			var attachErr error
			err = batch.Run(batchCtx, &out, func(ctx context.Context, in io.Reader, out io.Writer) error {
				attach := newAgentboxAttachCmd()
				attach.SetContext(ctx)
				attach.SetIn(in)
				attach.SetOut(out)
				attach.SetErr(io.Discard)
				target := controllerTarget{Operator: true, Call: func(ctx context.Context, _ controller.Request, streams controller.ClientIO) error {
					if err := boxUseBootstrap(streams.View, snapshot); err != nil {
						return err
					}
					e := &boxUseEmitter{view: streams.View}
					got := make([]byte, 5)
					if _, err := io.ReadFull(streams.In, got); err != nil {
						return err
					}
					// The frontend's write has returned; holder admission is withheld.
					if err := e.accept("peer", 64); err != nil {
						return err
					}
					if err := e.accept("self", 2); err != nil {
						return err
					}
					select {
					case <-streams.Signals:
						return errors.New("detached before the holder accepted input")
					default:
					}
					switch tc {
					case "disconnected":
						delivered <- string(got)
						return errors.New("session view lost")
					case "canceled":
						stopBatch()
					case "accepted":
						if err := e.accept("self", 5); err != nil {
							return err
						}
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-streams.Signals:
					}
					rest, err := io.ReadAll(streams.In)
					delivered <- string(got) + string(rest)
					return err
				}}
				attachErr = callControllerBoxUse(attach, target, controller.Request{ID: "test-" + tc, Method: "box.attach"})
				return attachErr
			})
			if got := <-delivered; got != "hello" {
				t.Fatalf("holder input = %q, want exactly one copy", got)
			}
			sent := strings.Contains(out.String(), `"type":"sent"`)
			if tc == "accepted" {
				if err != nil || !sent || !strings.Contains(out.String(), `"type":"detached"`) {
					t.Fatalf("accepted input: %v\n%s", err, out.String())
				}
				return
			}
			if sent || err == nil || attachErr == nil || !strings.Contains(attachErr.Error(), "inspect before sending again") {
				t.Fatalf("unconfirmed input: batch %v, attach %v\n%s", err, attachErr, out.String())
			}
			if tc == "disconnected" && !strings.Contains(out.String(), `"type":"input_uncertain"`) {
				t.Fatalf("no uncertain record:\n%s", out.String())
			}
		})
	}
}

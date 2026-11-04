package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

const maxClipboardEvent = 2 << 20

type subscription struct {
	Passthrough bool   `json:"passthrough"`
	Bundle      string `json:"bundle"`
	Core        string `json:"core"`
	Browser     bool   `json:"browser,omitempty"`
}

type viewState struct {
	Bundle, Core, Session string
	Client                string
	Input                 InputActivity
	Sequence              uint64
	SnapshotBytes         int
	Colors                terminal.Colors
	ImagePaste            bool
}

// InputActivity reports accepted input, not application completion or identity
// authority. Sequence counts input packets; Source is an ephemeral attachment ID.
// Bytes is Source's cumulative payload accepted into the holder input queue.
// Terminal query replies are not participant input. No key contents are exposed.
type InputActivity struct {
	Sequence uint64 `json:"sequence"`
	Source   string `json:"source"`
	Bytes    uint64 `json:"bytes"`
}

// ViewEvent is delivered only over trusted infrastructure descriptors, never
// decoded from a command's stdout. Snapshot replaces state quietly; subsequent
// events are ordered exactly as at the holder, including canonical resizes.
type ViewEvent struct {
	SessionID string
	ClientID  string
	Input     *InputActivity
	Snapshot  []byte
	Data      []byte
	Size      terminal.Geometry
	Colors    *terminal.Colors
	Clipboard *termio.OSC52
	EndHold   bool
}

func decodeJSON(p []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(p))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing session metadata")
	}
	return nil
}

func sequenced(seq uint64, p []byte) []byte {
	out := make([]byte, 8+len(p))
	binary.BigEndian.PutUint64(out, seq)
	copy(out[8:], p)
	return out
}

// Relay preserves the framed session on a private duplex channel. The worker
// still observes the final status, so controller receipts and CLI status agree.
// Stdin remains the command's original ordered input path, including typeahead
// buffered during setup. The caller owns cancellation of a blocking stdin read.
// Closing the local endpoint detaches without touching the agent.
func Relay(ctx context.Context, remote, local io.ReadWriteCloser, stdin io.Reader) error {
	defer func() { _ = remote.Close() }()
	defer func() { _ = local.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); _ = remote.Close(); _ = local.Close() }()
	var mu sync.Mutex
	send := func(kind byte, p []byte) error { mu.Lock(); defer mu.Unlock(); return writeFrame(remote, kind, p) }
	go func() {
		defer func() { _ = remote.Close() }()
		for {
			f, err := readFrame(local)
			if err != nil || send(f.Type, f.Payload) != nil {
				return
			}
		}
	}()
	var inputOnce sync.Once
	startInput := func() {
		inputOnce.Do(func() {
			if stdin == nil {
				return
			}
			go func() {
				buf := make([]byte, maxPayload)
				for {
					n, err := stdin.Read(buf)
					if n > 0 && send(frameData, buf[:n]) != nil {
						return
					}
					if err != nil || ctx.Err() != nil {
						return
					}
				}
			}()
		})
	}
	for first := true; ; first = false {
		f, err := readFrame(remote)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrSessionClosed
		}
		if err := writeFrame(local, f.Type, f.Payload); err != nil {
			return err
		}
		if first && foreignHello(f) {
			// The local view refuses it too; this side names it.
			return buildMismatch{protocolMismatch}
		}
		switch f.Type {
		case frameSnapshotEnd:
			startInput()
		case frameState:
			var state viewState
			if decodeJSON(f.Payload, &state) == nil && state.SnapshotBytes == 0 {
				startInput()
			}
		}
		if f.Type == frameExit {
			status, err := decodeExit(f.Payload)
			if err != nil {
				return err
			}
			if status != 0 {
				return &ExitError{Status: status}
			}
			return nil
		}
		if f.Type == frameError {
			return viewRefusal(f.Payload)
		}
	}
}

// protocolMismatch is the error for a holder speaking another protocol.
const protocolMismatch = "session protocol mismatch; quit the old agent session and attach again with the selected build, or explicitly restart its box"

// foreignHello reports a well-formed HELLO of another protocol version.
func foreignHello(f frame) bool {
	return f.Type == frameHello && len(f.Payload) == 1 && f.Payload[0] != protocolVersion
}

// viewRefusal is a holder's refusal to admit a view. Its exact build/core
// reason, alone or followed by the advice that holder's build appends,
// matches ErrBuildMismatch, so a caller can replace that build's fixed
// advice; the message stays the holder's own.
func viewRefusal(payload []byte) error {
	reason := string(payload)
	if reason == holderMismatchReason || strings.HasPrefix(reason, holderMismatchReason+"; ") {
		return buildMismatch{reason}
	}
	return errors.New(reason)
}

// attachView implements both subscriptions. Passthrough is explicitly selected
// by omitting OnEvent in AttachIO; managed clients validate and restore native
// snapshots rather than treating the blob as terminal bytes.
func attachView(ctx context.Context, transport io.ReadWriteCloser, aio AttachIO) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = transport.Close() }()
	go func() { <-ctx.Done(); _ = transport.Close() }()
	first, err := readFrame(transport)
	if err != nil {
		return fmt.Errorf("session handshake: %w", err)
	}
	if foreignHello(first) {
		return buildMismatch{protocolMismatch}
	}
	if first.Type != frameHello || len(first.Payload) != 1 {
		// Only a well-formed HELLO names another build.
		return errors.New(protocolMismatch)
	}
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		return err
	}
	sub, _ := json.Marshal(subscription{Passthrough: aio.OnEvent == nil, Bundle: bundle, Core: terminal.CoreIdentity(),
		Browser: aio.OnEvent != nil && aio.Host != nil && aio.Host.OnBrowser != nil})
	if aio.Host != nil {
		defer aio.Host.end()
	}
	var mu sync.Mutex
	send := func(kind byte, p []byte) error { mu.Lock(); defer mu.Unlock(); return writeFrame(transport, kind, p) }
	if err := send(frameSubscribe, sub); err != nil {
		return err
	}
	// Only begin reading user input after the server has accepted the view and
	// its complete initial state. No input can be mistaken for admission bytes.
	ready := make(chan struct{})
	go func() {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		if aio.Stdin == nil {
			return
		}
		buf := make([]byte, maxPayload)
		for {
			n, err := aio.Stdin.Read(buf)
			if n > 0 {
				if e := send(frameData, buf[:n]); e != nil {
					return
				}
			}
			if err != nil || ctx.Err() != nil {
				return
			}
		}
	}()
	// Record validation failures before closing this view. The reader must not
	// mistake our local refusal for a lost holder; no sender waits for a receiver.
	localErrors := make(chan error, 1)
	go func() {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		sizes, colors := aio.Resize, aio.Colors
		for {
			select {
			case <-ctx.Done():
				return
			case size, ok := <-sizes:
				if !ok {
					sizes = nil
					continue
				}
				p, err := encodeResize(size)
				if err != nil {
					select {
					case localErrors <- err:
					default:
					}
					_ = transport.Close()
					return
				}
				if err := send(frameResize, p); err != nil {
					return
				}
			case value, ok := <-colors:
				if !ok {
					colors = nil
					continue
				}
				p, _ := json.Marshal(value)
				if err := send(frameColors, p); err != nil {
					return
				}
			}
		}
	}()
	var state viewState
	var snapshot, clipboard []byte
	started, accepted := false, false
	for {
		f, err := readFrame(transport)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case err := <-localErrors:
				return err
			default:
			}
			return ErrSessionClosed
		}
		if f.Type == frameError {
			return viewRefusal(f.Payload)
		}
		if !accepted {
			if f.Type != frameState || decodeJSON(f.Payload, &state) != nil || state.Core != terminal.CoreIdentity() || state.Bundle != bundle || state.Session == "" || state.Client == "" || state.SnapshotBytes < 0 || state.SnapshotBytes > terminal.MaxSnapshotBytes {
				return errors.New("invalid or incompatible session state")
			}
			accepted = true
			if aio.OnEvent == nil {
				started = true
				close(ready)
			}
			continue
		}
		if !started {
			switch f.Type {
			case frameSnapshot:
				if len(f.Payload) > state.SnapshotBytes-len(snapshot) {
					return errors.New("session snapshot exceeds declared size")
				}
				snapshot = append(snapshot, f.Payload...)
			case frameSnapshotEnd:
				if len(f.Payload) != 0 || len(snapshot) != state.SnapshotBytes || len(snapshot) == 0 {
					return errors.New("incomplete session snapshot")
				}
				if aio.Host != nil {
					aio.Host.begin(send, func() { _ = transport.Close() }, state.ImagePaste)
				}
				if err := aio.OnEvent(ViewEvent{Snapshot: snapshot, Colors: &state.Colors, SessionID: state.Session, ClientID: state.Client, Input: &state.Input}); err != nil {
					return err
				}
				snapshot = nil
				started = true
				close(ready)
			default:
				return errors.New("expected complete session snapshot")
			}
			continue
		}
		if f.Type == frameExit {
			status, err := decodeExit(f.Payload)
			if err != nil {
				return err
			}
			if status != 0 {
				return &ExitError{Status: status}
			}
			return nil
		}
		if aio.OnEvent == nil {
			if f.Type != frameData {
				return errors.New("unexpected passthrough frame")
			}
			if aio.Stdout != nil {
				if _, err := aio.Stdout.Write(f.Payload); err != nil {
					return err
				}
			}
			continue
		}
		if f.Type == frameBrowser || f.Type == frameBrowserCancel || f.Type == frameImageResult || f.Type == frameImageProbed {
			if aio.Host == nil {
				return errors.New("unexpected host integration message")
			}
			if err := aio.Host.receive(f.Type, f.Payload); err != nil {
				return err
			}
			continue
		}
		if len(f.Payload) < 8 || binary.BigEndian.Uint64(f.Payload) != state.Sequence+1 {
			return errors.New("session event gap; detach and reattach for a fresh snapshot")
		}
		state.Sequence++
		p := f.Payload[8:]
		if len(clipboard) > 0 && f.Type != frameClipboard && f.Type != frameClipboardEnd {
			return errors.New("interrupted clipboard event")
		}
		var event ViewEvent
		switch f.Type {
		case frameActivity:
			event.Input = new(InputActivity)
			err = decodeJSON(p, event.Input)
			if err == nil && (event.Input.Sequence <= state.Input.Sequence || event.Input.Source == "" || event.Input.Bytes == 0) {
				err = errors.New("invalid input activity")
			}
			if err == nil {
				state.Input = *event.Input
			}
		case frameData:
			event.Data = p
		case frameResize:
			event.Size, err = decodeResize(p)
		case frameColors:
			event.Colors = new(terminal.Colors)
			err = decodeJSON(p, event.Colors)
		case frameHoldEnd:
			if len(p) != 1 || p[0] != 0 {
				return errors.New("invalid synchronized-output release")
			}
			event.EndHold = true
		case frameClipboard:
			if len(p) > maxClipboardEvent-len(clipboard) {
				return errors.New("clipboard event exceeds limit")
			}
			clipboard = append(clipboard, p...)
			continue
		case frameClipboardEnd:
			if len(p) != 1 || p[0] != 0 {
				return errors.New("invalid clipboard end")
			}
			event.Clipboard = new(termio.OSC52)
			err = decodeJSON(clipboard, event.Clipboard)
			clipboard = nil
		default:
			return errors.New("unexpected session event")
		}
		if err != nil {
			return err
		}
		if err := aio.OnEvent(event); err != nil {
			return err
		}
	}
}

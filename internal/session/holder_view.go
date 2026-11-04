package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
)

// initView runs before starting the agent, so parser compilation cannot stall
// live PTY I/O. Tests construct holders directly and call it explicitly.
func (h *holder) initView(rows, cols int) error {
	if h.hostCtx == nil {
		h.hostCtx = context.Background()
	}
	var err error
	h.bundle, err = controller.ExecutableIdentity()
	if err != nil {
		return err
	}
	h.incarnation = rand.Text()
	h.failed = make(chan struct{})
	// Native history is byte-bounded; each viewer applies its own line cap.
	// A default-sized viewer must not discard history needed by another one.
	h.vt, err = terminal.New(rows, cols, terminal.MaxHistoryLines, func(p []byte) { h.colors.Reply(p) })
	if err != nil {
		return err
	}
	h.colors = &terminal.ColorQueries{Terminal: h.vt, Send: func(p []byte) {
		// A single PTY batch may contain hundreds of palette/DSR queries. Coalesce
		// their ordered replies before using the bounded input packet queue.
		if len(p) > maxClientOutputBytes-len(h.pendingReplies) {
			h.failViewLocked(errors.New("session terminal reply buffer overflow"))
			return
		}
		h.pendingReplies = append(h.pendingReplies, p...)
	}}
	h.vt.SetHoldHandler(h.renderHoldLocked)
	return nil
}

func (h *holder) cancelHoldLocked() {
	h.holdEpoch++
	if h.holdTimer != nil {
		h.holdTimer.Stop()
		h.holdTimer = nil
	}
}

// Query state and replicas must observe the same timed release, including
// while no viewer is attached. Use a typed event, never bytes injected into a
// potentially incomplete control sequence. A later hold invalidates this timer.
func (h *holder) renderHoldLocked(held bool) {
	h.cancelHoldLocked()
	if !held || h.closed {
		return
	}
	epoch := h.holdEpoch
	h.holdTimer = time.AfterFunc(terminal.RenderHoldTimeout, func() {
		h.mu.Lock()
		if h.closed || h.holdEpoch != epoch {
			h.mu.Unlock()
			return
		}
		h.cancelHoldLocked()
		h.vt.EndHold()
		if err := h.vt.Err(); err != nil {
			h.failViewLocked(err)
		}
		dropped := h.emitLocked(frameHoldEnd, []byte{0})
		h.mu.Unlock()
		h.abortDropped(dropped)
	})
}

func (h *holder) flushRepliesLocked() {
	for len(h.pendingReplies) > 0 {
		n := min(len(h.pendingReplies), maxPayload)
		if !h.queueInput(h.pendingReplies[:n]) {
			h.failViewLocked(errors.New("session terminal reply queue overflow"))
			break
		}
		h.pendingReplies = h.pendingReplies[n:]
	}
	h.pendingReplies = nil
}

func (h *holder) queueInput(p []byte) bool {
	if len(p) == 0 {
		return true
	}
	if h.input == nil {
		return false
	}
	// Frame size bounds each input packet; copy because native reply memory and
	// the output pump's buffers are borrowed. Total pending input <= 4 MiB.
	if len(p) > maxPayload {
		return false
	}
	select {
	case h.input <- bytes.Clone(p):
		return true
	default:
		return false
	}
}

// emitLocked defines the global event order, including output and resize.
// Every managed view sees each sequence once. Raw views receive only DATA.
func (h *holder) emitLocked(kind byte, p []byte) (dropped []*holderClient) {
	for len(p) > 0 {
		n := min(len(p), maxPayload-8)
		h.sequence++
		packet := sequenced(h.sequence, p[:n])
		for i := 0; i < len(h.clients); i++ {
			c := h.clients[i]
			if c.managed && !c.enqueueFrame(kind, packet) {
				dropped = append(dropped, c)
				h.removeClientLocked(i)
				i--
			}
		}
		p = p[n:]
	}
	return dropped
}

func (h *holder) resizeView(size terminal.Geometry) error {
	h.resizeMu.Lock()
	defer h.resizeMu.Unlock()
	var dropped []*holderClient
	defer func() { h.abortDropped(dropped) }()
	payload, err := encodeResize(size)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vt == nil {
		return errors.New("session terminal unavailable")
	}
	if h.vt.Geometry() == size {
		return nil
	}
	if err := h.vt.ResizeGeometry(size); err != nil {
		return err
	}
	h.flushRepliesLocked() // mode 2048 may report even a font-only resize
	dropped = h.emitLocked(frameResize, payload)
	// Release the parser lock before SSH I/O, retaining resize ordering.
	h.mu.Unlock()
	if h.sess != nil {
		err = resizePTY(h.sess, size)
	}
	h.mu.Lock()
	return errors.Join(err, h.viewErr)
}

func (h *holder) updateColors(c *holderClient, p []byte) error {
	var colors terminal.Colors
	if err := decodeJSON(p, &colors); err != nil {
		return err
	}
	h.mu.Lock()
	c.colors = &colors
	dropped, err := h.selectColorsLocked()
	h.mu.Unlock()
	h.abortDropped(dropped)
	return err
}

func (h *holder) selectColorsLocked() ([]*holderClient, error) {
	for _, c := range h.clients {
		if !c.managed || c.colors == nil || c.colors.Empty() {
			continue
		}
		colors := h.colors.Known.Merge(*c.colors)
		if colors.Equal(h.colors.Known) {
			return nil, nil
		}
		if err := h.colors.Apply(colors); err != nil {
			h.failViewLocked(err)
			return nil, err
		}
		h.flushRepliesLocked()
		p, _ := json.Marshal(colors)
		return h.emitLocked(frameColors, p), h.viewErr
	}
	// Retain the last measured defaults while there are no measured viewers.
	return nil, nil
}

func (h *holder) failViewLocked(err error) {
	if h.viewErr != nil {
		return
	}
	h.viewErr = err
	if h.failed != nil {
		close(h.failed)
	}
}

func (h *holder) abortDropped(clients []*holderClient) {
	for _, c := range clients {
		c.abort()
	}
}

// Each admission has its own bounded handshake. A silent or incompatible peer
// never blocks accepting others, and cannot register before validation.
func (h *holder) admitView(conn net.Conn) {
	c := newHolderClient(conn)
	c.id = rand.Text()
	defer func() {
		if !c.admitted {
			c.abort()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(clientWriteTimeout))
	if err := writeFrame(conn, frameHello, []byte{protocolVersion}); err != nil {
		return
	}
	f, err := readFrame(conn)
	if err != nil {
		return
	}
	if f.Type == frameStats {
		h.serveStats(conn, f.Payload)
		return
	}
	var sub subscription
	if f.Type != frameSubscribe || decodeJSON(f.Payload, &sub) != nil {
		return
	}
	if sub.Bundle != h.bundle || sub.Core != terminal.CoreIdentity() {
		_ = writeFrame(conn, frameError, []byte(holderMismatchReason+"; quit the old agent session and attach again with the selected build, or explicitly restart its box"))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	h.mu.Lock()
	var snapshot, raw []byte
	if h.viewErr != nil {
		err = h.viewErr
	} else if sub.Passthrough {
		raw, err = h.replay()
	} else if h.vt == nil {
		err = errors.New("session terminal unavailable")
	} else {
		snapshot, err = h.vt.Snapshot()
	}
	if err != nil {
		h.mu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
		_ = writeFrame(conn, frameError, []byte(fmt.Sprintf("cannot capture session state: %v; retry when the application finishes its control sequence", err)))
		return
	}
	state := viewState{Bundle: h.bundle, Core: terminal.CoreIdentity(), Session: h.incarnation, Sequence: h.sequence, SnapshotBytes: len(snapshot)}
	state.Client, state.Input = c.id, h.lastInput
	state.ImagePaste = h.upload != nil
	if h.colors != nil {
		state.Colors = h.colors.Known
	}
	meta, _ := json.Marshal(state)
	c.bootstrap = []frame{{frameState, meta}}
	for len(snapshot) > 0 {
		n := min(len(snapshot), maxPayload)
		c.bootstrap = append(c.bootstrap, frame{frameSnapshot, snapshot[:n]})
		snapshot = snapshot[n:]
	}
	if !sub.Passthrough {
		c.bootstrap = append(c.bootstrap, frame{frameSnapshotEnd, nil})
	}
	c.managed, c.admitted = !sub.Passthrough, true
	c.browser = c.managed && sub.Browser
	if h.closed {
		c.finish(h.exitPayload, time.Now().Add(clientWriteTimeout))
	} else {
		h.clients = append(h.clients, c)
	}
	closed := h.closed
	h.mu.Unlock()
	go h.clientWriteLoop(c, raw)
	if !closed {
		go h.clientReadLoop(c)
	}
}

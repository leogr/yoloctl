package term

import (
	"slices"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
)

// Match the pinned Ghostty frontend's bounded synchronized-output hold.
const renderHoldTimeout = terminal.RenderHoldTimeout

// Frame captures rows, cursor and history position under one emulator lock.
// Its slices belong to the caller; composing chrome cannot change a held frame.
func (e *emulator) Frame() terminal.Frame {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.heldFrame != nil {
		f := *e.heldFrame
		f.Lines = slices.Clone(f.Lines)
		f.Selection = slices.Clone(f.Selection)
		return f
	}
	return e.frameLocked()
}

func (e *emulator) frameLocked() terminal.Frame {
	if e.err == nil {
		var f terminal.Frame
		f, e.err = e.vt.Frame(e.trimFrame)
		if e.err == nil {
			return f
		}
	}
	return terminal.Frame{Rows: 1, ContentRows: 1, Lines: []string{"terminal error: " + e.err.Error()}}
}

// renderHoldLocked runs inside the native parser callback, with e.mu already
// held. Frame only reads the core; it does not overwrite the parser's input
// scratch buffer. Capture the boundary, even when several frames share a read.
func (e *emulator) renderHoldLocked(held bool) {
	e.releaseHoldLocked()
	if !held || e.closed {
		return
	}
	f := e.frameLocked()
	e.heldFrame = &f
	if e.replica {
		return // the holder publishes its timeout in the ordered event stream
	}
	epoch := e.holdEpoch
	e.holdTimer = time.AfterFunc(renderHoldTimeout, func() {
		e.mu.Lock()
		if e.closed || e.holdEpoch != epoch {
			e.mu.Unlock()
			return
		}
		e.vt.EndHold()
		e.releaseHoldLocked()
		wake := e.onFrame
		e.mu.Unlock()
		if wake != nil {
			wake()
		}
	})
}

func (e *emulator) releaseHoldLocked() {
	e.holdEpoch++
	if e.holdTimer != nil {
		e.holdTimer.Stop()
		e.holdTimer = nil
	}
	e.heldFrame = nil
}

// EndRenderHold publishes final output when the child exits without ending its
// update. It does not inject bytes into a possibly unfinished VT sequence.
func (e *emulator) EndRenderHold() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.vt.EndHold()
	}
	e.releaseHoldLocked()
}

// Scroll preserves an anchor to native text, not an offset from a moving tail.
func (e *emulator) Scroll(delta int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return 0
	}
	var offset int
	offset, e.err = e.vt.Scroll(delta, e.trimFrame)
	return offset
}

func (e *emulator) ScrollToBottom() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.vt.ScrollToBottom()
	}
}

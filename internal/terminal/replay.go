package terminal

import "errors"

// ErrReplayContinuation marks a recoverable, over-budget unfinished control.
var ErrReplayContinuation = errors.New("terminal replay continuation unavailable")

// NewInputTracker uses the same parser and mode state as the rendered terminal,
// but needs no screen history or query replies. The tiny grid is disposable;
// only input modes and a bounded parser continuation are exported.
func NewInputTracker() (*Terminal, error) {
	t, err := New(1, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	if status := t.call("yolo_vt_input_tracking", t.ptr); status != 0 && t.err == nil {
		t.err = errors.New("cannot initialize terminal input tracking")
	}
	if t.err != nil {
		err = t.err
		_ = t.Close()
		return nil, err
	}
	return t, nil
}

// InputReplay exports input modes, both screen-local keyboard stacks and the
// parser continuation. It must precede the bytes following this terminal's
// input, never be inserted after an arbitrary partial sequence. It is not a
// screen snapshot. Call only on a terminal from NewInputTracker.
//
// An unfinished control string exceeding the 32 KiB continuation limit makes
// export unavailable until the parser reaches another replay-safe boundary.
// Refuse that attach rather than inventing or truncating negotiated state.
func (t *Terminal) InputReplay() ([]byte, error) {
	n := t.call("yolo_vt_input_replay", t.ptr, t.scratch, scratchSize)
	if t.err != nil {
		return nil, t.err
	}
	if n > scratchSize {
		return nil, ErrReplayContinuation
	}
	p, ok := t.mod.Memory().Read(uint32(t.scratch), uint32(n))
	if !ok {
		return nil, errors.New("terminal input replay outside scratch memory")
	}
	return append([]byte(nil), p...), nil
}

// WriteUntilGround consumes at most the prefix ending at the next native parser
// boundary. Replay uses it to advance past an oversized, unexportable control
// string whose terminator may already be in the retained tail. Query replies
// remain disabled on the tracker. If no boundary exists it consumes all p.
func (t *Terminal) WriteUntilGround(p []byte) (int, error) {
	n := 0
	for len(p) > 0 && t.err == nil {
		chunk := min(len(p), scratchSize-4)
		if !t.mod.Memory().Write(uint32(t.scratch), p[:chunk]) {
			return n, errors.New("terminal recovery input outside scratch memory")
		}
		out := uint32(t.scratch + scratchSize - 4)
		status := int32(t.call("ghostty_terminal_vt_write_until_ground", t.ptr, t.scratch, uint64(chunk), uint64(out)))
		if t.err != nil {
			return n, t.err
		}
		consumed, ok := t.mod.Memory().ReadUint32Le(out)
		// GhosttyResult: success=0, no_value=-4 (consumed without reaching ground).
		if !ok || consumed > uint32(chunk) || (status != 0 && status != -4) || (status == -4 && consumed != uint32(chunk)) {
			return n, errors.New("invalid terminal recovery result")
		}
		n += int(consumed)
		if status == 0 {
			return n, nil
		}
		p = p[chunk:]
	}
	return n, t.err
}

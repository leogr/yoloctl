package terminal

import "errors"

// FilterBells uses a separate native stream to neutralize executed BEL bytes
// in replay only. NUL preserves the original UTF-8/control boundaries; OSC
// terminators and string payloads stay byte-exact. It emits no effects or
// replies and does not change this Terminal's screen or replay-prefix state.
// The module owns the small fixed-state filter until Close.
func (t *Terminal) FilterBells(p []byte) ([]byte, error) {
	if t.bellFilter == 0 && t.err == nil {
		t.bellFilter = t.call("yolo_vt_bell_filter_new", t.ptr)
		if t.bellFilter == 0 && t.err == nil {
			t.err = errors.New("terminal bell filter allocation failed")
		}
	}
	out := make([]byte, 0, len(p))
	for len(p) > 0 && t.err == nil {
		n := min(len(p), scratchSize)
		if !t.writeCore(uint32(t.scratch), p[:n]) {
			return nil, errors.New("terminal bell filter outside scratch memory")
		}
		t.call("yolo_vt_bell_filter", t.bellFilter, t.scratch, uint64(n))
		var ok bool
		if out, ok = t.appendCore(out, uint32(t.scratch), uint32(n)); !ok {
			return nil, errors.New("terminal bell filter result outside scratch memory")
		}
		p = p[n:]
	}
	return out, t.err
}

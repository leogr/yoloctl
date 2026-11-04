package terminal

import "errors"

// FilterPassthrough retains recognized UTF-8 display/input controls using a
// separate native stream, without altering this terminal's state. It withholds
// incomplete controls and drops queries, opaque extensions, invalid UTF-8 and
// controls larger than 2 MiB. It never produces a reply or a local effect.
func (t *Terminal) FilterPassthrough(p []byte) ([]byte, error) {
	if t.passthrough == 0 && t.err == nil {
		t.passthrough = t.call("yolo_vt_passthrough_new", t.ptr)
		if t.passthrough == 0 && t.err == nil {
			t.err = errors.New("terminal passthrough filter allocation failed")
		}
	}
	t.filtering = true
	t.filtered = nil
	defer func() { t.filtering = false; t.filtered = nil }()
	for len(p) > 0 && t.err == nil {
		n := min(len(p), scratchSize)
		if !t.mod.Memory().Write(uint32(t.scratch), p[:n]) {
			return nil, errors.New("terminal passthrough input outside memory")
		}
		if t.call("yolo_vt_passthrough", t.passthrough, t.scratch, uint64(n)) != 0 && t.err == nil {
			t.err = errors.New("terminal passthrough filter failed")
		}
		p = p[n:]
	}
	return t.filtered, t.err
}

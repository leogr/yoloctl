package terminal

import "errors"

// SelectionSpan is a native row's selected columns, with End exclusive.
// A wide grapheme is displayed selected if any of its cells intersects the span.
type SelectionSpan struct{ Start, End int }

// Select updates the native gesture at a displayed cell. Multi-clicks use a
// 500ms interval and cell-centre geometry; no host sub-cell precision is claimed.
// Frame and this method must share their owner's terminal lock.
func (t *Terminal) Select(action MouseAction, x, y int, rectangle bool, now uint64, trim bool) error {
	if action > MouseMotion || x < -1<<31 || x > 1<<31-1 || y < -1<<31 || y > 1<<31-1 {
		return errors.New("invalid selection event")
	}
	f, err := t.Frame(trim)
	if err != nil || len(f.Lines) == 0 {
		return err
	}
	if t.selection == 0 {
		t.selection = t.call("yolo_vt_selection_new", t.ptr)
		if t.selection == 0 {
			return errors.New("cannot allocate terminal selection")
		}
	}
	var rect uint64
	if rectangle {
		rect = 1
	}
	status := t.call("yolo_vt_selection_event", t.ptr, t.selection, uint64(action),
		uint64(uint32(int32(x))), uint64(f.top+t.historyBase()+min(max(y, 0), len(f.Lines)-1)),
		uint64(uint32(int32(y))), uint64(f.Rows), now, rect)
	if t.err != nil {
		return t.err
	}
	if status != 0 {
		return errors.New("cannot update terminal selection")
	}
	return nil
}

func (t *Terminal) ClearSelection() {
	if t.selection != 0 {
		t.call("yolo_vt_selection_event", t.ptr, t.selection, 3, 0, 0, 0, 0, 0, 0)
	}
}

// SelectedText unwraps native soft wraps and retains hard line breaks. The
// bounded writer fails instead of returning a silently truncated clipboard.
func (t *Terminal) SelectedText(limit int) (string, error) {
	if limit < 1 || limit > 1<<20 {
		return "", errors.New("invalid selection size limit")
	}
	p := t.call("ghostty_wasm_alloc", uint64(limit))
	if p == 0 || t.err != nil {
		return "", errors.New("cannot allocate selection text")
	}
	defer t.call("ghostty_wasm_free", p, uint64(limit))
	n := t.call("yolo_vt_selection_text", t.ptr, p, uint64(limit))
	if t.err != nil {
		return "", t.err
	}
	if n > uint64(limit) {
		return "", errors.New("selection exceeds clipboard size limit")
	}
	text, ok := t.mod.Memory().Read(uint32(p), uint32(n))
	if !ok {
		return "", errors.New("selection text outside memory")
	}
	return string(text), nil
}

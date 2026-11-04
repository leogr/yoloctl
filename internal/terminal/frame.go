package terminal

import (
	"errors"
	"strings"
	"time"
)

// RenderHoldTimeout matches the pinned Ghostty frontend's maximum synchronized
// output hold. Managed sessions time this at the holder, independently of views.
const RenderHoldTimeout = time.Second

// Cursor is captured with its frame; coordinates are relative to Lines.
// Shape follows the pinned core: bar=0, block=1, underline=2, hollow block=3.
type Cursor struct {
	X, Y           int
	Shape          int
	Visible, Blink bool
}

// Frame is an owned display snapshot. Capturing it must be serialized with
// writes and resize, like every Terminal operation. Only viewport-sized styled
// rows are exported, even when the native history is large.
type Frame struct {
	Lines                []string
	Selection            []SelectionSpan
	Rows, Cols           int
	History, ContentRows int
	GridStart            int // index of live grid row zero relative to Lines
	Offset               int
	Cursor               Cursor
	top                  int
}

// Frame captures the complete visible window and cursor together. trim is for
// the command overlay, whose display ends after its last non-blank grid row.
func (t *Terminal) Frame(trim bool) (Frame, error) {
	f := Frame{Rows: t.Get(2), Cols: t.Get(1), History: t.Get(9)}
	used := f.Rows
	if trim {
		plain, err := t.Lines(false, true, 0, f.Rows)
		if err != nil {
			return Frame{}, err
		}
		for used > 0 && strings.TrimSpace(plain[used-1]) == "" {
			used--
		}
	}
	f.ContentRows = f.History + used
	liveTop := max(f.ContentRows-f.Rows, 0)
	f.top = liveTop
	if t.anchor != 0 {
		if t.anchorScreen != t.Get(6) {
			t.ScrollToBottom()
		} else {
			y := t.call("yolo_vt_anchor_row", t.anchor)
			base := t.historyBase()
			if y == 0xfffffffe {
				t.ScrollToBottom() // reset replaced the screen owning this pin
			} else if y == 0xffffffff || int(y) < base {
				// The anchored text was evicted. Clamp to the oldest
				// surviving row, never jump to unrelated recent output.
				f.top = 0
				if err := t.setAnchor(0); err != nil {
					return Frame{}, err
				}
			} else {
				f.top = min(int(y)-base, liveTop)
			}
		}
		if f.top == liveTop {
			t.ScrollToBottom()
		}
	}
	f.Offset = liveTop - f.top
	f.GridStart = f.History - f.top
	end := min(f.top+f.Rows, f.ContentRows)
	if f.top < f.History {
		lines, err := t.Lines(true, false, f.top, min(end, f.History)-f.top)
		if err != nil {
			return Frame{}, err
		}
		f.Lines = append(f.Lines, lines...)
		f.Selection = append(f.Selection, t.spans...)
	}
	if end > f.History {
		start := max(f.top-f.History, 0)
		lines, err := t.Lines(false, false, start, end-f.History-start)
		if err != nil {
			return Frame{}, err
		}
		f.Lines = append(f.Lines, lines...)
		f.Selection = append(f.Selection, t.spans...)
	}
	f.Cursor = Cursor{X: t.Get(3), Y: f.History + t.Get(4) - f.top,
		Shape: t.Get(10), Blink: t.Get(11) != 0}
	f.Cursor.Visible = t.Get(7) != 0 && f.Offset == 0 && f.Cursor.Y >= 0 && f.Cursor.Y < len(f.Lines)
	return f, t.err
}

// Scroll moves the viewport by rows (positive is toward older text). The
// native pin follows its content through subsequent output and resize.
func (t *Terminal) Scroll(delta int, trim bool) (int, error) {
	f, err := t.Frame(trim)
	if err != nil {
		return 0, err
	}
	liveTop := max(f.ContentRows-f.Rows, 0)
	top := min(max(f.top-delta, 0), liveTop)
	if top == liveTop {
		t.ScrollToBottom()
	} else if err := t.setAnchor(top); err != nil {
		return 0, err
	}
	return liveTop - top, t.err
}

func (t *Terminal) setAnchor(row int) error {
	t.ScrollToBottom()
	t.anchor = t.call("yolo_vt_anchor", t.ptr, uint64(row+t.historyBase()))
	t.anchorScreen = t.Get(6)
	if t.anchor == 0 && t.err == nil {
		return errors.New("cannot anchor terminal viewport")
	}
	return t.err
}

// SetHistoryViewLimit caps this viewer's history without changing shared native
// retention, reflow or future snapshots. Existing anchors clamp on the next frame.
func (t *Terminal) SetHistoryViewLimit(lines int) error {
	if lines < 0 || uint64(lines) > MaxHistoryLines {
		return errors.New("invalid terminal history view limit")
	}
	t.visibleHistory = min(lines, t.history)
	return nil
}

// Native page allocation can retain more history than the configured visible
// line cap. Pins use native screen coordinates; the display uses the capped tail.
func (t *Terminal) historyBase() int {
	return max(int(t.call("yolo_vt_get", t.ptr, 9))-t.visibleHistory, 0)
}

func (t *Terminal) ScrollToBottom() {
	if t.anchor != 0 {
		t.call("yolo_vt_anchor_free", t.anchor)
		t.anchor = 0
	}
}

// SetHoldHandler receives native hold transitions synchronously during Write.
// A handler may capture a Frame, but must not write/resize or use the scratch
// buffer (e.g. EncodeKey, Title) while the native parser is consuming it.
func (t *Terminal) SetHoldHandler(fn func(bool)) { t.onHold = fn }

func (t *Terminal) EndHold() {
	t.call("yolo_vt_end_hold", t.ptr)
	t.hold = false
}

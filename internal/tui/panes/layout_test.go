package panes

import (
	"errors"
	"testing"
)

// three is a 240×61 tab split into left | (top / bottom), focus on bottom.
func three(t *testing.T) (*Layout, *Pane, *Pane, *Pane) {
	t.Helper()
	left, top, bottom := &Pane{Command: "left"}, &Pane{Command: "top"}, &Pane{Command: "bottom"}
	l := New(left)
	l.Resize(240, 61)
	if err := l.Split(true, top); err != nil {
		t.Fatal(err)
	}
	if err := l.Split(false, bottom); err != nil {
		t.Fatal(err)
	}
	return l, left, top, bottom
}

func TestSplitGeometryAndReflow(t *testing.T) {
	l, left, top, bottom := three(t)
	// The bottom row is the bottom-edge panes' bars; top's bar is the
	// horizontal border.
	if left.Rect() != (Rect{0, 0, 120, 60}) || top.Rect() != (Rect{121, 0, 119, 30}) || bottom.Rect() != (Rect{121, 31, 119, 29}) {
		t.Fatalf("rects %+v %+v %+v", left.Rect(), top.Rect(), bottom.Rect())
	}
	if left.Bar() != (Rect{0, 60, 120, 1}) || top.Bar() != (Rect{121, 30, 119, 1}) || bottom.Bar() != (Rect{121, 60, 119, 1}) {
		t.Fatalf("bars %+v %+v %+v", left.Bar(), top.Bar(), bottom.Bar())
	}
	if l.Focus() != bottom || !l.Divided() || len(l.Panes()) != 3 {
		t.Fatal("the new pane takes focus")
	}
	l.Resize(100, 30) // proportional re-flow on host resize
	if left.Rect() != (Rect{0, 0, 50, 29}) || top.Rect() != (Rect{51, 0, 49, 14}) || bottom.Rect() != (Rect{51, 15, 49, 14}) {
		t.Fatalf("reflow %+v %+v %+v", left.Rect(), top.Rect(), bottom.Rect())
	}
}

func TestSplitRefusedBelowTheMinimum(t *testing.T) {
	l := New(&Pane{})
	l.Resize(240, 61)
	n := 1
	for {
		err := l.Split(n%2 == 1, &Pane{})
		if err != nil {
			if !errors.Is(err, ErrTooSmall) {
				t.Fatal(err)
			}
			break
		}
		n++
	}
	for _, p := range l.Panes() {
		if r := p.Rect(); r.W < MinCols || r.H < MinRows {
			t.Fatalf("pane below the minimum: %+v", r)
		}
	}
	if n != 7 {
		t.Fatalf("alternating splits in 240×61 stop at %d panes, want 7", n)
	}
	small := New(&Pane{})
	small.Resize(40, 61)
	if small.CanSplit(true) == nil || small.CanSplit(false) != nil {
		t.Fatal("41 columns cannot hold two 20-column panes and a border; 61 rows can split")
	}
}

func TestArrowFocusNoWrapAndNotWhileZoomed(t *testing.T) {
	l, left, top, bottom := three(t)
	steps := []struct {
		d    Dir
		want *Pane
		ok   bool
	}{
		{Up, top, true}, {Up, top, false}, {Down, bottom, true}, {Left, left, true}, {Left, left, false},
		{Right, top, true}, // equal overlap: the nearer (top) wins
		{Down, bottom, true}, {Right, bottom, false},
	}
	for i, s := range steps {
		if ok := l.MoveFocus(s.d); ok != s.ok || l.Focus() != s.want {
			t.Fatalf("step %d: moved=%v focus=%q", i, ok, l.Focus().Command)
		}
	}
	l.ToggleZoom()
	if l.Zoomed() != bottom || l.MoveFocus(Left) || l.Focus() != bottom || bottom.Rect() != (Rect{0, 0, 240, 60}) || bottom.Bar() != (Rect{0, 60, 240, 1}) {
		t.Fatal("zoom shows the focused pane alone, with its bar, and freezes focus")
	}
	if l.Divided() || !l.Bars() || len(l.Visible()) != 1 {
		t.Fatal("a zoomed tab composes one frame and its bar")
	}
	l.ToggleZoom()
	if bottom.Rect() != (Rect{121, 31, 119, 29}) || !l.Divided() {
		t.Fatalf("zoom restores the layout: %+v", bottom.Rect())
	}
}

func TestKeyboardResizeAndEqualize(t *testing.T) {
	l, left, _, bottom := three(t)
	if !l.ResizeFocused(Left, 1) || left.Rect().W != 119 || bottom.Rect().X != 120 || bottom.Rect().W != 120 {
		t.Fatalf("Ctrl+← moves the vertical border one cell: %+v %+v", left.Rect(), bottom.Rect())
	}
	if !l.ResizeFocused(Up, 1) || bottom.Rect().Y != 30 || bottom.Rect().H != 30 {
		t.Fatalf("Ctrl+↑ moves the horizontal border one cell: %+v", bottom.Rect())
	}
	if !l.ResizeFocused(Right, 5) || bottom.Rect().X != 125 {
		t.Fatalf("Alt+→ moves the border five cells: %+v", bottom.Rect())
	}
	gen := l.Generation()
	for l.ResizeFocused(Right, 5) {
	}
	if bottom.Rect().W != MinCols || l.Generation() == gen {
		t.Fatalf("resizing by five stops exactly at the minimum: %+v", bottom.Rect())
	}
	if l.ResizeFocused(Right, 1) {
		t.Fatal("a border at the minimum does not move")
	}
	l.Equalize()
	if left.Rect().W != 120 || bottom.Rect() != (Rect{121, 31, 119, 29}) {
		t.Fatalf("equalize: %+v %+v", left.Rect(), bottom.Rect())
	}
}

// A border clamps on the recursive minimum: a nested subtree whose inner
// ratio is skewed starves a pane before the subtree's aggregate width
// reaches the sum of its minima.
func TestKeyboardResizeKeepsNestedMinimum(t *testing.T) {
	a, b, c := &Pane{Command: "a"}, &Pane{Command: "b"}, &Pane{Command: "c"}
	l := New(a)
	l.Resize(100, 20)
	if l.Split(true, b) != nil || l.Split(true, c) != nil {
		t.Fatal("split")
	}
	// b | c sits right of a; push the inner border left, so b is at its minimum.
	for l.ResizeFocused(Left, 1) {
	}
	if b.Rect().W != MinCols {
		t.Fatalf("inner border stops at b's minimum: %+v", b.Rect())
	}
	l.SetFocus(a)
	for l.ResizeFocused(Right, 1) {
	}
	if b.Rect().W < MinCols || c.Rect().W < MinCols {
		t.Fatalf("the outer border kept every nested pane at the minimum: b %+v c %+v", b.Rect(), c.Rect())
	}
}

func TestFocusNextAndSwap(t *testing.T) {
	l, left, top, bottom := three(t)
	l.SetFocus(left)
	if !l.FocusNext() || l.Focus() != top || !l.FocusNext() || l.Focus() != bottom || !l.FocusNext() || l.Focus() != left {
		t.Fatal("o walks the panes in tree order and wraps")
	}
	lr, tr := left.Rect(), top.Rect()
	gen := l.Generation()
	if !l.SwapFocused(true) || left.Rect() != tr || top.Rect() != lr || l.Focus() != left || l.Generation() == gen {
		t.Fatalf("} swaps with the next pane and focus stays on the moved pane: %+v %+v", left.Rect(), top.Rect())
	}
	if !l.SwapFocused(false) || left.Rect() != lr || top.Rect() != tr {
		t.Fatal("{ swaps back")
	}
	if !l.SwapFocused(false) || l.Panes()[2] != left || l.Panes()[0] != bottom {
		t.Fatal("{ on the first pane wraps to the last")
	}
	l.ToggleZoom()
	if l.FocusNext() || l.SwapFocused(true) {
		t.Fatal("nothing moves while zoomed")
	}
	one := New(&Pane{})
	if one.FocusNext() || one.SwapFocused(true) {
		t.Fatal("an undivided tab has nothing to move")
	}
}

func TestRemoveGivesTheSpaceToTheSibling(t *testing.T) {
	l, left, top, bottom := three(t)
	l.SetFocus(top)
	if l.Remove(top) || l.Focus() != bottom || bottom.Rect() != (Rect{121, 0, 119, 60}) {
		t.Fatalf("the sibling takes the space and the focus: %+v", bottom.Rect())
	}
	l.ToggleZoom()
	if l.Remove(bottom) || l.Zoomed() != nil || l.Focus() != left || left.Rect() != (Rect{0, 0, 240, 61}) || left.Bar() != (Rect{}) || l.Bars() {
		t.Fatal("removing the zoomed pane unzooms and leaves one pane")
	}
	if !l.Remove(left) || l.Focus() != nil || len(l.Panes()) != 0 {
		t.Fatal("removing the last pane empties the layout")
	}
}

func TestHitTestIncludingBorders(t *testing.T) {
	l, left, top, bottom := three(t)
	for _, c := range []struct {
		x, y   int
		want   *Pane
		lx, ly int
	}{
		{0, 0, left, 0, 0}, {119, 59, left, 119, 59}, {119, 60, nil, 0, 0}, {120, 5, nil, 0, 0}, {121, 0, top, 0, 0},
		{130, 30, nil, 0, 0}, {130, 31, bottom, 9, 0}, {239, 59, bottom, 118, 28}, {239, 60, nil, 0, 0}, {240, 0, nil, 0, 0},
	} {
		p, lx, ly := l.At(c.x, c.y)
		if p != c.want || p != nil && (lx != c.lx || ly != c.ly) {
			t.Errorf("At(%d,%d) = %v (%d,%d)", c.x, c.y, p, lx, ly)
		}
	}
	if !l.SetFocus(left) || left.Mark {
		t.Fatal("focusing clears the attention mark")
	}
	l.ToggleZoom()
	if p, _, _ := l.At(200, 40); p != left {
		t.Fatal("a zoomed pane covers the tab")
	}
	if l.SetFocus(top) {
		t.Fatal("focus cannot leave a zoomed pane")
	}
}

// A split counts the bars it adds: a first horizontal split needs 5 rows, a
// bar, 5 rows and the bottom bar; a vertical one 20 + 1 + 20 columns.
func TestCanSplitCountsBars(t *testing.T) {
	for _, c := range []struct {
		w, h     int
		vertical bool
		ok       bool
	}{
		{40, 11, false, false}, {40, 12, false, true}, {41, 5, true, false}, {41, 6, true, true},
		{40, 20, true, false}, {41, 20, true, true},
	} {
		l := New(&Pane{})
		l.Resize(c.w, c.h)
		if err := l.CanSplit(c.vertical); (err == nil) != c.ok {
			t.Errorf("%d×%d vertical=%v: %v", c.w, c.h, c.vertical, err)
		}
		if l.Bars() || l.Focus().Rect() != (Rect{0, 0, c.w, c.h}) {
			t.Fatalf("CanSplit leaves the layout as it was: %+v", l.Focus().Rect())
		}
	}
}

// Bar parts, border hit-testing and the horizontal split under a bar.
func TestBarAndBorderHitTesting(t *testing.T) {
	l, left, top, bottom := three(t)
	top.Command, top.Session = "chat", live("").Session // live: ⤢ and ✕
	l.SetFocus(top)
	bar := top.Bar() // " ▸ chat … ⤢   ✕  ": each button is its padded glyph
	if p, part := l.BarAt(bar.X+3, bar.Y); p != top || part != BarTitle {
		t.Fatalf("the title: %v %v", p, part)
	}
	for _, c := range []struct {
		dx   int
		part BarPart
	}{
		{0, BarNone}, {bar.W / 2, BarNone}, // band
		{bar.W - 9, BarNone}, {bar.W - 8, BarZoom}, {bar.W - 7, BarZoom}, {bar.W - 6, BarZoom},
		{bar.W - 5, BarNone}, {bar.W - 4, BarClose}, {bar.W - 3, BarClose}, {bar.W - 2, BarClose},
		{bar.W - 1, BarNone},
	} {
		if p, part := l.BarAt(bar.X+c.dx, bar.Y); p != top || part != c.part {
			t.Fatalf("bar column %d: %v %v, want %v", c.dx, p, part, c.part)
		}
	}
	if p, _ := l.BarAt(5, 5); p != nil {
		t.Fatal("content is not a bar")
	}
	s, ok := l.BorderAt(120, 10)
	if !ok || !s.Vertical() || l.BorderPos(s) != 120 {
		t.Fatal("the vertical border column")
	}
	if s2, ok := l.BorderAt(120, 60); !ok || s2 != s {
		t.Fatal("the │ on the bottom bar row takes the vertical border")
	}
	if _, ok := l.BorderAt(121, 10); ok {
		t.Fatal("pane content is not a border")
	}
	h, ok := l.SplitBelow(top)
	if !ok || h.Vertical() || l.BorderPos(h) != 30 {
		t.Fatal("top's bar is the horizontal border above bottom")
	}
	if _, ok := l.SplitBelow(bottom); ok {
		t.Fatal("a bottom-edge bar resizes nothing")
	}
	if _, ok := l.SplitBelow(left); ok {
		t.Fatal("left's bar is on the bottom edge")
	}
}

// A preview is clamped by the recursive minimum of each side's subtree and
// changes no committed rectangle until it is committed.
func TestPreviewClampsAndCommitsOnce(t *testing.T) {
	l, left, top, bottom := three(t)
	// right column: split top again so its subtree needs 20+1+20 columns
	l.SetFocus(top)
	inner := &Pane{}
	if err := l.Split(true, inner); err != nil {
		t.Fatal(err)
	}
	s, _ := l.BorderAt(120, 10)
	before := []Rect{left.Rect(), top.Rect(), inner.Rect(), bottom.Rect()}
	if got := l.Preview(s, 230); got != 240-41-1 {
		t.Fatalf("clamped by the nested right side (41 columns): %d", got)
	}
	if got := l.Preview(s, 3); got != MinCols {
		t.Fatalf("clamped by the left pane: %d", got)
	}
	if after := []Rect{left.Rect(), top.Rect(), inner.Rect(), bottom.Rect()}; after[0] != before[0] || after[3] != before[3] {
		t.Fatal("a preview changes no committed rectangle")
	}
	l.CancelPreview()
	if l.CommitPreview() {
		t.Fatal("nothing to commit after a cancel")
	}
	l.Preview(s, 100)
	if !l.CommitPreview() || left.Rect().W != 100 || bottom.Rect().X != 101 {
		t.Fatalf("commit: %+v %+v", left.Rect(), bottom.Rect())
	}
	l.Remove(inner)
	if l.Valid(s, false) || !l.Valid(s, true) {
		t.Fatal("the split stays valid with its axis")
	}
	l.Remove(top)
	l.Remove(bottom)
	if l.Valid(s, true) {
		t.Fatal("a split gone from the tree is not valid")
	}
}

// A side's minimum is a trial layout with its own ratios, not a sum of
// minima: a skewed inner split must not starve a pane, and a host resize
// during the drag clamps again or drops the preview.
func TestPreviewKeepsEveryPaneAtSkewedRatios(t *testing.T) {
	left, a, b := &Pane{}, &Pane{}, &Pane{}
	l := New(left)
	l.Resize(240, 30)
	if err := l.Split(true, a); err != nil {
		t.Fatal(err)
	}
	if err := l.Split(true, b); err != nil {
		t.Fatal(err)
	}
	inner, _ := l.BorderAt(a.Rect().X+a.Rect().W, 3)
	l.Preview(inner, a.Rect().X+80) // skew the right side: a wide, b narrow
	l.CommitPreview()
	root, _ := l.BorderAt(left.Rect().W, 3)
	check := func(what string) {
		t.Helper()
		geo, _ := l.previewGeometry()
		for p, g := range geo {
			if g[0].W < MinCols || g[0].H < MinRows {
				t.Fatalf("%s: a pane is starved in the preview: %+v (%v)", what, g[0], p == b)
			}
		}
	}
	l.Preview(root, 230)
	check("drag right")
	l.Resize(80, 30)
	check("host resize during the drag")
	if l.CommitPreview() {
		for _, p := range l.Panes() {
			if r := p.Rect(); r.W < MinCols {
				t.Fatalf("a commit after the resize starves a pane: %+v", r)
			}
		}
	}
}

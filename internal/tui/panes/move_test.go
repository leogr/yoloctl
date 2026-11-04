package panes

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// pair is a laid-out layout of two live panes, a left of b, b focused.
func pair(t *testing.T, w, h int, a, b string) (*Layout, *Pane, *Pane) {
	t.Helper()
	pa, pb := live(a), live(b)
	l := New(pa)
	l.Resize(w, h)
	if err := l.Split(true, pb); err != nil {
		t.Fatal(err)
	}
	return l, pa, pb
}

// R4: a graft puts the source's whole tree beside the target on each side,
// with half the space; the joined tree's focused pane takes focus; pane
// identity is kept and the source is left with no pane.
func TestGraftSides(t *testing.T) {
	for _, c := range []struct {
		side  Dir
		check func(dst, in Rect) bool
	}{
		{Left, func(dst, in Rect) bool { return in.X+in.W+1 == dst.X && in.Y == dst.Y }},
		{Right, func(dst, in Rect) bool { return dst.X+dst.W+1 == in.X && in.Y == dst.Y }},
		{Up, func(dst, in Rect) bool { return in.Y+in.H+1 == dst.Y && in.X == dst.X }},
		{Down, func(dst, in Rect) bool { return dst.Y+dst.H+1 == in.Y && in.X == dst.X }},
	} {
		dst := live("dst")
		l := New(dst)
		l.Resize(200, 60)
		src, s1, s2 := pair(t, 80, 24, "s1", "s2")
		src.SetFocus(s1)
		gen := l.Generation()
		if err := l.Graft(dst, c.side, src); err != nil {
			t.Fatal(err)
		}
		in := Rect{min(s1.Rect().X, s2.Rect().X), s1.Rect().Y, 0, s1.Rect().H}
		in.W = s2.Rect().X + s2.Rect().W - in.X
		if !c.check(dst.Rect(), in) {
			t.Errorf("side %d: dst %+v, joined tree %+v", c.side, dst.Rect(), in)
		}
		if ps := l.Panes(); len(ps) != 3 || l.Focus() != s1 || l.Generation() == gen {
			t.Errorf("side %d: %d panes, focus %q", c.side, len(ps), l.Focus().Command)
		}
		if src.Panes() != nil || src.Focus() != nil {
			t.Error("the source keeps no pane")
		}
	}
}

// A graft onto the empty pane takes its place, wherever it was split, and
// the empty pane leaves.
func TestGraftFillsTheEmptyPane(t *testing.T) {
	l, a, e := pair(t, 200, 60, "a", "")
	e.Session = nil
	if l.Empty() != e {
		t.Fatal("setup: b is the empty pane")
	}
	slot := e.Rect()
	src, s1, s2 := pair(t, 80, 24, "s1", "s2")
	if err := l.Graft(e, Down, src); err != nil {
		t.Fatal(err)
	}
	if l.Empty() != nil || len(l.Panes()) != 3 || l.Panes()[0] != a || l.Focus() != s2 {
		t.Fatalf("the source tree fills the empty pane: %d panes", len(l.Panes()))
	}
	if r1, r2 := s1.Rect(), s2.Rect(); r1.X != slot.X || r2.X+r2.W != slot.X+slot.W || r1.H != slot.H {
		t.Fatalf("the joined tree takes the empty pane's rectangle %+v: %+v %+v", slot, r1, r2)
	}
}

// The preflight is the recursive minimum of the joined tree with bars; a
// refusal changes nothing in either layout.
func TestGraftRefusedChangesNothing(t *testing.T) {
	l, a, b := pair(t, 100, 30, "a", "b")
	l.ToggleZoom()
	src, s1, s2 := pair(t, 100, 30, "s1", "s2")
	before := []Rect{a.Rect(), b.Rect(), s1.Rect(), s2.Rect()}
	gen, sgen := l.Generation(), src.Generation()
	// b's half is 49 columns: right of it, a 20+1+20 tree leaves b 24.
	if err := l.CanGraft(b, Right, src); !errors.Is(err, ErrTooSmall) {
		t.Fatalf("too small: %v", err)
	}
	if err := l.Graft(b, Right, src); !errors.Is(err, ErrTooSmall) {
		t.Fatalf("graft: %v", err)
	}
	after := []Rect{a.Rect(), b.Rect(), s1.Rect(), s2.Rect()}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("a refused graft moved a pane: %+v → %+v", before, after)
		}
	}
	if l.Zoomed() != b || l.Generation() != gen || src.Generation() != sgen || len(src.Panes()) != 2 || len(l.Panes()) != 2 {
		t.Fatal("a refused graft changes nothing, zoom included")
	}
	// Below b fits (rows are plenty): it commits and clears the zoom.
	if err := l.Graft(b, Down, src); err != nil {
		t.Fatal(err)
	}
	if l.Zoomed() != nil || len(l.Panes()) != 4 {
		t.Fatal("a committed graft clears zoom")
	}
	for _, p := range l.Panes() {
		if r := p.Rect(); r.W < MinCols || r.H < MinRows {
			t.Fatalf("a pane below the minimum: %+v", r)
		}
	}
	if err := l.CanGraft(live("gone"), Down, New(live("x"))); !errors.Is(err, ErrNoTarget) {
		t.Fatal("a target outside the layout is refused")
	}
}

// Replace fills a pane's place, keeping its rectangle and focus.
func TestReplaceKeepsPlaceAndFocus(t *testing.T) {
	l, _, e := pair(t, 100, 30, "a", "")
	e.Session = nil
	slot, gen := e.Rect(), l.Generation()
	p := live("filled")
	if !l.Replace(e, p) || p.Rect() != slot || l.Focus() != p || l.Empty() != nil || l.Generation() == gen {
		t.Fatal("Replace puts the pane in the empty pane's place")
	}
	if l.Replace(e, live("again")) {
		t.Fatal("a pane no longer in the layout is not replaced")
	}
}

// The drop side is the nearest edge, a column counting half a row; an empty
// pane is whole; borders and bars are no target.
func TestDropAtSides(t *testing.T) {
	l, a, _ := pair(t, 81, 21, "a", "b")
	r := a.Rect() // 40×20 at 0,0
	for _, c := range []struct {
		x, y int
		side Dir
	}{
		{0, 10, Left}, {r.W - 1, 10, Right}, {20, 0, Up}, {20, r.H - 1, Down},
		{19, 5, Up}, {19, 14, Down}, // at the centre column, rows decide
		{5, 10, Left}, {34, 10, Right}, // ten columns are five rows
		{9, 4, Up}, // 4.5 rows vs 4.75: up
	} {
		p, side, ok := l.DropAt(c.x, c.y)
		if !ok || p != a || side != c.side {
			t.Errorf("(%d,%d): %v %d, want %d", c.x, c.y, ok, side, c.side)
		}
	}
	if _, _, ok := l.DropAt(r.W, 5); ok {
		t.Error("a border is no target")
	}
	if _, _, ok := l.DropAt(5, r.H); ok {
		t.Error("a bar is no target")
	}
}

// A drop is drawn over the committed geometry: the target half tinted 94
// with its text kept and the pane's border orange when it would be taken,
// hatched grey when refused; the empty pane is a whole target.
func TestComposeDrop(t *testing.T) {
	l, a, b := pair(t, 60, 8, "a", "b")
	l.SetFocus(a)
	rows := map[*Pane][]string{a: frameRows(t, 7, 29, "AAAA"), b: frameRows(t, 7, 30, "BBBB")}
	l.SetDrop(Drop{Pane: b, Side: Right, OK: true})
	tinted := Compose(l, rows)
	golden(t, "drop-tint", tinted)
	if !strings.Contains(tinted[0], "48;5;94") || !strings.Contains(ansi.Strip(tinted[0]), "BBBB") {
		t.Fatal("the passing half is tinted and keeps its text")
	}
	l.SetDrop(Drop{Pane: b, Side: Down, OK: false})
	hatched := Compose(l, rows)
	golden(t, "drop-hatch", hatched)
	if plain := ansi.Strip(hatched[5]); !strings.Contains(plain, "╱╱╱") || strings.Contains(ansi.Strip(hatched[0]), "╱") {
		t.Fatalf("the refused lower half is hatched: %q", plain)
	}
	l.ClearDrop()
	if got := Compose(l, rows); strings.Contains(strings.Join(got, ""), "╱") || strings.Contains(strings.Join(got, ""), "48;5;94") {
		t.Fatal("ClearDrop removes the target")
	}
	b.Session = nil
	l.SetDrop(Drop{Pane: b, Side: Left, OK: true})
	whole := Compose(l, rows)
	if !strings.Contains(whole[0], "48;5;94") {
		t.Fatal("the empty pane is a whole target")
	}
}

// The empty pane's bar says "empty" and carries only ✕; no bar carries
// split buttons, focused, unfocused or zoomed: splits are Ctrl+Q keys.
func TestEmptyPaneBar(t *testing.T) {
	e := &Pane{}
	l := &Layout{focus: e}
	if got := barText(l, e, 30); got != " ▸ empty                   ✕  " {
		t.Fatalf("focused empty pane: %q", got)
	}
	p := live("p")
	for _, l := range []*Layout{{focus: p}, {focus: e}, {focus: p, zoomed: p}} {
		for _, s := range l.barSegs(p, Rect{0, 0, 120, 1}) {
			if s.part == BarSplitRight || s.part == BarSplitBelow {
				t.Fatalf("a bar has no split buttons: %+v", s)
			}
		}
	}
}

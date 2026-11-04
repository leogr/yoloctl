// Package panes is a tab's pane layout: a binary split tree whose leaves
// each host one session of the kind a tab hosts. A tab with one pane is
// exactly a tab. Panes are viewers: they create no holder, program or
// persistence, and closing one is closing a tab's client today (a detach
// for managed views).
package panes

import (
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// The minimum pane size in cells. A split that would make either pane
// smaller is refused; there is no fixed pane count.
const (
	MinCols = 20
	MinRows = 5
)

// ErrTooSmall refuses a split below the minimum pane size.
var ErrTooSmall = errors.New("not enough room to split: a pane needs at least 20×5 cells")

// Rect is a cell rectangle relative to the tab area's top-left cell.
type Rect struct{ X, Y, W, H int }

// In reports whether a cell lies inside the rectangle.
func (r Rect) In(x, y int) bool { return r.contains(x, y) }

func (r Rect) contains(x, y int) bool { return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H }

// Pane is one subprocess of a tab: a palette command under a PTY, or an
// empty pane (a placeholder) that shows what fills it.
type Pane struct {
	Session *term.Session // nil for an empty pane
	Command string        // the verbatim palette line, for title fallback
	Exited  bool          // subprocess ended; the pane stays until closed
	// Mark is attention (bell, exit, request) while the pane was not the
	// focused pane of the current tab; focusing it clears the mark.
	Mark bool
	rect Rect
	bar  Rect
}

// Empty reports a placeholder: a pane with no session. Every
// session-dependent path checks it, never Session itself.
func (p *Pane) Empty() bool { return p.Session == nil }

// Rect is the pane's content rectangle from the last layout: the cells its
// child draws, the PTY size, and the area for cursor, selection and
// hit-testing.
func (p *Pane) Rect() Rect { return p.rect }

// Bar is the pane's bar from the last layout: the one row below its content
// across its width, drawn by yoloctl. It is empty in an undivided tab.
func (p *Pane) Bar() Rect { return p.bar }

// Title prefers the child's OSC 0/2 title, falling back to the command
// line that opened the pane (sans the redundant `yoloctl ` prefix).
// Control characters are stripped: titles come from the child and must
// not corrupt the strip.
func (p *Pane) Title() string {
	var s string
	if p.Session != nil {
		s = strings.TrimSpace(p.Session.Title())
	}
	if s == "" {
		s = strings.TrimPrefix(p.Command, "yoloctl ")
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// node is a leaf (pane) or a split: vertical splits place a left of b
// with a one-cell vertical border, horizontal splits place a above b.
type node struct {
	pane     *Pane
	vertical bool
	a, b     *node
	ratio    float64 // share of the split's free cells given to a
	parent   *node
	rect     Rect // the node's area from the last layout, bars excluded
}

// Layout is one tab's pane tree.
type Layout struct {
	root   *node
	focus  *Pane
	zoomed *Pane
	w, h   int
	// preview is a split's ratio while its border is dragged: the composer
	// draws it, while every committed rectangle and session size stays.
	preview      *node
	previewRatio float64
	// drop is a dock drag's target, drawn over the committed geometry.
	drop *Drop
	// Method measures bar titles, as the tab strip measures its labels.
	Method ansi.Method
	gen    int // counts changes of the tree or its ratios, not host resizes
}

// Generation changes whenever the tree or a ratio changes: a split, a
// removal, zoom, equalize, a keyboard resize or a committed drag. A host
// resize leaves it. Gestures captured on the layout compare it.
func (l *Layout) Generation() int { return l.gen }

// New starts a layout with one pane, focused.
func New(p *Pane) *Layout {
	return &Layout{root: &node{pane: p}, focus: p}
}

// Focus is the focused pane; nil only after the last pane was removed.
func (l *Layout) Focus() *Pane { return l.focus }

// SetFocus focuses a pane of this layout and clears its attention mark.
func (l *Layout) SetFocus(p *Pane) bool {
	if l.leaf(p) == nil || l.zoomed != nil && p != l.zoomed {
		return false
	}
	l.focus = p
	p.Mark = false
	return true
}

// Zoomed is the pane shown alone, or nil.
func (l *Layout) Zoomed() *Pane { return l.zoomed }

// Divided reports whether more than one pane is shown, the only case that
// composes several frames.
func (l *Layout) Divided() bool {
	return l.zoomed == nil && l.root != nil && l.root.pane == nil
}

// Bars reports whether the tab is composed with pane bars: whenever it holds
// more than one pane, zoomed or not. An undivided tab has none.
func (l *Layout) Bars() bool { return l.root != nil && l.root.pane == nil }

// Panes lists every pane in tree order: left to right, top to bottom.
func (l *Layout) Panes() []*Pane {
	var out []*Pane
	var walk func(*node)
	walk = func(n *node) {
		switch {
		case n == nil:
		case n.pane != nil:
			out = append(out, n.pane)
		default:
			walk(n.a)
			walk(n.b)
		}
	}
	walk(l.root)
	return out
}

// Visible lists the panes on screen: the zoomed pane alone, or all.
func (l *Layout) Visible() []*Pane {
	if l.zoomed != nil {
		return []*Pane{l.zoomed}
	}
	return l.Panes()
}

func (l *Layout) leaf(p *Pane) *node {
	var found *node
	var walk func(*node)
	walk = func(n *node) {
		if n == nil || found != nil {
			return
		}
		if n.pane == p {
			found = n
			return
		}
		walk(n.a)
		walk(n.b)
	}
	if p != nil {
		walk(l.root)
	}
	return found
}

// CanSplit reports whether the focused pane can be split: both halves must
// keep the minimum content size once laid out, bars included (a first
// split also gives the tab its bottom bar row).
func (l *Layout) CanSplit(vertical bool) error {
	if l.zoomed != nil || l.focus == nil {
		return ErrTooSmall
	}
	n := l.leaf(l.focus)
	focus := l.focus
	probe := &Pane{}
	l.split(n, vertical, probe)
	a, b := focus.rect, probe.rect
	// Undo the probe: the leaf takes its pane back and the tree its layout.
	n.pane, n.a, n.b = focus, nil, nil
	l.focus = focus
	l.Resize(l.w, l.h)
	for _, r := range []Rect{a, b} {
		if r.W < MinCols || r.H < MinRows {
			return ErrTooSmall
		}
	}
	return nil
}

// Split divides the focused pane: the new pane goes right of it
// (vertical) or below it (horizontal) and takes focus. Callers check
// CanSplit before starting the pane's session.
func (l *Layout) Split(vertical bool, p *Pane) error {
	if err := l.CanSplit(vertical); err != nil {
		return err
	}
	l.split(l.leaf(l.focus), vertical, p)
	l.gen++
	return nil
}

func (l *Layout) split(n *node, vertical bool, p *Pane) {
	old := &node{pane: n.pane, parent: n}
	add := &node{pane: p, parent: n}
	n.pane, n.vertical, n.a, n.b, n.ratio = nil, vertical, old, add, 0.5
	l.focus = p
	l.Resize(l.w, l.h)
}

// Remove takes a pane out of the tree and reports whether none is left.
// The sibling takes the space; if the pane was focused, focus moves to
// the sibling's first pane. The caller closes the pane's session.
func (l *Layout) Remove(p *Pane) (empty bool) {
	n := l.leaf(p)
	if n == nil {
		return l.root == nil
	}
	if l.zoomed == p {
		l.zoomed = nil
	}
	l.gen++
	parent := n.parent
	if parent == nil {
		l.root, l.focus = nil, nil
		return true
	}
	l.preview = nil
	sib := parent.a
	if sib == n {
		sib = parent.b
	}
	parent.pane, parent.vertical, parent.a, parent.b, parent.ratio = sib.pane, sib.vertical, sib.a, sib.b, sib.ratio
	if parent.a != nil {
		parent.a.parent, parent.b.parent = parent, parent
	}
	if l.focus == p {
		first := parent
		for first.pane == nil {
			first = first.a
		}
		l.focus = first.pane
	}
	l.Resize(l.w, l.h)
	return false
}

// Empty is the layout's empty pane, or nil: a tab holds at most one.
func (l *Layout) Empty() *Pane {
	for _, p := range l.Panes() {
		if p.Empty() {
			return p
		}
	}
	return nil
}

// Replace puts p in old's place, with old's rectangle and focus: an empty
// pane filled by a session. Gestures captured on old see it gone.
func (l *Layout) Replace(old, p *Pane) bool {
	n := l.leaf(old)
	if n == nil {
		return false
	}
	n.pane = p
	if l.focus == old {
		l.focus = p
	}
	if l.zoomed == old {
		l.zoomed = p
	}
	l.gen++
	l.Resize(l.w, l.h)
	return true
}

// ErrNoTarget refuses a graft onto a pane that is not in the layout.
var ErrNoTarget = errors.New("the target pane is gone")

// CanGraft reports whether src's whole tree fits beside target on side d,
// or in target's place when target is the empty pane: a trial layout of the
// joined tree over the whole tab area, as if unzoomed and with bars, keeps
// every pane at the minimum content size (the recursive minimum).
func (l *Layout) CanGraft(target *Pane, d Dir, src *Layout) error {
	n := l.leaf(target)
	if n == nil || src == l || src.root == nil {
		return ErrNoTarget
	}
	undo := l.graft(n, d, src.root)
	ok := fits(l.root, Rect{0, 0, l.w, max(l.h-1, 0)})
	undo()
	if !ok {
		return ErrTooSmall
	}
	return nil
}

// Graft moves src's whole pane tree into this layout, as CanGraft checks
// first: as a sibling of target on side d with half the space, or in
// target's place when target is the empty pane (which leaves). No session
// starts or closes and every pane keeps its identity; zoom is cleared, the
// joined tree's focused pane takes focus, and src is left with no pane.
func (l *Layout) Graft(target *Pane, d Dir, src *Layout) error {
	if err := l.CanGraft(target, d, src); err != nil {
		return err
	}
	l.graft(l.leaf(target), d, src.root)
	l.focus = src.focus
	if l.focus == nil {
		l.focus = firstLeaf(src.root)
	}
	l.focus.Mark = false
	l.zoomed, l.preview = nil, nil
	src.root, src.focus, src.zoomed, src.preview = nil, nil, nil, nil
	l.gen++
	src.gen++
	l.Resize(l.w, l.h)
	return nil
}

// graft joins sub at leaf n, in place: n becomes a split of n's pane and
// sub, or takes sub's contents when n is the empty pane. undo restores both.
func (l *Layout) graft(n *node, d Dir, sub *node) (undo func()) {
	saved, subParent := *n, sub.parent
	if n.pane.Empty() {
		n.pane, n.vertical, n.a, n.b, n.ratio = sub.pane, sub.vertical, sub.a, sub.b, sub.ratio
		if n.a != nil {
			n.a.parent, n.b.parent = n, n
		}
		return func() {
			if sub.a != nil {
				sub.a.parent, sub.b.parent = sub, sub
			}
			*n = saved
		}
	}
	old := &node{pane: n.pane, parent: n}
	sub.parent = n
	a, b := old, sub
	if d == Left || d == Up {
		a, b = sub, old
	}
	n.pane, n.vertical, n.a, n.b, n.ratio = nil, d == Left || d == Right, a, b, 0.5
	return func() {
		sub.parent = subParent
		*n = saved
	}
}

// Drop is a dock drag's target as the composer draws it: the half of a pane
// on one side, or the whole of an empty pane, tinted when the drop would be
// taken and hatched when it would be refused.
type Drop struct {
	Pane *Pane
	Side Dir
	OK   bool
}

// SetDrop shows a dock drag's target; ClearDrop removes it. Neither changes
// any rectangle.
func (l *Layout) SetDrop(d Drop) { l.drop = &d }

// ClearDrop removes the drop target.
func (l *Layout) ClearDrop() { l.drop = nil }

// Dropping reports a shown drop target: the tab is composed to draw it,
// even undivided.
func (l *Layout) Dropping() bool { return l.drop != nil }

// DropAt is the drop target under a tab-area cell: the visible pane whose
// content holds it and the side whose edge is nearest, a column counting
// half a row (cells are about twice as tall as wide). An empty pane is a
// whole target. Borders and bars are no target.
func (l *Layout) DropAt(x, y int) (*Pane, Dir, bool) {
	p, px, py := l.At(x, y)
	if p == nil {
		return nil, 0, false
	}
	r := p.rect
	dist := [4]float64{ // Left, Right, Up, Down
		(float64(px) + 0.5) / 2,
		(float64(r.W-px) - 0.5) / 2,
		float64(py) + 0.5,
		float64(r.H-py) - 0.5,
	}
	side := Left
	for d := Right; d <= Down; d++ {
		if dist[d] < dist[side] {
			side = d
		}
	}
	return p, side, true
}

// dropRect is the drawn target: the pane's content half on the drop's side,
// or all of it for an empty pane.
func (d Drop) rect(r Rect) Rect {
	if d.Pane.Empty() {
		return r
	}
	switch d.Side {
	case Left:
		r.W = (r.W + 1) / 2
	case Right:
		r.X, r.W = r.X+r.W/2, r.W-r.W/2
	case Up:
		r.H = (r.H + 1) / 2
	case Down:
		r.Y, r.H = r.Y+r.H/2, r.H-r.H/2
	}
	return r
}

// ToggleZoom shows the focused pane alone in the tab, or restores the
// layout. The tree and every other pane are kept.
func (l *Layout) ToggleZoom() {
	l.gen++
	if l.zoomed != nil {
		l.zoomed = nil
	} else if l.root != nil && l.root.pane == nil {
		l.zoomed = l.focus
	}
	l.Resize(l.w, l.h)
}

// Equalize resets every split to half and half.
func (l *Layout) Equalize() {
	var walk func(*node)
	walk = func(n *node) {
		if n == nil || n.pane != nil {
			return
		}
		n.ratio = 0.5
		walk(n.a)
		walk(n.b)
	}
	walk(l.root)
	l.gen++
	l.Resize(l.w, l.h)
}

// Resize lays the tree out over a w×h tab area, proportionally. With bars,
// every pane's bar is the row below its content: the horizontal border
// under a stacked pane, or the tab's bottom row. A zoomed pane gets the
// whole area above its bar; hidden panes keep their last rectangle.
func (l *Layout) Resize(w, h int) {
	l.w, l.h = w, h
	if l.root == nil {
		return
	}
	if !l.Bars() {
		l.root.pane.rect, l.root.pane.bar, l.root.rect = Rect{0, 0, w, h}, Rect{}, Rect{0, 0, w, h}
		return
	}
	if l.zoomed != nil {
		l.zoomed.rect, l.zoomed.bar = Rect{0, 0, w, h - 1}, Rect{0, h - 1, w, 1}
		return
	}
	l.place(nil, 0, func(n *node, r Rect) {
		n.rect = r
		if n.pane != nil {
			n.pane.rect, n.pane.bar = r, Rect{r.X, r.Y + r.H, r.W, 1}
		}
	})
	if n := l.preview; n != nil {
		// A host resize during a drag: the preview is clamped again at the
		// new size; cancelling still returns to the committed ratios.
		free, origin := n.rect.H-1, n.rect.Y
		if n.vertical {
			free, origin = n.rect.W-1, n.rect.X
		}
		l.Preview(Split{n}, origin+int(math.Round(l.previewRatio*float64(free))))
	}
}

// place lays the unzoomed tree out over the tab area above the bottom bar
// row, giving override the ratio ratio instead of its own.
func (l *Layout) place(override *node, ratio float64, set func(*node, Rect)) {
	var walk func(*node, Rect)
	walk = func(n *node, r Rect) {
		set(n, r)
		if n.pane != nil {
			return
		}
		share := n.ratio
		if n == override {
			share = ratio
		}
		if n.vertical {
			free := r.W - 1
			aw := min(max(int(math.Round(share*float64(free))), 1), max(free-1, 1))
			walk(n.a, Rect{r.X, r.Y, aw, r.H})
			walk(n.b, Rect{r.X + aw + 1, r.Y, free - aw, r.H})
			return
		}
		free := r.H - 1
		ah := min(max(int(math.Round(share*float64(free))), 1), max(free-1, 1))
		walk(n.a, Rect{r.X, r.Y, r.W, ah})
		walk(n.b, Rect{r.X, r.Y + ah + 1, r.W, free - ah})
	}
	walk(l.root, Rect{0, 0, l.w, max(l.h-1, 0)})
}

// Size is the tab area the layout was last laid out over.
func (l *Layout) Size() (w, h int) { return l.w, l.h }

// Dir is an arrow direction.
type Dir int

const (
	Left Dir = iota
	Right
	Up
	Down
)

// MoveFocus focuses the pane across the focused pane's border in d: the
// one sharing the most of that border, then the nearest to the focused
// pane's top or left edge. At the tab's edge, and while zoomed, nothing
// moves (no wrap).
func (l *Layout) MoveFocus(d Dir) bool {
	if l.zoomed != nil || l.focus == nil {
		return false
	}
	cur := l.focus.rect
	var best *Pane
	bestOverlap, bestDist := 0, math.MaxInt
	for _, p := range l.Panes() {
		r := p.rect
		var adjacent bool
		var overlap, dist int
		switch d {
		case Left:
			adjacent, overlap, dist = r.X+r.W+1 == cur.X, span(r.Y, r.H, cur.Y, cur.H), abs(r.Y-cur.Y)
		case Right:
			adjacent, overlap, dist = cur.X+cur.W+1 == r.X, span(r.Y, r.H, cur.Y, cur.H), abs(r.Y-cur.Y)
		case Up:
			adjacent, overlap, dist = r.Y+r.H+1 == cur.Y, span(r.X, r.W, cur.X, cur.W), abs(r.X-cur.X)
		case Down:
			adjacent, overlap, dist = cur.Y+cur.H+1 == r.Y, span(r.X, r.W, cur.X, cur.W), abs(r.X-cur.X)
		}
		if !adjacent || overlap <= 0 {
			continue
		}
		if overlap > bestOverlap || overlap == bestOverlap && dist < bestDist {
			best, bestOverlap, bestDist = p, overlap, dist
		}
	}
	if best == nil {
		return false
	}
	return l.SetFocus(best)
}

// FocusNext focuses the pane after the focused one in tree order, wrapping
// to the first; while zoomed, nothing moves.
func (l *Layout) FocusNext() bool {
	ps := l.Panes()
	if l.zoomed != nil || len(ps) < 2 {
		return false
	}
	i := slices.Index(ps, l.focus)
	return l.SetFocus(ps[(i+1)%len(ps)])
}

// SwapFocused exchanges the focused pane with the previous (or the next)
// pane in tree order, wrapping at the ends. The panes trade places, not
// sizes: each takes the other's cell rectangle, and focus stays on the
// pane that moved. While zoomed, nothing moves.
func (l *Layout) SwapFocused(next bool) bool {
	ps := l.Panes()
	if l.zoomed != nil || len(ps) < 2 {
		return false
	}
	i := slices.Index(ps, l.focus)
	j := (i + len(ps) - 1) % len(ps)
	if next {
		j = (i + 1) % len(ps)
	}
	a, b := l.leaf(ps[i]), l.leaf(ps[j])
	a.pane, b.pane = b.pane, a.pane
	l.preview = nil
	l.gen++
	l.Resize(l.w, l.h)
	return true
}

// ResizeFocused moves the border on the focused pane's d side by up to
// cells cells: the nearest split of that axis above the focused pane. The
// border goes as far as it can toward cells while a trial layout of both
// sides, each with its own ratios, keeps every pane at the minimum content
// size (the recursive minimum a drag keeps); when it cannot move a cell,
// nothing changes.
func (l *Layout) ResizeFocused(d Dir, cells int) bool {
	if l.zoomed != nil || l.focus == nil || cells < 1 {
		return false
	}
	vertical := d == Left || d == Right
	n := l.leaf(l.focus)
	for n.parent != nil && n.parent.vertical != vertical {
		n = n.parent
	}
	p := n.parent
	if p == nil {
		return false
	}
	// The border lies between p.a and p.b: right/down moves it toward b.
	r, free, a := p.rect, p.rect.H-1, p.a.rect.H
	if vertical {
		free, a = r.W-1, p.a.rect.W
	}
	step := 1
	if d == Left || d == Up {
		step = -1
	}
	for c := cells; c > 0; c-- {
		to := a + step*c
		if to < 1 || to >= free {
			continue
		}
		ra, rb := Rect{r.X, r.Y, r.W, to}, Rect{r.X, r.Y + to + 1, r.W, free - to}
		if vertical {
			ra, rb = Rect{r.X, r.Y, to, r.H}, Rect{r.X + to + 1, r.Y, free - to, r.H}
		}
		if fits(p.a, ra) && fits(p.b, rb) {
			p.ratio = float64(to) / float64(free)
			l.preview = nil
			l.gen++
			l.Resize(l.w, l.h)
			return true
		}
	}
	return false
}

// At maps a tab-area cell to its pane and pane-local cell; a border cell
// (or a cell outside the area) maps to no pane.
func (l *Layout) At(x, y int) (*Pane, int, int) {
	for _, p := range l.Visible() {
		if p.rect.contains(x, y) {
			return p, x - p.rect.X, y - p.rect.Y
		}
	}
	return nil, 0, 0
}

// BarPart is what a bar cell does when pressed.
type BarPart int

const (
	BarNone       BarPart = iota // the band between the parts
	BarTitle                     // the marker, mark and title: focuses the pane
	BarZoom                      // ⤢: toggles zoom
	BarClose                     // ✕: detaches the pane
	BarSplitRight                // no longer on a bar; kept for its handlers
	BarSplitBelow                // no longer on a bar; kept for its handlers
)

// barSeg is one run of a bar: its part, first column and text.
type barSeg struct {
	part BarPart
	x    int
	text string
}

// barButton is one button of a bar: its part and its text.
type barButton struct {
	part BarPart
	text string
}

// barButtons are the button sets a bar tries, widest first: the zoom
// button (not on an empty pane's) with ✕, then ✕ alone, then none.
func barButtons(p *Pane) [][]barButton {
	close := barButton{BarClose, "✕"}
	var sets [][]barButton
	if !p.Empty() {
		sets = append(sets, []barButton{{BarZoom, "⤢"}, close})
	}
	return append(sets, []barButton{close}, nil)
}

// barSegs lays a pane's bar out over its bar rectangle, left to right: a
// band cell, the focus marker ▸, the attention mark ●, the title with its
// state word, the band, then the buttons, ✕ last. A button's segment is
// its glyph with a space on each side; one band cell follows each button.
// As columns run out the title truncates to …, then goes; then the zoom
// button; then ✕; at two columns or fewer the bar is a plain band.
// Everything not covered by a segment is band.
func (l *Layout) barSegs(p *Pane, r Rect) []barSeg {
	if r.W <= 2 {
		return nil
	}
	method := l.method()
	focused := p == l.focus
	lead := []barSeg{{BarNone, 0, " "}}
	if focused {
		lead = append(lead, barSeg{BarTitle, 1, "▸"})
	}
	title := p.Title()
	switch {
	case p.Empty():
		title = "empty"
	case l.zoomed == p:
		title += " · zoomed"
	case p.Exited:
		title += " · exited"
	}
	for i, buttons := range barButtons(p) {
		right := 0 // " ⤢  ✕  ": each button padded, then one band cell
		for _, b := range buttons {
			right += method.StringWidth(b.text) + 3
		}
		segs := append([]barSeg(nil), lead...)
		x := len(lead) + 1 // after the lead and a space
		if p.Mark && !focused && r.W-x-1-right >= 1 {
			segs = append(segs, barSeg{BarTitle, x, "●"})
			x += 2
		}
		// The title goes before any button: it needs a space after it and at
		// least one band cell, and only the full button set keeps it.
		if room := r.W - x - right - 2; i == 0 && room >= 2 && title != "" {
			t := title
			if method.StringWidth(t) > room {
				t = method.Truncate(t, room, "…")
			}
			segs = append(segs, barSeg{BarTitle, x, t})
		}
		if r.W-len(lead)-right < 1 {
			continue // not even one band cell fits: drop a button
		}
		bx := r.W - right
		for _, b := range buttons {
			text := " " + b.text + " "
			segs = append(segs, barSeg{b.part, bx, text})
			bx += method.StringWidth(text) + 1
		}
		return segs
	}
	return nil
}

func (l *Layout) method() ansi.Method {
	if l.Method == 0 {
		return ansi.GraphemeWidth
	}
	return l.Method
}

// BarAt maps a tab-area cell on a pane's bar to that pane and the part
// under it; any other cell maps to no pane.
func (l *Layout) BarAt(x, y int) (*Pane, BarPart) {
	if !l.Bars() {
		return nil, BarNone
	}
	method := l.method()
	for _, p := range l.Visible() {
		r := p.bar
		if !r.contains(x, y) {
			continue
		}
		for _, s := range l.barSegs(p, r) {
			if s.part != BarNone && x-r.X >= s.x && x-r.X < s.x+method.StringWidth(s.text) {
				return p, s.part
			}
		}
		return p, BarNone
	}
	return nil, BarNone
}

// Split is a captured split of a layout: a border that a drag moves.
type Split struct{ n *node }

// Vertical reports whether the split's border is a column.
func (s Split) Vertical() bool { return s.n != nil && s.n.vertical }

// Valid reports whether the split is still in the layout with its axis.
func (l *Layout) Valid(s Split, vertical bool) bool {
	if s.n == nil || s.n.pane != nil || s.n.vertical != vertical {
		return false
	}
	for n := s.n; n != nil; n = n.parent {
		if n == l.root {
			return true
		}
	}
	return false
}

// BorderAt maps a tab-area cell on a vertical border, or on a junction where
// one meets a bar row, to the vertical split it divides.
func (l *Layout) BorderAt(x, y int) (Split, bool) {
	if !l.Divided() {
		return Split{}, false
	}
	var found *node
	var walk func(*node)
	walk = func(n *node) {
		if n == nil || n.pane != nil || found != nil {
			return
		}
		if r := n.rect; n.vertical && x == n.a.rect.X+n.a.rect.W && y >= r.Y && y <= r.Y+r.H {
			found = n
			return
		}
		walk(n.a)
		walk(n.b)
	}
	walk(l.root)
	return Split{found}, found != nil
}

// SplitBelow is the horizontal split whose border is p's bar: the bar of a
// pane with another pane below it. A bar on the tab's bottom edge has none.
func (l *Layout) SplitBelow(p *Pane) (Split, bool) {
	if !l.Divided() {
		return Split{}, false
	}
	n := l.leaf(p)
	for ; n != nil && n.parent != nil; n = n.parent {
		if par := n.parent; !par.vertical && par.a == n && p.rect.Y+p.rect.H == n.rect.Y+n.rect.H {
			return Split{par}, true
		}
	}
	return Split{}, false
}

// BorderPos is the split's border position in the tab area: its column for
// a vertical split, its row for a horizontal one, as committed.
func (l *Layout) BorderPos(s Split) int {
	if s.n.vertical {
		return s.n.a.rect.X + s.n.a.rect.W
	}
	return s.n.a.rect.Y + s.n.a.rect.H
}

// Preview moves a split's border to pos for display only, to the position
// nearest pos at which a trial layout of both sides, each keeping its own
// ratios, leaves every pane at least the minimum content size (a sum of
// minima is not enough: a skewed inner ratio can still starve one pane).
// With no such position the preview is dropped. Committed rectangles and
// session sizes do not change; the shown border position is returned.
func (l *Layout) Preview(s Split, pos int) int {
	n := s.n
	r, free, origin := n.rect, n.rect.H-1, n.rect.Y
	if n.vertical {
		free, origin = r.W-1, r.X
	}
	best := -1
	for a := 1; a < free; a++ {
		if best >= 0 && abs(a-(pos-origin)) >= abs(best-(pos-origin)) {
			continue
		}
		ra, rb := Rect{r.X, r.Y, r.W, a}, Rect{r.X, r.Y + a + 1, r.W, free - a}
		if n.vertical {
			ra, rb = Rect{r.X, r.Y, a, r.H}, Rect{r.X + a + 1, r.Y, free - a, r.H}
		}
		if fits(n.a, ra) && fits(n.b, rb) {
			best = a
		}
	}
	if best < 0 {
		l.preview = nil // no position keeps every pane: nothing to preview
		return l.BorderPos(s)
	}
	l.preview, l.previewRatio = n, float64(best)/float64(free)
	return origin + best
}

// fits lays a subtree out over r with its own ratios, as place does, and
// reports whether every pane keeps the minimum content size.
func fits(n *node, r Rect) bool {
	if n.pane != nil {
		return r.W >= MinCols && r.H >= MinRows
	}
	if n.vertical {
		free := r.W - 1
		aw := min(max(int(math.Round(n.ratio*float64(free))), 1), max(free-1, 1))
		return fits(n.a, Rect{r.X, r.Y, aw, r.H}) && fits(n.b, Rect{r.X + aw + 1, r.Y, free - aw, r.H})
	}
	free := r.H - 1
	ah := min(max(int(math.Round(n.ratio*float64(free))), 1), max(free-1, 1))
	return fits(n.a, Rect{r.X, r.Y, r.W, ah}) && fits(n.b, Rect{r.X, r.Y + ah + 1, r.W, free - ah})
}

// CommitPreview makes the previewed ratio the split's ratio and lays the
// tree out; it reports whether there was a preview.
func (l *Layout) CommitPreview() bool {
	if l.preview == nil {
		return false
	}
	l.preview.ratio = l.previewRatio
	l.preview = nil
	l.gen++
	l.Resize(l.w, l.h)
	return true
}

// CancelPreview drops the preview; the committed ratios stand.
func (l *Layout) CancelPreview() { l.preview = nil }

// previewGeometry is every visible pane's content and bar rectangle as the
// composer draws them: the preview's, while a border is dragged.
func (l *Layout) previewGeometry() (map[*Pane][2]Rect, *node) {
	if l.preview == nil || l.zoomed != nil {
		return nil, nil
	}
	geo := make(map[*Pane][2]Rect)
	l.place(l.preview, l.previewRatio, func(n *node, r Rect) {
		if n.pane != nil {
			geo[n.pane] = [2]Rect{r, {r.X, r.Y + r.H, r.W, 1}}
		}
	})
	return geo, l.preview
}

func span(a, an, b, bn int) int { return min(a+an, b+bn) - max(a, b) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

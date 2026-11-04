package tui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
)

// The pane menu is the armed prefix's picture: a popup at the focused
// pane's top-left listing the key table. It has no highlight: a row runs
// when its key is pressed, exactly as the key table dispatches, or when it
// is clicked; Esc closes. A refusal shows in its footer for at least
// noticeTime and the menu stays open. A notice whose popup closed without
// an explicit dismissal (host blur, a tab switch, a paste) moves to an
// inert notice popup at the same spot, which only displays its text for the
// remaining time; any key or press dismisses it.

// noticeTime is how long a popup notice shows at least.
const noticeTime = 3 * time.Second

// menuHint is the menu footer without a notice.
const menuHint = "press a key or click · Esc closes"

// overlayMin is the smallest overlay a pane holds before the overlay is
// centered over the tab area instead.
const overlayMinW, overlayMinH = 20, 6

// menuRow is one menu row: a label and its keys. An action row runs its
// single binding; an information row (bind < 0) only shows keys that act;
// a separator (label "") groups rows.
type menuRow struct {
	label, keys string
	bind        int
}

// menuRows is the menu, grouped as PROPOSAL (b), each action row mapped to
// exactly one paneKeys binding (R1).
func menuRows(prefix string) []menuRow {
	bind := func(text string) int {
		for i, b := range paneKeys {
			if b.text == text {
				return i
			}
		}
		panic("pane menu: no binding " + text)
	}
	row := func(label, text string) menuRow { return menuRow{label, text, bind(text)} }
	info := func(label, keys string) menuRow { return menuRow{label, keys, -1} }
	sep := menuRow{bind: -1}
	return []menuRow{
		row("Split right", "|"),
		row("Split below", "-"),
		row("Zoom", "z"),
		row("Spread evenly", "="),
		row("Swap with previous", "{"),
		row("Swap with next", "}"),
		row("Break out", "!"),
		row("Bring a tab in", "j"),
		row("Detach pane", "x"),
		sep,
		info("Focus", "←↑↓→"),
		row("Focus next", "o"),
		info("Resize border", "^arrows Alt+arrows"),
		sep,
		row("New tab", "c"),
		row("Next tab", "n"),
		row("Previous tab", "p"),
		row("Pick a tab", "w"),
		info("Tab by number", "1…9 0"),
		row("Detach whole tab", "d"),
		sep,
		info("Send "+prefix, prefix),
	}
}

// menuState is the open menu's own state, kept on the armed prefix, with
// the origin it was last placed at, where a notice it hands on stays.
type menuState struct {
	scroll      int
	notice      string
	noticeUntil time.Time
	x, y        int
}

// noticePopup is the inert notice popup: text shown until a deadline, at
// the origin of the popup it came from (or of the focused pane's).
type noticePopup struct {
	text  string
	until time.Time
	x, y  int
}

// liveNotice is a notice still within its time.
func liveNotice(text string, until time.Time) string {
	if text != "" && clock().Before(until) {
		return text
	}
	return ""
}

// paneNotice shows a Ctrl+Q refusal in the popup that produced it: the
// close confirmation, the tab picker, the open menu, or the menu whose key
// is acting, which then stays open. With no popup it opens the inert notice
// popup at the focused pane. Nothing goes to the palette line or a bar.
func (m *model) paneNotice(text string) {
	until := clock().Add(noticeTime)
	if m.prefix == nil && m.acting != nil && m.picker == nil && m.confirm == nil {
		m.prefix = m.acting // the refused action keeps its menu open
	}
	switch {
	case m.confirm != nil:
		m.confirm.notice, m.confirm.noticeUntil = text, until
	case m.picker != nil:
		m.picker.notice, m.picker.noticeUntil = text, until
	case m.prefix != nil:
		m.prefix.menu.notice, m.prefix.menu.noticeUntil = text, until
	default:
		x, y, _, _ := m.placeOverlay(nil, noticeWidth(text), 3, 0)
		m.notice = &noticePopup{text: text, until: until, x: x, y: y}
	}
	m.noticeSeq++
}

// ownerClosed moves the notice of a popup whose owner became invalid to the
// inert notice popup at that popup's last origin, for its remaining time
// (R3).
func (m *model) ownerClosed(text string, until time.Time, x, y int) {
	if text = liveNotice(text, until); text != "" {
		m.notice = &noticePopup{text: text, until: until, x: x, y: y}
	}
}

// keepNotice moves a closing menu's live notice to the inert popup: host
// blur, a tab switch or a paste close the menu but never erase its notice.
func (m *model) keepNotice(p *prefixState) {
	if p == nil {
		return
	}
	if text := liveNotice(p.menu.notice, p.menu.noticeUntil); text != "" {
		m.notice = &noticePopup{text: text, until: p.menu.noticeUntil, x: p.menu.x, y: p.menu.y}
	}
}

// activeNotice is the inert notice popup while its time lasts.
func (m *model) activeNotice() *noticePopup {
	if n := m.notice; n != nil && liveNotice(n.text, n.until) != "" {
		return n
	}
	m.notice = nil
	return nil
}

// bindingWhy is why a binding cannot act now, "" when it can. A pressed
// key whose row is unavailable refuses with this text; the row is dimmed.
func (m *model) bindingWhy(b paneBinding) string {
	t := m.tabs.Current()
	var l *panes.Layout
	if t != nil {
		l = m.laidOut(t)
	}
	undivided := func(what string) string {
		switch {
		case l == nil:
			return "no tab yet"
		case l.Zoomed() != nil:
			return "zoomed: press z first"
		case !l.Divided():
			return "one pane: nothing to " + what
		}
		return ""
	}
	switch b.text {
	case "|", "-":
		switch {
		case l == nil:
			return "no pane to split"
		case l.Empty() != nil:
			return "fill or close the empty pane first"
		}
		if err := l.CanSplit(b.text == "|"); err != nil {
			return err.Error()
		}
	case "z":
		if l == nil || !l.Bars() {
			return "one pane: nothing to zoom"
		}
	case "=":
		return undivided("spread")
	case "{", "}":
		return undivided("swap")
	case "!", "x", "d", "o":
		if t == nil {
			return "no tab yet"
		}
	case "j":
		switch {
		case t == nil:
			return "no tab to bring a tab into"
		case len(m.tabs.Tabs) < 2:
			return "no other tab to bring in"
		}
	case "w":
		switch {
		case len(m.tabs.Tabs) == 0:
			return "no tabs to pick"
		case len(m.tabs.Tabs) < 2:
			return "no other tab to pick"
		}
	case "c":
		if m.console.Busy() {
			return "a palette command is still running — wait for it"
		}
	}
	return ""
}

// runBinding is a menu key or click: an unavailable binding refuses and
// the menu stays; otherwise the menu closes and the binding acts. A refusal
// while it acts reopens the menu with that notice (paneNotice).
func (m *model) runBinding(b paneBinding, k tea.Key) tea.Cmd {
	p := m.prefix
	seq := m.noticeSeq
	if why := m.bindingWhy(b); why != "" {
		m.paneNotice(why)
		return prefixTick(noticeTime)
	}
	m.prefix, m.acting = nil, p
	cmd := b.act(m, k)
	m.acting = nil
	if m.noticeSeq != seq {
		return tea.Batch(cmd, prefixTick(noticeTime))
	}
	return cmd
}

// bindingKey is the key a click on a binding's row stands for.
func bindingKey(b paneBinding) tea.Key {
	if b.text != "" {
		return tea.Key{Text: b.text, Code: []rune(b.text)[0]}
	}
	return tea.Key{Code: b.code, Mod: b.mod}
}

// overlay is the one viewport every modal pane popup renders in (R2): its
// origin and size in tab-area cells, and its frame.
type overlay struct {
	x, y, w, h int
	frame      popup
	scroll     int
	// rows maps each body line to its menu row, for hit tests.
	rows []menuRow
}

// tabArea is the visible tab area's size: the rows below the strip, less
// the palette rows pushing it down.
func (m *model) tabArea() (w, h int) {
	h = m.viewRows()
	if m.focusPalette && !m.standalone {
		h -= m.overlayContentRows()
	}
	return max(m.width, 0), max(h, 0)
}

// placeOverlay fits a popup of the wanted size at the anchor pane's
// top-left, one cell in (the focused pane when anchor is nil), within the
// part of the pane that is on screen (an open palette pushes the tab area
// down and hides its last rows; the pane keeps its size). A visible part
// that cannot hold 20×6 (or minW columns, for a popup whose buttons need
// them), or no pane, centers it over the visible tab area, clamped to it.
// Rendering, the footer, scrolling and hit tests all use this one result.
func (m *model) placeOverlay(anchor *panes.Pane, wantW, wantH, minW int) (x, y, w, h int) {
	aw, ah := m.tabArea()
	if t := m.tabs.Current(); t != nil {
		m.laidOut(t)
		p := anchor
		if p == nil || !hasPane(t, p) {
			p = t.Focused()
		}
		if p != nil {
			r := p.Rect()
			visW, visH := min(r.W, aw-r.X), min(r.H, ah-r.Y)
			if visW-2 >= max(overlayMinW, minW) && visH-2 >= overlayMinH {
				w, h = min(wantW, visW-2), min(wantH, visH-2)
				return r.X + 1, r.Y + 1, w, h
			}
		}
	}
	w, h = min(wantW, aw), min(wantH, ah)
	return max((aw-w)/2, 0), max((ah-h)/2, 0), w, h
}

// fitOverlay places a popup whose height depends on its width: it fixes the
// width first (as placeOverlay would grant it), then asks for the rows the
// popup needs there (rows, given the body width), clamped as placeOverlay
// clamps. A wrapped footer thus never takes rows the body could have had.
func (m *model) fitOverlay(anchor *panes.Pane, wantW, minW int, rows func(w int) int) (x, y, w, h int) {
	_, _, w, _ = m.placeOverlay(anchor, wantW, math.MaxInt32, minW)
	return m.placeOverlay(anchor, wantW, rows(w), minW)
}

// noticeWidth is the width an inert notice wants: its text and the frame.
func noticeWidth(text string) int { return max(ansi.StringWidth(text), 16) + 4 }

// menuLines are the menu's body lines at inner width: the label, then its
// keys at the right; separators are a dim rule.
func (m *model) menuLines(rows []menuRow, inner int) (lines []string, dim []bool) {
	for _, r := range rows {
		if r.label == "" {
			lines = append(lines, strings.Repeat("─", max(inner, 0)))
			dim = append(dim, true)
			continue
		}
		keys := r.keys
		label := ansi.Truncate(r.label, max(inner-ansi.StringWidth(keys)-1, 0), "…")
		gap := max(inner-ansi.StringWidth(label)-ansi.StringWidth(keys), 1)
		lines = append(lines, ansi.Truncate(label+strings.Repeat(" ", gap)+keys, max(inner, 0), "…"))
		dim = append(dim, r.bind < 0 || m.bindingWhy(paneKeys[r.bind]) != "")
	}
	return lines, dim
}

// menuOverlay is the open menu in its viewport: width the widest row plus
// the frame, rows as many as fit; the footer is pinned and the body
// scrolls by the rows the popup fits.
func (m *model) menuOverlay() *overlay {
	p := m.prefix
	if p == nil {
		return nil
	}
	rows := menuRows(m.panePrefix().String())
	widest := 0
	for _, r := range rows {
		widest = max(widest, ansi.StringWidth(r.label)+2+ansi.StringWidth(r.keys))
	}
	footer := menuHint
	if n := liveNotice(p.menu.notice, p.menu.noticeUntil); n != "" {
		footer = n
	}
	widest = max(widest, ansi.StringWidth(menuHint))
	frame := popup{Title: "panes", Footer: footer}
	x, y, w, h := m.fitOverlay(nil, widest+4, 0, func(w int) int { return frame.rowsFor(w, len(rows)) })
	p.menu.x, p.menu.y = x, y
	inner, _, capacity := frame.layout(w, h)
	lines, dim := m.menuLines(rows, inner)
	last := max(len(lines)-capacity, 0)
	p.menu.scroll = min(max(p.menu.scroll, 0), last)
	o := &overlay{x: x, y: y, w: w, h: h, scroll: p.menu.scroll, rows: rows}
	frame.Lines, frame.Dimmed = lines[p.menu.scroll:], dim[p.menu.scroll:]
	o.frame = frame
	return o
}

// noticeOverlay is the inert notice popup at its kept origin, as wide as
// its text wants within the visible tab area (moved left only when fewer
// than 20 columns remain right of it) and as tall as its wrapped text,
// clamped to that area.
func (m *model) noticeOverlay() *overlay {
	n := m.activeNotice()
	if n == nil {
		return nil
	}
	aw, ah := m.tabArea()
	want := noticeWidth(n.text)
	x := min(max(n.x, 0), aw)
	w := min(want, aw-x)
	if w < min(overlayMinW, want) {
		w = min(want, aw)
		x = max(aw-w, 0)
	}
	frame := popup{Title: "panes", Footer: n.text}
	rows := len(strings.Split(ansi.Hardwrap(n.text, max(w-4, 1), true), "\n")) + 2
	h := min(rows, ah)
	y := min(max(n.y, 0), max(ah-h, 0))
	return &overlay{x: x, y: y, w: w, h: h, frame: frame}
}

// activeOverlay is the modal pane popup on screen, if any: the close
// confirmation, the tab picker, the menu or the inert notice.
func (m *model) activeOverlay() *overlay {
	if o := m.confirmOverlay(); o != nil {
		return o
	}
	if o := m.pickerOverlay(); o != nil {
		return o
	}
	return m.menuNoticeOverlay()
}

// menuNoticeOverlay is the menu or the inert notice, the overlays
// overlayMouse handles; the picker and confirmation keep their own clicks.
func (m *model) menuNoticeOverlay() *overlay {
	if o := m.menuOverlay(); o != nil {
		return o
	}
	return m.noticeOverlay()
}

// overlayPoint is a screen cell in an overlay's own cells.
func (m *model) overlayPoint(o *overlay, mo tea.Mouse) (x, y int) {
	mo = m.mouseHit(mo)
	return mo.X - o.x, mo.Y - m.tabTop() - o.y
}

// applyOverlay draws the active overlay over the tab-area rows, after
// whichever base path drew them; each row outside it keeps its cells and
// every run is closed with a reset at the overlay's edges.
func (m *model) applyOverlay(base []string) {
	o := m.activeOverlay()
	if o == nil || o.w <= 0 || o.h <= 0 {
		return
	}
	popupRows := o.frame.render(o.w, o.h, m.surfaceBg())
	for i, row := range popupRows {
		if y := o.y + i; y >= 0 && y < len(base) {
			base[y] = spliceRow(base[y], row, o.x, o.w, m.width)
		}
	}
}

// spliceRow replaces width cells of a styled row at x with over, keeping
// the cells on each side; a wide cell cut by an edge becomes a space.
func spliceRow(row, over string, x, width, total int) string {
	var cells uv.Line
	if parsed := uv.NewStyledString(row).Lines(ansi.GraphemeWidth); len(parsed) > 0 {
		cells = parsed[0]
	}
	space := uv.Cell{Content: " ", Width: 1}
	line := make(uv.Line, 0, total)
	for _, c := range cells {
		if c.Width == 0 && c.Content == "" {
			continue
		}
		line = append(line, c)
		for i := 1; i < c.Width; i++ {
			line = append(line, uv.Cell{Width: 0})
		}
	}
	for len(line) < total {
		line = append(line, space)
	}
	line = line[:total]
	end := min(x+width, total)
	if x > 0 && x < total {
		for i := x - 1; i >= 0; i-- { // a wide cell crossing the left edge
			if line[i].Width > 0 {
				if i+line[i].Width > x {
					for j := i; j < x; j++ {
						line[j] = space
					}
				}
				break
			}
		}
	}
	if end < total && line[end].Width == 0 { // its other half past the right edge
		for j := end; j < total && line[j].Width == 0; j++ {
			line[j] = space
		}
	}
	var b strings.Builder
	b.WriteString(line[:min(x, total)].Render())
	b.WriteString(ansi.ResetStyle)
	b.WriteString(over)
	b.WriteString(ansi.ResetStyle)
	if end < total {
		b.WriteString(line[end:].Render())
		b.WriteString(ansi.ResetStyle)
	}
	return b.String()
}

// overlayHit maps a screen cell to the active overlay: whether it is inside,
// and the menu row under it, if any.
func (m *model) overlayHit(o *overlay, mo tea.Mouse) (inside bool, row *menuRow) {
	x, y := m.overlayPoint(o, mo)
	if x < 0 || y < 0 || x >= o.w || y >= o.h {
		return false, nil
	}
	_, top, capacity := o.frame.layout(o.w, o.h)
	if i := y - top; o.rows != nil && i >= 0 && i < capacity && i < len(o.frame.Lines) {
		return true, &o.rows[o.scroll+i]
	}
	return true, nil
}

// overlayMouse takes every mouse event while a modal pane popup is open,
// before the prefix and chrome/child routing: a press on a menu row runs
// it, a press elsewhere closes the menu (an explicit dismissal, with its
// notice), any press dismisses the inert notice; the press is consumed
// through its release; the wheel scrolls the menu.
func (m *model) overlayMouse(msg tea.MouseMsg, mo tea.Mouse) (tea.Cmd, bool) {
	o := m.menuNoticeOverlay()
	if o == nil {
		return nil, false
	}
	switch msg.(type) {
	case tea.MouseClickMsg:
		if m.mouseOwners == nil {
			m.mouseOwners = make(map[tea.MouseButton]mouseOwner)
		}
		m.mouseOwners[mo.Button] = mouseOwner{}
		if m.prefix == nil {
			m.notice = nil
			return nil, true
		}
		inside, row := m.overlayHit(o, mo)
		switch {
		case !inside:
			m.prefix = nil
		case row != nil && row.bind >= 0 && mo.Button == tea.MouseLeft:
			b := paneKeys[row.bind]
			return m.runBinding(b, bindingKey(b)), true
		}
		return nil, true
	case tea.MouseWheelMsg:
		if m.prefix != nil {
			switch mo.Button {
			case tea.MouseWheelUp:
				m.prefix.menu.scroll--
			case tea.MouseWheelDown:
				m.prefix.menu.scroll++
			}
		}
		return nil, true
	}
	return nil, false // motion and releases finish at their owners
}

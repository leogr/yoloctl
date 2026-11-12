package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// tabPicker is prefix w: the tabs as a list in the pane overlay at the
// focused pane, to switch to one; prefix j opens it in join mode, to bring
// one in. Its highlight binds to a *Tab, never an index: when tabs close,
// open, renumber or retitle while it is open, the list follows and the
// highlight stays on its tab, or moves to the row where it was when that tab
// is gone. Opening and closing leave the palette draft, the suggestions and
// a busy command's output as they were; focus returns where it was.
type tabPicker struct {
	highlight     *tabs.Tab
	row           int       // the highlight's last row
	scroll        int       // the first row shown
	notice        string    // a refusal, shown in the footer
	noticeUntil   time.Time // at least noticeTime, whatever keys follow
	x, y          int       // where it was last placed: its notice stays there
	returnPalette bool
	// Join mode captures its destination at open: the tab, and its empty
	// pane or else its focused pane. If either changes, the picker cancels.
	join   bool
	dest   *tabs.Tab
	target *panes.Pane
}

func (m *model) openPicker() {
	if len(m.tabs.Tabs) == 0 {
		m.paneNotice("no tabs to pick")
		return
	}
	m.picker = &tabPicker{highlight: m.tabs.Current(), row: m.tabs.Active, returnPalette: m.focusPalette}
	m.scrollPicker()
}

// openJoinPicker is prefix j: the picker in join mode, for the current tab.
func (m *model) openJoinPicker() {
	t := m.tabs.Current()
	if t == nil {
		m.paneNotice("no tab to bring a tab into")
		return
	}
	m.laidOut(t)
	m.openPicker()
	p := m.picker
	p.join, p.dest, p.target = true, t, joinTarget(t)
}

// closePicker closes the list; focus returns where it was, or to the
// palette when no tab is left.
func (m *model) closePicker() {
	if p := m.picker; p != nil {
		m.focusPalette = p.returnPalette || m.tabs.Current() == nil
		m.picker = nil
	}
}

// checkPicker follows tab changes: a highlighted tab that is gone hands the
// highlight to the tab now on its row; with no tab left the list closes.
func (m *model) checkPicker() {
	p := m.picker
	if p == nil {
		return
	}
	if p.join && (m.tabs.Current() != p.dest || joinTarget(p.dest) != p.target) {
		// The destination changed (a tab switch, focus moved, the empty pane
		// came or went): the join cancels rather than land elsewhere, and
		// says so in the inert notice popup.
		m.closePicker()
		m.ownerClosed("the join was cancelled: its destination changed", clock().Add(noticeTime), p.x, p.y)
		return
	}
	n := len(m.tabs.Tabs)
	if n == 0 {
		m.closePicker()
		m.ownerClosed(p.notice, p.noticeUntil, p.x, p.y)
		return
	}
	if i := m.tabs.Index(p.highlight); i >= 0 {
		p.row = i
	} else {
		p.row = min(p.row, n-1)
		p.highlight = m.tabs.Tabs[p.row]
	}
	m.scrollPicker()
}

// pickerFrame is the picker's popup without its rows: the footer carries a
// refusal while there is one.
func (m *model) pickerFrame() popup {
	footer, title := pickerFooter, "switch to a tab"
	if p := m.picker; p.join {
		footer, title = joinFooter, "bring a tab in below the focused pane"
		if p.target.Empty() {
			title = "bring a tab into the empty pane"
		}
	}
	if n := liveNotice(m.picker.notice, m.picker.noticeUntil); n != "" {
		footer = m.noticeLine(prettylog.KindWarn, n) + " · " + footer
	}
	frame := popup{Title: title, Footer: footer}
	if liveNotice(m.picker.notice, m.picker.noticeUntil) != "" {
		frame.FooterStyle = m.noticeFooter(prettylog.KindWarn)
	}
	return frame
}

// pickerLabels are every tab's row text, and whether the join would refuse
// it, greyed with its reason.
func (m *model) pickerLabels() (labels []string, dimmed []bool) {
	p := m.picker
	for i, t := range m.tabs.Tabs {
		label := circledNumber(i+1) + " " + strings.Join(strings.Fields(ansi.Strip(t.Title())), " ")
		if t.Badge {
			label += " •"
		}
		why := ""
		if p.join {
			why = m.canJoin(t, p.dest, p.target, panes.Down)
		}
		switch {
		case why != "":
			label += " · " + why
		case i == m.tabs.Active:
			label += " · current"
		}
		labels = append(labels, label)
		dimmed = append(dimmed, why != "")
	}
	return labels, dimmed
}

// pickerOverlay is the open picker in the pane overlay, at the focused
// pane: wide enough for its rows and footer, as tall as its rows and its
// footer wrapped at the granted width need. The scroll keeps the highlight
// among the rows shown, so drawing, keys and hit tests agree.
func (m *model) pickerOverlay() *overlay {
	p := m.picker
	if p == nil {
		return nil
	}
	frame := m.pickerFrame()
	labels, dimmed := m.pickerLabels()
	want := ansi.StringWidth(frame.Footer)
	for _, l := range labels {
		want = max(want, ansi.StringWidth(l))
	}
	x, y, w, h := m.fitOverlay(nil, want+4, 0, func(w int) int { return frame.rowsFor(w, len(labels)) })
	p.x, p.y = x, y
	inner, _, shown := frame.layout(w, h)
	if shown >= 1 {
		p.scroll = min(max(p.scroll, p.row-shown+1), p.row)
		p.scroll = max(min(p.scroll, len(labels)-shown), 0)
	} else {
		p.scroll = p.row // no room for a row: nothing else is shown either
	}
	end := min(p.scroll+shown, len(labels))
	for i := p.scroll; i < end; i++ {
		frame.Lines = append(frame.Lines, ansi.Truncate(labels[i], inner, "…"))
	}
	if p.scroll < end {
		frame.Dimmed = dimmed[p.scroll:end]
	}
	frame.Highlight = p.row - p.scroll + 1
	return &overlay{x: x, y: y, w: w, h: h, frame: frame, scroll: p.scroll}
}

// pickerLayout is where the tab rows go in the overlay, from the popup's
// own layout: the wrap width, the row of the first tab, and how many tabs
// fit beside the wrapped footer.
func (m *model) pickerLayout() (inner, top, rows int) {
	o := m.pickerOverlay()
	return o.frame.layout(o.w, o.h)
}

// scrollPicker keeps the highlighted row in view (pickerOverlay does).
func (m *model) scrollPicker() { m.pickerOverlay() }

// pickerKey takes every key while the list is open: arrows move the
// highlight, Enter switches to it, a digit switches at once to the tab with
// that strip number, Esc closes. Nothing else acts or reaches a child. A
// refusal stays in the footer for at least noticeTime, whatever keys follow.
func (m *model) pickerKey(k tea.Key) (cmd tea.Cmd) {
	p := m.picker
	seq := m.noticeSeq
	defer func() {
		if m.noticeSeq != seq {
			cmd = prefixTick(noticeTime)
		}
	}()
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	switch {
	case k.Code == tea.KeyUp && mod == 0:
		p.row = max(p.row-1, 0)
	case k.Code == tea.KeyDown && mod == 0:
		p.row = min(p.row+1, len(m.tabs.Tabs)-1)
	case k.IsRepeat:
		return nil
	case k.Code == tea.KeyEnter && mod == 0:
		m.pickTab(p.highlight)
		return nil
	case k.Code == tea.KeyEscape && mod == 0:
		m.closePicker()
		return nil
	default:
		if n, ok := digitNumber(k); ok {
			if n > len(m.tabs.Tabs) {
				m.paneNotice(fmt.Sprintf("no tab %d", n)) // the list stays open
			} else {
				m.pickTab(m.tabs.Tabs[n-1])
			}
		}
		return nil
	}
	p.highlight = m.tabs.Tabs[p.row]
	m.scrollPicker()
	return nil
}

// pickTab switches to t, as it is at this moment, and closes the list. In
// join mode it brings t in, with the preflight repeated now; a refusal
// stays in the footer and the list stays open.
func (m *model) pickTab(t *tabs.Tab) {
	if p := m.picker; p.join {
		if why := m.canJoin(t, p.dest, p.target, panes.Down); why != "" {
			m.paneNotice(why)
			return
		}
		m.closePicker()
		if why := m.joinTab(t, p.dest, p.target, panes.Down); why != "" {
			m.paneNotice(why) // the picker closed: the inert notice popup
		}
		return
	}
	m.closePicker()
	if i := m.tabs.Index(t); i >= 0 {
		m.tabs.Select(i)
		m.scrollToLive()
	}
}

const (
	pickerFooter = "↑↓ move · Enter or 1-9 0 switch · Esc close"
	joinFooter   = "↑↓ move · Enter or 1-9 0 bring in · Esc close"
)

// pickerClick takes a press while the list is open: a press on a shown tab
// row switches to that tab; any other press, on the frame, the footer or a
// blank row, closes the list. Either way the press is consumed and
// chrome-owned through its release.
func (m *model) pickerClick(mo tea.Mouse) {
	p := m.picker
	o := m.pickerOverlay()
	inner, top, shown := o.frame.layout(o.w, o.h)
	left := (o.w - inner) / 2 // the frame's border and inset, when framed
	x, y := m.overlayPoint(o, mo)
	i := y - top
	if mo.Button == tea.MouseLeft && y < o.h && i >= 0 && i < min(shown, len(m.tabs.Tabs)-p.scroll) && x >= left && x < left+inner {
		m.pickTab(m.tabs.Tabs[p.scroll+i])
		return
	}
	m.closePicker()
}

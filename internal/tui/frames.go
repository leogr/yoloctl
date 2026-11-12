package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// A View uses exactly one immutable snapshot per surface. All layout helpers,
// rows and cursor composition share it, even while the PTY advances the core.
// tab is the current tab's focused pane; panes holds every visible pane of
// a divided tab, the focused one included.
type displayFrames struct {
	console terminal.Frame
	tab     terminal.Frame
	panes   map[*panes.Pane]terminal.Frame
}

// eachSession visits every pane of every tab that has a session: every
// pane but the empty one.
func (m *model) eachSession(visit func(*panes.Pane)) {
	m.eachPane(func(_ *tabs.Tab, p *panes.Pane) {
		if !p.Empty() {
			visit(p)
		}
	})
}

// eachPane visits every pane of every tab.
func (m *model) eachPane(visit func(*tabs.Tab, *panes.Pane)) {
	for _, t := range m.tabs.Tabs {
		for _, p := range t.Layout.Panes() {
			visit(t, p)
		}
	}
}

func (m *model) consoleFrame() terminal.Frame {
	if m.frames != nil {
		return m.frames.console
	}
	return m.consoleDisplayFrame()
}

// consoleDisplayFrame is the palette console's frame with every image
// placeholder neutralized: the console never displays a preview.
func (m *model) consoleDisplayFrame() terminal.Frame {
	return m.filterImages(nil, m.console.Frame())
}

// sessionFrame is a session's frame with image placeholders admitted only
// for that session's registered preview (see chatimage.FilterLine).
func (m *model) sessionFrame(s *term.Session) terminal.Frame {
	return m.filterImages(s, s.Frame())
}

// filterImages neutralizes every placeholder cell in f that is not a cell of
// the owner's registered preview. Rows without placeholders are not copied.
func (m *model) filterImages(owner *term.Session, f terminal.Frame) terminal.Frame {
	var key any
	if owner != nil {
		key = owner
	}
	p, ok := m.imageRegistry.Active(key)
	var lines []string
	for i, line := range f.Lines {
		if filtered := chatimage.FilterLine(line, p, ok); filtered != line {
			if lines == nil {
				lines = slices.Clone(f.Lines)
			}
			lines[i] = filtered
		}
	}
	if lines != nil {
		f.Lines = lines
	}
	return f
}

func (m *model) tabFrame() terminal.Frame {
	if m.frames != nil {
		return m.frames.tab
	}
	if p := m.tabs.Focused(); p != nil && !p.Empty() {
		return m.sessionFrame(p.Session)
	}
	return terminal.Frame{}
}

type scrollSurface interface {
	Scroll(int) int
	ScrollToBottom()
}

func (m *model) scroller() scrollSurface {
	if m.focusPalette {
		return m.console
	}
	if p := m.tabs.Focused(); p != nil && !p.Empty() {
		return p.Session
	}
	return nil // an empty pane has nothing to scroll
}

func (m *model) scrollBy(delta int) {
	if s := m.scroller(); s != nil {
		m.scrollOff = s.Scroll(delta)
	}
}

func (m *model) scrollToLive() {
	if s := m.scroller(); s != nil {
		s.ScrollToBottom()
	}
	m.scrollOff = 0
}

func frameCursor(c terminal.Cursor, y int) *tea.Cursor {
	if !c.Visible {
		return nil
	}
	cursor := tea.NewCursor(c.X, y)
	cursor.Blink = c.Blink
	switch c.Shape {
	case 0:
		cursor.Shape = tea.CursorBar
	case 2:
		cursor.Shape = tea.CursorUnderline
	default:
		cursor.Shape = tea.CursorBlock
	}
	return cursor
}

func (m *model) syncFocus() {
	// The close popup takes the palette area but not focus: panes and the
	// console keep the focus they had when it opened.
	palette := m.focusPalette
	switch {
	case m.spacePicker != nil:
		palette = m.spacePicker.returnPalette
	case m.confirm != nil:
		palette = m.confirm.returnPalette
	case m.picker != nil:
		palette = m.picker.returnPalette
	}
	if m.console != nil {
		if err := m.console.UpdateFocus(!m.hostBlurred && m.browserDialog == nil && palette && m.console.Busy()); err != nil {
			m.setStatus(prettylog.KindError, "console focus: "+err.Error())
		}
	}
	// Only the current tab's focused pane is focused: one source of focus.
	focused := m.tabs.Focused()
	m.eachSession(func(p *panes.Pane) {
		if p.Exited {
			return
		}
		if err := p.Session.UpdateFocus(!m.hostBlurred && !palette && p == focused); err != nil {
			m.setStatus(prettylog.KindError, "tab focus: "+err.Error())
		}
	})
}

package tui

import (
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/term"
)

var selectionClock = time.Now()

type selectionTickMsg struct{}

func (m *model) selectionScrollDirection() int {
	owner := m.mouseOwners[tea.MouseLeft]
	if m.focusPalette || owner.pane == nil || !owner.selection || m.selectionPointer == nil {
		return 0
	}
	// Past its own pane's top or bottom edge, a selection scrolls history.
	owner = m.paneOwner(owner.pane, true)
	if m.selectionPointer.Y < owner.row0 {
		return 1
	}
	if m.selectionPointer.Y >= owner.row0+owner.pane.Rect().H {
		return -1
	}
	return 0
}

func (m *model) selectionScrollCmd() tea.Cmd {
	if m.selectionTickPending || m.selectionScrollDirection() == 0 {
		return nil
	}
	m.selectionTickPending = true
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return selectionTickMsg{} })
}

func (m *model) scrollSelection() tea.Cmd {
	if delta := m.selectionScrollDirection(); delta != 0 {
		owner := m.mouseOwners[tea.MouseLeft]
		owner.surface.Scroll(delta)
		m.selectMouse(owner, *m.selectionPointer, terminal.MouseMotion)
	}
	return m.selectionScrollCmd()
}

func (m *model) selectionTarget(mo tea.Mouse) mouseOwner {
	mo = m.mouseHit(mo)
	if m.focusPalette || mo.X < 0 || mo.X >= m.width || mo.Y < m.chromeRows() || mo.Y >= m.height {
		return mouseOwner{}
	}
	if p := m.paneAt(mo); p != nil && !p.Empty() {
		if p.Exited || p.Session.Frame().Offset != 0 || p.Session.MouseMode().Tracking == 0 {
			return m.paneOwner(p, true)
		}
	}
	return mouseOwner{}
}

func (m *model) selectMouse(owner mouseOwner, mo tea.Mouse, action terminal.MouseAction) {
	x, y := mo.X-owner.col0, mo.Y-owner.row0
	if t := m.tabs.Current(); owner.pane != nil && t != nil && t.Layout.Bars() {
		// In a tab with pane bars a local selection stays inside its pane: it
		// never crosses a border or bar. An undivided tab keeps its unclamped
		// positions.
		r := owner.pane.Rect()
		x, y = min(max(x, 0), r.W-1), min(max(y, 0), r.H-1)
	}
	if err := owner.surface.SelectMouse(action, x, y, mo.Mod&tea.ModAlt != 0, uint64(time.Since(selectionClock))); err != nil {
		m.status = err.Error()
	}
}

func (m *model) clearTabSelection() {
	if !m.focusPalette {
		if tab := m.tabs.Focused(); tab != nil && !tab.Empty() {
			tab.Session.ClearSelection()
			if owner := m.mouseOwners[tea.MouseLeft]; owner.selection && owner.surface == tab.Session {
				m.mouseOwners[tea.MouseLeft] = mouseOwner{}
				m.selectionPointer = nil
			}
		}
	}
}

// The host may consume its own copy binding before it reaches us. Handle only
// an explicit copy with an existing local selection; all other keys keep the
// application's original path. Palette selection is entirely host-owned.
func (m *model) copySelection(k tea.Key) (tea.Cmd, bool) {
	mod := k.Mod & ^(tea.ModCapsLock | tea.ModNumLock)
	copyKey := unicode.ToLower(k.Code) == 'c' && (mod == tea.ModCtrl|tea.ModShift || runtime.GOOS == "darwin" && mod == tea.ModSuper)
	if !copyKey || m.focusPalette {
		return nil, false
	}
	tab := m.tabs.Focused()
	if tab == nil || tab.Empty() {
		return nil, false
	}
	text, err := m.selectedCopyText(tab.Session)
	if err != nil {
		m.status = err.Error()
		return nil, true
	}
	if text == "" {
		return nil, false
	}
	msg := m.osc52HostMsg(termio.OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte(text))})
	return func() tea.Msg { return msg }, true
}

// Selection bounds come from native cells/pins; ANSI decoding here changes
// presentation only. Never reconstruct selected text from rendered strings.
func selectionLines(f terminal.Frame) []string {
	lines := append([]string(nil), f.Lines...)
	for y, span := range f.Selection {
		if y >= len(lines) || span.End <= span.Start {
			continue
		}
		text := lines[y] + strings.Repeat(" ", max(span.End-ansi.StringWidth(lines[y]), 0))
		rows := uv.NewStyledString(text).Lines(ansi.GraphemeWidth)
		if len(rows) == 0 {
			continue
		}
		x := 0
		for i := range rows[0] {
			// StyledString.Lines returns graphemes, without the wide-cell
			// placeholders used by ScreenBuffer. Track columns separately.
			cell := &rows[0][i]
			width := max(cell.Width, 1)
			if x < span.End && x+width > span.Start {
				cell.Style.Attrs ^= uv.AttrReverse
			}
			x += width
		}
		lines[y] = rows[0].Render() + cReset
	}
	return lines
}

// selectedCopyText is a session's selected text for the clipboard. A native
// chat session's preview image cells become their attachment chips (from the
// host's bounded ledger, or a generic marker when it no longer knows them);
// the result keeps the plain clipboard bound.
func (m *model) selectedCopyText(s *term.Session) (string, error) {
	if !s.CopiesImages() {
		return s.SelectedText()
	}
	styled, err := s.SelectedStyledText()
	if err != nil || styled == "" {
		return "", err
	}
	text, err := chatimage.MapSelection(styled, m.imageLedger)
	if err != nil {
		return "", errors.New("cannot copy this chat selection: unexpected terminal output")
	}
	if len(text) > termio.MaxOSC52DecodedBytes {
		return "", errors.New("selection exceeds clipboard size limit")
	}
	return text, nil
}

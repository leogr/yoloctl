package tui

import (
	"encoding/base64"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/termio"
)

func TestLocalSelectionExplicitCopy(t *testing.T) {
	m := newTestModel(t, 40, 8)
	s, capture := mouseCapture(t, m, "\x1b[2J\x1b[H\x1b[31m\x1b]8;;https://example.org\x1b\\界e\u0301👩‍💻\x1b]8;;\x1b\\ rest\x1b[0m")
	_, _ = m.Update(click(0, barRows))
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: barRows})
	_, cmd := m.Update(releaseAt(5, barRows))
	if cmd != nil {
		t.Fatal("selection release must not copy automatically")
	}
	if got, err := s.SelectedText(); err != nil || got != "界e\u0301👩‍💻" {
		t.Fatalf("selected text = %q, %v", got, err)
	}
	// Highlight every cell of the selected graphemes, retaining text/style.
	view := m.View().Content
	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(view).Draw(buf, buf.Bounds())
	for _, x := range []int{0, 2, 3} {
		cell := buf.CellAt(x, barRows)
		if cell.Style.Attrs&uv.AttrReverse == 0 || cell.Style.Fg == nil || cell.Link.URL != "https://example.org" {
			t.Fatalf("selection styling missing at %d: %+v", x, cell)
		}
	}
	if buf.CellAt(5, barRows).Style.Attrs&uv.AttrReverse != 0 {
		t.Fatal("highlight spilled past selection")
	}
	// Enhanced-keyboard modifier presses precede C and must keep selection.
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeftCtrl, Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeftShift, Mod: tea.ModCtrl | tea.ModShift})
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift | tea.ModNumLock})
	if cmd == nil {
		t.Fatal("copy did not produce a host write")
	}
	raw, ok := cmd().(tea.RawMsg)
	want := termio.OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte("界e\u0301👩‍💻"))}.Sequence()
	if !ok || raw.Msg != want {
		t.Fatalf("copy event = %#v, want %q", raw, want)
	}
	_, _ = m.Update(tea.KeyReleaseMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
	if got := capture(); got != "" {
		t.Fatalf("selection/copy leaked to child: %q", got)
	}
}

func TestSelectionCopyPolicyAndOrdinaryInput(t *testing.T) {
	m := newTestModel(t, 40, 8)
	s, capture := mouseCapture(t, m, "\x1b[2J\x1b[Hhello world")
	_, _ = m.Update(click(0, barRows))
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: barRows})
	_, _ = m.Update(releaseAt(5, barRows))
	disabled := false
	m.cfg.Seams.Clipboard = &disabled
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
	if cmd == nil {
		t.Fatal("missing blocked-copy notice")
	}
	if msg, ok := cmd().(osc52NoticeMsg); !ok || msg.notice != termio.OSC52DisabledNotice {
		t.Fatal("copy bypassed OSC 52 policy")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got, err := s.SelectedText(); got != "" || err != nil {
		t.Fatalf("typing left stale selection: %q, %v", got, err)
	}
	if got := capture(); got != "x" {
		t.Fatalf("typed bytes = %q, want x", got)
	}
}

func TestSelectionDoesNotTakePaletteOrApplicationMouse(t *testing.T) {
	m := newTestModel(t, 40, 8)
	s, capture := mouseCapture(t, m, "\x1b[?1002h\x1b[?1006h")
	_, _ = m.Update(click(0, barRows))
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: barRows})
	_, _ = m.Update(releaseAt(5, barRows))
	if got, _ := s.SelectedText(); got != "" {
		t.Fatal("application gesture became local selection")
	}
	m.focusPalette = true
	_, _ = m.console.Write([]byte("palette output"))
	_, _ = m.Update(click(0, 1))
	_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 5, Y: 1})
	_, cmd := m.Update(releaseAt(5, 1))
	if cmd != nil {
		t.Fatal("palette drag initiated local copy")
	}
	if got, _ := m.console.SelectedText(); got != "" {
		t.Fatal("palette text selected locally")
	}
	if got, want := capture(), "\x1b[<0;1;1M\x1b[<32;6;1M\x1b[<0;6;1m"; got != want {
		t.Fatalf("application selection = %q, want %q", got, want)
	}
}

func TestSelectionAutoscrollStopsOnRelease(t *testing.T) {
	m := newTestModel(t, 40, 8)
	s, capture := mouseCapture(t, m, strings.Repeat("\r\nold", 12)+"\r\nlast")
	_, _ = m.Update(click(0, barRows))
	_, cmd := m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 0, Y: barRows - 1})
	if cmd == nil {
		t.Fatal("drag above viewport did not schedule scrolling")
	}
	_, _ = m.Update(selectionTickMsg{})
	if s.Frame().Offset != 1 {
		t.Fatal("selection tick did not scroll history")
	}
	_, _ = m.Update(releaseAt(0, 0))
	_, cmd = m.Update(selectionTickMsg{})
	if s.Frame().Offset != 1 || cmd != nil {
		t.Fatal("released selection continued scrolling")
	}
	if got, err := s.SelectedText(); err != nil || !strings.Contains(got, "old") {
		t.Fatalf("history selection = %q, %v", got, err)
	}
	if got := capture(); got != "" {
		t.Fatalf("history drag leaked to child: %q", got)
	}
}

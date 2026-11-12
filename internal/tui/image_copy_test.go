package tui

import (
	"encoding/base64"
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// A native chat pane's copy maps preview image cells to the attachment chip
// the host recorded for that image ID; other panes copy plain native text.
func TestChatCopyMapsPreviewCellsToChips(t *testing.T) {
	const cell = "\U0010EEEE̅̅"
	content := "\x1b[2J\x1b[Hsee \x1b[38;2;1;2;3m" + cell + cell + "\x1b[0m ok"
	for _, c := range []struct {
		imageCopy bool
		want      string
	}{
		{true, "see [file cat.png · 1 KiB] ok"},
		{false, "see " + cell + cell + " ok"},
	} {
		m := newTestModel(t, 40, 8)
		m.imageLedger.Remember(0x010203, "[file cat.png · 1 KiB]")
		s, err := term.Start(term.Options{Rows: m.viewRows(), Cols: m.width, Argv: []string{"sleep", "30"}, ImageCopy: c.imageCopy})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		// Known default colours, as managed and host views install them.
		s.SetColors(color.White, color.Black)
		_, _ = s.Write([]byte(content))
		m.tabs.Add(testTab(s, "chat", false))
		m.focusPalette = false
		_, _ = m.Update(click(0, barRows))
		_, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 9, Y: barRows})
		_, _ = m.Update(releaseAt(9, barRows))
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
		if cmd == nil {
			t.Fatalf("imageCopy=%v: copy did not produce a host write", c.imageCopy)
		}
		raw, ok := cmd().(tea.RawMsg)
		want := termio.OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte(c.want))}.Sequence()
		if !ok || raw.Msg != want {
			got := ""
			if ok {
				got = raw.Msg.(string)
			}
			t.Fatalf("imageCopy=%v: copy event %q, want %q", c.imageCopy, got, want)
		}
	}
}

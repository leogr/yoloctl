package tui

import (
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestFocusHostAndPaletteReachPTY(t *testing.T) {
	m := newTestModel(t, 40, 12)
	capture := t.TempDir() + "/focus-input"
	s, err := term.Start(term.Options{
		Argv: []string{"sh", "-c", `stty raw -echo
printf '\033[?1004h'
exec dd bs=1 count=15 of="$1" 2>/dev/null`, "sh", capture},
		Rows: m.viewRows(), Cols: m.width,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "focus-test", false))
	m.focusPalette = false
	deadline := time.Now().Add(5 * time.Second)
	for !s.FocusReporting() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.FocusReporting() {
		t.Fatal("child did not enable focus reporting")
	}
	if !m.View().ReportFocus {
		t.Fatal("host focus events were not requested")
	}
	for _, msg := range []tea.Msg{
		tea.FocusMsg{}, tea.FocusMsg{}, tea.BlurMsg{}, tea.BlurMsg{}, tea.FocusMsg{},
		tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}, // palette owns focus
		tea.KeyPressMsg{Code: tea.KeyEscape},         // return to the tab
	} {
		_, _ = m.Update(msg)
	}
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b[I\x1b[O\x1b[I\x1b[O\x1b[I" {
		t.Fatalf("focus bytes at the PTY = %q", got)
	}
}

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// Placeholder cells reach the display only for the session that registered
// that preview. The same cells forged by another pane, a stale session or the
// palette console are neutralized, and the view keeps its width.
func TestFramesAdmitPlaceholdersOnlyForTheirSession(t *testing.T) {
	const id = 0x010203
	tile := func(r, c int) string {
		return "\U0010EEEE" + string(kitty.Diacritic(r)) + string(kitty.Diacritic(c))
	}
	content := "\x1b[2J\x1b[Hpic \x1b[38;2;1;2;3m" + tile(0, 0) + tile(0, 1) + "\x1b[0m end"
	m := newTestModel(t, 40, 8)
	start := func() *term.Session {
		s, err := term.Start(term.Options{Rows: m.viewRows(), Cols: m.width, Argv: []string{"sleep", "30"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		_, _ = s.Write([]byte(content))
		return s
	}
	owner, forger := start(), start()
	m.imageRegistry.Activate(owner, chatimage.Placement{ID: id, Rows: 1, Cols: 2}, "[file a.png · 1 KiB]")
	count := func(lines []string) int { return strings.Count(strings.Join(lines, "\n"), "\U0010EEEE") }
	if got := count(m.sessionFrame(owner).Lines); got != 2 {
		t.Fatalf("owner shows %d placeholder cells, want 2", got)
	}
	f := m.sessionFrame(forger)
	if got := count(f.Lines); got != 0 {
		t.Fatalf("forging pane shows %d placeholder cells", got)
	}
	if got, want := ansi.StringWidth(f.Lines[0]), ansi.StringWidth(m.sessionFrame(owner).Lines[0]); got != want || !strings.Contains(ansi.Strip(f.Lines[0]), "pic    end") {
		t.Fatalf("forged row %q: width %d, owner %d", f.Lines[0], got, want)
	}
	_, _ = m.console.Write([]byte(content))
	if got := count(m.consoleDisplayFrame().Lines); got != 0 {
		t.Fatalf("console shows %d placeholder cells", got)
	}
	// The composed view shows the owner's cells and no one else's.
	m.tabs.Add(testTab(owner, "chat", false))
	m.focusPalette = false
	if got := strings.Count(m.View().Content, "\U0010EEEE"); got != 2 {
		t.Fatalf("owner view shows %d placeholder cells", got)
	}
	m.tabs.Add(testTab(forger, "shell", false))
	if got := strings.Count(m.View().Content, "\U0010EEEE"); got != 0 {
		t.Fatalf("forger view shows %d placeholder cells", got)
	}
	// After release the owner's cells are neutralized too.
	m.imageRegistry.Release(owner)
	if got := count(m.sessionFrame(owner).Lines); got != 0 {
		t.Fatalf("released owner still shows %d cells", got)
	}
}

// Workload-set titles are chrome: placeholder cells in a title never reach
// the tab strip, pane bars or window title (review step 3 F1).
func TestTitlePlaceholdersNeverReachTheView(t *testing.T) {
	tile := "\U0010EEEE" + string(kitty.Diacritic(0)) + string(kitty.Diacritic(1))
	m := newTestModel(t, 60, 8)
	s, err := term.Start(term.Options{Rows: m.viewRows(), Cols: m.width, Argv: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	_, _ = s.Write([]byte("\x1b]2;T" + tile + tile + "Z\x07"))
	m.imageRegistry.Activate(s, chatimage.Placement{ID: 0x010203, Rows: 1, Cols: 2}, "chip")
	m.tabs.Add(testTab(s, "chat", false))
	m.tabs.Add(testTab(s, "chat", false)) // two tabs: the strip shows titles
	m.focusPalette = false
	v := m.View()
	if strings.ContainsRune(v.Content, '\U0010EEEE') || strings.ContainsRune(v.WindowTitle, '\U0010EEEE') {
		t.Fatalf("title placeholders reached the view: %q / %q", v.Content, v.WindowTitle)
	}
	if !strings.Contains(v.Content, "T  Z") {
		t.Fatalf("title text lost: %q", v.Content)
	}
}

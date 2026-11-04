package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestTabStripStripsOnlyExactLeadingYoloctlPrefix(t *testing.T) {
	m := newTestModel(t, 200, 24)
	s, err := term.Start(term.Options{Argv: []string{"true"}, Rows: 20, Cols: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Wait(5 * time.Second)
	tab := testTab(s, "fallback", true)
	m.tabs.Add(tab)
	for _, tc := range []struct{ title, want string }{
		{"yoloctl — space view", "space view"},
		{"yoloctl — yoloctl — chat", "yoloctl — chat"},
		{"other yoloctl — chat", "other yoloctl — chat"},
		{"yoloctl - chat", "yoloctl - chat"},
		{"yoloctl – chat", "yoloctl – chat"},
		{"Yoloctl — chat", "Yoloctl — chat"},
		{"yoloctl —chat", "yoloctl —chat"},
	} {
		if _, err := s.Write([]byte("\x1b]2;" + tc.title + "\x07")); err != nil {
			t.Fatal(err)
		}
		if got := m.tabStripLabels()[0]; got != "❶ "+tc.want {
			t.Errorf("%q displayed as %q; want %q", tc.title, got, tc.want)
		}
		if got := tab.Title(); got != tc.title {
			t.Fatalf("stored title changed: %q", got)
		}
	}
}

func TestWindowTitleCollapsesOnlyExactGeneratedPrefixes(t *testing.T) {
	m := newTestModel(t, 200, 24)
	if got := m.View().WindowTitle; got != "yoloctl" {
		t.Fatalf("no tab: window title %q", got)
	}
	s, err := term.Start(term.Options{Argv: []string{"true"}, Rows: 20, Cols: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Wait(5 * time.Second)
	tab := testTab(s, "", true)
	m.tabs.Add(tab)
	setTitle := func(title string) {
		t.Helper()
		if _, err := s.Write([]byte("\x1b]2;" + title + "\x07")); err != nil {
			t.Fatal(err)
		}
		if got := s.Title(); got != title {
			t.Fatalf("terminal title %q; want %q", got, title)
		}
	}
	for _, tc := range []struct{ title, want string }{
		{"space view", "yoloctl — space view"},
		{"yoloctl — space view", "yoloctl — space view"},
		{"yoloctl — yoloctl — yoloctl — chat", "yoloctl — chat"},
		{"", "yoloctl"},
		{"yoloctl", "yoloctl"},
		{"yoloctl — yoloctl", "yoloctl"},
		{"yoloctl — ", "yoloctl"},
		{"yoloctl — yoloctl — ", "yoloctl"},
		{"other yoloctl — chat", "yoloctl — other yoloctl — chat"},
		{"yoloctl — chat — yoloctl — x", "yoloctl — chat — yoloctl — x"},
		{"yoloctl — Yoloctl — chat", "yoloctl — Yoloctl — chat"},
		{"yoloctl - chat", "yoloctl — yoloctl - chat"},
		{"yoloctl – chat", "yoloctl — yoloctl – chat"},
		{"Yoloctl — chat", "yoloctl — Yoloctl — chat"},
		{"yoloctl —chat", "yoloctl — yoloctl —chat"},
		{"yoloctl —", "yoloctl"},
		{"yoloctl —x", "yoloctl — yoloctl —x"},
	} {
		setTitle(tc.title)
		stored := tab.Title()
		got := m.View().WindowTitle
		if got != tc.want {
			t.Errorf("%q: window title %q; want %q", tc.title, got, tc.want)
		}
		if tab.Title() != stored || s.Title() != tc.title {
			t.Fatalf("%q: stored title changed to %q", tc.title, tab.Title())
		}
		// A nested yoloctl reports its window title as this tab's title.
		setTitle(got)
		if again := m.View().WindowTitle; again != got {
			t.Errorf("%q: nested window title %q; want %q", tc.title, again, got)
		}
	}
}

func TestChatCtrlPForwardsThroughHostInput(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"", "\x10"}, {"\x1b[>3u", "\x1b[112;5u"}} {
		target := newKeyTerminal(t)
		if _, err := target.Write([]byte(tc.mode)); err != nil {
			t.Fatal(err)
		}
		for _, sequence := range []string{"\x10", "\x1b[112;5u"} {
			var decoder uv.EventDecoder
			n, event := decoder.Decode([]byte(sequence))
			key, ok := event.(uv.KeyPressEvent)
			if !ok || n != len(sequence) {
				t.Fatalf("decode: %d %#v", n, event)
			}
			if isPaletteKey(tea.KeyPressMsg(key), "ctrl+y") {
				t.Fatal("default palette consumes Ctrl+P")
			}
			got, err := childKey(target, tea.Key(key), false)
			if err != nil || string(got) != tc.want {
				t.Fatalf("Ctrl+P reaches child as %q (%v), want %q", got, err, tc.want)
			}
			if !isPaletteKey(tea.KeyPressMsg(key), "ctrl+p") {
				t.Fatal("custom palette binding must retain priority")
			}
		}
	}
}

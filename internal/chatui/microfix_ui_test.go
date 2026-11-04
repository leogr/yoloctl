package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func TestChatHeaderSharesDashboardStyle(t *testing.T) {
	m, _ := readyModel()
	m.Update(MembersMsg{Members: []Member{{Nick: "leo"}, {Nick: "sol", Away: true}}})
	for _, width := range []int{20, 40, 80, 160} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		row := strings.Split(m.View().Content, "\n")[0]
		plain := ansi.Strip(row)
		if ansi.StringWidthWc(row) != width || !strings.HasPrefix(plain, " "+spaceview.ChatIcon+" ") || !strings.HasSuffix(plain, "◉ LIVE ") {
			t.Fatalf("%d: header icon, status or width: %q", width, row)
		}
		for _, removed := range []string{"F1", "F2", "Help", "Settings", "Participants"} {
			if strings.Contains(plain, removed) {
				t.Fatalf("header still has %s: %q", removed, plain)
			}
		}
		for _, h := range m.hits {
			if h.area == hitTop && h.id != "channels" && h.id != "live" {
				t.Fatalf("removed header button has a hit: %+v", h)
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	if header := ansi.Strip(m.topBar(160)); !strings.Contains(header, "2 online · 1 away") {
		t.Fatal(header)
	}
	m.Update(ConnectionMsg{})
	if header := ansi.Strip(m.topBar(160)); !strings.HasSuffix(header, "◌ OFFLINE ") {
		t.Fatal(header)
	}
	m.reconnecting = true
	if header := ansi.Strip(m.topBar(160)); !strings.HasSuffix(header, "◌ CONNECTING ") {
		t.Fatal(header)
	}
}

func TestChatParticipantsCtrlPDecodedAndPreservesDraft(t *testing.T) {
	for _, width := range []int{40, 100} {
		m, _ := readyModel()
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		paste(m, "keep\nthis 界")
		before, cursor := m.Draft(), m.editor.cursor
		isOpen := func() bool {
			if width < 80 {
				return m.rosterOverlay
			}
			return m.rosterOpen
		}
		open := isOpen()
		for _, sequence := range []string{"\x10", "\x1b[112;5u"} {
			var decoder uv.EventDecoder
			n, event := decoder.Decode([]byte(sequence))
			k, ok := event.(uv.KeyPressEvent)
			if !ok || n != len(sequence) {
				t.Fatalf("decode: %d %#v", n, event)
			}
			m.Update(tea.KeyPressMsg(k))
			if isOpen() == open || m.Draft() != before || m.editor.cursor != cursor {
				t.Fatalf("Ctrl+P changed draft or failed toggle: %q", m.Draft())
			}
			open = isOpen()
		}
		press(m, 'n', tea.ModCtrl)
		if isOpen() != open {
			t.Fatal("old Ctrl+N still toggles participants")
		}
	}
}

func TestChatHelpAndSettingsUseSlashCommands(t *testing.T) {
	for _, command := range []struct {
		text string
		kind overlayKind
	}{{"/help", overlayHelp}, {"/settings", overlaySettings}} {
		m, sends := readyModel()
		for _, key := range []rune{tea.KeyF1, tea.KeyF2} {
			press(m, key, 0)
		}
		if m.overlay != nil {
			t.Fatal("function key opened an overlay")
		}
		paste(m, command.text)
		press(m, tea.KeyEnter, 0)
		if m.overlay == nil || m.overlay.kind != command.kind || len(*sends) != 0 {
			t.Fatalf("%s did not open locally", command.text)
		}
		for _, key := range []rune{tea.KeyF1, tea.KeyF2} {
			press(m, key, 0)
		}
		if m.overlay == nil || m.overlay.kind != command.kind {
			t.Fatal("function key changed or closed the overlay")
		}
	}
}

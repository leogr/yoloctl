package chatui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func readyModel() (*Model, *[]Outgoing) {
	var sends []Outgoing
	m := New(Options{Space: "trial", Nick: "human", Send: func(out Outgoing) tea.Cmd {
		sends = append(sends, out)
		return nil // queued is deliberately not a confirmation
	}})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, &sends
}

func press(m *Model, code rune, mod tea.KeyMod) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: code, Mod: mod}))
	return cmd
}

func paste(m *Model, text string) { m.Update(tea.PasteMsg{Content: text}) }

func TestMultilinePasteNeverSubmitsAndPreservesCode(t *testing.T) {
	m, sends := readyModel()
	text := "# Review\n```go\n\tfmt.Println(\"界 👩‍💻\")\n```\n"
	paste(m, text)
	if m.Draft() != text || len(*sends) != 0 {
		t.Fatalf("paste changed/submitted draft: %q, %v", m.Draft(), *sends)
	}
	m.Update(tea.PasteStartMsg{})
	press(m, tea.KeyEnter, 0)
	press(m, 'c', tea.ModCtrl) // control-looking paste must not execute Quit
	m.Update(tea.PasteEndMsg{})
	if len(*sends) != 0 || m.Draft() != text+"\n" {
		t.Fatal("fragmented bracketed paste submitted or lost its newline")
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Text != text+"\n" || m.Draft() != text+"\n" || m.Messages()[0].Delivery != Pending {
		t.Fatal("send must keep exact draft pending until full confirmation")
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 {
		t.Fatal("Enter duplicated an unresolved send")
	}
}

func TestExplicitNewlineKeys(t *testing.T) {
	m, sends := readyModel()
	paste(m, "one")
	press(m, tea.KeyEnter, tea.ModAlt)
	paste(m, "two")
	press(m, tea.KeyEnter, tea.ModShift)
	press(m, 'j', tea.ModCtrl)
	if m.Draft() != "one\ntwo\n\n" || len(*sends) != 0 {
		t.Fatalf("newline keys: %q, %v", m.Draft(), *sends)
	}
}

func TestCommittedTextCannotImpersonateCommands(t *testing.T) {
	for _, code := range []rune{0, tea.KeyExtended} {
		m, sends := readyModel()
		for _, text := range []string{"enter", "ctrl+c", "up", "tab", "界👩‍💻"} {
			_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: code, Text: text}))
			if cmd != nil {
				t.Fatalf("committed text %q dispatched a command", text)
			}
		}
		if m.Draft() != "enterctrl+cuptab界👩‍💻" || len(*sends) != 0 {
			t.Fatalf("text commit was interpreted as control: %q, %v", m.Draft(), *sends)
		}
		press(m, tea.KeyBackspace, 0)
		if m.Draft() != "enterctrl+cuptab界" {
			t.Fatal("committed emoji was not edited as one grapheme")
		}
	}
}

func TestGraphemeEditingAndKillUndo(t *testing.T) {
	m, _ := readyModel()
	paste(m, "A👩‍💻e\u0301🇮🇹")
	for _, want := range []string{"A👩‍💻e\u0301", "A👩‍💻", "A", ""} {
		press(m, tea.KeyBackspace, 0)
		if m.Draft() != want || !utf8.ValidString(m.Draft()) {
			t.Fatalf("grapheme delete: %q, want %q", m.Draft(), want)
		}
	}
	paste(m, "first\n界 code\nlast")
	press(m, 'a', tea.ModCtrl)
	press(m, tea.KeyUp, 0)
	press(m, tea.KeyRight, 0)
	press(m, 'k', tea.ModCtrl)
	if m.Draft() != "first\n界\nlast" {
		t.Fatalf("Ctrl+K removed more than the logical line tail: %q", m.Draft())
	}
	press(m, 'y', tea.ModCtrl)
	if m.Draft() != "first\n界 code\nlast" {
		t.Fatal("Ctrl+Y did not restore the killed text")
	}
	press(m, 'z', tea.ModCtrl)
	if m.Draft() != "first\n界\nlast" {
		t.Fatal("undo did not revert the yank")
	}
	press(m, 'z', tea.ModCtrl|tea.ModShift)
	if m.Draft() != "first\n界 code\nlast" {
		t.Fatal("redo did not restore the yank")
	}
	press(m, 'e', tea.ModCtrl)
	press(m, 'w', tea.ModCtrl)
	if m.Draft() != "first\n界 \nlast" {
		t.Fatalf("word kill: %q", m.Draft())
	}
}

func TestDraftLimitIsAtomicAndControlsAreInert(t *testing.T) {
	m := New(Options{MaxMessageBytes: 8})
	paste(m, "safe")
	paste(m, "👩‍💻")
	if m.Draft() != "safe" || !strings.Contains(m.notice, "limit") {
		t.Fatal("oversize paste partially changed draft")
	}
	paste(m, string([]byte{0xff}))
	if m.Draft() != "safe" {
		t.Fatal("invalid UTF-8 changed draft")
	}
	if err := m.SetDraft("x\r\n\t\x1b\a\x00z"); err != nil || m.Draft() != "x\n\tz" {
		t.Fatalf("text controls: %q, %v", m.Draft(), err)
	}
}

func TestDeliveryStatePreservesDraftAndDoesNotRetry(t *testing.T) {
	for _, state := range []Delivery{Rejected, Uncertain, Confirmed} {
		t.Run(string(state), func(t *testing.T) {
			m, sends := readyModel()
			paste(m, "original\ncode")
			press(m, tea.KeyEnter, 0)
			id := (*sends)[0].ID
			m.Update(DeliveryMsg{ID: "unknown", State: Confirmed})
			if m.Draft() == "" {
				t.Fatal("unrelated receipt cleared draft")
			}
			m.Update(DeliveryMsg{ID: id, MessageID: "server-1", State: state, Err: errors.New("test receipt")})
			if (m.Draft() == "") != (state == Confirmed) {
				t.Fatalf("%s draft=%q", state, m.Draft())
			}
			if len(*sends) != 1 {
				t.Fatal("receipt automatically retried a send")
			}
			if state == Uncertain {
				m.Update(ConnectionMsg{Connected: false})
				m.Update(ConnectionMsg{Connected: true})
				press(m, tea.KeyEnter, 0)
				if len(*sends) != 1 {
					t.Fatal("reconnect/Enter replayed an uncertain send")
				}
				m.Update(DeliveryMsg{ID: id, MessageID: "server-1", State: Confirmed})
				if m.Draft() != "" {
					t.Fatal("explicit recovered confirmation did not resolve draft")
				}
			}
		})
	}
}

func TestConfirmedSendDoesNotClearLaterEdits(t *testing.T) {
	m, sends := readyModel()
	paste(m, "first")
	press(m, tea.KeyEnter, 0)
	paste(m, " then edited")
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, MessageID: "one", State: Confirmed})
	if m.Draft() != "first then edited" {
		t.Fatalf("confirmation lost edits: %q", m.Draft())
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, State: Uncertain})
	if m.Messages()[0].Delivery != Confirmed {
		t.Fatal("stale status downgraded confirmed delivery")
	}
}

func TestEchoCorrelationAndReplayDedup(t *testing.T) {
	m, sends := readyModel()
	paste(m, "exact\nbody")
	press(m, tea.KeyEnter, 0)
	msg := Message{ID: "echo", ClientID: (*sends)[0].ID, Text: "wrong", Account: "human", Nick: "human"}
	m.Update(IncomingMsg{Message: msg})
	if m.Draft() == "" || m.Messages()[0].Delivery != Pending {
		t.Fatal("mismatched echo confirmed the send")
	}
	msg.Text = "exact\nbody"
	m.Update(IncomingMsg{Message: msg})
	if m.Draft() != "" || len(m.Messages()) != 1 || m.Messages()[0].Account != "human" {
		t.Fatal("full echo did not correlate exactly once")
	}
	m.follow = false
	msg.Replay = true
	m.Update(IncomingMsg{Message: msg})
	if len(m.Messages()) != 1 || m.unread != 0 {
		t.Fatal("replay overlap duplicated message or unread count")
	}
	m.Update(IncomingMsg{Message: Message{ID: "new", Text: "live"}})
	if m.unread != 1 {
		t.Fatal("live incoming while away did not increment unread")
	}
}

func TestCopyReplyAndLinksRequireExplicitActions(t *testing.T) {
	m, sends := readyModel()
	var copied, opened []string
	m.opts.Copy = func(text string) tea.Cmd { copied = append(copied, text); return nil }
	m.opts.OpenLink = func(link string) tea.Cmd { opened = append(opened, link); return nil }
	raw := "# Review\n```go\n\t界()\n```\n[docs](https://example.org/guide)"
	m.Update(IncomingMsg{Message: Message{ID: "reply-target", Text: raw}})
	m.View()
	if len(copied)+len(opened) != 0 {
		t.Fatal("incoming text/render invoked a host action")
	}
	press(m, 'c', tea.ModCtrl|tea.ModShift)
	press(m, 'c', tea.ModAlt)
	press(m, 'o', tea.ModCtrl)
	if len(copied) != 2 || copied[0] != raw || copied[1] != "\t界()\n" || len(opened) != 1 || opened[0] != "https://example.org/guide" {
		t.Fatalf("copy/open changed originals: %q, %q", copied, opened)
	}
	press(m, 'r', tea.ModCtrl)
	paste(m, "reply")
	press(m, tea.KeyEnter, 0)
	if (*sends)[0].ReplyTo != "reply-target" {
		t.Fatal("reply lost logical server ID")
	}
}

func TestRenderFitsAndDoesNotExecuteRemoteControls(t *testing.T) {
	for _, size := range [][2]int{{20, 7}, {24, 10}, {40, 20}, {80, 24}, {140, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := readyModel()
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.Update(IncomingMsg{Message: Message{ID: "one", Nick: "agent\ninjected", Account: "real", Text: "# Heading\n> quote\n- list\n```go\n\t界 👩‍💻\n```\n\x1b]52;c;ZXZpbA==\a", Time: time.Unix(0, 0)}})
			paste(m, "界👩‍💻 e\u0301\n"+strings.Repeat("wrapped ", 15))
			m.notice = "error\nsecond line\x1b[2J"
			view := m.View()
			rows := strings.Split(view.Content, "\n")
			if len(rows) != size[1] || strings.Contains(view.Content, "\x1b]52;") || strings.Contains(view.Content, "\x1b[2J") {
				t.Fatalf("invalid dimensions/remote controls: rows=%d", len(rows))
			}
			for i, row := range rows {
				if n := ansi.StringWidthWc(row); n != size[0] {
					t.Fatalf("row %d width=%d, want %d: %q", i, n, size[0], row)
				}
			}
			if view.Cursor == nil || view.Cursor.X < 0 || view.Cursor.X >= size[0] || view.Cursor.Y < 0 || view.Cursor.Y >= size[1] {
				t.Fatalf("cursor outside view: %+v", view.Cursor)
			}
		})
	}
}

func TestHistoryBoundKeepsPendingReceipt(t *testing.T) {
	m, sends := readyModel()
	paste(m, "unresolved")
	press(m, tea.KeyEnter, 0)
	for i := range maxMessages + 10 {
		m.Update(IncomingMsg{Message: Message{ID: fmt.Sprint(i), Text: "message"}})
	}
	if len(m.Messages()) != maxMessages || m.Messages()[0].ClientID != (*sends)[0].ID {
		t.Fatal("bounded display evicted the pending receipt correlation")
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, State: Confirmed, MessageID: "confirmed"})
	if m.Draft() != "" {
		t.Fatal("receipt was lost after history eviction")
	}
}

func TestDisplayEvictionAlsoReleasesRenderedBodies(t *testing.T) {
	m, _ := readyModel()
	for i := range maxMessages + 2 {
		m.Update(IncomingMsg{Message: Message{ID: fmt.Sprint(i), Text: "body"}})
		m.View()
	}
	if _, exists := m.bodyCache["server:0"]; exists {
		t.Fatal("display eviction retained an obsolete rendered body")
	}
	if len(m.bodyCache) > len(m.messages) {
		t.Fatal("render cache grew beyond retained messages")
	}
}

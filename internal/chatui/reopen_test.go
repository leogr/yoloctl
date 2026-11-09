package chatui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func disconnectedReopen(t *testing.T) (*Model, *[]Outgoing, *int) {
	t.Helper()
	m, sends := readyModel()
	n := new(int)
	m.opts.Reconnect = func() tea.Cmd { *n++; return func() tea.Msg { return nil } }
	m.Update(ConnectionMsg{Err: errors.New("link lost")})
	return m, sends, n
}

func TestDisconnectedChatReopenPreservesUncertainDelivery(t *testing.T) {
	m, sends := readyModel()
	n := 0
	m.opts.Reconnect = func() tea.Cmd { n++; return func() tea.Msg { return nil } }
	paste(m, "draft reply")
	m.replyTo = "previous"
	press(m, tea.KeyEnter, 0)
	id := m.inflight
	if len(*sends) != 1 || id == "" {
		t.Fatal("missing pending send")
	}
	m.Update(ConnectionMsg{Err: errors.New("lost")})
	if m.Messages()[0].Delivery != Uncertain {
		t.Fatal("missing uncertain marker")
	}
	before := m.Draft()
	out := view(m)
	h, ok := hitWith(m, hitFooter, "reopen")
	if !ok || !strings.Contains(out, "[ Reopen ]") {
		t.Fatal("disconnected live view lacks action")
	}
	if cmd := clickAt(m, h.x0, h.y); cmd == nil {
		t.Fatal("no reconnect command")
	}
	clickAt(m, h.x0, h.y)
	if n != 1 || !m.reconnecting || m.Draft() != before || m.replyTo != "previous" || m.inflight != id || len(*sends) != 1 || m.Messages()[0].Delivery != Uncertain {
		t.Fatal("reopen replayed or lost draft/history/uncertainty")
	}
	m.Update(ConnectionMsg{}) // old stream shuts down during explicit replacement
	m.Update(SessionMsg{Nick: "human"})
	m.Update(ConnectionMsg{Connected: true})
	if n != 1 || m.reconnecting || m.Draft() != before || m.replyTo != "previous" || m.inflight != id || len(*sends) != 1 || m.Messages()[0].Delivery != Uncertain {
		t.Fatal("connection success changed retained state")
	}
}

func TestChatReopenKeyboardFocusAndTextPriority(t *testing.T) {
	m, sends, n := disconnectedReopen(t)
	paste(m, "unsent ")
	m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if *n != 0 || m.Draft() != "unsent r" {
		t.Fatal("composer r was stolen")
	}
	press(m, tea.KeyTab, tea.ModShift)
	if m.focus != focusReopen || !strings.Contains(view(m), "r/Enter") {
		t.Fatal("Shift+Tab did not focus visible action")
	}
	// IME/committed data and bracketed paste stay literal even from action focus.
	m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "r"}))
	if m.focus != focusComposer || m.Draft() != "unsent rr" || *n != 0 {
		t.Fatal("committed text became action")
	}
	press(m, tea.KeyTab, tea.ModShift)
	m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Text: "enter"}))
	if *n != 0 || m.Draft() != "unsent rrenter" {
		t.Fatal("committed control-looking text became an action")
	}
	press(m, tea.KeyTab, tea.ModShift)
	m.Update(tea.PasteStartMsg{})
	m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	press(m, tea.KeyEnter, 0)
	m.Update(tea.PasteEndMsg{})
	if *n != 0 || m.Draft() != "unsent rrenterr\n" {
		t.Fatal("paste dispatched action")
	}
	press(m, tea.KeyTab, tea.ModShift)
	press(m, tea.KeyTab, 0)
	if m.focus != focusComposer {
		t.Fatal("Tab did not return to draft")
	}
	press(m, tea.KeyTab, tea.ModShift)
	press(m, tea.KeyEscape, 0)
	if m.focus != focusComposer {
		t.Fatal("Esc did not return to draft")
	}
	press(m, tea.KeyTab, tea.ModShift)
	m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "R", Mod: tea.ModCapsLock | tea.ModNumLock}))
	if *n != 1 || len(*sends) != 0 {
		t.Fatal("focused r did not reconnect exactly once without sending")
	}
}

func TestChatReopenFailureModalAndAvailability(t *testing.T) {
	m, _, n := disconnectedReopen(t)
	m.openHelp()
	press(m, tea.KeyTab, tea.ModShift)
	press(m, 'r', 0)
	if *n != 0 {
		t.Fatal("modal dispatched reopen")
	}
	m.overlay = nil
	m.View()
	stale, ok := hitWith(m, hitFooter, "reopen")
	if !ok {
		t.Fatal("missing fixture button")
	}
	m.Update(tea.WindowSizeMsg{Width: 19, Height: 6})
	m.View()
	clickAt(m, stale.x0, stale.y)
	if *n != 0 {
		t.Fatal("invisible stale button reopened")
	}
	press(m, tea.KeyTab, tea.ModShift)
	if m.focus == focusReopen {
		t.Fatal("invisible action captured keyboard")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	press(m, tea.KeyTab, tea.ModShift)
	if cmd := press(m, tea.KeyEnter, 0); cmd == nil || *n != 1 {
		t.Fatal("Enter did not activate")
	}
	m.Update(ConnectionMsg{Err: errors.New("still unavailable")})
	if !strings.Contains(view(m), "still unavailable") || !m.reopenOffered() {
		t.Fatal("retry lost error or action")
	}
	m.View()
	h, ok := hitWith(m, hitFooter, "reopen")
	if !ok {
		t.Fatal("missing retry button")
	}
	clickAt(m, h.x0, h.y)
	if *n != 2 {
		t.Fatal("explicit retry failed")
	}
	m.Update(ConnectionMsg{Connected: true})
	if m.reopenOffered() {
		t.Fatal("connected chat offers reopen")
	}
	m.Update(TelemetryLostMsg{Err: errors.New("telemetry only")})
	if m.reopenOffered() {
		t.Fatal("telemetry loss offers IRC reopen")
	}
	m.Update(ConnectionMsg{Err: errors.New("lost")})
	m.deleted = true
	m.View()
	if m.reopenOffered() {
		t.Fatal("deleted channel offers reopen")
	}
}

func TestChatReopenDoesNotTakeOverNick(t *testing.T) {
	m, _, n := disconnectedReopen(t)
	takes := 0
	m.opts.TakeOver = func() tea.Cmd { takes++; return nil }
	m.Update(NickHeldMsg{})
	if m.overlay == nil || m.overlay.kind != overlayTakeOver {
		t.Fatal("nickname refusal lost its explicit prompt")
	}
	m.reopen()
	if *n != 0 || takes != 0 {
		t.Fatal("reopen bypassed nickname prompt")
	}
}

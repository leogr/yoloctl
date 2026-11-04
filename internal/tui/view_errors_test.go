package tui

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

const rejectedViewMessage = "session build/core mismatch; quit the old agent session and attach again with the selected build, or explicitly restart its box"

// Speak the real admission protocol over the inherited private descriptor.
// The diagnostic never comes from PTY stdout or an injected model field.
func TestRejectedViewChild(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_REJECTED_VIEW") != "1" {
		return
	}
	f, err := termio.InheritedView()
	if err != nil || f == nil {
		os.Exit(80)
	}
	if _, err := f.Write([]byte{'h', 0, 1, 6}); err != nil {
		os.Exit(81)
	}
	var header [3]byte
	if _, err := io.ReadFull(f, header[:]); err != nil || header[0] != 'v' {
		os.Exit(82)
	}
	if _, err := io.CopyN(io.Discard, f, int64(binary.BigEndian.Uint16(header[1:]))); err != nil {
		os.Exit(83)
	}
	packet := make([]byte, 3+len(rejectedViewMessage))
	packet[0] = '!'
	binary.BigEndian.PutUint16(packet[1:3], uint16(len(rejectedViewMessage)))
	copy(packet[3:], rejectedViewMessage)
	if _, err := f.Write(packet); err != nil {
		os.Exit(84)
	}
	time.Sleep(time.Minute) // the frontend must end this failed local client
	os.Exit(85)
}

func rejectedView(t *testing.T, m *model) *tabs.Tab {
	t.Helper()
	s, err := term.Start(term.Options{
		Argv:        []string{os.Args[0], "-test.run=^TestRejectedViewChild$"},
		Env:         append(os.Environ(), "YOLOCTL_TEST_REJECTED_VIEW=1"),
		TrustedView: true, Rows: m.viewRows(), Cols: m.width,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	tab := testTab(s, "yoloctl attach example", false)
	m.tabs.Add(tab)
	m.focusPalette = false
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("rejected attachment did not finish")
	}
	return tab
}

func assertPaletteViewError(t *testing.T, m *model) {
	t.Helper()
	_, _ = m.Update(wakeMsg{})
	if !m.focusPalette {
		t.Fatal("failed attachment did not reveal the palette result")
	}
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	if strings.Contains(rows[0], "mismatch") || strings.Contains(m.status, "mismatch") {
		t.Fatalf("failure leaked into one-line status: %q", rows[0])
	}
	// Remove layout whitespace: native wrapping can split a word at any column.
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	body := strings.Join(rows[1:1+m.overlayContentRows()], "\n")
	if !strings.Contains(compact(body), compact(rejectedViewMessage)) {
		t.Fatalf("palette result lost or clipped the failure: %q", body)
	}
	if !strings.Contains(body, "attach example") {
		t.Fatalf("palette result lost command context: %q", body)
	}
}

func TestRejectedAttachmentErrorAppearsBelowPaletteInput(t *testing.T) {
	m := newTestModel(t, 64, 24)
	tab := rejectedView(t, m)
	_, _ = m.Update(tabExitMsg{session: tab.Focused().Session})
	assertPaletteViewError(t, m)
	before := m.console.GridText()
	_, _ = m.Update(wakeMsg{})
	if m.console.GridText() != before {
		t.Fatal("a later wake duplicated or erased the failure")
	}
}

func TestRejectedAttachmentWaitsForBusyPalette(t *testing.T) {
	m := newTestModel(t, 64, 24)
	started, err := m.console.Run(nil, "$ confirmation", "sh", "-c", "printf WAITING; read answer; printf 'answer:%s' \"$answer\"")
	if !started || err != nil {
		t.Fatalf("start confirmation: %v, %v", started, err)
	}
	_ = rejectedView(t, m)
	_, _ = m.Update(wakeMsg{})
	if !m.console.Busy() || strings.Contains(m.console.GridText(), "mismatch") || m.focusPalette {
		t.Fatal("failure interrupted the running palette command")
	}
	if err := m.console.SendInput([]byte("yes\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.console.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.console.Busy() || !strings.Contains(m.console.GridText(), "answer:yes") {
		t.Fatal("pending attachment failure broke the running command")
	}
	assertPaletteViewError(t, m)
}

func TestRejectedBackgroundAttachmentDoesNotStealFocus(t *testing.T) {
	m := newTestModel(t, 64, 24)
	failed := rejectedView(t, m)
	s, err := term.Start(term.Options{Argv: []string{"sh", "-c", "read answer"}, Rows: 20, Cols: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	live := testTab(s, "live tab", false)
	m.tabs.Add(live)
	_, _ = m.Update(wakeMsg{})
	if m.focusPalette || m.tabs.Current() != live || !failed.Badge {
		t.Fatal("background failure changed focus or failed to mark its tab")
	}
	m.enterPalette()
	assertPaletteViewError(t, m)
}

func TestStandaloneRejectedAttachmentRetainsActionableError(t *testing.T) {
	m := newTestModel(t, 64, 24)
	m.standalone = true
	tab := rejectedView(t, m)
	_, _ = m.Update(wakeMsg{})
	if m.focusPalette || !strings.Contains(m.status, rejectedViewMessage) {
		t.Fatal("standalone error lost its visible notice")
	}
	if err := tab.Focused().Session.Wait(time.Second); err == nil || !strings.Contains(err.Error(), rejectedViewMessage) {
		t.Fatalf("standalone result lost the actionable error: %v", err)
	}
}

package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// brokerChild sends one preview request for generation 1 and prints every
// frame the host sends.
const brokerChild = `
import json, os, struct, time
fd = int(os.environ["YOLOCTL_TUI_IMAGE_FD"])
os.set_blocking(fd, True)
f = os.fdopen(fd, "r+b", buffering=0)
body = json.dumps({"v":1,"op":"preview","generation":1,"request":7,
    "room_id":"0123456789abcdef0123456789abcdef","disk_id":"0123456789abcdef0123456789abcdef",
    "human_id":"0123456789abcdef0123456789abcdef","channel_id":"0123456789abcdef0123456789abcdef","channel":"#space",
    "message_id":"01234567-89ab-cdef-0123-456789abcdef","artifact":"01234567-89ab-cdef-0123-456789abcdef",
    "bytes":1024,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}).encode()
f.write(struct.pack(">I", len(body)) + body)
while True:
    hdr = f.read(4)
    if len(hdr) < 4:
        time.sleep(30)
    n = struct.unpack(">I", hdr)[0]
    print(f.read(n).decode(), flush=True)
`

func brokerModel(t *testing.T) (*model, *term.Session) {
	t.Helper()
	m := newTestModel(t, 200, 12)
	m.hostGeometry.width, m.hostGeometry.height = 9, 18
	s, err := term.Start(term.Options{Rows: m.viewRows(), Cols: m.width,
		Argv: []string{"python3", "-c", brokerChild}, OnImage: m.imageHandler()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "chat", false))
	m.focusPalette = false
	return m, s
}

// pump runs updates until the child has queued its request or printed want.
func pump(t *testing.T, m *model, s *term.Session, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out; child shows %q", s.GridText())
		}
		_, _ = m.Update(wakeMsg{})
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBrokerProbesOnFirstChatPaneAndAnswersHeldRequests(t *testing.T) {
	for name, c := range map[string]struct {
		version, graphics, code string
		supported               bool
	}{
		"supported terminal": {"kitty(0.35.2)", "OK", "unavailable", true},
		"nested libghostty":  {"libghostty", "", "unsupported", false},
	} {
		m, s := brokerModel(t)
		pump(t, m, s, func() bool { return m.imageHost.state == imageProbing && len(m.images.pending) == 1 })
		_, _ = m.Update(tea.TerminalVersionMsg{Name: c.version})
		if c.graphics != "" {
			_, _ = m.Update(graphicsReply(probeQueryID(t, m), c.graphics))
		}
		pump(t, m, s, func() bool { return strings.Contains(s.GridText(), `"code":"`+c.code+`"`) })
		text := s.GridText()
		capability := strings.Contains(text, `"op":"capability"`) && strings.Contains(text, `"generation":1`)
		if !capability || strings.Contains(text, `"supported":true`) != c.supported {
			t.Fatalf("%s: child saw %q", name, text)
		}
		if !strings.Contains(text, `"request":7`) {
			t.Fatalf("%s: request not answered: %q", name, text)
		}
	}
}

func TestBrokerReleasesOnExitAndBoundsItsInbox(t *testing.T) {
	m, s := brokerModel(t)
	m.imageRegistry.Activate(s, chatimage.Placement{ID: 5, Rows: 1, Cols: 1, Token: "t1"}, "chip")
	if m.imageRegistry.ReleaseToken(s, "t2") {
		t.Fatal("a stale token released the preview")
	}
	_, _ = m.Update(tabExitMsg{session: s})
	if _, ok := m.imageRegistry.Active(s); ok {
		t.Fatal("ended session kept its preview")
	}
	handle := m.imageHandler()
	for i := 0; i < imageInboxCap+10; i++ {
		handle(s, chatimage.Frame{Op: chatimage.OpRelease, Token: "x"})
	}
	m.images.mu.Lock()
	n := len(m.images.inbox)
	m.images.mu.Unlock()
	if n > imageInboxCap {
		t.Fatalf("inbox holds %d frames", n)
	}
}

// A pane whose capability frame could not be queued is told again on a
// later update (review step 6 condition 2).
func TestBrokerResendsADroppedCapability(t *testing.T) {
	m, s := brokerModel(t)
	m.imageHost.state = imageSupported
	m.images.outbox = make(chan imageMsg) // no room: every send is dropped
	_, _ = m.Update(wakeMsg{})
	if m.images.told[s] != 0 {
		t.Fatal("dropped capability counted as told")
	}
	m.images.outbox = nil // the next send starts a working sender
	_, _ = m.Update(wakeMsg{})
	if m.images.told[s] != m.images.advertised.generation {
		t.Fatal("capability not resent")
	}
	pump(t, m, s, func() bool { return strings.Contains(s.GridText(), `"op":"capability"`) })
}

// A pane that stops reading costs one write deadline, then its channel is
// closed so later frames fail at once (review step 6 condition 1).
func TestBrokerClosesAChannelAfterAFailedSend(t *testing.T) {
	m := newTestModel(t, 80, 8)
	s, err := term.Start(term.Options{Rows: 4, Cols: 80,
		Argv: []string{"sleep", "30"}, OnImage: m.imageHandler()}) // never reads
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	big := chatimage.Frame{Op: chatimage.OpReady, Generation: 1, Request: 1, Token: strings.Repeat("t", 64), ID: 1, Cols: 1, Rows: 1}
	deadline := time.Now().Add(20 * time.Second)
	// Fill the socket until a write hits its deadline.
	for s.SendImage(big) == nil {
		if time.Now().After(deadline) {
			t.Fatal("socket never filled")
		}
	}
	// The broker's sender meets the same full socket and closes the channel.
	if !m.sendImage(s, big) {
		t.Fatal("frame not queued")
	}
	for time.Now().Before(deadline) {
		start := time.Now()
		if err := s.SendImage(big); err != nil && time.Since(start) < 200*time.Millisecond {
			return // closed: fails at once
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("channel of a non-reading pane was not closed")
}

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
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
    "bytes":1024,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","chip":"[file a.png]"}).encode()
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
	if _, ok := m.imageRegistry.ReleaseToken(s, "t2"); ok {
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

// rawOutput runs cmd and every command it batches or sequences, in order,
// collecting raw host output; other messages go back into m when m is set.
func rawOutput(cmd tea.Cmd, m ...*model) string {
	if cmd == nil {
		return ""
	}
	msg := cmd()
	if raw, ok := msg.(tea.RawMsg); ok {
		return fmt.Sprint(raw.Msg)
	}
	if v := reflect.ValueOf(msg); v.IsValid() && v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out strings.Builder
		for i := 0; i < v.Len(); i++ {
			out.WriteString(rawOutput(v.Index(i).Interface().(tea.Cmd), m...))
		}
		return out.String()
	}
	if len(m) > 0 && msg != nil {
		_, _ = m[0].Update(msg)
	}
	return ""
}

func previewFrame(request uint64) chatimage.Frame {
	return chatimage.Frame{V: 1, Op: chatimage.OpPreview, Generation: 1, Request: request,
		RoomID: "0123456789abcdef0123456789abcdef", DiskID: "0123456789abcdef0123456789abcdef",
		HumanID: "0123456789abcdef0123456789abcdef", ChannelID: "0123456789abcdef0123456789abcdef", Channel: "#space",
		MessageID: "01234567-89ab-cdef-0123-456789abcdef", Artifact: "01234567-89ab-cdef-0123-456789abcdef",
		Bytes: 1, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Chip: "[file cat.png · 1 KiB]"}
}

// supportedBroker is a broker whose host terminal supports previews and
// whose child prints every frame it receives (it sends nothing itself).
func supportedBroker(t *testing.T, read func(context.Context, string, chatimage.Frame) ([]byte, error)) (*model, *term.Session) {
	t.Helper()
	old := ReadChatArtifact
	ReadChatArtifact = read
	t.Cleanup(func() { ReadChatArtifact = old })
	m := newTestModel(t, 200, 12)
	m.hostGeometry.width, m.hostGeometry.height = 10, 20
	m.imageHost.state = imageSupported
	s, err := term.Start(term.Options{Rows: m.viewRows(), Cols: m.width,
		Argv:    []string{"python3", "-c", strings.Replace(brokerChild, "f.write(struct.pack", "0 and f.write(struct.pack", 1)},
		OnImage: m.imageHandler()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "chat", false))
	m.focusPalette = false
	_, _ = m.Update(wakeMsg{}) // advertise generation 1
	return m, s
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBrokerFetchesPreparesTransmitsAndRegisters(t *testing.T) {
	data := smallPNG(t)
	var space string
	m, s := supportedBroker(t, func(_ context.Context, sp string, _ chatimage.Frame) ([]byte, error) {
		space = sp
		return data, nil
	})
	m.imageHandler()(s, previewFrame(7))
	var raw strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw.WriteString(rawOutput(m.syncImages(), m)) // only here: no other path consumes the job
		if _, ok := m.imageRegistry.Active(s); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	p, _ := m.imageRegistry.Active(s)
	if p.Cols != 4 || p.Rows != 1 || p.Token == "" || space != "" {
		t.Fatalf("placement %+v space %q", p, space)
	}
	if !strings.Contains(raw.String(), "\x1b_Ga=T,f=100,t=d,q=2,U=1,i="+strconv.Itoa(int(p.ID))+",c=4,r=1,") {
		t.Fatalf("no transmit for id %d: %q", p.ID, raw.String())
	}
	if m.imageLedger.Chip(p.ID) != "[file cat.png · 1 KiB]" {
		t.Fatal("chip not recorded")
	}
	pump(t, m, s, func() bool {
		return strings.Contains(s.GridText(), `"op":"ready"`) && strings.Contains(s.GridText(), p.Token)
	})
	// The pane's release deletes exactly that image.
	m.imageHandler()(s, chatimage.Frame{V: 1, Op: chatimage.OpRelease, Token: p.Token})
	if out := rawOutput(m.syncImages()); out != chatimage.DeleteSequence(p.ID) {
		t.Fatalf("release output %q", out)
	}
}

func TestBrokerAnswersBusyStaleAndFailures(t *testing.T) {
	release := make(chan struct{})
	m, s := supportedBroker(t, func(ctx context.Context, _ string, f chatimage.Frame) ([]byte, error) {
		switch f.Request {
		case 1:
			<-release
			return smallPNG(t), nil
		case 3:
			return []byte("GIF89a"), nil
		}
		return nil, errors.New("denied")
	})
	m.imageHandler()(s, previewFrame(1))
	_ = m.syncImages()
	m.imageHandler()(s, previewFrame(2)) // the slot is taken
	_ = m.syncImages()
	pump(t, m, s, func() bool { return strings.Contains(s.GridText(), `"request":2,"code":"busy"`) })
	m.hostGeometry.width = 12 // the measurement changed while reading
	close(release)
	pump(t, m, s, func() bool { _ = m.syncImages(); return strings.Contains(s.GridText(), `"request":1,"code":"stale"`) })
	m.hostGeometry.width = 10
	for _, c := range []struct {
		request uint64
		code    string
	}{{3, "not_png"}, {4, "failed"}} {
		m.imageHandler()(s, previewFrame(c.request))
		pump(t, m, s, func() bool {
			_ = m.syncImages()
			return strings.Contains(s.GridText(), `"request":`+strconv.Itoa(int(c.request))+`,"code":"`+c.code+`"`)
		})
	}
	if _, ok := m.imageRegistry.Active(s); ok {
		t.Fatal("a failed preview was registered")
	}
}

// Review step 9 conditions: ready only after the transmit write, owned
// images freed when the TUI exits, a pane's job canceled when the pane ends.
func TestBrokerReadyFollowsTransmitAndCleansUp(t *testing.T) {
	data := smallPNG(t)
	m, s := supportedBroker(t, func(context.Context, string, chatimage.Frame) ([]byte, error) { return data, nil })
	m.imageHandler()(s, previewFrame(7))
	var cmd tea.Cmd
	deadline := time.Now().Add(10 * time.Second)
	for cmd == nil || !strings.Contains(rawOutputPeek(cmd), "a=T") {
		cmd = m.syncImages()
		if time.Now().After(deadline) {
			t.Fatal("no transmit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(s.GridText(), `"op":"ready"`) {
		t.Fatal("ready sent before the transmit was written")
	}
	_ = rawOutput(cmd, m) // the transmit, then the ready message
	pump(t, m, s, func() bool { return strings.Contains(s.GridText(), `"op":"ready"`) })
	p, _ := m.imageRegistry.Active(s)
	var out strings.Builder
	m.deleteOwnedImages(&out)
	if out.String() != chatimage.DeleteSequence(p.ID) {
		t.Fatalf("exit cleanup %q", out.String())
	}
}

// rawOutputPeek runs cmd only far enough to see a raw write it batches or
// sequences first, without delivering later messages.
func rawOutputPeek(cmd tea.Cmd) string { return rawOutput(cmd) }

func TestBrokerCancelsAnEndedPanesJob(t *testing.T) {
	canceled := make(chan struct{})
	m, s := supportedBroker(t, func(ctx context.Context, _ string, _ chatimage.Frame) ([]byte, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	m.imageHandler()(s, previewFrame(9))
	_ = m.syncImages()
	if m.images.job == nil {
		t.Fatal("job not started")
	}
	_, _ = m.Update(tabExitMsg{session: s})
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("job of an ended pane was not canceled")
	}
}

// seams.images off gives chat panes no image channel at all.
func TestImagesSeamOffGivesChatNoChannel(t *testing.T) {
	m := newTestModel(t, 80, 8)
	if m.chatImageHandler([]string{"chat"}) == nil {
		t.Fatal("chat pane has no image channel with previews on")
	}
	off := false
	m.cfg.Seams.Images = &off
	if m.chatImageHandler([]string{"chat"}) != nil {
		t.Fatal("chat pane has an image channel with seams.images off")
	}
	if m.chatImageHandler([]string{"space", "view"}) != nil {
		t.Fatal("a non-chat pane has an image channel")
	}
}

func TestBrokerReshowsACachedPreviewWithoutAnotherRead(t *testing.T) {
	data := smallPNG(t)
	var reads atomic.Int32
	m, s := supportedBroker(t, func(context.Context, string, chatimage.Frame) ([]byte, error) {
		reads.Add(1)
		return data, nil
	})
	show := func(request uint64) (chatimage.Placement, string) {
		t.Helper()
		m.imageHandler()(s, previewFrame(request))
		var raw strings.Builder
		deadline := time.Now().Add(10 * time.Second)
		for {
			raw.WriteString(rawOutput(m.syncImages(), m))
			if p, ok := m.imageRegistry.Active(s); ok {
				return p, raw.String()
			}
			if time.Now().After(deadline) {
				t.Fatal("preview never registered")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	hide := func(p chatimage.Placement) {
		t.Helper()
		m.imageHandler()(s, chatimage.Frame{V: 1, Op: chatimage.OpRelease, Token: p.Token})
		rawOutput(m.syncImages())
	}
	first, _ := show(7)
	hide(first)
	again, raw := show(8)
	if reads.Load() != 1 {
		t.Fatalf("%d reads, want 1: a cached preview was read again", reads.Load())
	}
	if again.ID == first.ID || again.Token == first.Token ||
		!strings.Contains(raw, "\x1b_Ga=T,f=100,t=d,q=2,U=1,i="+strconv.Itoa(int(again.ID))+",c=4,r=1,") {
		t.Fatalf("cached preview not transmitted under a new ID: %+v %q", again, raw)
	}
	// An ended chat pane clears the cache: the next show reads again.
	hide(again)
	m.endImagePane(s)
	show(9)
	if reads.Load() != 2 {
		t.Fatalf("%d reads after a pane ended, want 2", reads.Load())
	}
}

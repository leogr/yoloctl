package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/chatui"
)

// runStandalone runs cmd and everything it batches or sequences, in order,
// returning raw terminal output and the other messages.
func runStandalone(cmd tea.Cmd) (string, []tea.Msg) {
	if cmd == nil {
		return "", nil
	}
	msg := cmd()
	if raw, ok := msg.(tea.RawMsg); ok {
		return fmt.Sprint(raw.Msg), nil
	}
	if v := reflect.ValueOf(msg); v.IsValid() && v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out strings.Builder
		var msgs []tea.Msg
		for i := 0; i < v.Len(); i++ {
			o, m := runStandalone(v.Index(i).Interface().(tea.Cmd))
			out.WriteString(o)
			msgs = append(msgs, m...)
		}
		return out.String(), msgs
	}
	if msg == nil {
		return "", nil
	}
	return "", []tea.Msg{msg}
}

var graphicsQueryID = regexp.MustCompile(`\x1b_Gi=(\d+),`)

// probedImages is a standalone owner whose probe met a supported terminal
// with 10×20 pixel cells; read serves artifact reads.
func probedImages(t *testing.T, read func(context.Context, string, chatimage.Frame) ([]byte, error)) (*standaloneImages, chan tea.Msg) {
	t.Helper()
	img := newStandaloneImages("dev", read)
	sent := make(chan tea.Msg, 4)
	img.send = func(m tea.Msg) { sent <- m }
	if img.start() == nil || img.probe == nil { // its tick is not run: it is the budget
		t.Fatal("no probe started")
	}
	match := graphicsQueryID.FindStringSubmatch(img.probe.GraphicsQuery())
	if match == nil {
		t.Fatal("no graphics query")
	}
	id, _ := strconv.Atoi(match[1])
	for _, m := range []tea.Msg{tea.TerminalVersionMsg{Name: "ghostty 1.3.1"}, uv.KittyGraphicsEvent{Options: kitty.Options{ID: id}, Payload: []byte("OK")}} {
		if cmd, _ := img.update(m); cmd != nil {
			t.Fatal("capability before the cell size")
		}
	}
	cmd, ok := img.update(uv.CellSizeEvent{Width: 10, Height: 20})
	_, msgs := runStandalone(cmd)
	want := chatui.PreviewCapabilityMsg{Supported: true, Generation: 1, MaxCols: 60, MaxRows: 12}
	if !ok || len(msgs) != 1 || msgs[0] != want {
		t.Fatalf("capability %v", msgs)
	}
	return img, sent
}

func standalonePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func standaloneRequest(request uint64) standalonePreviewMsg {
	req := chatui.PreviewRequest{Request: request, Generation: 1, Channel: "#space", MessageID: "01234567-89ab-cdef-0123-456789abcdef",
		Artifact: chatui.ArtifactRef{ID: "01234567-89ab-cdef-0123-456789abcdef"}, Chip: "[file cat.png · 1 KiB]"}
	return standalonePreviewMsg{req: req, frame: chatimage.Frame{Artifact: req.Artifact.ID, SHA256: "h", RoomID: "r", DiskID: "d", HumanID: "u", ChannelID: "c"}}
}

// shown answers request through a finished job and returns the transmit and
// ready reply.
func showStandalone(t *testing.T, img *standaloneImages, sent chan tea.Msg, request uint64) (string, chatui.PreviewReadyMsg) {
	t.Helper()
	cmd, _ := img.update(standaloneRequest(request))
	if cmd == nil {
		select {
		case done := <-sent:
			cmd, _ = img.update(done)
		case <-time.After(10 * time.Second):
			t.Fatal("job never finished")
		}
	}
	out, msgs := runStandalone(cmd)
	if len(msgs) != 1 {
		t.Fatalf("replies %v", msgs)
	}
	ready, ok := msgs[0].(chatui.PreviewReadyMsg)
	if !ok {
		t.Fatalf("reply %#v", msgs[0])
	}
	return out, ready
}

func TestStandaloneProbeFailureAdvertisesNoSupport(t *testing.T) {
	img := newStandaloneImages("dev", nil)
	img.start()
	img.update(uv.CellSizeEvent{Width: 10, Height: 20})
	cmd, _ := img.update(tea.TerminalVersionMsg{Name: "tmux 3.4"})
	_, msgs := runStandalone(cmd)
	if len(msgs) != 1 || msgs[0].(chatui.PreviewCapabilityMsg).Supported {
		t.Fatalf("capability %v", msgs)
	}
	cmd, _ = img.update(standaloneRequest(1))
	if _, msgs := runStandalone(cmd); len(msgs) != 1 || msgs[0].(chatui.PreviewErrorMsg).Code != "unsupported" {
		t.Fatalf("reply %v", msgs)
	}
	// Silence is a failure too.
	img = newStandaloneImages("dev", nil)
	img.start()
	cmd, _ = img.update(standaloneProbeTick(1))
	if _, msgs := runStandalone(cmd); len(msgs) != 1 || msgs[0].(chatui.PreviewCapabilityMsg).Supported {
		t.Fatalf("silent probe: %v", msgs)
	}
}

func TestStandaloneReadsPreparesTransmitsAndReleases(t *testing.T) {
	data := standalonePNG(t)
	reads := 0
	var space string
	img, sent := probedImages(t, func(_ context.Context, sp string, f chatimage.Frame) ([]byte, error) {
		reads++
		space = sp
		return data, nil
	})
	out, ready := showStandalone(t, img, sent, 7)
	if ready.Request != 7 || ready.Generation != 1 || ready.Cols != 4 || ready.Rows != 1 || ready.Token == "" || space != "dev" {
		t.Fatalf("ready %+v space %q", ready, space)
	}
	if !strings.HasPrefix(out, "\x1b_Ga=T,f=100,t=d,q=2,U=1,i="+strconv.Itoa(int(ready.ID))+",c=4,r=1,") {
		t.Fatalf("transmit %q", out)
	}
	// A stale or unknown release deletes nothing; the right token deletes
	// exactly that image.
	if cmd, _ := img.update(standaloneReleaseMsg("other")); cmd != nil {
		t.Fatal("unknown token released the image")
	}
	cmd, _ := img.update(standaloneReleaseMsg(ready.Token))
	if out, _ := runStandalone(cmd); out != chatimage.DeleteSequence(ready.ID) {
		t.Fatalf("release %q", out)
	}
	// Shown again from the cache: no second read, a new ID and token.
	out, again := showStandalone(t, img, sent, 8)
	if reads != 1 || again.ID == ready.ID || again.Token == ready.Token || !strings.Contains(out, "i="+strconv.Itoa(int(again.ID))+",") {
		t.Fatalf("cached show: reads %d ready %+v", reads, again)
	}
	// Another preview replaces it; exit deletes what is still shown.
	img.cache.Clear()
	out, third := showStandalone(t, img, sent, 9)
	if !strings.HasPrefix(out, chatimage.DeleteSequence(again.ID)) {
		t.Fatalf("replacement did not delete the previous image: %q", out)
	}
	var exit bytes.Buffer
	img.close(&exit)
	if exit.String() != chatimage.DeleteSequence(third.ID) {
		t.Fatalf("exit output %q", exit.String())
	}
}

func TestStandaloneAnswersBusyStaleAndFailures(t *testing.T) {
	release := make(chan struct{})
	img, sent := probedImages(t, func(ctx context.Context, _ string, _ chatimage.Frame) ([]byte, error) {
		<-release
		return []byte("not a png"), nil
	})
	if cmd, _ := img.update(standaloneRequest(1)); cmd != nil {
		t.Fatal("first request did not start a job")
	}
	code := func(cmd tea.Cmd) string {
		_, msgs := runStandalone(cmd)
		if len(msgs) != 1 {
			t.Fatalf("replies %v", msgs)
		}
		return msgs[0].(chatui.PreviewErrorMsg).Code
	}
	cmd, _ := img.update(standaloneRequest(2))
	if c := code(cmd); c != "busy" {
		t.Fatalf("second request: %s", c)
	}
	stale := standaloneRequest(3)
	stale.req.Generation = 9
	cmd, _ = img.update(stale)
	if c := code(cmd); c != "stale" {
		t.Fatalf("old generation: %s", c)
	}
	close(release)
	cmd, _ = img.update(<-sent)
	if c := code(cmd); c != "not_png" {
		t.Fatalf("decode failure: %s", c)
	}

	// A cell size change while a job runs makes its result stale and starts
	// a new generation.
	img, sent = probedImages(t, func(context.Context, string, chatimage.Frame) ([]byte, error) {
		return nil, errors.New("read failed")
	})
	img.update(standaloneRequest(4))
	done := <-sent
	cmd, _ = img.update(uv.CellSizeEvent{Width: 12, Height: 24})
	if _, msgs := runStandalone(cmd); len(msgs) != 1 || msgs[0].(chatui.PreviewCapabilityMsg).Generation != 2 {
		t.Fatalf("cell change: %v", msgs)
	}
	cmd, _ = img.update(done)
	if c := code(cmd); c != "stale" {
		t.Fatalf("result after a cell change: %s", c)
	}
	img.update(standaloneRequest(5)) // generation 1 is gone
	req := standaloneRequest(6)
	req.req.Generation = 2
	img.update(req)
	cmd, _ = img.update(<-sent)
	if c := code(cmd); c != "failed" {
		t.Fatalf("read failure: %s", c)
	}
}

func TestStandaloneFramesShowOnlyTheShownPreview(t *testing.T) {
	data := standalonePNG(t)
	img, sent := probedImages(t, func(context.Context, string, chatimage.Frame) ([]byte, error) { return data, nil })
	cell := func(id uint32) string {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%c%c%c\x1b[m", uint8(id>>16), uint8(id>>8), uint8(id), kitty.Placeholder, kitty.Diacritic(0), kitty.Diacritic(0))
	}
	forged := "topic " + cell(0x0a0b0c)
	if got := img.filter(forged); strings.ContainsRune(got, kitty.Placeholder) {
		t.Fatalf("forged cell displayed with no preview: %q", got)
	}
	_, ready := showStandalone(t, img, sent, 1)
	frame := "header\n" + cell(ready.ID) + "\n" + forged
	got := strings.Split(img.filter(frame), "\n")
	if !strings.ContainsRune(got[1], kitty.Placeholder) || strings.ContainsRune(got[2], kitty.Placeholder) || got[0] != "header" {
		t.Fatalf("filtered frame %q", got)
	}
}

// standaloneInner records what reaches the chat model.
type standaloneInner struct{ got []tea.Msg }

func (s *standaloneInner) Init() tea.Cmd { return nil }
func (s *standaloneInner) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	s.got = append(s.got, msg)
	return s, nil
}
func (s *standaloneInner) View() tea.View { return tea.NewView("chat") }

func TestStandaloneChatKeepsImageMessagesFromChat(t *testing.T) {
	inner := &standaloneInner{}
	c := newStandaloneImages("dev", nil).wrap(inner)
	if c.Init() == nil {
		t.Fatal("no probe at start")
	}
	c.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: 1}, Payload: []byte("OK")})
	c.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	c.Update(tea.TerminalVersionMsg{Name: "ghostty 1.3.1"})
	c.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if len(inner.got) != 2 {
		t.Fatalf("chat saw %#v", inner.got)
	}
	if _, ok := inner.got[0].(tea.TerminalVersionMsg); !ok {
		t.Fatalf("chat saw %#v", inner.got)
	}

	// The version reply can be the probe's last signal: its capability must
	// still reach chat.
	inner = &standaloneInner{}
	img := newStandaloneImages("dev", nil)
	c = img.wrap(inner)
	c.Init()
	id, _ := strconv.Atoi(graphicsQueryID.FindStringSubmatch(img.probe.GraphicsQuery())[1])
	c.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: id}, Payload: []byte("OK")})
	c.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	_, cmd := c.Update(tea.TerminalVersionMsg{Name: "ghostty 1.3.1"})
	_, msgs := runStandalone(cmd)
	if len(msgs) != 1 || !msgs[0].(chatui.PreviewCapabilityMsg).Supported {
		t.Fatalf("capability after the version reply: %v", msgs)
	}
	_, cmd = c.Update(tea.FocusMsg{})
	if out, _ := runStandalone(cmd); out != "\x1b[16t" {
		t.Fatalf("focus output %q", out)
	}
}

func TestOnlyAStandaloneViewOwnsTerminalImages(t *testing.T) {
	a, b, err := chatimage.ChannelPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	withChannel := &chatPreviews{conn: chatimage.NewConn(a, false)}
	for _, c := range []struct {
		previews *chatPreviews
		tui      string
		enabled  bool
		want     bool
	}{
		{&chatPreviews{}, "", true, true},
		{&chatPreviews{}, "1", true, false}, // inside a TUI without a channel: chips only
		{withChannel, "1", true, false},     // the TUI channel path
		{&chatPreviews{}, "", false, false}, // seams.images off
	} {
		if got := ownsTerminalImages(c.previews, c.tui, c.enabled); got != c.want {
			t.Errorf("ownsTerminalImages(channel=%v, YOLOCTL_TUI=%q, enabled=%v) = %v", c.previews.conn != nil, c.tui, c.enabled, got)
		}
	}
}

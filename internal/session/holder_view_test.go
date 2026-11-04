package session

import (
	"bytes"
	"context"
	"image/color"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

type screenClient struct {
	mu       sync.Mutex
	vt       *terminal.Terminal
	ready    chan struct{}
	done     chan error
	sizes    chan terminal.Geometry
	colors   chan terminal.Colors
	detach   func()
	input    *io.PipeWriter
	effects  []byte
	copies   []termio.OSC52
	clientID string
	inputs   []InputActivity
}

func attachScreen(t *testing.T, ln *pipeListener) *screenClient {
	t.Helper()
	server, peer := net.Pipe()
	ln.connections <- server
	t.Cleanup(func() { _ = server.Close() })
	return attachScreenConn(t, peer)
}

func attachScreenConn(t *testing.T, peer net.Conn) *screenClient {
	t.Helper()
	r, w := io.Pipe()
	c := &screenClient{ready: make(chan struct{}), done: make(chan error, 1), sizes: make(chan terminal.Geometry, 4), colors: make(chan terminal.Colors, 4), input: w, detach: func() { _ = peer.Close() }}
	go func() {
		c.done <- Attach(context.Background(), peer, AttachIO{Stdin: r, Resize: c.sizes, Colors: c.colors, OnEvent: func(e ViewEvent) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if e.ClientID != "" {
				c.clientID = e.ClientID
			}
			if e.Input != nil {
				c.inputs = append(c.inputs, *e.Input)
			}
			if len(e.Snapshot) > 0 {
				var err error
				c.vt, err = terminal.RestoreSnapshot(context.Background(), e.Snapshot, nil)
				close(c.ready)
				return err
			}
			if e.Size.Cols > 0 {
				return c.vt.ResizeGeometry(e.Size)
			}
			if e.Colors != nil {
				for i, v := range e.Colors.Defaults {
					if v != nil {
						c.vt.SetColor(i, v)
					}
				}
				for i, v := range e.Colors.Palette {
					if v != nil {
						_ = c.vt.SetPaletteColor(i, v)
					}
				}
			}
			if e.EndHold {
				c.vt.EndHold()
			}
			if e.Clipboard != nil {
				c.copies = append(c.copies, *e.Clipboard)
			}
			c.effects = append(c.effects, e.Data...)
			_, err := c.vt.Write(e.Data)
			return err
		}})
	}()
	t.Cleanup(func() {
		_ = peer.Close()
		_ = r.Close()
		_ = w.Close()
		<-c.done
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.vt != nil {
			_ = c.vt.Close()
		}
	})
	select {
	case <-c.ready:
	case err := <-c.done:
		c.done <- err
		t.Fatalf("view handshake: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("view handshake timed out")
	}
	return c
}

func TestPeerActivityFromEveryViewLeavesPassthroughUnchanged(t *testing.T) {
	h, ln := fanoutHolder(t)
	h.broadcast([]byte("ready"))
	first, second := attachScreen(t, ln), attachScreen(t, ln)
	raw, _ := pipeAttach(t, ln)
	expectAttachFrame(t, raw, frameHello, []byte{protocolVersion})
	expectAttachFrame(t, raw, frameData, []byte("ready"))
	for i, send := range []func() error{
		func() error { _, err := first.input.Write([]byte("first-private-input")); return err },
		func() error { _, err := second.input.Write([]byte("second-private-input")); return err },
		func() error { return writeFrame(raw, frameData, []byte("raw-private-input")) },
	} {
		if err := send(); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*screenClient{first, second} {
			waitFor(t, "peer activity", func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()
				return len(c.inputs) == i+2 && c.inputs[i+1].Sequence == uint64(i+1)
			})
		}
	}
	if _, err := first.input.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "own cumulative activity", func() bool {
		first.mu.Lock()
		defer first.mu.Unlock()
		return len(first.inputs) == 5
	})
	first.mu.Lock()
	if first.inputs[1].Source != first.clientID || first.inputs[2].Source == first.clientID || first.inputs[3].Source == "" || strings.Contains(first.inputs[3].Source, "private-input") {
		t.Errorf("activity identities: %+v", first.inputs)
	}
	// Each source's count is its own queued payload, not the global total.
	if got := []uint64{first.inputs[1].Bytes, first.inputs[2].Bytes, first.inputs[3].Bytes, first.inputs[4].Bytes}; got[0] != 19 || got[1] != 20 || got[2] != 17 || got[3] != 24 || first.inputs[4].Source != first.clientID {
		t.Errorf("accepted bytes: %+v", first.inputs)
	}
	first.mu.Unlock()
	h.broadcast([]byte("visible"))
	// No activity frames or participant key contents enter compatibility output.
	expectAttachFrame(t, raw, frameData, []byte("visible"))
	late := attachScreen(t, ln)
	late.mu.Lock()
	defer late.mu.Unlock()
	if len(late.inputs) != 1 || late.inputs[0].Sequence != 4 || late.inputs[0].Bytes != 24 {
		t.Fatalf("snapshot activity: %+v", late.inputs)
	}
}

func TestManagedViewsShareResizesAndTransferMeasuredColors(t *testing.T) {
	h, ln := fanoutHolder(t)
	first, second := attachScreen(t, ln), attachScreen(t, ln)
	for _, step := range []struct {
		client *screenClient
		size   terminal.Geometry
	}{
		{first, terminal.Geometry{Cols: 90, Rows: 30, CellWidth: 19, CellHeight: 42}},
		{second, terminal.Geometry{Cols: 90, Rows: 30, CellWidth: 10, CellHeight: 20}},
		{second, terminal.Geometry{Cols: 40, Rows: 12}},
		{first, terminal.Geometry{Cols: 70, Rows: 20, CellWidth: 11, CellHeight: 22}},
	} {
		step.client.sizes <- step.size
		for _, client := range []*screenClient{first, second} {
			waitFor(t, "shared geometry", func() bool {
				client.mu.Lock()
				defer client.mu.Unlock()
				return client.vt.Geometry() == step.size
			})
		}
	}
	red, green, blue := &color.RGBA{R: 200, A: 255}, &color.RGBA{G: 200, A: 255}, &color.RGBA{B: 200, A: 255}
	a, b := terminal.Colors{}, terminal.Colors{}
	a.Palette[1], a.Palette[2], b.Palette[1] = red, blue, green
	first.colors <- a
	waitColor := func(c *screenClient, slot int, expected color.RGBA) {
		waitFor(t, "measured color", func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			got, _ := c.vt.PaletteColor(slot)
			return got == expected
		})
	}
	waitColor(second, 1, *red)
	second.colors <- b
	waitFor(t, "second measurement stored", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.clients) == 2 && h.clients[1].colors != nil
	})
	h.mu.Lock()
	stillFirst := h.colors.Known.Palette[1] != nil && *h.colors.Known.Palette[1] == *red
	h.mu.Unlock()
	if !stillFirst {
		t.Fatal("later viewer changed the active measured theme")
	}
	h.broadcast([]byte("\x1b]4;3;#112233\a"))
	waitColor(second, 3, color.RGBA{R: 17, G: 34, B: 51, A: 255})
	first.detach()
	waitColor(second, 1, *green)
	waitColor(second, 2, *blue)                                   // unmeasured entries keep prior measurements
	waitColor(second, 3, color.RGBA{R: 17, G: 34, B: 51, A: 255}) // app override survives
	late := attachScreen(t, ln)
	waitColor(late, 1, *green)
	late.mu.Lock()
	defer late.mu.Unlock()
	if late.vt.Geometry() != (terminal.Geometry{Cols: 70, Rows: 20, CellWidth: 11, CellHeight: 22}) {
		t.Fatal("late snapshot did not carry current geometry")
	}
}

func TestSnapshotAttachKeepsScreenBeyondRawRing(t *testing.T) {
	h, ln := fanoutHolder(t)
	h.mu.Lock()
	h.ring = NewRing(16)
	h.mu.Unlock()
	h.broadcast([]byte("\x1b[1;1HHEADER\x1b[4;1H" + strings.Repeat("x", 100) + "\x1b[6n"))
	first := attachScreen(t, ln)
	second := attachScreen(t, ln)
	for _, c := range []*screenClient{first, second} {
		c.mu.Lock()
		lines, err := c.vt.Lines(false, true, 0, 1)
		c.mu.Unlock()
		if err != nil || !strings.Contains(lines[0], "HEADER") {
			t.Fatalf("restored header: %v, %v", lines, err)
		}
	}
	h.broadcast([]byte("\x1b[2;3H\x1b[6nAFTER"))
	for _, c := range []*screenClient{first, second} {
		waitFor(t, "ordered live output", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return bytes.Contains(c.effects, []byte("AFTER")) })
	}
}

func TestMixedViewsDoNotAnswerQueriesOrRepeatEffects(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	raw := attachWhenReady(t, opts.AttachSocket)
	_, _ = raw.stdinW.Write([]byte("raw-ready"))
	waitFor(t, "raw input", func() bool { fs.mu.Lock(); defer fs.mu.Unlock(); return fs.stdin.String() == "raw-ready" })
	fs.emit("\x1b[2;3H\x1b[6n\a\x1b]52;c;Y29weQ==\aMARKER")
	waitFor(t, "single authoritative reply", func() bool { fs.mu.Lock(); defer fs.mu.Unlock(); return fs.stdin.String() == "raw-ready\x1b[2;3R" })
	waitFor(t, "raw output", func() bool { return strings.Contains(raw.output(), "MARKER") })
	if strings.Contains(raw.output(), "\x1b[6n") {
		t.Fatal("raw host received a terminal query")
	}
	// A managed viewer joins after the historical query/effects. Its snapshot
	// must not turn them into new output or responses.
	conn, err := net.Dial("unix", opts.AttachSocket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var vt *terminal.Terminal
	ready := make(chan struct{})
	done := make(chan error, 1)
	inputR, inputW := io.Pipe()
	defer func() { _ = inputR.Close() }()
	defer func() { _ = inputW.Close() }()
	go func() {
		done <- Attach(ctx, conn, AttachIO{Stdin: inputR, OnEvent: func(e ViewEvent) error {
			if len(e.Snapshot) > 0 {
				var err error
				vt, err = terminal.RestoreSnapshot(ctx, e.Snapshot, nil)
				close(ready)
				return err
			}
			if e.Size.Cols > 0 {
				return vt.ResizeGeometry(e.Size)
			}
			_, err := vt.Write(e.Data)
			return err
		}})
	}()
	<-ready
	_, _ = inputW.Write([]byte("managed-ready"))
	waitFor(t, "both clients interactive", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "raw-ready\x1b[2;3Rmanaged-ready"
	})
	fs.exit(0)
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = vt.Close()
	if err := raw.wait(t); err != nil {
		t.Fatal(err)
	}
}

func TestClipboardEventDoesNotInterruptPartialCanonicalControl(t *testing.T) {
	h, ln := fanoutHolder(t)
	c := attachScreen(t, ln)
	payload := strings.Repeat("YQ==", 20000)
	h.broadcast([]byte("\x1b]52;c;" + payload + "\a\x1b[38;2;"))
	h.broadcast([]byte("1;2;3mMARK"))
	waitFor(t, "clipboard and continued output", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.copies) == 1 && bytes.Contains(c.effects, []byte("MARK"))
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.copies[0].Payload != payload {
		t.Fatal("chunked clipboard changed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	got, _ := c.vt.Lines(false, false, 0, 1)
	want, _ := h.vt.Lines(false, false, 0, 1)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("replica control corrupted: %q != %q", got, want)
	}
}

func TestQueryBurstCoalescesBeforeBoundedInputQueue(t *testing.T) {
	h := &holder{input: make(chan []byte, 128)}
	if err := h.initView(24, 80); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.vt.Close() }()
	var queries strings.Builder
	for range 512 {
		queries.WriteString("\x1b[6n")
	}
	h.mu.Lock()
	_, err := h.vt.Write([]byte(queries.String()))
	h.flushRepliesLocked()
	h.mu.Unlock()
	if err != nil || h.viewErr != nil {
		t.Fatalf("ordinary query burst overflowed packet queue: %v / %v", err, h.viewErr)
	}
	var replies []byte
	for len(h.input) > 0 {
		replies = append(replies, (<-h.input)...)
	}
	if got := strings.Count(string(replies), "\x1b[1;1R"); got != 512 {
		t.Fatalf("replies=%d want 512", got)
	}
}

func TestHolderHistoryIsNotCappedByDefaultViewerPreference(t *testing.T) {
	h := &holder{}
	if err := h.initView(2, 12); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.vt.Close() }()
	if _, err := h.vt.Write([]byte(strings.Repeat("line\r\n", 10020))); err != nil {
		t.Fatal(err)
	}
	if got := h.vt.Get(9); got != 10019 {
		t.Fatalf("history=%d, want 10019 within native memory budget", got)
	}
	snapshot, err := h.vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := terminal.RestoreSnapshot(context.Background(), snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if got := restored.Get(9); got != 10019 {
		t.Fatalf("restored history=%d, want 10019", got)
	}
}

func TestHolderEndsSynchronizedOutputWithoutCorruptingContinuation(t *testing.T) {
	h, ln := fanoutHolder(t)
	c := attachScreen(t, ln)
	h.broadcast([]byte("old\x1b[?2026h\x1b[Hnew\x1b[38;2;"))
	waitFor(t, "holder synchronized-output timeout", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return !h.vt.Mode(2026)
	})
	late := attachScreen(t, ln)
	h.broadcast([]byte("1;2;3mAFTER"))
	for _, view := range []*screenClient{c, late} {
		waitFor(t, "timeout and continued live output", func() bool {
			view.mu.Lock()
			defer view.mu.Unlock()
			return !view.vt.Mode(2026) && strings.Contains(string(view.effects), "AFTER")
		})
		view.mu.Lock()
		got, err := view.vt.Lines(false, false, 0, 1)
		view.mu.Unlock()
		h.mu.Lock()
		want, _ := h.vt.Lines(false, false, 0, 1)
		h.mu.Unlock()
		if err != nil || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("timeout corrupted partial control: got=%q want=%q err=%v", got, want, err)
		}
	}
}

package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/hostintegration"
)

type hostTestView struct {
	host     HostClient
	requests chan BrowserRequest
	detach   func()
	send     func([]byte) error
}

func attachHostTestView(t *testing.T, ln *pipeListener, browser bool) *hostTestView {
	t.Helper()
	server, peer := net.Pipe()
	ln.connections <- server
	c := &hostTestView{requests: make(chan BrowserRequest, 8), detach: func() { _ = peer.Close() }}
	if browser {
		c.host.OnBrowser = func(r BrowserRequest, _ <-chan struct{}) { c.requests <- r }
	}
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- Attach(context.Background(), peer, AttachIO{Host: &c.host, OnEvent: func(e ViewEvent) error {
			if len(e.Snapshot) > 0 {
				close(ready)
			}
			return nil
		}})
	}()
	t.Cleanup(func() { _ = peer.Close(); _ = server.Close(); <-done })
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("host view did not attach")
	}
	c.send = func(p []byte) error { return c.host.send(frameData, p) }
	return c
}

func requestBrowser(t *testing.T, h *holder, c *hostTestView) (BrowserRequest, <-chan error) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- h.openBrowser("https://example.org/path?query=value") }()
	select {
	case request := <-c.requests:
		return request, result
	case err := <-result:
		t.Fatalf("browser request failed early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("browser request not delivered")
	}
	return BrowserRequest{}, nil
}

func TestBrowserRequestsTargetOneLiveViewAndNeverReplay(t *testing.T) {
	h, ln := fanoutHolder(t)
	if err := h.openBrowser("https://example.org/"); err == nil {
		t.Fatal("detached browser request succeeded")
	}
	first := attachHostTestView(t, ln, true)
	second := attachHostTestView(t, ln, true)
	_ = attachHostTestView(t, ln, false) // an opted-out viewer cannot steal routing
	request, result := requestBrowser(t, h, second)
	select {
	case <-first.requests:
		t.Fatal("one request reached two viewers")
	default:
	}
	if err := first.host.ReplyBrowser(request.ID, nil); err == nil {
		t.Fatal("unrelated viewer accepted a request")
	}
	if err := second.host.ReplyBrowser(request.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := first.send([]byte("input")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "input updates browser owner", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.activity > 0
	})
	request, result = requestBrowser(t, h, first)
	if err := first.host.ReplyBrowser(request.ID, errors.New("declined")); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil || err.Error() != "declined" {
		t.Fatalf("decline=%v", err)
	}
	third := attachHostTestView(t, ln, true)
	select {
	case <-third.requests:
		t.Fatal("replayed a browser action")
	default:
	}
	// Losing the chosen view cancels the request without rerouting it.
	h.mu.Lock()
	for _, c := range h.clients {
		c.activity = 0
	}
	h.mu.Unlock()
	_, result = requestBrowser(t, h, third)
	third.detach()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("disconnect reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect left request pending")
	}
	select {
	case <-first.requests:
		t.Fatal("retried uncertain request")
	case <-second.requests:
		t.Fatal("rerouted uncertain request")
	default:
	}
	// A later, explicit request can choose a surviving view even when the
	// disconnected client has not yet been removed by an output broadcast.
	request, result = requestBrowser(t, h, second)
	if err := second.host.ReplyBrowser(request.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestImageTransferCancellationInterruptsBlockedTransport(t *testing.T) {
	server, peer := net.Pipe()
	defer func() { _ = server.Close(); _ = peer.Close() }()
	var c HostClient
	writing := make(chan struct{}, 1)
	c.begin(func(kind byte, p []byte) error {
		select {
		case writing <- struct{}{}:
		default:
		}
		return writeFrame(peer, kind, p)
	}, func() { _ = peer.Close() }, true)
	defer c.end()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := c.UploadImage(ctx, data.Bytes()); result <- err }()
	select {
	case <-writing:
	case <-time.After(3 * time.Second):
		t.Fatal("upload never started")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled partial upload succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked upload ignored cancellation")
	}
}

func TestBrowserCallbackCanDeclineWithoutHoldingClientLock(t *testing.T) {
	var c HostClient
	c.begin(func(kind byte, p []byte) error { return nil }, func() {}, false)
	defer c.end()
	c.OnBrowser = func(r BrowserRequest, _ <-chan struct{}) { _ = c.ReplyBrowser(r.ID, errors.New("declined")) }
	done := make(chan error, 1)
	go func() {
		done <- c.receive(frameBrowser, []byte(`{"id":"0123456789ABCDEFG","url":"https://example.org/"}`))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("browser callback deadlocked")
	}
}

func TestImageTransferRoundTripIsPrivateAndBounded(t *testing.T) {
	h, ln := fanoutHolder(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	stored := make(chan []byte, 1)
	h.upload = func(_ context.Context, p []byte) (string, error) {
		stored <- bytes.Clone(p)
		return "/tmp/yoloctl-image-0123456789ABCDEFG/clipboard.png", nil
	}
	view := attachHostTestView(t, ln, false)
	other := attachHostTestView(t, ln, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path, err := view.host.UploadImage(ctx, data.Bytes())
	if err != nil || !validImagePath(path) {
		t.Fatalf("upload=%q %v", path, err)
	}
	if !bytes.Equal(<-stored, data.Bytes()) {
		t.Fatal("image bytes changed")
	}
	h.mu.Lock()
	sequence := h.sequence
	h.imageBytes = 64 << 20
	h.mu.Unlock()
	if sequence != 0 {
		t.Fatal("image upload modified terminal replay/state")
	}
	if _, err := other.host.UploadImage(ctx, data.Bytes()); err == nil {
		t.Fatal("image budget not enforced")
	}
	select {
	case <-stored:
		t.Fatal("rejected upload wrote a file")
	default:
	}
	if _, err := view.host.UploadImage(ctx, []byte("bad")); err == nil {
		t.Fatal("uploaded invalid PNG")
	}
}

func TestHostActionFramesRejectUnsafeMetadata(t *testing.T) {
	for _, value := range []string{`{"id":"0123456789ABCDEFG","url":"file:///tmp/x"}`, `{"id":"bad","url":"https://example.org"}`, `{"id":"0123456789ABCDEFG","url":"https://example.org","command":"id"}`} {
		c := &HostClient{OnBrowser: func(BrowserRequest, <-chan struct{}) { t.Fatal("unsafe request delivered") }}
		c.begin(func(byte, []byte) error { return nil }, func() {}, true)
		if err := c.receive(frameBrowser, []byte(value)); err == nil {
			t.Fatalf("accepted %s", value)
		}
		c.end()
	}
	for _, path := range []string{"/etc/passwd", "/tmp/yoloctl-image-0123456789ABCDEFG/../../bad", "/tmp/yoloctl-image-0123456789ABCDEFG/clipboard.png\ncommand"} {
		c := &HostClient{}
		c.begin(func(byte, []byte) error { return nil }, func() {}, true)
		p, _ := json.Marshal(hostResult{ID: "0123456789ABCDEFG", Path: path})
		if err := c.receive(frameImageResult, p); err == nil {
			t.Fatal("unsafe image path accepted")
		}
		c.end()
	}
}

func TestImageReuseAvoidsTransferAndFallsBackOnMiss(t *testing.T) {
	h, ln := fanoutHolder(t)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	file := hostintegration.ClipboardImage{Path: "/home/repo/shared image.png", Data: pngData.Bytes()}
	sharedPath := file.Path
	probes := make(chan imageProbe, 4)
	uploads := make(chan []byte, 4)
	h.reuse = func(_ context.Context, p imageProbe) (string, error) {
		probes <- p
		if p.Path == sharedPath {
			return imageGuestPath(p.Path), nil
		}
		return "", nil
	}
	h.upload = func(_ context.Context, p []byte) (string, error) {
		uploads <- bytes.Clone(p)
		return "/tmp/yoloctl-image-0123456789ABCDEFG/clipboard.png", nil
	}
	v := attachHostTestView(t, ln, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path, err := v.host.PasteImage(ctx, file)
	if err != nil || path != imageGuestPath(file.Path) {
		t.Fatalf("reuse=%q %v", path, err)
	}
	p := <-probes
	if p.Path != file.Path || p.Size != len(file.Data) || p.SHA256 != fmt.Sprintf("%x", sha256.Sum256(file.Data)) {
		t.Fatalf("probe=%+v", p)
	}
	select {
	case <-uploads:
		t.Fatal("reuse uploaded bytes")
	default:
	}
	h.mu.Lock()
	bytesUsed, sequence := h.imageBytes, h.sequence
	h.mu.Unlock()
	if bytesUsed != 0 || sequence != 0 {
		t.Fatal("reuse spent upload budget or changed replay")
	}
	file.Path = "/home/elsewhere/image.png"
	path, err = v.host.PasteImage(ctx, file)
	if err != nil || !validImagePath(path) {
		t.Fatalf("fallback=%q %v", path, err)
	}
	if !bytes.Equal(<-uploads, file.Data) {
		t.Fatal("fallback bytes changed")
	}
	<-probes
	file.Path = ""
	if _, err := v.host.PasteImage(ctx, file); err != nil {
		t.Fatal(err)
	}
	<-uploads
	select {
	case <-probes:
		t.Fatal("screenshot probed a nonexistent source file")
	default:
	}
	// Reuse remains available after the upload budget has been exhausted.
	h.mu.Lock()
	h.imageBytes = 64 << 20
	h.mu.Unlock()
	file.Path = "/home/repo/shared image.png"
	if _, err := v.host.PasteImage(ctx, file); err != nil {
		t.Fatal(err)
	}
}

func TestImageReuseResultCannotSubstituteAnotherPath(t *testing.T) {
	for _, tc := range []struct {
		kind byte
		path string
	}{
		{frameImageProbed, "/etc/passwd"},
		{frameImageProbed, "/workdir/home/repo/other.png"},
		{frameImageProbed, "/workdir/home/repo/file.png\ncommand"},
		{frameImageResult, "/tmp/yoloctl-image-0123456789ABCDEFG/clipboard.png"},
	} {
		var c HostClient
		c.begin(func(byte, []byte) error { return nil }, func() {}, true)
		id := "0123456789ABCDEFG"
		c.waiters[id] = imageWaiter{result: make(chan hostResult, 1), kind: frameImageProbed, path: "/workdir/home/repo/file.png"}
		p, _ := json.Marshal(hostResult{ID: id, Path: tc.path})
		if err := c.receive(tc.kind, p); err == nil {
			t.Errorf("accepted substituted response %q %q", tc.kind, tc.path)
		}
		c.end()
	}
}

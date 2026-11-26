package session

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/hostintegration"
	"golang.org/x/crypto/ssh"
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
	c.begin(t.Context(), func(kind byte, p []byte) error {
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
	c.begin(t.Context(), func(kind byte, p []byte) error { return nil }, func() {}, false)
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

// The first connection accepts an exec request without acknowledging it.
// Suppress every subsequent SSH write, including automatic channel-close
// replies, while still allowing the test to observe transport EOF.
type silentExecConn struct {
	net.Conn
	silent atomic.Bool
}

func (c *silentExecConn) Write(p []byte) (int, error) {
	if c.silent.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func imageSSHPeer(t *testing.T, socket string) (<-chan string, <-chan error) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	execs := make(chan string, 4)
	closed := make(chan error, 2)
	go func() {
		for attempt := 0; ; attempt++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(first bool) {
				closed <- serveImageSSHPeer(t.Context(), conn, cfg, first, execs)
			}(attempt == 0)
		}
	}()
	return execs, closed
}

func serveImageSSHPeer(ctx context.Context, conn net.Conn, cfg *ssh.ServerConfig, first bool, execs chan<- string) error {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	transport := &silentExecConn{Conn: conn}
	server, channels, requests, err := ssh.NewServerConn(transport, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	go ssh.DiscardRequests(requests)
	newChannel, ok := <-channels
	if !ok {
		return errors.New("missing image session channel")
	}
	channel, channelRequests, err := newChannel.Accept()
	if err != nil {
		return err
	}
	req, ok := <-channelRequests
	if !ok || req.Type != "exec" {
		return errors.New("missing image exec request")
	}
	var exec struct{ Command string }
	if err := ssh.Unmarshal(req.Payload, &exec); err != nil {
		return err
	}
	transport.silent.Store(first)
	execs <- exec.Command
	if first {
		// No request ack, exit status, or close reply can reach the client.
		// Keep the transport alive until the client closes it.
		_ = server.Wait()
		return nil
	}
	if err := req.Reply(true, nil); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, channel); err != nil {
		return err
	}
	if _, err := channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{})); err != nil {
		return err
	}
	return channel.Close()
}

func TestImageSSHExecStallReleasesPaste(t *testing.T) {
	if imageActionFailSafe != 2*time.Minute {
		t.Fatalf("image fail-safe = %s, want two minutes", imageActionFailSafe)
	}
	for _, mode := range []string{"reuse expires", "upload detaches"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "reuse expires" {
				saved := imageActionFailSafe
				imageActionFailSafe = 2 * time.Second
				t.Cleanup(func() { imageActionFailSafe = saved })
			}
			h, ln := fanoutHolder(t)
			opts, _ := testPaths(t)
			execs, closed := imageSSHPeer(t, opts.SSHSocket)
			// Use one slot so the follow-up can only succeed if cancellation
			// released the previous transport as well as view admission.
			endpoint, err := os.Stat(opts.SSHSocket)
			if err != nil {
				t.Fatal(err)
			}
			images := sandboxImages{socket: opts.SSHSocket, endpoint: endpoint, slots: make(chan struct{}, 1)}
			h.upload = images.upload
			h.reuse = func(ctx context.Context, probe imageProbe) (string, error) {
				return images.reuse(ctx, "/home/repo", probe)
			}
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			image := hostintegration.ClipboardImage{Data: data.Bytes()}
			view := attachHostTestView(t, ln, false)
			other := attachHostTestView(t, ln, false)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if mode == "reuse expires" {
				image.Path = "/home/repo/copied.png"
			}
			paste := func(view *hostTestView) <-chan error {
				done := make(chan error, 1)
				go func() {
					path, err := view.host.PasteImage(ctx, image)
					if err == nil && image.Path != "" && path != imageGuestPath(image.Path) {
						err = fmt.Errorf("reused path = %q", path)
					}
					if err == nil && image.Path == "" && !validImagePath(path) {
						err = fmt.Errorf("uploaded path = %q", path)
					}
					done <- err
				}()
				return done
			}
			first := paste(view)
			awaitExec := func() {
				t.Helper()
				select {
				case command := <-execs:
					want := "clipboard.png"
					if image.Path != "" {
						want = "copied.png"
					}
					if !strings.Contains(command, want) {
						t.Fatalf("wrong image command: %s", command)
					}
				case <-ctx.Done():
					t.Fatal("image exec did not reach its SSH peer")
				}
			}
			awaitExec()
			if err := other.send([]byte("still attached")); err != nil {
				t.Fatalf("other view while SSH is stalled: %v", err)
			}
			waitFor(t, "other view input accepted", func() bool {
				h.mu.Lock()
				defer h.mu.Unlock()
				return h.activity != 0
			})
			next := view
			if mode == "upload detaches" {
				view.detach()
				next = other
			}
			select {
			case err := <-first:
				if err == nil {
					t.Fatal("stalled paste succeeded")
				}
				if mode == "reuse expires" && !strings.Contains(err.Error(), "could not check clipboard image") {
					t.Fatalf("expected holder deadline result, got %v", err)
				}
			case <-ctx.Done():
				t.Fatal("stalled paste did not release its view")
			}
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("stalled paste left its SSH connection open")
			}
			select {
			case <-execs:
				t.Fatal("stalled paste retried the command")
			default:
			}
			second := paste(next)
			awaitExec()
			select {
			case err := <-second:
				if err != nil {
					t.Fatalf("second explicit paste: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("second explicit paste remained blocked")
			}
		})
	}
}

func TestImageSSHRejectsReplacementEndpoint(t *testing.T) {
	opts, _ := testPaths(t)
	original, err := net.Listen("unix", opts.SSHSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = original.Close() }()
	endpoint, err := os.Stat(opts.SSHSocket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(opts.SSHSocket, opts.SSHSocket+".old"); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", opts.SSHSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	images := sandboxImages{socket: opts.SSHSocket, endpoint: endpoint, slots: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = images.run(ctx, nil, []string{"impossible image command"})
	if err == nil || !strings.Contains(err.Error(), "endpoint changed") {
		t.Fatalf("replacement endpoint = %v", err)
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
		c.begin(t.Context(), func(byte, []byte) error { return nil }, func() {}, true)
		if err := c.receive(frameBrowser, []byte(value)); err == nil {
			t.Fatalf("accepted %s", value)
		}
		c.end()
	}
	for _, path := range []string{"/etc/passwd", "/tmp/yoloctl-image-0123456789ABCDEFG/../../bad", "/tmp/yoloctl-image-0123456789ABCDEFG/clipboard.png\ncommand"} {
		c := &HostClient{}
		c.begin(t.Context(), func(byte, []byte) error { return nil }, func() {}, true)
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
		c.begin(t.Context(), func(byte, []byte) error { return nil }, func() {}, true)
		id := "0123456789ABCDEFG"
		c.waiters[id] = imageWaiter{result: make(chan hostResult, 1), kind: frameImageProbed, path: "/workdir/home/repo/file.png"}
		p, _ := json.Marshal(hostResult{ID: id, Path: tc.path})
		if err := c.receive(tc.kind, p); err == nil {
			t.Errorf("accepted substituted response %q %q", tc.kind, tc.path)
		}
		c.end()
	}
}

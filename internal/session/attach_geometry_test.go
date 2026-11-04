package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
)

type geometryReadyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *geometryReadyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

type geometryAttachment struct {
	sizes  chan terminal.Geometry
	done   chan error
	ready  <-chan struct{}
	cancel context.CancelFunc
}

func attachGeometry(t *testing.T, ln *pipeListener, managed bool, initial terminal.Geometry) *geometryAttachment {
	t.Helper()
	server, peer := net.Pipe()
	ln.connections <- server
	ctx, cancel := context.WithCancel(t.Context())
	w := &geometryReadyWriter{ready: make(chan struct{})}
	a := &geometryAttachment{sizes: make(chan terminal.Geometry, 1), done: make(chan error, 1), ready: w.ready, cancel: cancel}
	a.sizes <- initial
	aio := AttachIO{Resize: a.sizes, Stdout: w}
	if managed {
		aio.OnEvent = func(e ViewEvent) error {
			if len(e.Snapshot) > 0 {
				w.once.Do(func() { close(w.ready) })
			}
			return nil
		}
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		a.done <- Attach(ctx, peer, aio)
	}()
	t.Cleanup(func() {
		cancel()
		_ = peer.Close()
		_ = server.Close()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("geometry attachment did not stop")
		}
	})
	return a
}

func geometryResult(t *testing.T, a *geometryAttachment) error {
	t.Helper()
	select {
	case err := <-a.done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("geometry attachment did not return")
		return nil
	}
}

func waitGeometry(t *testing.T, h *holder, a *geometryAttachment, size terminal.Geometry) {
	t.Helper()
	select {
	case <-a.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("geometry attachment was not admitted")
	}
	waitFor(t, "canonical geometry", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.vt.Geometry() == size
	})
	select {
	case err := <-a.done:
		t.Fatalf("valid geometry lost its attachment: %v", err)
	default:
	}
}

func TestAttachInvalidLocalGeometryKeepsHolder(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, size := range []terminal.Geometry{
			{}, {Cols: 100}, {Rows: 24},
			{Cols: 100, Rows: 24, CellWidth: 8},
			{Cols: -1, Rows: 24},
		} {
			t.Run(fmt.Sprintf("managed=%t/%+v", managed, size), func(t *testing.T) {
				h, ln := fanoutHolder(t)
				h.broadcast([]byte("ready"))
				bad := attachGeometry(t, ln, managed, size)
				err := geometryResult(t, bad)
				if err == nil || errors.Is(err, ErrSessionClosed) {
					t.Fatalf("invalid local geometry reported holder loss: %v", err)
				}
				if size.Cols == 0 || size.Rows == 0 {
					for _, want := range []string{fmt.Sprintf("%d columns x %d rows", size.Cols, size.Rows), "set a nonzero terminal size and attach again"} {
						if !strings.Contains(err.Error(), want) {
							t.Fatalf("geometry error %q lacks %q", err, want)
						}
					}
				} else if !strings.Contains(err.Error(), "invalid resize geometry") {
					t.Fatalf("other invalid geometry misdiagnosed: %v", err)
				}
				h.mu.Lock()
				closed, got := h.closed, h.vt.Geometry()
				h.mu.Unlock()
				if closed || got != (terminal.Geometry{Cols: 80, Rows: 24}) {
					t.Fatalf("invalid resize affected holder: closed=%t geometry=%+v", closed, got)
				}
				// Unknown pixels with a valid grid remain valid on the same holder.
				valid := terminal.Geometry{Cols: 100, Rows: 24}
				live := attachGeometry(t, ln, managed, valid)
				waitGeometry(t, h, live, valid)
				live.cancel()
				if err := geometryResult(t, live); !errors.Is(err, context.Canceled) {
					t.Fatalf("explicit cancellation = %v", err)
				}
			})
		}
	}
}

func TestAttachLaterInvalidLocalGeometry(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%t", managed), func(t *testing.T) {
			h, ln := fanoutHolder(t)
			h.broadcast([]byte("ready"))
			valid := terminal.Geometry{Cols: 100, Rows: 24}
			a := attachGeometry(t, ln, managed, valid)
			waitGeometry(t, h, a, valid)
			a.sizes <- terminal.Geometry{Cols: 100}
			err := geometryResult(t, a)
			if err == nil || errors.Is(err, ErrSessionClosed) || !strings.Contains(err.Error(), "100 columns x 0 rows") {
				t.Fatalf("later invalid resize = %v", err)
			}
			h.mu.Lock()
			got := h.vt.Geometry()
			h.mu.Unlock()
			if got != valid {
				t.Fatalf("invalid later resize reached holder: %+v", got)
			}
			live := attachGeometry(t, ln, managed, valid)
			waitGeometry(t, h, live, valid)
			live.cancel()
			if err := geometryResult(t, live); !errors.Is(err, context.Canceled) {
				t.Fatalf("subsequent view cancellation = %v", err)
			}
		})
	}
}

func TestAttachGeometryPreservesRemoteResults(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, status := range []int{-1, 0, 137} {
			t.Run(fmt.Sprintf("managed=%t/status=%d", managed, status), func(t *testing.T) {
				h, ln := fanoutHolder(t)
				h.broadcast([]byte("ready"))
				valid := terminal.Geometry{Cols: 100, Rows: 24}
				a := attachGeometry(t, ln, managed, valid)
				waitGeometry(t, h, a, valid)
				var payload []byte
				if status >= 0 {
					payload = encodeExit(status)
				}
				h.closeClients(payload)
				err := geometryResult(t, a)
				var exit *ExitError
				if status < 0 && !errors.Is(err, ErrSessionClosed) ||
					status == 0 && err != nil ||
					status > 0 && (!errors.As(err, &exit) || exit.Status != status) {
					t.Fatalf("remote status %d became %v", status, err)
				}
			})
		}
	}
}

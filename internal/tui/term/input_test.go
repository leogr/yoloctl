package term

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	xterm "golang.org/x/term"
)

func TestPreparedInputOrdersLaterKeysAndCancelsOnClose(t *testing.T) {
	w := newBlockedInput()
	close(w.gate)
	q := newInputQueue(w, nil)
	t.Cleanup(q.Close)
	ready, release := make(chan struct{}), make(chan struct{})
	if _, err := q.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	if err := q.Transform(func(ctx context.Context) ([]byte, error) {
		close(ready)
		select {
		case <-release:
			return []byte("image-path"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	<-ready
	if _, err := q.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	if got := w.text(); got != "before" {
		t.Fatalf("later input overtook paste: %q", got)
	}
	close(release)
	awaitInput(t, "ordered image", func() bool { return w.text() == "beforeimage-pathafter" })
	ready = make(chan struct{})
	if err := q.Transform(func(ctx context.Context) ([]byte, error) { close(ready); <-ctx.Done(); return nil, ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	<-ready
	done := make(chan struct{})
	go func() { q.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel clipboard operation")
	}
}

type blockedInput struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	gate    chan struct{}
	closed  chan struct{}
	started chan struct{}
	once    sync.Once
	close   sync.Once
	fail    error
}

func newBlockedInput() *blockedInput {
	return &blockedInput{gate: make(chan struct{}), closed: make(chan struct{}), started: make(chan struct{})}
}

func (w *blockedInput) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.gate:
	case <-w.closed:
		return 0, io.ErrClosedPipe
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Force partial writes even on success.
	n, _ := w.buf.Write(p[:min(3, len(p))])
	return n, w.fail
}

func (w *blockedInput) Close() error {
	w.close.Do(func() { close(w.closed) })
	return nil
}

func (w *blockedInput) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func awaitInput(t *testing.T, label string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", label)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInputQueueOrdersTypedPasteAndReplies(t *testing.T) {
	w := newBlockedInput()
	q := newInputQueue(w, nil)
	t.Cleanup(q.Close)
	p := []byte("typed")
	if _, err := q.Write(p); err != nil {
		t.Fatal(err)
	}
	p[0] = 'X' // The caller can reuse its buffer immediately.
	e := newTestEmulator(t, 3, 20, q, 0)
	if _, err := e.Write([]byte("\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Write([]byte("\x1b[200~paste\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	close(w.gate)
	const want = "typed\x1b[1;1R\x1b[200~paste\x1b[201~"
	awaitInput(t, "ordered input", func() bool { return len(w.text()) >= len(want) })
	if got := w.text(); got != want {
		t.Fatalf("input=%q, want %q", got, want)
	}
}

func TestInputQueueRejectsWholePacketAtBounds(t *testing.T) {
	for _, limit := range []string{"bytes", "packets"} {
		t.Run(limit, func(t *testing.T) {
			w := newBlockedInput()
			q := newInputQueue(w, nil)
			t.Cleanup(q.Close) // Must interrupt the blocked worker.
			if limit == "bytes" {
				if n, err := q.Write(bytes.Repeat([]byte{'x'}, maxInputBytes)); n != maxInputBytes || err != nil {
					t.Fatalf("enqueue: %d %v", n, err)
				}
			} else {
				for range maxInputPackets {
					if _, err := q.Write([]byte("x")); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Include the in-flight write in the bound, not just pending items.
			select {
			case <-w.started:
			case <-time.After(3 * time.Second):
				t.Fatal("writer did not start")
			}
			if n, err := q.Write([]byte("rejected")); n != 0 || !errors.Is(err, errInputFull) {
				t.Fatalf("overflow partially accepted or not reported: %d %v", n, err)
			}
			if !errors.Is(q.takeError(), errInputFull) || q.takeError() != nil {
				t.Fatal("overflow notice must be consumable exactly once")
			}
		})
	}
}

func TestInputQueueDoesNotReplayAfterPartialFailure(t *testing.T) {
	w := newBlockedInput()
	w.fail = errors.New("child gone")
	q := newInputQueue(w, nil)
	t.Cleanup(q.Close)
	for _, p := range []string{"abcdef", "later"} {
		if _, err := q.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	close(w.gate)
	select {
	case <-q.done:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not finish")
	}
	if got := w.text(); got != "abc" {
		t.Fatalf("retried uncertain input: %q", got)
	}
	if err := q.takeError(); !errors.Is(err, w.fail) || !strings.Contains(err.Error(), "may not have reached") {
		t.Fatalf("missing partial-delivery notice: %v", err)
	}
	if _, err := q.Write([]byte("new")); !errors.Is(err, w.fail) {
		t.Fatalf("failed writer accepted more input: %v", err)
	}
}

// Runs only in a child test process. It announces raw mode before refusing input;
// no timing guess about the shell's terminal settings is needed.
func TestBlockedPTYChild(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_BLOCKED_PTY") != "1" {
		return
	}
	if _, err := xterm.MakeRaw(0); err != nil {
		os.Exit(91)
	}
	_, _ = os.Stdout.WriteString("PTY_READY")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestPTYBackpressureDoesNotBlockControl(t *testing.T) {
	for _, console := range []bool{false, true} {
		name := "tab"
		if console {
			name = "console"
		}
		t.Run(name, func(t *testing.T) {
			argv := []string{os.Args[0], "-test.run=^TestBlockedPTYChild$"}
			env := append(os.Environ(), "YOLOCTL_TEST_BLOCKED_PTY=1")
			var send func([]byte) error
			var text func() string
			var resize func()
			var stop func()
			var writer *inputQueue
			if console {
				c := NewConsole(4, 40, 20, nil)
				t.Cleanup(func() { _ = c.Close() })
				if _, err := c.Run(env, "blocked input test", argv...); err != nil {
					t.Fatal(err)
				}
				send, text, writer = c.SendInput, c.GridText, c.writer
				resize = func() { c.Resize(5, 50) }
				stop = func() { c.Kill(); awaitInput(t, "console exit", func() bool { return !c.Busy() }) }
			} else {
				s, err := Start(Options{Argv: argv, Env: env, Rows: 4, Cols: 40})
				if err != nil {
					t.Fatal(err)
				}
				send, text, stop, writer = s.SendInput, s.GridText, s.Close, s.writer
				resize = func() { _ = s.Resize(5, 50) }
			}
			t.Cleanup(stop)
			awaitInput(t, "raw PTY readiness", func() bool { return strings.Contains(text(), "PTY_READY") })
			completed := make(chan error, 1)
			go func() {
				err := send(bytes.Repeat([]byte{'x'}, 1<<20))
				resize()
				_ = text()
				completed <- err
			}()
			select {
			case err := <-completed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("input blocked rendering or resize")
			}
			stop()
			select {
			case <-writer.done:
			default:
				t.Fatal("completion left an input worker alive")
			}
		})
	}
}

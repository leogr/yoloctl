package chatstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// A stream that stops reports why: the transport's own error (which carries
// the controller's), a clean end, this side's deadline or cancellation, or a
// malformed frame, instead of one message for every cause.
func TestStreamEndsSayWhy(t *testing.T) {
	defer func(d time.Duration) { streamReplyTimeout = d }(streamReplyTimeout)
	streamReplyTimeout = 50 * time.Millisecond
	for _, tc := range []struct {
		name string
		feed func(*io.PipeWriter, context.CancelFunc)
		want string
	}{
		{"backend error", func(w *io.PipeWriter, _ context.CancelFunc) {
			_ = w.CloseWithError(errors.New("invalid AIRC client feature probe"))
		}, "managed chat stream failed: invalid AIRC client feature probe"},
		{"clean end", func(w *io.PipeWriter, _ context.CancelFunc) { _ = w.Close() }, "managed chat stream ended"},
		{"partial frame", func(w *io.PipeWriter, _ context.CancelFunc) {
			_, _ = w.Write([]byte(`{"event"`))
			_ = w.Close()
		}, "managed chat stream ended inside a frame"},
		{"oversized", func(w *io.PipeWriter, _ context.CancelFunc) {
			_, _ = w.Write([]byte(strings.Repeat("x", ResponseLimit+2)))
		}, "frame over"},
		{"not UTF-8", func(w *io.PipeWriter, _ context.CancelFunc) { _, _ = w.Write([]byte("\xff\n")) }, "not UTF-8"},
		{"deadline", func(*io.PipeWriter, context.CancelFunc) {}, "sent no room header and client handshake within 50ms"},
		{"cancelled", func(_ *io.PipeWriter, cancel context.CancelFunc) { cancel() }, "was closed by this view"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := io.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go tc.feed(w, cancel)
			err := RunChannels(ctx, r, nopWriteCloser{io.Discard}, nil, nil, nil, nil, func(Event) {})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

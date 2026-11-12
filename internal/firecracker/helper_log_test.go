package firecracker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/rotatelog"
)

type observedFinalLog struct {
	io.Writer
	once sync.Once
	seen chan struct{}
}

func (w *observedFinalLog) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && bytes.Contains(p[:n], []byte("VM helper ended")) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func TestHelperFinalLogPrecedesRetirementWithLiveIngress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm.log")
	w, err := rotatelog.Open(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	observed := &observedFinalLog{Writer: w.BestEffort(), seen: make(chan struct{})}
	output, done, err := rotatelog.Pipe(observed, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = output.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("relay did not drain")
		}
	})
	fileLog := logging.NewFileLogger(output)
	var result error
	finish := helperLogFinalizer(fileLog, fileLog.Slog(), &result)
	// Model both return defers: the post-Start one runs before retirement;
	// the pre-Start fallback runs again after it, without another record.
	func() {
		defer finish()
		defer func() {
			select {
			case <-done:
				t.Fatal("live producer ingress closed early")
			default:
			}
			// This channel represents progress during the already-owned wait;
			// the timeout is only a test watchdog, not a production delay.
			select {
			case <-observed.seen:
			case <-time.After(5 * time.Second):
				t.Fatal("final record was not emitted before retirement")
			}
			got, err := rotatelog.ReadTail(path, 4096)
			if err != nil || bytes.Count(got, []byte("VM helper ended")) != 1 || !bytes.Contains(got, []byte("fixture final error")) {
				t.Fatalf("record during retirement: %q %v", got, err)
			}
		}()
		defer finish()
		result = errors.New("fixture final error")
	}()
	_ = output.Close()
	<-done
	got, err := rotatelog.ReadTail(path, 4096)
	if err != nil || bytes.Count(got, []byte("VM helper ended")) != 1 {
		t.Fatalf("final record duplicated or lost: %q %v", got, err)
	}
}

func TestHelperFinalLogEarlyReturnAndQuietResults(t *testing.T) {
	for _, result := range []error{errors.New("early failure"), context.Canceled, nil} {
		var out bytes.Buffer
		fileLog := logging.NewFileLogger(&out)
		finish := helperLogFinalizer(fileLog, fileLog.Slog(), &result)
		finish()
		finish()
		want := 0
		if result != nil && !errors.Is(result, context.Canceled) {
			want = 1
		}
		if n := bytes.Count(out.Bytes(), []byte("VM helper ended")); n != want {
			t.Fatalf("result=%v records=%d: %q", result, n, out.String())
		}
	}
}

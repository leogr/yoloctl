package rotatelog

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// A relay may split a complete producer write at any read/copy boundary.
// Hold one such boundary inside the real grow marker without relying on pipe
// capacity, read batching, sleeps or a particular scheduler.
type markerGate struct {
	writer  io.Writer
	marker  []byte
	entered chan struct{}
	release chan struct{}
	gated   bool
}

func (g *markerGate) Write(p []byte) (int, error) {
	at := bytes.Index(p, g.marker)
	if g.gated || at < 0 {
		return g.writer.Write(p)
	}
	g.gated = true
	split := at + len(g.marker)/2
	n, err := g.writer.Write(p[:split])
	if err != nil {
		return n, err
	}
	close(g.entered)
	<-g.release
	m, err := g.writer.Write(p[split:])
	return n + m, err
}

func TestMarkerAndHelperRecordsUseOneIngress(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		name := "one-ingress"
		if bypass {
			name = "bypass-negative-control"
		}
		t.Run(name, func(t *testing.T) {
			w, path := openTestLog(t, 128<<10)
			marker := []byte((vmwire.DataDiskGrow{UUID: "12345678-1234-1234-1234-123456789abc", From: 10 << 30, To: 20 << 30}).GrownLine())
			// A complete marker fits even POSIX's minimum atomic pipe write.
			if len(marker)+1 > 512 {
				t.Fatal("marker exceeds the fixture's atomic write bound")
			}
			gate := &markerGate{writer: w.BestEffort(), marker: marker, entered: make(chan struct{}), release: make(chan struct{})}
			output, done, err := Pipe(gate, nil)
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			t.Cleanup(func() {
				release()
				_ = output.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("relay did not drain")
				}
			})
			if _, err := output.Write(append(bytes.Clone(marker), '\n')); err != nil {
				t.Fatal(err)
			}
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("marker did not reach relay boundary")
			}
			var ingress io.Writer = output // helper uses its captured stderr
			if bypass {
				ingress = w
			} // old direct-file route must be detected
			log := logging.NewFileLogger(ingress)
			log.Slog().Warn("structured helper warning", "actor", "vm")
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			release()
			_ = output.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("relay did not drain")
			}
			got, err := ReadTail(path, 128<<10)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(got, marker) == bypass {
				t.Fatalf("marker preserved=%v with bypass=%v: %q", bytes.Contains(got, marker), bypass, got)
			}
			if !bytes.Contains(got, []byte("structured helper warning")) {
				t.Fatalf("helper record lost: %q", got)
			}
		})
	}
}

func TestSharedIngressRetainsSinkFailure(t *testing.T) {
	w, path := openTestLog(t, 1024)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	output, done, err := Pipe(w.BestEffort(), nil)
	if err != nil {
		t.Fatal(err)
	}
	log := logging.NewFileLogger(output)
	log.Slog().Warn("pipe acceptance is not file persistence")
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	_ = output.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed sink stopped draining")
	}
	if w.Err() == nil {
		t.Fatal("asynchronous sink failure was lost")
	}
	if got := readLog(t, path); len(got) != 0 {
		t.Fatalf("unsafe sink was written: %q", got)
	}
}

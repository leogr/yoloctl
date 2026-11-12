package followview

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	"golang.org/x/sys/unix"
)

// Run each sub-benchmark in a separate process with -benchtime=1x for an
// independent process RSS high-water mark. This never contacts a live VM.
func BenchmarkFollow(b *testing.B) {
	for _, tc := range []struct {
		name   string
		size   int
		format Format
	}{
		{"small", 12 << 10, Markdown}, {"text-2MiB", WholeBytes, Text}, {"windowed-16MiB", 16 << 20, Text}, {"markdown-2MiB", WholeBytes, Markdown},
	} {
		b.Run(tc.name, func(b *testing.B) {
			root, err := filepath.EvalSymlinks(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(root, "document")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			chunk := []byte("A complete source line with **strong** and `code`, several ordinary words, and a Unicode 界.\n")
			for written := 0; written < tc.size; {
				p := chunk[:min(len(chunk), tc.size-written)]
				if _, err := file.Write(p); err != nil {
					b.Fatal(err)
				}
				written += len(p)
			}
			if err := file.Sync(); err != nil {
				b.Fatal(err)
			}
			theme := spaceview.NewTheme(true, nil, nil)
			for range b.N {
				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)
				var peak atomic.Uint64
				peak.Store(before.HeapAlloc)
				stop := make(chan struct{})
				sampleDone := make(chan struct{})
				go func() {
					defer close(sampleDone)
					timer := time.NewTicker(time.Millisecond)
					defer timer.Stop()
					for {
						var mem runtime.MemStats
						runtime.ReadMemStats(&mem)
						for old := peak.Load(); mem.HeapAlloc > old && !peak.CompareAndSwap(old, mem.HeapAlloc); old = peak.Load() {
						}
						select {
						case <-stop:
							return
						case <-timer.C:
						}
					}
				}()
				reader := HostReader{Path: path}
				start := time.Now()
				snapshot, err := reader.Read(context.Background(), Query{Tail: true})
				readLatency := time.Since(start)
				if err != nil {
					b.Fatal(err)
				}
				start = time.Now()
				view := renderFile(context.Background(), snapshot.Data, snapshot.Large, false, tc.format, 80, ansi.GraphemeWidth, theme, drawing{})
				renderLatency := time.Since(start)
				start = time.Now()
				narrow := renderFile(context.Background(), snapshot.Data, snapshot.Large, false, tc.format, 32, ansi.GraphemeWidth, theme, drawing{})
				resizeLatency := time.Since(start)
				if len(view.rows) == 0 || len(narrow.rows) == 0 {
					b.Fatal("missing complete layout")
				}
				if _, err := file.WriteAt([]byte("EDITED"), snapshot.Start+int64(len(snapshot.Data))/2); err != nil {
					b.Fatal(err)
				}
				start = time.Now()
				edited, err := reader.Read(context.Background(), Query{Tail: true, Previous: snapshot.Key})
				if err != nil {
					b.Fatal(err)
				}
				changedView := renderFile(context.Background(), edited.Data, edited.Large, false, tc.format, 80, ansi.GraphemeWidth, theme, drawing{})
				editLatency := time.Since(start)
				runtime.KeepAlive(changedView)
				// Actual finite guest script, locally: reports its own read/hash time and
				// real encoded response size. Excludes SSH/hypervisor latency by design.
				guest := pythonReader(path, func() []string { return []string{root} })
				remote, err := guest.Read(context.Background(), Query{Tail: true})
				if err != nil {
					b.Fatal(err)
				}
				idle, err := guest.Read(context.Background(), Query{Tail: true, Previous: remote.Key})
				if err != nil || idle.Changed || len(idle.Data) != 0 {
					b.Fatal("unchanged guest payload", err)
				}
				m := New(context.Background(), Options{Reader: reader, Path: path, Format: tc.format})
				m.width, m.height = 80, 24
				m.snapshot, m.raw, m.view = edited, edited.Data, changedView
				m.pendingRead = false
				_, command := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
				done := make(chan tea.Msg, 1)
				started := make(chan struct{})
				go func() { close(started); done <- command() }()
				<-started
				maxInput := time.Duration(0)
				for range 20 {
					start := time.Now()
					m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
					maxInput = max(maxInput, time.Since(start))
					runtime.Gosched()
				}
				<-done
				m.Close()
				close(stop)
				<-sampleDone
				var usage unix.Rusage
				if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
					b.Fatal(err)
				}
				rss := uint64(usage.Maxrss)
				if runtime.GOOS != "darwin" {
					rss *= 1024
				}
				metrics := map[string]any{"case": tc.name, "source_bytes": tc.size, "active_bytes": len(snapshot.Data), "rows": len(view.rows), "read_ms": float64(readLatency.Microseconds()) / 1000, "render_ms": float64(renderLatency.Microseconds()) / 1000, "resize_ms": float64(resizeLatency.Microseconds()) / 1000, "edit_read_render_ms": float64(editLatency.Microseconds()) / 1000, "max_input_us": float64(maxInput.Nanoseconds()) / 1000, "sampled_peak_go_heap_bytes": peak.Load(), "baseline_go_heap_bytes": before.HeapAlloc, "process_peak_rss_bytes": rss, "local_guest_read_hash_ms": float64(remote.ReadNS) / 1e6, "guest_changed_wire_bytes": remote.ReceivedBytes, "guest_unchanged_wire_bytes": idle.ReceivedBytes, "live_vm": false}
				raw, _ := json.Marshal(metrics)
				fmt.Println("FOLLOW_MEASUREMENT", string(raw))
				b.ReportMetric(float64(len(view.rows)), "rows")
				b.ReportMetric(float64(peak.Load())/(1<<20), "peak-heap-MiB")
				b.ReportMetric(float64(rss)/(1<<20), "peak-RSS-MiB")
			}
		})
	}
}

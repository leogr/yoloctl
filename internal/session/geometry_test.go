package session

import (
	"context"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

// Exercise the real SSH requests and holder query owner together. The SSH
// convenience methods synthesize 8x8 cells; these expectations must not.
func TestHolderMeasuredPTYGeometry(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := runHolderAsync(ctx, opts)
	<-fs.started
	fs.mu.Lock()
	pixels := fs.ptyPixels
	fs.mu.Unlock()
	if pixels != [2]int{} {
		t.Fatalf("initial PTY invented pixel dimensions: %v", pixels)
	}
	c := attachWhenReady(t, opts.AttachSocket)
	defer func() { _ = c.stdinW.Close() }()
	resize := func(g terminal.Geometry, n int, want [2]int) {
		t.Helper()
		c.resize <- g
		waitFor(t, "measured SSH resize", func() bool {
			fs.mu.Lock()
			defer fs.mu.Unlock()
			return len(fs.resizes) == n && fs.resizes[n-1] == [2]int{g.Cols, g.Rows} && fs.resizePixels[n-1] == want
		})
	}
	g := terminal.Geometry{Cols: 121, Rows: 33, CellWidth: 19, CellHeight: 42}
	resize(g, 1, [2]int{2299, 1386})
	fs.emit("\x1b[14t\x1b[16t\x1b[18t\x1b[?2048h")
	want := "\x1b[4;1386;2299t\x1b[6;42;19t\x1b[8;33;121t\x1b[48;33;121;1386;2299t"
	waitInput := func() {
		t.Helper()
		waitFor(t, "single canonical size reply", func() bool {
			fs.mu.Lock()
			defer fs.mu.Unlock()
			return fs.stdin.String() == want
		})
	}
	waitInput()
	// Same cells, different font: this must not be discarded as a no-op.
	g.CellWidth, g.CellHeight = 20, 43
	resize(g, 2, [2]int{2420, 1419})
	want += "\x1b[48;33;121;1419;2420t"
	waitInput()
	// A cell-only compatibility viewer cannot report pixels it never measured.
	g.CellWidth, g.CellHeight = 0, 0
	resize(g, 3, [2]int{})
	want += "\x1b[48;33;121;0;0t"
	waitInput()
	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("holder exit: %+v", r)
	}
	if err := c.wait(t); err != nil {
		t.Fatal(err)
	}
}

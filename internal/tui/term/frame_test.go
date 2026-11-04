package term

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestFrameSynchronizedBoundary(t *testing.T) {
	for _, chunk := range []int{1, 3, 7, 4096} {
		t.Run(strconv.Itoa(chunk), func(t *testing.T) {
			var replies bytes.Buffer
			e := newTestEmulator(t, 2, 10, &replies, 10)
			write := func(s string) {
				for len(s) > 0 {
					n := min(len(s), chunk)
					feed(t, e, s[:n])
					s = s[n:]
				}
			}
			write("AB\x1b[?2026h\x1b[HXY\x1b[6n")
			f := e.Frame()
			if got := strings.TrimSpace(ansi.Strip(f.Lines[0])); got != "AB" || f.Cursor.X != 2 {
				t.Fatalf("held boundary = %q, cursor %+v", got, f.Cursor)
			}
			// Complete a frame and start another in the same PTY write, with
			// no render between: the completed XY frame must not disappear.
			write("\x1b[?2026l\x1b[?2026h\x1b[HZZ\x1b[6n")
			f = e.Frame()
			if got := strings.TrimSpace(ansi.Strip(f.Lines[0])); got != "XY" {
				t.Fatalf("intermediate complete frame lost: %q", got)
			}
			f.Lines[0] = "caller mutation"
			if e.Frame().Lines[0] == "caller mutation" {
				t.Fatal("frame shares mutable rows with caller")
			}
			write("\x1b[?2026l")
			if got := strings.TrimSpace(ansi.Strip(e.Frame().Lines[0])); got != "ZZ" {
				t.Fatalf("released frame = %q", got)
			}
			if replies.String() != "\x1b[1;3R\x1b[1;3R" {
				t.Fatalf("snapshot corrupted parser scratch or query delivery: %q", replies.String())
			}
		})
	}
}

func TestFrameHoldTimeoutAndResize(t *testing.T) {
	e := newTestEmulator(t, 2, 10, nil, 10)
	wake := make(chan struct{}, 2)
	e.onFrame = func() { wake <- struct{}{} }
	feed(t, e, "old\x1b[?2026h\x1b[Hnew")
	select {
	case <-wake:
	case <-time.After(3 * time.Second):
		t.Fatal("silent child's held frame never woke the renderer")
	}
	if got := strings.TrimSpace(ansi.Strip(e.Frame().Lines[0])); got != "new" {
		t.Fatalf("timeout did not publish output: %q", got)
	}
	if e.vt.Mode(2026) {
		t.Fatal("timeout left synchronized-output mode enabled")
	}
	feed(t, e, "\x1b[?2026h\x1b[Hend")
	e.Resize(3, 20)
	f := e.Frame()
	if f.Rows != 3 || !strings.Contains(ansi.Strip(f.Lines[0]), "end") {
		t.Fatalf("resize retained stale frame: %+v", f)
	}
	feed(t, e, "\x1b[?2026h\x1b[Hbye")
	e.EndRenderHold()
	if got := ansi.Strip(e.Frame().Lines[0]); !strings.Contains(got, "bye") {
		t.Fatalf("child exit retained held frame: %q", got)
	}
}

func TestFrameHistoryStylesAndLinks(t *testing.T) {
	e := newTestEmulator(t, 2, 10, nil, 10)
	feed(t, e, "\x1b[1;38;2;255;0;0mA\x1b[22mB\x1b]8;;https://example.org\x1b\\C\x1b]8;;\x1b\\\r\nline\r\nnew")
	e.Scroll(1)
	f := e.Frame()
	buf := uv.NewScreenBuffer(10, 2)
	uv.NewStyledString(strings.Join(f.Lines, "\n")).Draw(buf, buf.Bounds())
	a, b, c := buf.CellAt(0, 0), buf.CellAt(1, 0), buf.CellAt(2, 0)
	if a.Content != "A" || b.Content != "B" || c.Content != "C" {
		t.Fatalf("historical text lost: %q", f.Lines)
	}
	if a.Style.Attrs&uv.AttrBold == 0 || b.Style.Attrs&uv.AttrBold != 0 || b.Style.Fg == nil {
		t.Fatalf("historical bold-off/red style lost: %+v %+v", a.Style, b.Style)
	}
	r, g, blue, _ := b.Style.Fg.RGBA()
	if r != 65535 || g != 0 || blue != 0 || c.Link.URL != "https://example.org" {
		t.Fatalf("history color/link = %+v %+v, rows %q", b.Style, c.Link, f.Lines)
	}
}

func TestFrameHyperlinkRunsPreserveGraphemeColumns(t *testing.T) {
	e := newTestEmulator(t, 2, 12, nil, 10)
	feed(t, e, "P\x1b]8;id=probe;https://example.org/a;b\x1b\\e\u0301界\x1b]8;;\x1b\\Q")
	f := e.Frame()
	buf := uv.NewScreenBuffer(12, 2)
	uv.NewStyledString(strings.Join(f.Lines, "\n")).Draw(buf, buf.Bounds())
	for _, tc := range []struct {
		x     int
		text  string
		width int
		link  bool
	}{{0, "P", 1, false}, {1, "e\u0301", 1, true}, {2, "界", 2, true}, {4, "Q", 1, false}} {
		c := buf.CellAt(tc.x, 0)
		if c.Content != tc.text || c.Width != tc.width || (c.Link.URL != "") != tc.link {
			t.Fatalf("column %d = %+v, frame %q", tc.x, c, f.Lines)
		}
		if tc.link && (c.Link.URL != "https://example.org/a;b" || c.Link.Params != "id=probe") {
			t.Fatalf("column %d link = %+v", tc.x, c.Link)
		}
	}
}

func TestFrameAtomicWithConcurrentOutput(t *testing.T) {
	e := newTestEmulator(t, 2, 10, nil, 10)
	a := "\x1b[2J\x1b[HA\x1b[2;1HA\x1b[1;2H"
	b := "\x1b[2J\x1b[HBBB\x1b[2;1HBBB\x1b[2;4H"
	feed(t, e, a)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			_, _ = e.Write([]byte(b))
			_, _ = e.Write([]byte(a))
		}
	})
	defer wg.Wait()
	for range 200 {
		f := e.Frame()
		first := strings.TrimSpace(ansi.Strip(f.Lines[0]))
		second := strings.TrimSpace(ansi.Strip(f.Lines[1]))
		valid := first == "A" && second == "A" && f.Cursor.X == 1 && f.Cursor.Y == 0
		valid = valid || (first == "BBB" && second == "BBB" && f.Cursor.X == 3 && f.Cursor.Y == 1)
		if !valid {
			t.Fatalf("mixed frame: %q/%q cursor %+v", first, second, f.Cursor)
		}
	}
}

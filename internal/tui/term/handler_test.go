package term

import (
	"bytes"
	"image/color"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDefaultColorQueries(t *testing.T) {
	for _, end := range []string{"\a", "\x1b\\"} {
		query := "\x1b]10;?;?" + end
		for split := 0; split <= len(query); split++ {
			var replies bytes.Buffer
			e := newTestEmulator(t, 4, 40, &replies, 0)
			e.SetColors(color.RGBA{R: 0xee, G: 0xdd, B: 0xcc, A: 255}, color.RGBA{R: 0x12, G: 0x23, B: 0x34, A: 255})
			_, _ = e.Write([]byte(query[:split]))
			_, _ = e.Write([]byte(query[split:]))
			want := "\x1b]10;rgb:eeee/dddd/cccc" + end + "\x1b]11;rgb:1212/2323/3434" + end
			if got := replies.String(); got != want {
				t.Errorf("end=%q split=%d: %q, want %q", end, split, got, want)
			}
		}
	}
}

func TestDefaultColorQueriesWaitForHost(t *testing.T) {
	var replies bytes.Buffer
	e := newTestEmulator(t, 4, 40, &replies, 0)
	for range 100 {
		_, _ = e.Write([]byte("\x1b]10;?\a\x1b]11;?\a"))
	}
	if replies.Len() != 0 {
		t.Fatal("invented colors before host detection")
	}
	e.SetColors(nil, color.Black)
	if got := replies.String(); got != "\x1b]11;rgb:0000/0000/0000\a" {
		t.Fatalf("background reply: %q", got)
	}
	replies.Reset()
	e.SetColors(color.White, color.Black)
	if got := replies.String(); got != "\x1b]10;rgb:ffff/ffff/ffff\a" {
		t.Fatalf("foreground reply: %q", got)
	}
	replies.Reset()
	e.SetPaletteColor(0, color.Black)
	// Child color changes affect its virtual terminal and are queryable;
	// they never mutate the host or another tab. Cursor color falls back to
	// the known foreground; the virtual palette is independently queryable.
	_, _ = e.Write([]byte("\x1b]11;#ff0000\a\x1b]4;0;?\a\x1b]12;?\a\x1b]11;?\a"))
	if got := replies.String(); !strings.Contains(got, "\x1b]4;0;rgb:") || !strings.Contains(got, "\x1b]12;rgb:ffff/ffff/ffff\a") || !strings.HasSuffix(got, "\x1b]11;rgb:ffff/0000/0000\a") {
		t.Fatalf("virtual color query: %q", got)
	}
	other := newTestEmulator(t, 4, 40, nil, 0)
	other.SetColors(color.White, color.Black)
	_, _ = other.Write([]byte("other"))
	if strings.Contains(other.RenderLine(0), "48;2;255;0;0") || ansi.Strip(other.RenderLine(0)) != "other" {
		t.Fatal("color change escaped its terminal")
	}
}

func TestDefaultColorQueryAfterClear(t *testing.T) {
	var replies bytes.Buffer
	e := newTestEmulator(t, 4, 40, &replies, 0)
	_, _ = e.Write([]byte("\x1b]11;?\a"))
	e.Clear()
	e.SetColors(color.White, color.Black)
	if replies.Len() != 0 {
		t.Fatal("replied to an old command's pending query")
	}
	_, _ = e.Write([]byte("\x1b]11;?\a"))
	if replies.Len() == 0 {
		t.Fatal("new command lost color responder")
	}
}

func TestCursorVisibilityFromFirstHide(t *testing.T) {
	e := newTestEmulator(t, 4, 40, nil, 0)
	for _, tc := range []struct {
		seq     string
		visible bool
	}{
		{"", true}, {"\x1b[?25l", false}, {"\x1b[?25h", true},
		{"\x1b[?1049h", true}, {"\x1b[?25l", false},
		{"\x1b[?1049l", false}, {"\x1b[?1049h", false},
	} {
		_, _ = e.Write([]byte(tc.seq))
		_, _, got := e.CursorPos()
		if got != tc.visible {
			t.Errorf("after %q: visible=%v, want %v", tc.seq, got, tc.visible)
		}
		_ = e.RenderLine(0)
		_, _, after := e.CursorPos()
		if after != got {
			t.Fatal("render changed cursor visibility")
		}
	}
}

func TestGroupedModesAndClear(t *testing.T) {
	e := newTestEmulator(t, 4, 40, nil, 0)
	for _, chunk := range []string{"\x1b[?1;100", "7;2004h"} {
		_, _ = e.Write([]byte(chunk))
	}
	if !e.ApplicationCursorKeys() || !e.AlternateScroll() || !e.BracketedPaste() {
		t.Fatal("grouped input modes not tracked")
	}
	_, _ = e.Write([]byte("\x1b[48;2;30;40;50m prompt     \x1b[0m"))
	if !strings.Contains(e.RenderLine(0), "48;2;30;40;50") {
		t.Fatal("explicit prompt background lost")
	}
	_, _ = e.Write([]byte("\x1b[?1049h\x1b[?25l\x1b[31m\x1b[1;2r"))
	e.Clear()
	if e.ApplicationCursorKeys() || !e.AlternateScroll() || e.BracketedPaste() || e.IsAlt() {
		t.Fatal("old command modes leaked after clear")
	}
	_, _ = e.Write([]byte("fresh"))
	if out := e.RenderLine(0); strings.Contains(out, "31m") {
		t.Fatalf("old style leaked: %q", out)
	}
	if top, bottom := e.scrollRegion(); top != 0 || bottom != 3 {
		t.Fatal("old scroll region leaked")
	}
}

func TestResizeCollapsedConsole(t *testing.T) {
	c := NewConsole(4, 40, 0, nil)
	t.Cleanup(func() { _ = c.Close() })
	c.Resize(0, 0)
	_, _ = c.Write([]byte("x"))
	if c.Rows() != 1 || c.rows != 1 || c.cols != 1 {
		t.Fatal("collapsed console did not keep valid geometry")
	}
	c.Resize(4, 40)
	_, _ = c.Write([]byte("\r\nrestored"))
	if !strings.Contains(c.GridText(), "restored") {
		t.Fatal("console did not recover from collapsed geometry")
	}
}

type yieldingWriter struct {
	mu sync.Mutex
	bytes.Buffer
}

func (w *yieldingWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		w.mu.Lock()
		_ = w.WriteByte(b)
		w.mu.Unlock()
		runtime.Gosched()
	}
	return len(p), nil
}

func TestTerminalRepliesAndInputSerialized(t *testing.T) {
	var output yieldingWriter
	e := newTestEmulator(t, 4, 40, &output, 0)
	e.SetColors(color.White, color.Black)
	var wg sync.WaitGroup
	for worker := range 2 {
		wg.Go(func() {
			for range 100 {
				if worker == 0 {
					_, _ = e.Write([]byte("\x1b]11;?\a"))
				} else {
					_, _ = e.input.Write([]byte("\x1b[1;3A"))
				}
			}
		})
	}
	wg.Wait()
	got := output.String()
	for _, packet := range []string{"\x1b]11;rgb:0000/0000/0000\a", "\x1b[1;3A"} {
		if n := strings.Count(got, packet); n != 100 {
			t.Fatalf("interleaved writes: found %d of %q", n, packet)
		}
		got = strings.ReplaceAll(got, packet, "")
	}
	if got != "" {
		t.Fatalf("unexpected input bytes: %q", got)
	}
}

func newTestEmulator(t *testing.T, rows, cols int, responses io.Writer, history int) *emulator {
	t.Helper()
	e := newEmulator(rows, cols, responses, history)
	if e.err != nil {
		t.Fatal(e.err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// ClearKeepHistory gives the next command the same fresh modes, colour
// responder and parser as Clear, but earlier rows stay and the new output
// starts below them.
func TestClearKeepHistoryKeepsRowsResetsState(t *testing.T) {
	var replies bytes.Buffer
	e := newTestEmulator(t, 4, 40, &replies, 0)
	_, _ = e.Write([]byte("earlier command\r\n\x1b[?1;2004h\x1b]11;?\a\x1b[31m\x1b[?1049h\x1b]0;unfinished"))
	e.ClearKeepHistory()
	e.SetColors(color.White, color.Black)
	if replies.Len() != 0 {
		t.Fatal("replied to an old command's pending query")
	}
	if e.ApplicationCursorKeys() || e.BracketedPaste() || e.IsAlt() {
		t.Fatal("old command modes leaked")
	}
	_, _ = e.Write([]byte("fresh\x1b]11;?\a"))
	if replies.Len() == 0 {
		t.Fatal("new command lost color responder")
	}
	if got := ansi.Strip(e.RenderLine(0)); !strings.HasPrefix(got, "earlier command") {
		t.Fatalf("earlier row lost: %q", got)
	}
	if out := e.RenderLine(1); !strings.HasPrefix(ansi.Strip(out), "fresh") || strings.Contains(out, "31m") {
		t.Fatalf("new output %q", out)
	}
}

package mermaid

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestFrameRefusesLengthsBeforeReadingPayload(t *testing.T) {
	for _, n := range []uint32{0, maxRequest + 1, ^uint32(0)} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		if _, err := readFrame(bytes.NewReader(h[:]), maxRequest); err == nil {
			t.Fatalf("accepted length %d", n)
		}
	}
	for _, p := range [][]byte{nil, {0}, {0, 0, 0}, {0, 0, 0, 2, 'x'}} {
		if _, err := readFrame(bytes.NewReader(p), maxRequest); err == nil {
			t.Fatalf("accepted incomplete frame %q", p)
		}
	}
	var stream bytes.Buffer
	for _, source := range []string{"flowchart LR\n A-->B", "sequenceDiagram\n A->>B: hi"} {
		want := request{protocol, moduleHash(), 80, "grapheme", source}
		if err := writeFrame(&stream, want, maxRequest); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		p, err := readFrame(&stream, maxRequest)
		var req request
		if err != nil || strictJSON(p, &req) != nil || !req.valid() {
			t.Fatalf("round trip: %v %s", err, p)
		}
	}
	if _, err := readFrame(&stream, maxRequest); err != io.EOF {
		t.Fatalf("clean EOF: %v", err)
	}
}

func TestProtocolRefusesUntrustedContent(t *testing.T) {
	for _, p := range []string{`{"v":1,"extra":true}`, `{} {}`, `null true`, `{`} {
		if err := strictJSON([]byte(p), new(request)); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	valid := request{protocol, moduleHash(), 80, "grapheme", "flowchart LR\n A-->B"}
	if !valid.valid() {
		t.Fatal("valid request rejected")
	}
	for _, source := range []string{"", "\xff", "x\t", "x\r", "\x1b[31m", "\x00"} {
		req := valid
		req.Source = source
		if req.valid() {
			t.Fatalf("accepted source %q", source[:min(20, len(source))])
		}
	}
	for _, modify := range []func(*request){
		func(r *request) { r.Version++ }, func(r *request) { r.Artifact = "other" },
		func(r *request) { r.Width = 0 }, func(r *request) { r.Width = 4097 },
		func(r *request) { r.Method = "unknown" },
	} {
		req := valid
		modify(&req)
		if req.valid() {
			t.Fatalf("accepted %+v", req)
		}
	}
}

func TestDrawingBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		width      int
		status     Status
	}{
		{"normal", "┌─┐\n│A│\n└─┘\n", 3, OK},
		{"too wide", "abcd", 3, TooWide},
		{"TAB", "\t", 80, Failed}, {"CR", "x\r\n", 80, Failed},
		{"ESC", "\x1b[31mred", 80, Failed}, {"BEL", "\a", 80, Failed},
		{"UTF8", "\xff", 80, Failed}, {"empty", "", 80, Failed},
		{"rows at cap", strings.Repeat("x\n", MaxRows), 80, OK},
		{"rows past cap", strings.Repeat("x\n", MaxRows+1), 80, TooTall},
		{"bytes at cap", strings.Repeat("x", MaxDrawing), MaxDrawing, OK},
		{"bytes past cap", strings.Repeat("x", MaxDrawing+1), MaxDrawing, Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := verifyDrawing(tc.text, tc.width, ansi.GraphemeWidth)
			if r.Status != tc.status {
				t.Fatalf("got %+v, want %s", r, tc.status)
			}
			if !r.valid(tc.width, ansi.GraphemeWidth) {
				t.Fatalf("own result rejected: %+v", r)
			}
		})
	}
	for _, r := range []Result{
		{Status: OK}, {Status: OK, Lines: []string{"a\nb"}},
		{Status: OK, Lines: []string{"\x1b"}}, {Status: OK, Lines: []string{"x"}, NeedCols: 2},
		{Status: TooWide, NeedCols: 80}, {Status: TooWide, NeedCols: 81, Lines: []string{"x"}},
		{Status: ParseError, Lines: []string{"oops"}}, {Status: Failed, NeedCols: 2},
		{Status: Busy}, {Status: "evil"},
	} {
		if r.valid(80, ansi.GraphemeWidth) {
			t.Fatalf("accepted invalid worker reply %+v", r)
		}
	}
	// The actual display width method, not Rust's width, is authoritative.
	if r := verifyDrawing("▶️", 1, ansi.GraphemeWidth); r.Status != TooWide || r.NeedCols != 2 {
		t.Fatalf("grapheme width: %+v", r)
	}
	if r := verifyDrawing("▶️", 1, ansi.WcWidth); r.Status != OK {
		t.Fatalf("wcwidth: %+v", r)
	}
}

func FuzzFramingAndDrawing(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 'x'})
	f.Add([]byte("┌──┐\n│hi│\n└──┘"))
	f.Fuzz(func(t *testing.T, p []byte) {
		if len(p) > MaxDrawing+1 {
			return
		}
		_, _ = readFrame(bytes.NewReader(p), maxRequest)
		_ = verifyDrawing(string(p), 80, ansi.GraphemeWidth)
		_ = strictJSON(p, new(response))
	})
}

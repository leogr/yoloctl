package panes

import (
	"flag"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/tui/term"
)

var update = flag.Bool("update", false, "rewrite the composition goldens")

// frameRows renders text through a real terminal core of rows×cols and
// returns its styled rows, as a pane's frame provides them.
// live is a pane with a session, as every pane but the empty one has; the
// session never starts, so the title falls back to the command.
func live(command string) *Pane { return &Pane{Session: new(term.Session), Command: command} }

func frameRows(t *testing.T, rows, cols int, text string) []string {
	t.Helper()
	term, err := terminal.New(rows, cols, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = term.Close() }()
	if _, err := term.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	f, err := term.Frame(false)
	if err != nil {
		t.Fatal(err)
	}
	return f.Lines
}

func golden(t *testing.T, name string, rows []string) {
	t.Helper()
	got := strings.Join(rows, "\n") + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from its golden:\n%s", name, strings.Join(plain(rows), "\n"))
	}
}

func plain(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = ansi.Strip(r)
	}
	return out
}

// Three panes in a 60×12 tab: borders with a junction, the focused pane's
// border in the focus colour, a wide character cut at a border, a colour
// run that stops at its pane's edge, and no cursor anywhere.
func TestComposeThreePanes(t *testing.T) {
	left, top, bottom := live(""), live(""), live("")
	l := New(left)
	l.Resize(60, 12)
	if err := l.Split(true, top); err != nil {
		t.Fatal(err)
	}
	if err := l.Split(false, bottom); err != nil {
		t.Fatal(err)
	}
	l.SetFocus(left)
	lr, tr, br := left.Rect(), top.Rect(), bottom.Rect()
	rows := map[*Pane][]string{
		// A red run erased to the edge must stop at the border.
		left: frameRows(t, lr.H, lr.W, "shell $ make check\r\n\x1b[41;97m red to the edge\x1b[K"),
		// Wider than its pane (as a D1 frame can be): the wide character on
		// the pane's last column is cut and becomes a space.
		top:    frameRows(t, tr.H, tr.W+2, "chat\r\n"+strings.Repeat("x", tr.W-1)+"漢"),
		bottom: frameRows(t, br.H, br.W, "space view\r\n● opus idle"),
	}
	out := Compose(l, rows)
	golden(t, "three-panes", out)
	p := plain(out)
	if len(out) != 12 {
		t.Fatalf("rows: %d", len(out))
	}
	for i, row := range p {
		if w := ansi.StringWidth(row); w != 60 {
			t.Fatalf("row %d is %d wide: %q", i, w, row)
		}
	}
	cells := uv.NewStyledString(out[1]).Lines(ansi.GraphemeWidth)[0]
	if cells[lr.W-1].Style.Bg == nil || cells[lr.W].Content != "│" || cells[lr.W].Style.Bg != nil || cells[lr.W].Style.Fg == nil {
		t.Fatalf("the red run ends at its pane's edge and the border is unstyled by it: %+v / %+v", cells[lr.W-1], cells[lr.W])
	}
	if strings.Contains(p[1][lr.W+1:], "漢") || !strings.HasSuffix(strings.TrimRight(p[1], " "), "x") {
		t.Fatalf("the wide cell at the border is cut: %q", p[1])
	}
	if strings.ContainsAny(strings.Join(p, ""), "─├┤┬┴┼") {
		t.Fatalf("no line or junction glyph on bar rows: %q", p)
	}
	// The top-right bar is the horizontal border: a blue band, not
	// underlined; the vertical border crossing it is a │ on the terminal
	// background, orange there because the focused pane's content runs
	// beside it.
	upper := uv.NewStyledString(out[tr.H]).Lines(ansi.GraphemeWidth)[0]
	for _, c := range []uv.Cell{upper[lr.W+1], upper[len(upper)-1]} {
		if c.Style.Bg != unfocusedBar.band || c.Style.Underline != uv.UnderlineNone {
			t.Fatalf("the upper bar is a blue band without underline: %+v", c)
		}
	}
	if j := upper[lr.W]; j.Content != "│" || j.Style.Bg != nil || j.Style.Fg != focusColor {
		t.Fatalf("the border on the upper bar row is an orange │ off the band: %+v", j)
	}
	if b := upper[len(upper)-3]; b.Content != "✕" || b.Style.Bg != unfocusedBar.button || b.Style.Fg != unfocusedBar.glyph {
		t.Fatalf("✕ on its blue button: %+v", b)
	}
	// The bottom row: the focused bar is orange, the other blue, and the
	// border between them a half block leaning toward the focused bar.
	last := uv.NewStyledString(out[len(out)-1]).Lines(ansi.GraphemeWidth)[0]
	if last[0].Style.Bg != focusedBar.band || last[1].Style.Fg != focusedBar.text || last[lr.W+1].Style.Bg != unfocusedBar.band || last[lr.W+2].Style.Fg != unfocusedBar.text {
		t.Fatal("the focused bar is orange with dark text, the other blue")
	}
	if j := last[lr.W]; j.Content != "▌" || j.Style.Bg != nil || j.Style.Fg != focusColor {
		t.Fatalf("▌ leans toward the focused bar on its left: %+v", j)
	}
}

// A D1 short frame (the shared session is smaller than this pane): rows
// past the frame are blank and the border stays whole.
func TestComposeShortFrameKeepsTheBorder(t *testing.T) {
	a, b := live(""), live("")
	l := New(a)
	l.Resize(50, 10)
	if err := l.Split(true, b); err != nil {
		t.Fatal(err)
	}
	ar, br := a.Rect(), b.Rect()
	out := Compose(l, map[*Pane][]string{
		a: frameRows(t, 4, ar.W, "short\r\nframe"), // 4 of 10 rows
		b: frameRows(t, br.H, br.W, "full"),
	})
	golden(t, "short-frame", out)
	for i, row := range plain(out)[:ar.H] {
		if []rune(row)[ar.W] != '│' {
			t.Fatalf("row %d lost its border: %q", i, row)
		}
	}
}

// Zoomed: one pane fills the tab above its bar, which says so; no vertical
// border is drawn.
func TestComposeZoomedKeepsItsBar(t *testing.T) {
	a, b := live("yoloctl box attach a"), live("yoloctl box attach b")
	l := New(a)
	l.Resize(40, 12)
	if err := l.Split(false, b); err != nil {
		t.Fatal(err)
	}
	l.ToggleZoom()
	out := plain(Compose(l, map[*Pane][]string{b: frameRows(t, 11, 40, "zoomed")}))
	golden(t, "zoomed", out)
	if strings.ContainsAny(strings.Join(out[:11], ""), "│─├┤┬┴┼") || !strings.HasPrefix(out[0], "zoomed") {
		t.Fatalf("zoomed view: %q", out)
	}
	if !strings.HasPrefix(out[11], " ▸ box attach b · zoomed ") {
		t.Fatalf("the zoomed pane's bar: %q", out[11])
	}
	l.ToggleZoom()
	if out := plain(Compose(l, map[*Pane][]string{})); !strings.Contains(out[5], "box attach a") || strings.Contains(out[11], "zoomed") {
		t.Fatalf("zoom and back: %q", out)
	}
}

// A marked pane other than the focused one shows ● in its bar; the focused
// pane's mark is never drawn (focusing clears it anyway). An exited pane
// says so.
func TestComposeAttentionMarkAndState(t *testing.T) {
	left, top, bottom := live("left"), live("top"), live("bottom")
	l := New(left)
	l.Resize(60, 12)
	if err := l.Split(true, top); err != nil {
		t.Fatal(err)
	}
	if err := l.Split(false, bottom); err != nil {
		t.Fatal(err)
	}
	l.SetFocus(left)
	bottom.Mark, left.Mark, top.Exited = true, true, true
	out := Compose(l, map[*Pane][]string{})
	golden(t, "mark-and-state", plain(out))
	p := plain(out)
	if bar := bottom.Bar(); !strings.HasPrefix(string([]rune(p[bar.Y])[bar.X:]), "  ● bottom ") {
		t.Fatalf("the bottom pane's mark leads its bar: %q", p[bar.Y])
	}
	if strings.Count(strings.Join(p, ""), "●") != 1 {
		t.Fatalf("only the unfocused marked pane shows a mark:\n%s", strings.Join(p, "\n"))
	}
	if bar := top.Bar(); !strings.Contains(p[bar.Y], "top · exited") {
		t.Fatalf("exited: %q", p[bar.Y])
	}
	if l.SetFocus(bottom); bottom.Mark {
		t.Fatal("focusing clears the mark")
	}
}

// A bar degrades as columns run out: the title truncates, then goes, then
// the zoom button, then ✕; at two columns it is a plain band. The bar has
// no split buttons and no words on its buttons at any width.
func TestBarDegradation(t *testing.T) {
	var got []string
	for _, w := range []int{70, 40, 20, 13, 12, 7, 4, 2} {
		p := live("box attach coder --space dev")
		l := &Layout{focus: p}
		got = append(got, fmt.Sprintf("%2d|%s|", w, barText(l, p, w)))
	}
	golden(t, "bar-degradation", got)
	want := []string{
		"70| ▸ box attach coder --space dev                                ⤢   ✕  |",
		"40| ▸ box attach coder --space d…   ⤢   ✕  |",
		"20| ▸ box at…   ⤢   ✕  |",
		"13| ▸    ⤢   ✕  |",
		"12| ▸   ⤢   ✕  |",
		" 7| ▸  ✕  |",
		" 4| ▸  |",
		" 2|  |",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("width %s:\n got %q\nwant %q", want[i][:2], got[i], want[i])
		}
	}
}

// barText renders one pane's bar alone at width w, as plain text: every
// cell is band, including the line laid under it.
func barText(l *Layout, p *Pane, w int) string {
	grid := [][]cell{make([]cell, w)}
	for x := range grid[0] {
		grid[0][x] = cell{Cell: uv.Cell{Content: "─", Width: 1}}
	}
	drawBar(grid, l, p, Rect{0, 0, w, 1})
	return ansi.Strip(renderRow(grid[0]))
}

// A wide and a combining-character title are measured, not counted in runes.
func TestBarWideAndCombiningTitles(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"漢字のタイトル", " ▸ 漢字の…   ⤢   ✕  "},
		{"cafe\u0301 au lait", " ▸ cafe\u0301 a…   ⤢   ✕  "},
	} {
		p := live(c.title)
		l := &Layout{focus: p}
		if got := barText(l, p, 20); got != c.want || ansi.StringWidth(got) != 20 {
			t.Errorf("%q: %q (%d columns)", c.title, got, ansi.StringWidth(got))
		}
	}
}

// While a border is dragged the preview is drawn, its border orange, and
// the panes show their committed frames clipped or short.
func TestComposePreview(t *testing.T) {
	left, right := live("left"), live("right")
	l := New(left)
	l.Resize(60, 8)
	if err := l.Split(true, right); err != nil {
		t.Fatal(err)
	}
	l.SetFocus(right)
	lr, rr := left.Rect(), right.Rect()
	rows := map[*Pane][]string{
		left:  frameRows(t, lr.H, lr.W, strings.Repeat("L", lr.W)),
		right: frameRows(t, rr.H, rr.W, strings.Repeat("R", rr.W)),
	}
	s, _ := l.BorderAt(lr.W, 0)
	l.Preview(s, 35)
	out := Compose(l, rows)
	golden(t, "preview", plain(out))
	p := plain(out)
	if []rune(p[0])[35] != '│' || !strings.HasPrefix(p[0], strings.Repeat("L", lr.W)+strings.Repeat(" ", 35-lr.W)) {
		t.Fatalf("the left frame is short in the wider preview: %q", p[0])
	}
	cells := uv.NewStyledString(out[0]).Lines(ansi.GraphemeWidth)[0]
	if cells[35].Style.Fg != focusColor {
		t.Fatal("the dragged border is orange")
	}
	if left.Rect() != lr || right.Rect() != rr {
		t.Fatal("committed rectangles are unchanged")
	}
}

// r4: five panes, the focused one between two columns: its bar row shows ▐
// on its left border and ▌ on its right, both leaning toward it in orange;
// no border-column cell of a bar row takes a band background.
func TestComposeHalfBlocksLeanToTheFocusedBar(t *testing.T) {
	left, mid, right := live("left"), live("mid"), live("right")
	l := New(left)
	l.Resize(90, 12)
	for _, s := range []struct {
		at       *Pane
		vertical bool
		p        *Pane
	}{{left, true, mid}, {mid, true, right}, {right, false, live("right2")}, {left, false, live("left2")}} {
		l.SetFocus(s.at)
		if err := l.Split(s.vertical, s.p); err != nil {
			t.Fatal(err)
		}
	}
	l.SetFocus(mid)
	out := Compose(l, map[*Pane][]string{})
	golden(t, "five-panes", out)
	m, fb := mid.Rect(), mid.Bar()
	cols := []int{m.X - 1, m.X + m.W}
	for y, row := range out {
		cells := uv.NewStyledString(row).Lines(ansi.GraphemeWidth)[0]
		for _, x := range cols {
			c := cells[x]
			if c.Style.Bg != nil {
				t.Fatalf("row %d: the border column takes no band: %+v", y, c)
			}
			want, fg := "│", color.Color(focusColor)
			switch {
			case y == fb.Y && x == cols[0]:
				want = "▐"
			case y == fb.Y && x == cols[1]:
				want = "▌"
			}
			if c.Content != want || c.Style.Fg != fg {
				t.Fatalf("row %d col %d: %q %v, want %q in orange", y, x, c.Content, c.Style.Fg, want)
			}
		}
	}
}

// r4 r1.1/r1.2 (PREVIEW-r7): three panes stacked left with the middle one
// focused, two stacked right. The separator is orange from the focused
// pane's first content row to its own bar row, where it is ▌; on the bar
// row above it (the upper pane's, blue) it is a blue │.
func TestComposeStackedFocusSeparator(t *testing.T) {
	l1, r1 := live("upper"), live("go test")
	l := New(l1)
	l.Resize(80, 30)
	r2, l2, l3 := live("airc"), live("claude"), live("git")
	for _, s := range []struct {
		at       *Pane
		vertical bool
		p        *Pane
	}{{l1, true, r1}, {r1, false, r2}, {l1, false, l2}, {l2, false, l3}} {
		l.SetFocus(s.at)
		if err := l.Split(s.vertical, s.p); err != nil {
			t.Fatal(err)
		}
	}
	l.SetFocus(l2)
	out := Compose(l, map[*Pane][]string{})
	golden(t, "stacked-focus", out)
	f, fb := l2.Rect(), l2.Bar()
	x := f.X + f.W
	at := func(y int) uv.Cell { return uv.NewStyledString(out[y]).Lines(ansi.GraphemeWidth)[0][x] }
	for _, c := range []struct {
		y     int
		glyph string
		fg    color.Color
	}{{f.Y - 1, "│", borderColor}, {f.Y, "│", focusColor}, {f.Y + f.H - 1, "│", focusColor}, {fb.Y, "▌", focusColor}, {fb.Y + 1, "│", borderColor}} {
		if got := at(c.y); got.Content != c.glyph || got.Style.Fg != c.fg || got.Style.Bg != nil {
			t.Fatalf("row %d: %q %v, want %q %v off the band", c.y, got.Content, got.Style.Fg, c.glyph, c.fg)
		}
	}
	if bar := uv.NewStyledString(out[f.Y-1]).Lines(ansi.GraphemeWidth)[0]; bar[0].Style.Bg != unfocusedBar.band {
		t.Fatal("the bar above the focused pane is the upper pane's, blue")
	}
}

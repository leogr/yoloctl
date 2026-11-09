package chatui

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// stripped drops the table metadata from lines: what bodyRows rendered
// before tables were fitted to the view.
func stripped(lines []displayLine) []displayLine {
	out := slices.Clone(lines)
	for i := range out {
		out[i].Table, out[i].Cells = nil, nil
	}
	return out
}

func tableView(text string, width int) ([]displayLine, []bodyRow) {
	theme := New(Options{}).theme
	lines := displayLines(ansi.WcWidth, text, false, theme, "")
	return lines, bodyRows(ansi.WcWidth, lines, width, theme)
}

// separators returns the cells where a row's " │ " separators start.
func separators(runs []run) []int {
	var at []int
	x := 0
	for _, r := range runs {
		if !r.Copy && r.Text == " │ " {
			at = append(at, x)
		}
		x += ansi.StringWidthWc(r.Text)
	}
	return at
}

// ruleDividers returns the cells of header crossings or body-rule dividers.
func ruleDividers(rule string) []int {
	var at []int
	for x, r := range []rune(rule) {
		if r == '┼' || r == '│' {
			at = append(at, x)
		}
	}
	return at
}

// checkWrappedTable checks every row of the table in lines: rows fit width,
// separators stay in the same cells on every row and line up with the
// header/body-rule dividers, and each cell's fragments cover exactly its bytes, in
// order, hanging spaces included.
func checkWrappedTable(t *testing.T, lines []displayLine, rows []bodyRow, width int) {
	t.Helper()
	var seps []int
	wrapped := false
	for _, row := range rows {
		dl := lines[row.Line]
		if dl.Table == nil {
			continue
		}
		if w := runsWidth(ansi.WcWidth, row.Runs); w > width {
			t.Fatalf("line %d: a row %d cells wide in %d", row.Line, w, width)
		}
		at := separators(row.Runs)
		if row.Decor {
			at = ruleDividers(row.Runs[0].Text)
			for i := range at {
				at[i]-- // each divider is the middle of " │ "
			}
		}
		if seps == nil {
			seps = at
		} else if !slices.Equal(at, seps) {
			t.Fatalf("line %d: separators at %v, other rows at %v", row.Line, at, seps)
		}
		wrapped = wrapped || row.Frags != nil
	}
	if !wrapped {
		t.Fatal("the table was not wrapped to the view")
	}
	for n, dl := range lines {
		if dl.Cells == nil {
			continue
		}
		for c, cr := range dl.Cells {
			next := cr[0]
			for _, row := range rows {
				if row.Line != n {
					continue
				}
				for _, f := range row.Frags {
					if f[0] >= cr[0] && f[1] <= cr[1] && f[0] < f[1] {
						if f[0] != next {
							t.Fatalf("line %d cell %d: fragment %v after byte %d", n, c, f, next)
						}
						next = f[1]
					}
				}
			}
			if next != cr[1] {
				t.Fatalf("line %d cell %d: fragments end at %d of %v (%q)", n, c, next, cr, dl.Text[cr[0]:cr[1]])
			}
		}
	}
}

// A table that fits the view renders exactly as before: same rows, bytes,
// styles and targets, plus decorative body-row dividers and the empty row
// that separates it from the line after it.
func TestFittingTablesRenderAsBefore(t *testing.T) {
	theme := New(Options{}).theme
	lines := displayLines(ansi.WcWidth, sampleTable+"\nafter", false, theme, "")
	natural := 0
	for _, dl := range lines {
		natural = max(natural, ansi.StringWidthWc(dl.Text))
	}
	after := len(lines) - 1
	for _, width := range []int{natural, natural + 1, 200} {
		want := bodyRows(ansi.WcWidth, stripped(lines), width, theme)
		at := slices.IndexFunc(want, func(r bodyRow) bool { return r.Line == after })
		want = slices.Insert(want, at, bodyRow{Line: after, Decor: true})
		got := bodyRows(ansi.WcWidth, lines, width, theme)
		got = slices.DeleteFunc(got, tableRowRuleDecoration)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("width %d: a fitting table changed:\n%+v\n%+v", width, got, want)
		}
	}
}

// A table wider than the view keeps its columns: separators and the rule
// stay in place on every row, cells wrap inside their columns, the header
// stays bold and every cell byte, empty and unequal cells included, belongs
// to exactly one fragment.
func TestNarrowTablesWrapCellsInTheirColumns(t *testing.T) {
	text := "| Item | Detail | Done |\n|---|---|---|\n" +
		"| short | " + strings.Repeat("several words of detail ", 6) + "| ✓ |\n" +
		"| a much longer item name |  | x |\n" +
		"|  | one |  |"
	for _, width := range []int{24, 31, 40, 57} {
		lines, rows := tableView(text, width)
		checkWrappedTable(t, lines, rows, width)
		for _, row := range rows {
			if row.Line != 0 {
				continue
			}
			for _, r := range row.Runs {
				if r.Copy && !r.Style.Bold {
					t.Fatalf("width %d: header text %q is not bold", width, r.Text)
				}
			}
		}
	}
}

// Wrapped cells keep their column's alignment on every row.
func TestWrappedCellsKeepAlignment(t *testing.T) {
	words := strings.Repeat("ab cdef g ", 5)
	text := "| L | C | R |\n|:---|:---:|---:|\n| " + words + "| " + words + "| " + words + "|"
	const width = 34
	lines, rows := tableView(text, width)
	checkWrappedTable(t, lines, rows, width)
	var rule string
	for _, row := range rows {
		if row.Decor && lines[row.Line].Table != nil {
			rule = row.Runs[0].Text
		}
	}
	parts := strings.Split(rule, "─┼─")
	starts := []int{0}
	for _, p := range parts[:len(parts)-1] {
		starts = append(starts, starts[len(starts)-1]+ansi.StringWidthWc(p)+3)
	}
	for _, row := range rows {
		if row.Line != 2 {
			continue
		}
		dl := lines[2]
		for c, cr := range dl.Cells {
			lo, hi, x := -1, -1, 0
			for _, r := range row.Runs {
				w := ansi.StringWidthWc(r.Text)
				if r.Copy && r.Start >= cr[0] && r.Start < cr[1] {
					if lo < 0 {
						lo = x
					}
					hi = x + w
				}
				x += w
			}
			if lo < 0 {
				continue
			}
			alloc := ansi.StringWidthWc(parts[c])
			before, after := lo-starts[c], starts[c]+alloc-hi
			switch dl.Table.Align[c] {
			case 'l':
				if before != 0 {
					t.Fatalf("left column starts %d cells in", before)
				}
			case 'c':
				if before != (before+after)/2 {
					t.Fatalf("centered fragment has %d cells before, %d after", before, after)
				}
			case 'r':
				if after != 0 {
					t.Fatalf("right column ends %d cells early", after)
				}
			}
		}
	}
}

// Wide, combining and emoji graphemes, CJK text and a long unbroken URL
// wrap inside their columns without overflowing them.
func TestWrappedCellsKeepGraphemesWhole(t *testing.T) {
	text := "| 名前 | 内容 |\n|---|---|\n" +
		"| 界界界界界界界界 éé | 👩‍💻👩‍💻 漢字かな交じり文 https://example.com/" + strings.Repeat("p", 60) + " |"
	for _, width := range []int{15, 22, 30} {
		lines, rows := tableView(text, width)
		checkWrappedTable(t, lines, rows, width)
	}
	if widestGrapheme(ansi.WcWidth, "ab界é") != 2 || widestGrapheme(ansi.WcWidth, "") != 0 {
		t.Fatal("widest grapheme")
	}
}

// Columns share the view deterministically: a column within its fair share
// keeps its natural width, none gets less than its floor, the rest share
// what remains with the remainder going left to right, and floors that do
// not fit refuse the layout.
func TestWaterFill(t *testing.T) {
	for _, tc := range []struct {
		natural, floor []int
		avail          int
		want           []int
	}{
		{[]int{2, 50, 30}, []int{2, 4, 4}, 40, []int{2, 19, 19}},
		{[]int{2, 50, 30}, []int{2, 4, 4}, 41, []int{2, 20, 19}},
		{[]int{1, 9, 9}, []int{1, 4, 4}, 9, []int{1, 4, 4}},
		{[]int{0, 20, 0, 20}, []int{0, 4, 0, 4}, 9, []int{0, 5, 0, 4}},
		{[]int{10, 10, 10}, []int{4, 4, 4}, 11, nil},
	} {
		got, ok := waterFill(tc.natural, tc.floor, tc.avail)
		if !slices.Equal(got, tc.want) || ok != (tc.want != nil) {
			t.Fatalf("waterFill(%v, %v, %d) = %v %v, want %v", tc.natural, tc.floor, tc.avail, got, ok, tc.want)
		}
	}
	r := rand.New(rand.NewSource(1))
	for range 2000 {
		cols := 1 + r.Intn(8)
		natural, floor := make([]int, cols), make([]int, cols)
		sum := 0
		for c := range cols {
			natural[c] = r.Intn(40)
			floor[c] = min(natural[c], tableFloor)
			sum += natural[c]
		}
		avail := r.Intn(sum + 1)
		alloc, ok := waterFill(natural, floor, avail)
		if !ok {
			need := 0
			for _, f := range floor {
				need += f
			}
			if need <= avail {
				t.Fatalf("refused %v %v in %d", natural, floor, avail)
			}
			continue
		}
		total := 0
		for c, a := range alloc {
			if a < floor[c] || a > natural[c] {
				t.Fatalf("column %d got %d outside [%d, %d]", c, a, floor[c], natural[c])
			}
			total += a
		}
		if total > avail {
			t.Fatalf("%v takes %d of %d", alloc, total, avail)
		}
		if again, _ := waterFill(natural, floor, avail); !slices.Equal(again, alloc) {
			t.Fatal("water-filling is not deterministic")
		}
	}
}

// When even the columns' floors cannot fit, the table wraps as prose, as
// before; no width panics.
func TestTablesTooNarrowForTheirFloorsWrapAsProse(t *testing.T) {
	text := "|" + strings.Repeat(" abcdef |", 12) + "\n|" + strings.Repeat("---|", 12) + "\n|" + strings.Repeat(" xyzzy plugh |", 12)
	theme := New(Options{}).theme
	lines := displayLines(ansi.WcWidth, text, false, theme, "")
	for width := 1; width <= 60; width++ {
		if got, want := bodyRows(ansi.WcWidth, lines, width, theme), bodyRows(ansi.WcWidth, stripped(lines), width, theme); !reflect.DeepEqual(got, want) {
			t.Fatalf("width %d: a table too narrow for its floors must wrap as prose", width)
		}
	}
}

// Links, mentions and shared m: references keep their targets on every
// fragment; separators and padding have none.
func TestWrappedCellsKeepTargets(t *testing.T) {
	url := "https://example.com/" + strings.Repeat("p", 60)
	ref, _ := refID(sharedTarget)
	lines, rows := tableView("| who | where |\n|---|---|\n| @sol | see "+url+" now "+sharedTarget+" |", 30)
	checkWrappedTable(t, lines, rows, 30)
	var links, mentions, refs int
	for _, row := range rows {
		for _, r := range row.Runs {
			switch {
			case !r.Copy && r.Act.Kind != actNone:
				t.Fatalf("decoration %q has a target", r.Text)
			case r.Act.Kind == actLink:
				if r.Act.Value != url {
					t.Fatalf("a link fragment points at %q", r.Act.Value)
				}
				links++
			case r.Act.Kind == actRef:
				if r.Act.Value != ref || ref == "" {
					t.Fatalf("a reference fragment points at %q, want %q", r.Act.Value, ref)
				}
				refs++
			case r.Act.Kind == actMention && r.Act.Value == "sol":
				mentions++
			}
		}
	}
	if links < 2 || refs < 2 || mentions != 1 {
		t.Fatalf("link fragments %d, reference fragments %d, mentions %d", links, refs, mentions)
	}
}

// A drag over a wrapped table copies the logical row: part of one cell
// across its fragments, or across cells with their separators, in either
// direction, and the same text after a resize. Padding and separators snap
// to cell text and carry no target.
func TestWrappedTableSelectionCopiesLogicalRows(t *testing.T) {
	m, copied := copyingModel(50, 30)
	text := "| Name | Notes |\n|---|---|\n| alpha beta gamma delta epsilon | one two three four five six seven eight nine ten |"
	m.Update(IncomingMsg{Message: Message{ID: "t", Nick: "x", Text: text}})
	m.View()
	key := "server:t"
	dl := m.body(&m.messages[0], m.lastTextWidth).lines[2]
	var notes [][2]int
	for _, row := range m.lastTranscript {
		if row.key == key && row.line == 2 {
			for _, f := range row.frags {
				if f[0] >= dl.Cells[1][0] {
					notes = append(notes, f)
				}
			}
		}
	}
	if len(notes) < 2 {
		t.Fatalf("the notes cell must wrap: %v", notes)
	}
	copyRange := func(a, b int) string {
		t.Helper()
		x0, y0 := cellOf(t, m, key, 2, a)
		x1, y1 := cellOf(t, m, key, 2, b)
		drag(m, x0, y0, x1, y1)
		forward := (*copied)[len(*copied)-1]
		drag(m, x1, y1, x0, y0)
		if backward := (*copied)[len(*copied)-1]; backward != forward {
			t.Fatalf("reverse drag copied %q, forward %q", backward, forward)
		}
		return forward
	}
	a, b := dl.Cells[1][0]+2, notes[1][0]+1
	if got := copyRange(a, b); got != dl.Text[a:b+1] {
		t.Fatalf("partial cell copied %q, want %q", got, dl.Text[a:b+1])
	}
	a = dl.Cells[0][0] + 1
	if got := copyRange(a, b); got != dl.Text[a:b+1] || !strings.Contains(got, " │ ") {
		t.Fatalf("cross-cell copy %q, want %q", got, dl.Text[a:b+1])
	}
	before, _ := m.selectedText(*m.sel)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	m.View()
	if after, _ := m.selectedText(*m.sel); after != before {
		t.Fatalf("resize changed the selection: %q -> %q", before, after)
	}
	if m.Messages()[0].Text != text {
		t.Fatal("rendering changed the original text")
	}
	for i, row := range m.lastTranscript {
		if row.key != key || row.frags == nil {
			continue
		}
		x := 0
		for _, r := range row.runs {
			if !r.Copy && r.Text == " │ " {
				y := i - m.scroll + m.layout.bodyTop
				p, ok := m.pointAt(x+1, y)
				boundary := p.lo == row.end
				for _, f := range row.frags {
					boundary = boundary || p.lo == f[0]
				}
				if !ok || !boundary || actionAt(ansi.WcWidth, row.runs, x+1).Kind != actNone {
					t.Fatalf("a separator click gave %+v on row %+v", p, row.frags)
				}
			}
			x += ansi.StringWidthWc(r.Text)
		}
	}
}

// The reading position inside a wrapped table stays on the row that shows
// the anchor's byte, whichever column it is in, across resizes, the panel
// and arrivals, even when an earlier column wraps far longer than a later
// one at another width. The row offset alone cannot tell: every row of a
// still-running first column starts before any byte of the second.
func TestWrappedTableKeepsTheReadingPosition(t *testing.T) {
	first := strings.Repeat("aa bb cc dd ", 4)
	second := strings.Repeat("the second column wraps much further ", 12)
	text := "| A | B |\n|---|---|\n| " + first + "| " + second + "|"
	for _, column := range []int{0, 1} {
		t.Run(fmt.Sprint("column", column), func(t *testing.T) {
			m, _ := copyingModel(60, 16)
			m.Update(IncomingMsg{Message: Message{ID: "t", Nick: "x", Text: text}})
			for i := range 12 {
				m.Update(IncomingMsg{Message: Message{ID: fmt.Sprint("n", i), Nick: "x", Text: "newer"}})
			}
			m.View()
			dl := m.body(&m.messages[0], m.lastTextWidth).lines[2]
			top := -1
			for i, row := range m.lastTranscript {
				if row.key != "server:t" || row.line != 2 || len(row.frags) == 0 {
					continue
				}
				f := row.frags[0]
				inFirst := f[0] < dl.Cells[1][0]
				if column == 0 && inFirst && f[0] > dl.Cells[0][0] || column == 1 && !inFirst && f[0] > dl.Cells[1][0] && top < 0 {
					top = i
				}
			}
			if top < 0 {
				t.Fatal("fixture has no row to read from")
			}
			m.follow, m.anchor, m.scroll = false, nil, top
			want := m.lastTranscript[top].offset
			check := func(step string) {
				t.Helper()
				m.View()
				row := m.lastTranscript[m.scroll]
				if m.anchor == nil || m.anchor.offset != want || row.key != "server:t" || row.line != 2 || !row.holds(want) {
					t.Fatalf("%s: top row %+v does not show byte %d (anchor %+v)", step, row, want, m.anchor)
				}
			}
			for _, w := range []int{40, 100, 36, 60} {
				m.Update(tea.WindowSizeMsg{Width: w, Height: 16})
				check(fmt.Sprint("width ", w))
			}
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
			press(m, 'p', tea.ModCtrl)
			check("panel")
			press(m, 'p', tea.ModCtrl)
			check("panel again")
			m.Update(IncomingMsg{Message: Message{ID: "late", Nick: "x", Text: "late"}})
			check("arrival")
		})
	}
	// The seam itself: the row whose fragment holds the byte wins over a
	// later row of the same line whose offset also precedes it.
	m, _ := readyModel()
	m.follow, m.anchor = false, &viewAnchor{key: "k", line: 2, offset: 50}
	rows := []transcriptRow{
		{key: "k", line: 2, offset: 0, end: 60, frags: [][2]int{{0, 5}, {45, 55}}},
		{key: "k", line: 2, offset: 5, end: 60, frags: [][2]int{{5, 10}, {55, 60}}},
		{key: "k", line: 2, offset: 10, end: 15, frags: [][2]int{{10, 15}}},
	}
	for range 20 {
		rows = append(rows, transcriptRow{key: "other"})
	}
	m.place(rows, 3)
	if m.scroll != 0 {
		t.Fatalf("the anchor went to row %d, not the row showing its byte", m.scroll)
	}
}

// tableModel shows the table in text at width, with newer messages below it
// so the reading position is never clamped to the live tail.
func tableModel(width int, text string) *Model {
	m, _ := copyingModel(width, 16)
	m.Update(IncomingMsg{Message: Message{ID: "t", Nick: "x", Text: text}})
	for i := range 12 {
		m.Update(IncomingMsg{Message: Message{ID: fmt.Sprint("n", i), Nick: "x", Text: "newer"}})
	}
	m.View()
	return m
}

// A table that fit, read from a row whose first cell is padded or empty,
// keeps that row on top once narrowing wraps it. The anchor's offset 0 lies
// in padding no fragment shows, every wrapped row of its line starts later,
// and the header above has grown: only the next cell text can place it.
func TestFitToWrappedTableKeepsPaddedRows(t *testing.T) {
	long := strings.Repeat("wrapping text ", 3)
	for name, body := range map[string]string{"padded": "| x | " + long + "|", "empty": "|  | " + long + "|"} {
		t.Run(name, func(t *testing.T) {
			m := tableModel(120, "| A long right-aligned header | B |\n|---:|---|\n"+body)
			atLine := func(r transcriptRow) bool { return r.key == "server:t" && r.line == 2 }
			top := slices.IndexFunc(m.lastTranscript, atLine)
			if top < 0 || m.lastTranscript[top].frags != nil {
				t.Fatal("fixture must fit the wide view")
			}
			m.follow, m.anchor, m.scroll = false, nil, top
			m.Update(tea.WindowSizeMsg{Width: 50, Height: 16})
			m.View()
			first := slices.IndexFunc(m.lastTranscript, atLine)
			if row := m.lastTranscript[m.scroll]; m.anchor == nil || m.anchor.offset != 0 || row.frags == nil || m.scroll != first || first == top {
				t.Fatalf("top row %d %+v, want the padded line's first row %d (was %d)", m.scroll, row, first, top)
			}
		})
	}
}

// An anchor in a wrapped table's separator, which no row shows, goes to the
// row showing the next cell text: the second cell's first row, not a later
// row of the taller first cell, though that row's offset precedes it too.
func TestWrappedTableGapAnchorGoesToTheNextCellText(t *testing.T) {
	m := tableModel(50, "| A | B |\n|---|---|\n| "+strings.Repeat("first column words ", 4)+"| short |")
	dl := m.body(&m.messages[0], m.lastTextWidth).lines[2]
	gap := dl.Cells[0][1] + 1 // the separator's bar
	m.follow, m.anchor = false, &viewAnchor{key: "server:t", line: 2, offset: gap}
	m.View()
	row := m.lastTranscript[m.scroll]
	if row.key != "server:t" || row.line != 2 || !row.holds(dl.Cells[1][0]) {
		t.Fatalf("a gap anchor placed row %+v, want the row showing byte %d", row, dl.Cells[1][0])
	}
	later := m.lastTranscript[m.scroll+1]
	if later.key != "server:t" || later.line != 2 || later.offset > gap || later.holds(gap) {
		t.Fatalf("fixture needs a later row whose offset precedes the gap: %+v", later)
	}
}

// A wrapped table stays within a fixed budget, twice its source plus
// tableSlack bytes, and the rows left: over either, the whole table wraps
// as prose, counted before any of its rows is built.
func TestWrappedTablesAreBounded(t *testing.T) {
	row := func(n int) string {
		return strings.Repeat("| "+strings.Repeat("long words ", 18)+"|"+strings.Repeat(" wxyz |", 7)+"\n", n)
	}
	header := "| h |" + strings.Repeat(" wxyz |", 7) + "\n|" + strings.Repeat("---|", 8) + "\n"
	theme := New(Options{}).theme
	small := displayLines(ansi.WcWidth, header+row(2), false, theme, "")
	if fitted := fitTable(ansi.WcWidth, small, 0, 60, maxBodyRows, theme); fitted == nil {
		t.Fatal("a small skewed table within its budget must wrap in place")
	} else if fitTable(ansi.WcWidth, small, 0, 60, 3, theme) != nil {
		t.Fatal("a table needing more rows than are left must wrap as prose")
	}
	large := displayLines(ansi.WcWidth, header+row(40), false, theme, "")
	if large[0].Table == nil {
		t.Fatal("fixture must be within the aligned table bounds")
	}
	if fitTable(ansi.WcWidth, large, 0, 60, maxBodyRows, theme) != nil {
		t.Fatal("a wrapped layout over its byte budget must wrap as prose")
	}
	plain := stripped(large)
	if got, want := bodyRows(ansi.WcWidth, large, 60, theme), bodyRows(ansi.WcWidth, plain, 60, theme); !reflect.DeepEqual(got, want) {
		t.Fatal("the refused table must render exactly as prose")
	}
	cells := 0
	for _, dl := range large {
		cells += len(dl.Cells)
	}
	refused := testing.AllocsPerRun(5, func() { bodyRows(ansi.WcWidth, large, 60, theme) })
	prose := testing.AllocsPerRun(5, func() { bodyRows(ansi.WcWidth, plain, 60, theme) })
	if refused > prose+float64(8*cells+64) {
		t.Fatalf("refusing the layout cost %.0f allocations, prose %.0f", refused, prose)
	}
}

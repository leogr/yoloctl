package chatui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// spacer reports whether a body row is a table spacer: an empty decor row.
func spacer(r bodyRow) bool { return r.Decor && len(r.Runs) == 0 }

// shown renders text at width as one string per body row, a spacer as "~".
func shown(text string, source bool, width int) []string {
	theme := New(Options{}).theme
	var out []string
	for _, r := range bodyRows(ansi.WcWidth, displayLines(ansi.WcWidth, text, source, theme, ""), width, theme) {
		if spacer(r) {
			out = append(out, "~")
			continue
		}
		var b strings.Builder
		for _, run := range r.Runs {
			b.WriteString(run.Text)
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

// unspaced is lines with every table's spacing turned off: the rows bodyRows
// produced before tables were spaced.
func unspaced(lines []displayLine) []displayLine {
	out := make([]displayLine, len(lines))
	copied := map[*tableInfo]*tableInfo{}
	for i, dl := range lines {
		if dl.Table != nil {
			if copied[dl.Table] == nil {
				c := *dl.Table
				c.SpaceBefore, c.SpaceAfter = false, false
				copied[dl.Table] = &c
			}
			dl.Table = copied[dl.Table]
		}
		out[i] = dl
	}
	return out
}

const spacedTable = "| a | b |\n|---|---|\n| 1 | 2 |"

// A table gets one empty row on each side that borders a non-blank source
// line, and none at the message's edges or next to a blank (empty or
// whitespace-only) line, which stays exactly as written. Rejected tables,
// fenced examples and source mode are untouched.
func TestTablesAreSpacedFromNonBlankNeighbours(t *testing.T) {
	table := []string{"a │ b", "──┼──", "1 │ 2"}
	rows := func(parts ...any) []string {
		var out []string
		for _, p := range parts {
			switch v := p.(type) {
			case string:
				out = append(out, v)
			case []string:
				out = append(out, v...)
			}
		}
		return out
	}
	rows120 := strings.Repeat("| x | y |\n", maxTableRows+1)
	for _, tc := range []struct {
		name, text string
		source     bool
		want       []string
	}{
		{"leogr's case", "intro\n" + spacedTable + "\noutro", false, rows("intro", "~", table, "~", "outro")},
		{"table only", spacedTable, false, rows(table)},
		{"first", spacedTable + "\noutro", false, rows(table, "~", "outro")},
		{"last", "intro\n" + spacedTable, false, rows("intro", "~", table)},
		{"trailing newline", "intro\n" + spacedTable + "\n", false, rows("intro", "~", table, "")},
		{"header only", "intro\n| a | b |\n|---|---|\noutro", false, rows("intro", "~", "a │ b", "──┼──", "~", "outro")},
		{"one blank each side", "intro\n\n" + spacedTable + "\n\noutro", false, rows("intro", "", table, "", "outro")},
		{"two blanks", "intro\n\n\n" + spacedTable + "\n\n\noutro", false, rows("intro", "", "", table, "", "", "outro")},
		{"whitespace-only neighbours", "intro\n  \n" + spacedTable + "\n\t\noutro", false, rows("intro", "", table, "", "outro")},
		{"empty heading is not blank", "# \n" + spacedTable, false, rows("", "~", table)},
		{"fence closer is not blank", "```\nx\n```\n" + spacedTable, false, nil},
		{"two tables, one real blank", spacedTable + "\n\n" + spacedTable, false, rows(table, "", table)},
		{"two tables around prose", spacedTable + "\nmid\n" + spacedTable, false, rows(table, "~", "mid", "~", table)},
		{"invalid table", "intro\n| a | b |\noutro", false, rows("intro", "| a | b |", "outro")},
		{"oversized table", "intro\n| a | b |\n|---|---|\n" + rows120 + "outro", false, nil},
		{"fenced example", "```\nintro\n" + spacedTable + "\noutro\n```", false, nil},
		{"source mode", "intro\n" + spacedTable + "\noutro", true, rows("intro", "| a | b |", "|---|---|", "| 1 | 2 |", "outro")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := shown(tc.text, tc.source, 60)
			switch tc.name {
			case "fence closer is not blank":
				if got[3] != "~" || got[4] != "a │ b" {
					t.Fatalf("rows %q: a fence closer is not a blank line", got)
				}
				return
			case "oversized table", "fenced example":
				if strings.Contains(strings.Join(got, "\n"), "~") {
					t.Fatalf("rows %q: a literal table gained spacing", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows %q\nwant %q", got, tc.want)
			}
		})
	}
}

// At every width, natural, wrapped in its columns or fallen back to prose,
// the table's rows are exactly its rows without spacing, with one spacer
// before and one after and none inside it.
func TestTableSpacingKeepsEveryLayout(t *testing.T) {
	theme := New(Options{}).theme
	text := "intro\n| Name | Notes |\n|---|:-:|\n| alpha beta gamma | 一二三四五六七八九十 wraps |\noutro"
	lines := displayLines(ansi.WcWidth, text, false, theme, "")
	for _, width := range []int{200, 24, 6} {
		got, want := bodyRows(ansi.WcWidth, lines, width, theme), bodyRows(ansi.WcWidth, unspaced(lines), width, theme)
		var kept []bodyRow
		var at []int
		for i, r := range got {
			if spacer(r) {
				at = append(at, i)
				continue
			}
			kept = append(kept, r)
		}
		if !reflect.DeepEqual(kept, want) {
			t.Fatalf("width %d: spacing changed the table's rows", width)
		}
		last := len(got) - 1 - len(bodyRows(ansi.WcWidth, lines[4:], width, theme))
		firstLine := 1
		if len(at) > 0 && got[at[0]+1].Decor {
			firstLine = 3
		} // labelled notice anchors to the first visible value
		if len(at) != 2 || got[at[0]-1].Line != 0 || got[at[0]+1].Line != firstLine || at[1] != last || got[at[1]].Line != 4 {
			t.Fatalf("width %d: spacers at %v (outro starts after row %d): %+v", width, at, last, got)
		}
	}
}

// Spacers copy nothing: a drag across intro, table and outro copies exactly
// the text of its logical endpoints, and a press on a spacer starts at the
// line it borders on the far side, so a drag ending on the spacer after the
// table keeps the table's last row. Quote takes no blank row from a spacer:
// the table quotes as its cell text, and a drag to a spacer quotes the table
// as far as the line that spacer borders. The source maps are unchanged.
func TestTableSpacersCopyNothing(t *testing.T) {
	text := "intro\n" + spacedTable + "\noutro"
	m, copied := copyingModel(60, 30)
	m.Update(IncomingMsg{Message: Message{ID: "t", Nick: "x", Text: text}})
	m.View()
	key := "server:t"
	logical := func(a, b selPoint) string {
		t.Helper()
		got, _ := m.selectedText(selection{anchor: a, focus: b})
		return got
	}
	start, end := selPoint{key: key}, selPoint{key: key, line: 4, lo: len("outro"), hi: len("outro")}
	x0, y0 := cellOf(t, m, key, 0, 0)
	x1, y1 := cellOf(t, m, key, 4, len("outro"))
	drag(m, x0, y0, x1, y1)
	if got, want := (*copied)[len(*copied)-1], logical(start, end); got != want || want != "intro\n"+tableCopy(t, m, key)+"\noutro" {
		t.Fatalf("drag copied %q, logical %q", got, want)
	}
	var spacers []int
	for i, row := range m.lastTranscript {
		if row.key == key && row.kind == rowDecor && row.line >= 0 && strings.TrimSpace(ansiText(row.runs)) == "" {
			spacers = append(spacers, i)
		}
	}
	if len(spacers) != 2 {
		t.Fatalf("spacer rows %v", spacers)
	}
	for k, line := range []int{1, 4} {
		p, ok := m.pointAt(5, spacers[k]-m.scroll+m.layout.bodyTop)
		if !ok || p.line != line || p.lo != 0 || p.hi != 0 {
			t.Fatalf("a press on spacer %d gave %+v, want the start of line %d", k, p, line)
		}
	}
	drag(m, x0, y0, 5, spacers[1]-m.scroll+m.layout.bodyTop)
	if got, want := (*copied)[len(*copied)-1], logical(start, selPoint{key: key, line: 4}); got != want || !strings.Contains(got, "1 │ 2") {
		t.Fatalf("a drag to the spacer after the table copied %q, want %q", got, want)
	}
	m.quoteSelection()
	if m.Draft() != "> intro\n> a\tb\n> 1\t2\n\n" {
		t.Fatalf("a drag to the spacer after the table quotes %q (%q)", m.Draft(), m.notice)
	}
	_ = m.SetDraft("")
	sx, sy := 5, spacers[0]-m.scroll+m.layout.bodyTop
	ex, ey := cellOf(t, m, key, 3, len("1"))
	drag(m, sx, sy, ex, ey)
	m.quoteSelection()
	if m.Draft() != "> a\tb\n> 1\n\n" {
		t.Fatalf("a drag from the spacer before the table quotes %q (%q)", m.Draft(), m.notice)
	}

	q := quoteModel(t, text)
	lines := q.body(&q.messages[q.indexOf("server:q")], 80).lines
	highlight(q, "q", 0, 0, 4, len(lines[4].Text))
	q.quoteSelection()
	if q.Draft() != "> intro\n> a\tb\n> 1\t2\n> outro\n\n" {
		t.Fatalf("whole message: %q (%q)", q.Draft(), q.notice)
	}
	_ = q.SetDraft("")
	highlight(q, "q", 1, 0, 3, 1)
	q.quoteSelection()
	if q.Draft() != "> a\tb\n> 1\n\n" {
		t.Fatalf("part of a table: %q %q", q.Draft(), q.notice)
	}
	theme := New(Options{}).theme
	var srcs []lineSrc
	if mapped := displayLinesMap(ansi.WcWidth, text, false, theme, "", &srcs); !reflect.DeepEqual(mapped, displayLines(ansi.WcWidth, text, false, theme, "")) || len(srcs) != len(mapped) {
		t.Fatalf("the mapped display differs or misses lines: %d sources for %d lines", len(srcs), len(mapped))
	}
}

// A reading position in the table, in the outro or on a spacer survives
// resizes, the panel and an arrival; one on a spacer settles on the text of
// the line it borders, never on another message.
func TestTableSpacersKeepTheReadingPosition(t *testing.T) {
	text := "intro\n" + spacedTable + "\noutro"
	for _, start := range []string{"cell", "outro", "spacer before", "spacer after"} {
		t.Run(start, func(t *testing.T) {
			m := tableModel(60, text)
			key := "server:t"
			top, wantLine := -1, 0
			for i, row := range m.lastTranscript {
				if row.key != key {
					continue
				}
				empty := row.kind == rowDecor && row.line >= 1 && row.line <= 4 && len(strings.TrimSpace(ansiText(row.runs))) == 0
				switch {
				case start == "cell" && row.kind == rowBody && row.line == 3:
					top, wantLine = i, 3
				case start == "outro" && row.kind == rowBody && row.line == 4:
					top, wantLine = i, 4
				case start == "spacer before" && empty && row.line == 1:
					top, wantLine = i, 1
				case start == "spacer after" && empty && row.line == 4:
					top, wantLine = i, 4
				}
				if top >= 0 {
					break
				}
			}
			if top < 0 {
				t.Fatalf("fixture has no %s row", start)
			}
			m.follow, m.anchor, m.scroll = false, nil, top
			check := func(step string) {
				t.Helper()
				m.View()
				row := m.lastTranscript[m.scroll]
				if row.key != key || row.line != wantLine || (start != "spacer before" && start != "spacer after" && row.kind != rowBody) {
					t.Fatalf("%s: top row %+v, want line %d of %s", step, row, wantLine, key)
				}
			}
			check("first frame")
			for _, w := range []int{20, 100, 60} {
				m.Update(tea.WindowSizeMsg{Width: w, Height: 16})
				check(fmt.Sprint("width ", w))
			}
			press(m, 'p', tea.ModCtrl)
			check("panel")
			press(m, 'p', tea.ModCtrl)
			m.Update(IncomingMsg{Message: Message{ID: "late", Nick: "x", Text: "late"}})
			check("arrival")
		})
	}
}

// ansiText joins the text of runs.
func ansiText(runs []run) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// Spacers count toward the row limit on every path: at most maxBodyRows rows
// and the notice, including when a spacer or a fence frame is the row that
// reaches it.
func TestTableSpacingStaysWithinTheRowLimit(t *testing.T) {
	theme := New(Options{}).theme
	notice := func(r bodyRow) bool {
		return r.Decor && len(r.Runs) == 1 && strings.HasPrefix(r.Runs[0].Text, "[display row limit")
	}
	prose := func(k int) string { return strings.Repeat("p\n", k) }
	for _, tc := range []struct {
		name, text string
		last       func(bodyRow) bool
	}{
		// k prose rows, the spacer before, three table rows: the spacer
		// after is the row at the limit.
		{"spacer after at the limit", prose(maxBodyRows-5) + spacedTable + "\noutro", spacer},
		{"spacer before at the limit", prose(maxBodyRows-1) + spacedTable + "\noutro", spacer},
		{"table near the limit", prose(maxBodyRows-3) + spacedTable + "\noutro", nil},
		{"fence frame at the limit", prose(maxBodyRows-1) + "```\ncode\n```", func(r bodyRow) bool { return r.Decor }},
		{"repeated tables", strings.Repeat("p\n"+spacedTable+"\n", maxBodyRows/4), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := bodyRows(ansi.WcWidth, displayLines(ansi.WcWidth, tc.text, false, theme, ""), 60, theme)
			if len(rows) != maxBodyRows+1 || !notice(rows[maxBodyRows]) {
				t.Fatalf("%d rows, last %+v: want %d rows and the notice", len(rows), rows[len(rows)-1], maxBodyRows)
			}
			if tc.last != nil && !tc.last(rows[maxBodyRows-1]) {
				t.Fatalf("the row at the limit is %+v", rows[maxBodyRows-1])
			}
		})
	}
}

// tableCopy is what selecting the whole of the fixture's table copies: its
// rows as shown, the frame rule excluded.
func tableCopy(t *testing.T, m *Model, key string) string {
	t.Helper()
	lines := m.body(&m.messages[m.indexOf(key)], m.lastTextWidth).lines
	return lines[1].Text + "\n" + lines[3].Text
}

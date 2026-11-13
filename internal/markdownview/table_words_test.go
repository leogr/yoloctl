package markdownview

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTableBreaksStayOutOfProse(t *testing.T) {
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for _, tc := range []struct {
			text  string
			width int
			first string
			floor int
		}{
			{"abc-def", 5, "abc-", 4}, {"abc/def", 5, "abc/", 4}, {"abc_def", 5, "abc_", 4},
			{"中文描述", 6, "中文描", 2}, {"one two", 4, "one ", 3}, {"éclair words", 6, "éclair ", 6},
		} {
			rs := tableWrapRanges(wm, tc.text, tc.width)
			if got := tc.text[rs[0][0]:rs[0][1]]; got != tc.first {
				t.Fatalf("%q: first %q", tc.text, got)
			}
			if got := tableRunWidth(wm, tc.text); got != tc.floor {
				t.Fatalf("%q: floor%d want%d", tc.text, got, tc.floor)
			}
		}
		text := "abc-def"
		r := WrapRanges(wm, text, 5, true)
		if text[r[0][0]:r[0][1]] != "abc-d" {
			t.Fatal("prose changed")
		}
	}
}

func TestReplacementRefreshesTableWordFloor(t *testing.T) {
	p := documentOptions()
	doc := Parse("|key|value|\n|---|---|\n|a|xxxxxxx|\n", p)
	r := doc.Lines[2].Cells[1]
	doc.Lines[2].Text = doc.Lines[2].Text[:r[0]] + "abc/def" + doc.Lines[2].Text[r[1]:]
	doc.Lines[2].Subs = []TextSub{{Start: r[0], End: r[1], Base: r[0], Copy: "xxxxxxx"}}
	lines := tableLayoutLines(p.Method, doc.Lines)
	if lines[0].Table.WordFloor[1] != 5 || doc.Lines[0].Table.WordFloor[1] != 7 {
		t.Fatal("literal replacement floor/cache ownership", lines[0].Table.WordFloor)
	}
}

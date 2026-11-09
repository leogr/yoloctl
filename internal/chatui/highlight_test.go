package chatui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHighlightedBlockRendersOnTheCard(t *testing.T) {
	m, copied := copyingModel(80, 24)
	text := "```go\nfunc main() { \"x\" }\n```"
	m.Update(IncomingMsg{Message: Message{ID: "c", Nick: "x", Text: text}})
	view(m)
	body := m.body(&m.messages[0], m.lastTextWidth)
	bg := codeCard(m.theme)
	var keyword, str bool
	for _, r := range body.rows {
		for _, run := range r.Runs {
			if run.Style.BG != bg && (body.lines[r.Line].Code || body.lines[r.Line].Fence != fenceNone) {
				t.Fatalf("untinted run %q", run.Text)
			}
			if run.Copy && run.Text == "func" && run.Style.FG == m.theme.Account {
				keyword = true
			}
			if run.Copy && run.Text == `"x"` && run.Style.FG == m.theme.OK {
				str = true
			}
		}
	}
	if !keyword || !str {
		t.Fatal("go code is not highlighted in the view")
	}
	x0, y0 := cellOf(t, m, "server:c", 1, 0)
	x1, y1 := cellOf(t, m, "server:c", 1, len(`func main() { "x" }`))
	drag(m, x0, y0, x1, y1)
	if len(*copied) != 1 || (*copied)[0] != `func main() { "x" }` {
		t.Fatalf("drag copied %q", *copied)
	}
	*copied = nil
	cell := copyButtons(m, "server:c")[0]
	click(m, cell[0], cell[1])
	if len(*copied) != 1 || (*copied)[0] != "func main() { \"x\" }\n" {
		t.Fatalf("button copied %q", *copied)
	}
	if w := ansi.StringWidthWc(view(m)); w == 0 {
		t.Fatal("empty view")
	}
}

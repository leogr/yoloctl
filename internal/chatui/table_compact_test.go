package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/markdownview"
)

// Wrapped-layout tests explicitly open their fixture through the same toggle
// path; compact-default behavior is exercised separately below.
func expandChatTableRows(m *Model, key string) {
	index := m.indexOf(key)
	if index < 0 {
		return
	}
	body := m.body(&m.messages[index], max(m.lastTextWidth, 1))
	for _, record := range markdownview.TableRecords(markdownview.Document{Lines: body.lines}) {
		if !m.tableRows[key][record.ID] {
			m.toggleTableRow(action{Kind: markdownview.IntentToggleTableRow, Value: record.ID.String(), Extra: key})
		}
	}
	m.View()
}
func compactChatFixture(t *testing.T) (*Model, *[]string) {
	t.Helper()
	m, copied := copyingModel(90, 60)
	text := "|Name|Detail|\n|---|---|\n|alpha|" + (strings.Repeat("first detail ", 25) + strings.Repeat("x", 200)) + "|\n|beta|" + (strings.Repeat("second detail ", 25) + strings.Repeat("x", 200)) + "|"
	m.Update(IncomingMsg{Message: Message{ID: "q", Nick: "x", Text: text}})
	m.View()
	return m, copied
}
func compactChatClick(t *testing.T, m *Model, id string) {
	t.Helper()
	cells := targetCells(m, markdownview.IntentToggleTableRow, id)
	if len(cells) == 0 {
		t.Fatal("missing table toggle", id, m.lastTextWidth)
	}
	clickCell(m, cells[0])
	m.View()
}
func TestChatCompactRowsDefaultToggleAndSelectionClear(t *testing.T) {
	m, copied := compactChatFixture(t)
	body := m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth)
	if len(body.closed) != 2 {
		t.Fatal("not closed by default", body.closed)
	}
	highlight(m, "q", 2, 0, 2, 3)
	compactChatClick(t, m, "0:0")
	body = m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth)
	if body.closed[2] || !body.closed[3] || m.sel != nil || len(*copied) != 0 {
		t.Fatal("toggle copied/kept selection or opened wrong row")
	}
	compactChatClick(t, m, "0:0")
	if !m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth).closed[2] {
		t.Fatal("did not close row")
	}
}
func TestChatCompactCopyAndQuoteHiddenText(t *testing.T) {
	m, copied := compactChatFixture(t)
	dl := m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth).lines[2]
	a, b := dl.Cells[0], dl.Cells[1]
	highlight(m, "q", 2, b[0]+1, 2, b[0]+4)
	if got, _ := m.selectedText(*m.sel); got != dl.CopyText(b[0]+1, b[0]+4) {
		t.Fatal("partial visible cell widened", got)
	}
	highlight(m, "q", 2, a[0]+1, 2, b[0]+3)
	if got, _ := m.selectedText(*m.sel); got != dl.CopyText(a[0], b[1]) {
		t.Fatal("crossed cells lost hidden bytes", got)
	}
	if got := quoted(m); got != "> "+dl.Text[a[0]:a[1]]+"\t"+dl.Text[b[0]:b[1]]+"\n\n" {
		t.Fatal("Quote lost hidden bytes/added controls", got)
	}
	// A real drag ending on the ellipsis includes the hidden tail, and the
	// marker/ellipsis themselves never reach the host copy callback.
	var endX, endY int
	for i, row := range m.lastTranscript {
		if row.key != "server:q" || row.line != 2 {
			continue
		}
		x := 0
		for _, run := range row.runs {
			if run.Tail > 0 {
				endX, endY = x, i-m.scroll+m.layout.bodyTop
			}
			x += m.widthMethod.StringWidth(run.Text)
		}
	}
	startX, startY := cellOf(t, m, "server:q", 2, a[0]+1)
	m.Update(tea.MouseClickMsg(tea.Mouse{X: startX, Y: startY, Button: tea.MouseLeft}))
	m.Update(tea.MouseMotionMsg(tea.Mouse{X: endX, Y: endY, Button: tea.MouseLeft}))
	m.Update(tea.MouseReleaseMsg(tea.Mouse{X: endX, Y: endY, Button: tea.MouseLeft}))
	if len(*copied) != 1 || (*copied)[0] != dl.CopyText(a[0], b[1]) {
		t.Fatal("ellipsis copy mismatch", *copied)
	}
}
func TestChatCompactStateSurvivesModeAndIsEvictedWithMessage(t *testing.T) {
	m, _ := compactChatFixture(t)
	compactChatClick(t, m, "0:1")
	reportWidth(m, ansi.ModeSet) // ModeSet: renderer and model change together.
	m.View()
	if m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth).closed[3] {
		t.Fatal("width-mode cache reset lost expansion")
	}
	m.evict(m.indexOf("server:q"))
	if len(m.tableRows) != 0 {
		t.Fatal("evicted message retained expansion state")
	}
}
func TestChatCompactExpansionRefusalIsVisible(t *testing.T) {
	m, _ := copyingModel(200, 60)
	source := "|heading|" + strings.Repeat("heading|", 31) + "\n|" + strings.Repeat("---|", 32) + "\n|" + (strings.Repeat("w", 200) + " " + strings.Repeat("word ", 8000)) + "|" + strings.Repeat("x|", 31)
	m.Update(IncomingMsg{Message: Message{ID: "q", Nick: "x", Text: source}})
	m.View()
	compactChatClick(t, m, "0:0")
	if !strings.Contains(m.notice, "kept closed") {
		t.Fatal("refused expansion had no notice", m.lastTextWidth, m.notice)
	}
	if !m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth).closed[2] {
		t.Fatal("refused row opened")
	}
}

func TestChatBChoicesSurviveWrappedAResize(t *testing.T) {
	m, _ := compactChatFixture(t)
	compactChatClick(t, m, "0:0")
	for _, open := range []bool{true, false} {
		if !open {
			compactChatClick(t, m, "0:0")
		}
		m.Update(tea.WindowSizeMsg{Width: 300, Height: 60})
		m.View()
		key := "server:q"
		body := m.body(&m.messages[m.indexOf(key)], m.lastTextWidth)
		for _, row := range body.rows {
			for _, run := range row.Runs {
				if run.Act.Kind == markdownview.IntentToggleTableRow {
					t.Fatal("A has table action")
				}
			}
		}
		m.toggleTableRow(action{Kind: markdownview.IntentToggleTableRow, Value: "0:0", Extra: key})
		if m.tableRows[key][markdownview.TableRowID{Table: 0, Row: 0}] != open {
			t.Fatal("A changed dormant choice")
		}
		m.Update(tea.WindowSizeMsg{Width: 4, Height: 60})
		m.View()
		m.Update(tea.WindowSizeMsg{Width: 90, Height: 60})
		m.View()
		body = m.body(&m.messages[m.indexOf(key)], m.lastTextWidth)
		if body.closed[2] == open {
			t.Fatal("B choice lost after A resize", open)
		}
	}
}

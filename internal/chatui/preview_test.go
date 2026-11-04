package chatui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// previewRoom holds a loaded target (refTarget, by qa) and a message t1
// that references it.
func previewRoom(t *testing.T, text string) *Model {
	t.Helper()
	m, _, _ := filterModel(t)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	arrive(m, refTarget, "mq", "qa", "plan ready: tests pass and the smoke is green")
	arrive(m, "t1", "mo", "opus", text)
	m.View()
	return m
}

func lineOf(m *Model, id string, n int) displayLine {
	return m.body(&m.messages[m.indexOf("server:"+id)], max(m.lastTextWidth, 1)).lines[n]
}

// A reference to a loaded message shows in its place as one dimmed span,
// ↗ author: snippet…, whose click shows that message; copy and Quote keep
// the reference.
func TestReferencePreviewReplacesTheToken(t *testing.T) {
	m := previewRoom(t, "see "+sharedTarget+" and msgid:"+refTarget+" now")
	dl := lineOf(m, "t1", 0)
	label := "↗ qa: plan ready: tests pass a…"
	if want := "see " + label + " and " + label + " now"; dl.text != want {
		t.Fatalf("line %q, want %q", dl.text, want)
	}
	if out := view(m); !strings.Contains(out, "see ↗ qa: plan ready") || strings.Contains(out, sharedTarget) {
		t.Fatalf("frame:\n%s", out)
	}
	if got := dl.copyText(0, len(dl.text)); got != "see "+sharedTarget+" and msgid:"+refTarget+" now" {
		t.Fatalf("copy keeps the tokens: %q", got)
	}
	cells := targetCells(m, actRef, refTarget)
	if len(cells) == 0 {
		t.Fatal("no span target")
	}
	clickCell(m, [2]int{cells[0][0] + 3, cells[0][1]})
	if msg := m.selectedMessage(); msg == nil || msg.ID != refTarget || !m.picked {
		t.Fatalf("the span shows its message: %+v", msg)
	}
	highlight(m, "t1", 0, 0, 0, len(dl.text))
	_ = m.SetDraft("")
	m.quoteSelection()
	if m.Draft() != "> see "+sharedTarget+" and msgid:"+refTarget+" now\n\n" {
		t.Fatalf("Quote keeps the tokens: %q (%q)", m.Draft(), m.notice)
	}
}

// Unloaded, unknown and self references, code, link targets, table cells
// and source view keep the token.
func TestReferencePreviewOnlyForLoadedTargets(t *testing.T) {
	self, _ := compactRef("0f8fad5b-d9cb-469f-a165-70867728950e")
	m := previewRoom(t, "x")
	arrive(m, "0f8fad5b-d9cb-469f-a165-70867728950e", "mo", "opus", "me "+self+" and "+sharedMissing+" and `"+sharedTarget+"` and [a]("+sharedTarget+")")
	arrive(m, "tb", "mo", "opus", "| ref |\n|---|\n| "+sharedTarget+" |")
	m.View()
	if dl := lineOf(m, "0f8fad5b-d9cb-469f-a165-70867728950e", 0); dl.subs != nil || !strings.Contains(dl.text, self) || !strings.Contains(dl.text, sharedMissing) {
		t.Fatalf("tokens stay: %q", dl.text)
	}
	if dl := lineOf(m, "tb", 2); dl.subs != nil || !strings.Contains(dl.text, sharedTarget) {
		t.Fatalf("a table cell keeps its token: %q", dl.text)
	}
	other := previewRoom(t, "see "+sharedTarget)
	other.source = true
	other.View()
	if dl := lineOf(other, "t1", 0); dl.subs != nil || dl.text != "see "+sharedTarget {
		t.Fatalf("source view keeps the token: %q", dl.text)
	}
}

// The budget cuts a long name and a long text; multibyte text is cut by
// rune.
func TestReferencePreviewBudget(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, refTarget, "mx", "a-rather-long-nickname-indeed", strings.Repeat("é", 30))
	arrive(m, "t1", "mo", "opus", sharedTarget)
	m.View()
	if got, want := lineOf(m, "t1", 0).text, "↗ a-rather-long-n…: "+strings.Repeat("é", previewSnippet)+"…"; got != want {
		t.Fatalf("label %q, want %q", got, want)
	}
}

// A target that arrives later, live or by Page Up, replaces the token on
// the next frame; a highlight in the referencing message then clears. A
// new author name rebuilds the label.
func TestReferencePreviewFollowsWhatIsLoaded(t *testing.T) {
	m, _, _ := filterModel(t)
	arrive(m, "t1", "mo", "opus", "see "+sharedTarget)
	m.View()
	highlight(m, "t1", 0, 0, 0, 3)
	if lineOf(m, "t1", 0).subs != nil {
		t.Fatal("no target yet")
	}
	m.Update(IncomingMsg{Message: Message{ID: refTarget, Account: "mq", Nick: "qa", Text: "older plan", Older: true, Time: time.Unix(900, 0)}})
	m.View()
	if dl := lineOf(m, "t1", 0); dl.text != "see ↗ qa: older plan" || m.sel != nil {
		t.Fatalf("an older target: %q, highlight %v", dl.text, m.sel)
	}
	ids := m.opts.Identities
	id := ids["mq"]
	id.Nick = "quality"
	ids["mq"] = id
	m.Update(SessionMsg{Nick: m.opts.Nick, Identities: ids})
	m.View()
	if dl := lineOf(m, "t1", 0); dl.text != "see ↗ quality: older plan" {
		t.Fatalf("a renamed author: %q", dl.text)
	}
}

// A span wrapped over two rows stays one target: a click on either part
// shows the message, a drag ending inside it covers it whole and copies the
// token.
func TestReferencePreviewWrapsAtomically(t *testing.T) {
	m := previewRoom(t, "a "+sharedTarget+" b")
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 40})
	m.View()
	cells := targetCells(m, actRef, refTarget)
	if len(cells) < 2 {
		t.Fatalf("the span should wrap at this width: %v", cells)
	}
	move(m, cells[1][0], cells[1][1])
	m.View()
	hovered := 0
	for _, row := range m.lastTranscript {
		for _, r := range m.hoverRow(row) {
			if r.act.kind == actRef && r.style.Underline {
				hovered++
			}
		}
	}
	if hovered < 2 {
		t.Fatalf("hovering one part marks both: %d", hovered)
	}
	clickCell(m, cells[1])
	if msg := m.selectedMessage(); msg == nil || msg.ID != refTarget {
		t.Fatalf("the second part shows the message: %+v", msg)
	}
	m.picked = false
	x0, y0 := cells[0][0]-2, cells[0][1]
	m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: cells[1][0] + 1, Y: cells[1][1], Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: cells[1][0] + 1, Y: cells[1][1], Button: tea.MouseLeft})
	if m.sel == nil {
		t.Fatal("no highlight")
	}
	if got, _ := m.selectedText(*m.sel); got != "a "+sharedTarget {
		t.Fatalf("a drag ending inside the span covers it whole: %q", got)
	}
}

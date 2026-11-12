package chatui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/mermaid"
)

// fakeDiagrams caches what it renders like the client; rows sets the
// drawing height and busy makes renders transient.
type fakeDiagrams struct {
	mu      sync.Mutex
	cache   map[diagramKey]mermaid.Result
	renders []diagramKey
	rows    int
	busy    bool
}

func (f *fakeDiagrams) Render(_ context.Context, source string, width int, _ ansi.Method) mermaid.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := diagramKey{source, width}
	f.renders = append(f.renders, k)
	if f.busy {
		return mermaid.Result{Status: mermaid.Busy}
	}
	lines := []string{"drawn " + strings.Fields(source)[2]}
	for range f.rows - 1 {
		lines = append(lines, "│")
	}
	r := mermaid.Result{Status: mermaid.OK, Lines: lines}
	if f.cache == nil {
		f.cache = map[diagramKey]mermaid.Result{}
	}
	f.cache[k] = r
	return r
}

func (f *fakeDiagrams) Lookup(source string, width int, _ ansi.Method) (mermaid.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.cache[diagramKey{source, width}]
	return r, ok
}

func (f *fakeDiagrams) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.renders) }

func chatFence(n int) string { return fmt.Sprintf("```mermaid\nflowchart LR\n n%d --> x\n```", n) }

func diagramChat(t *testing.T, f *fakeDiagrams, width, height int) *Model {
	t.Helper()
	m := New(Options{Space: "trial", Nick: "human", Mermaid: f, Send: func(Outgoing) tea.Cmd { return nil }})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// settle runs the diagram commands the way the program would, painting
// between updates, until nothing is left.
func settle(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	m.View()
	if cmd == nil {
		_, cmd = m.Update(diagramSettleMsg{})
	}
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 2000 {
			t.Fatal("diagram updates never settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case diagramResultMsg, diagramSettleMsg:
			_, next := m.Update(msg)
			m.View()
			queue = append(queue, next)
		}
	}
}

func arriveText(m *Model, id, text string) {
	m.Update(IncomingMsg{Message: Message{ID: id, Nick: "x", Text: text}})
}

// shownDiagrams counts the drawings and notes the visible rows show.
func shownDiagrams(m *Model) (drawings, notes []string) {
	rows := m.built.rows[min(m.scroll, len(m.built.rows)):min(m.scroll+m.lastBodyHeight, len(m.built.rows))]
	for _, row := range rows {
		body := m.bodyCache[row.key]
		if row.line < 0 || row.line >= len(body.lines) || row.offset != 0 {
			continue
		}
		dl := body.lines[row.line]
		switch {
		case dl.Diagram > 0 && strings.HasPrefix(dl.Text, "drawn"):
			drawings = append(drawings, dl.Text)
		case dl.Decor && strings.HasPrefix(dl.Text, "mermaid:"):
			notes = append(notes, dl.Text)
		}
	}
	return drawings, notes
}

func TestChatDrawsAVisibleFenceAndQuotesItsSource(t *testing.T) {
	f := &fakeDiagrams{}
	m := diagramChat(t, f, 80, 24)
	text := "look:\n" + chatFence(1) + "\nok"
	arriveText(m, "a", text)
	settle(t, m, nil)
	drawings, notes := shownDiagrams(m)
	if len(drawings) != 1 || len(notes) != 0 || f.count() != 1 {
		t.Fatalf("drawings %q notes %q renders %d", drawings, notes, f.count())
	}
	if f.renders[0].width != markdownview.DiagramWidth(m.lastTextWidth) {
		t.Fatalf("rendered at %d, the card's inner width is %d", f.renders[0].width, markdownview.DiagramWidth(m.lastTextWidth))
	}
	// The whole drawing quotes the exact fence source.
	i := m.indexOf("server:a")
	body := m.body(&m.messages[i], m.lastTextWidth)
	first, last := -1, -1
	for n, dl := range body.lines {
		if dl.Diagram > 0 {
			if first < 0 {
				first = n
			}
			last = n
		}
	}
	highlight(m, "a", first, 0, last, len(body.lines[last].Text))
	got, _, why := m.quoteSource(i)
	if why != "" || len(got) == 0 || joinQuote(got) != chatFence(1) {
		t.Fatalf("quote %q (%s)", joinQuote(got), why)
	}
}

// Twenty fence occurrences on a screen draw, repeated sources included and
// rendered once; the visible ones past them show source with a note.
func TestChatScreenDrawsTwentyOccurrences(t *testing.T) {
	f := &fakeDiagrams{}
	m := diagramChat(t, f, 100, 200)
	arriveText(m, "a", strings.Repeat(chatFence(1)+"\n", 22))
	settle(t, m, nil)
	drawings, notes := shownDiagrams(m)
	if len(drawings) != maxScreenDiagrams || len(notes) != 2 || notes[0] != diagramTooMany || f.count() != 1 {
		t.Fatalf("drawings %d notes %q renders %d", len(drawings), notes, f.count())
	}
}

// Scrolling is a new screen: its fences are counted again, so a fence past
// the twentieth draws once an earlier one leaves the screen.
func TestChatScrollingRecountsTheScreen(t *testing.T) {
	f := &fakeDiagrams{}
	m := diagramChat(t, f, 100, 160)
	for n := range 30 {
		arriveText(m, fmt.Sprintf("m%02d", n), chatFence(n))
	}
	key := func(k rune, mod tea.KeyMod) {
		_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: k, Mod: mod}))
		settle(t, m, cmd)
	}
	settle(t, m, nil)
	for m.scroll > 0 {
		key(tea.KeyPgUp, 0)
	}
	drawings, notes := shownDiagrams(m)
	if len(drawings) != maxScreenDiagrams || len(notes) == 0 || drawings[0] != "drawn n0" || drawings[19] != "drawn n19" {
		t.Fatalf("top screen: drawings %q notes %q", drawings, notes)
	}
	if _, ok := f.Lookup("flowchart LR\n n20 --> x", markdownview.DiagramWidth(m.lastTextWidth), m.widthMethod); ok {
		t.Fatal("the 21st fence on the screen was rendered")
	}
	// One message further down: n0 leaves, n20 is now within the twenty.
	top := m.built.rows[m.scroll].key
	for m.built.rows[m.scroll].key == top {
		key(tea.KeyDown, tea.ModCtrl)
	}
	drawings, _ = shownDiagrams(m)
	if len(drawings) > maxScreenDiagrams || drawings[0] != "drawn n1" || !slices.Contains(drawings, "drawn n20") {
		t.Fatalf("scrolled screen: drawings %q", drawings)
	}
	if len(m.diagrams.eligible) > maxScreenDiagrams {
		t.Fatalf("%d eligible", len(m.diagrams.eligible))
	}
}

// A fence cut by the bottom of the screen counts: its first rows are on it.
func TestChatPartlyVisibleFenceCounts(t *testing.T) {
	f := &fakeDiagrams{rows: 30}
	m := diagramChat(t, f, 100, 24)
	arriveText(m, "a", chatFence(1))
	settle(t, m, nil)
	// A tall drawing at the bottom: the view follows; the fence is drawn.
	drawings, _ := shownDiagrams(m)
	body := m.bodyCache["server:a"]
	if body.outcomes[0].Lines == nil || len(body.outcomes[0].Lines) != 30 {
		t.Fatalf("outcome %+v, drawings %q", body.outcomes[0], drawings)
	}
	// Scrolled to show only its first rows, it stays drawn.
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	settle(t, m, cmd)
	if body := m.bodyCache["server:a"]; body.outcomes[0].Lines == nil {
		t.Fatal("a partly visible fence fell back")
	}
}

// Drawings taller than the code they replace move rows; the counts settle
// instead of switching fences back and forth.
func TestChatTallDrawingsSettle(t *testing.T) {
	f := &fakeDiagrams{rows: 12}
	m := diagramChat(t, f, 100, 30)
	for n := range 8 {
		arriveText(m, fmt.Sprintf("m%d", n), chatFence(n))
	}
	settle(t, m, nil) // fails if it never settles
	drawings, notes := shownDiagrams(m)
	if len(drawings) == 0 || len(notes) != 0 {
		t.Fatalf("drawings %q notes %q", drawings, notes)
	}
	renders := f.count()
	settle(t, m, nil)
	if f.count() != renders {
		t.Fatal("a settled screen rendered again")
	}
}

// A result for a width the view no longer uses is not shown: the body
// asks again at its current width.
func TestChatResizeRequestsTheNewWidth(t *testing.T) {
	f := &fakeDiagrams{}
	m := diagramChat(t, f, 80, 24)
	arriveText(m, "a", chatFence(1))
	m.View()
	m.Update(diagramSettleMsg{}) // makes it eligible
	m.View()
	_, request := m.Update(diagramSettleMsg{})
	if request == nil || !m.diagrams.inflight {
		t.Fatal("no request")
	}
	_, resized := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24}) // while it renders
	settle(t, m, tea.Batch(request, resized))
	if f.count() != 2 || f.renders[1].width != markdownview.DiagramWidth(m.lastTextWidth) {
		t.Fatalf("renders %+v", f.renders)
	}
	if drawings, _ := shownDiagrams(m); len(drawings) != 1 {
		t.Fatalf("drawings %q", drawings)
	}
}

// A transient answer shows its reason and is not asked again on the same
// screen; a new screen asks again.
func TestChatTransientAnswerWaitsForANewScreen(t *testing.T) {
	f := &fakeDiagrams{busy: true}
	m := diagramChat(t, f, 80, 24)
	arriveText(m, "a", chatFence(1))
	settle(t, m, nil)
	if _, notes := shownDiagrams(m); len(notes) != 1 || notes[0] != "mermaid: not drawn (renderer busy)" || f.count() != 1 {
		t.Fatalf("notes %q renders %d", notes, f.count())
	}
	settle(t, m, nil)
	if f.count() != 1 {
		t.Fatal("asked again on the same screen")
	}
	f.busy = false
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	settle(t, m, cmd)
	if drawings, _ := shownDiagrams(m); len(drawings) != 1 {
		t.Fatalf("drawings %q after a new screen", drawings)
	}
}

func TestChatWithoutRendererKeepsCodeCards(t *testing.T) {
	m, _ := readyModel()
	arriveText(m, "a", chatFence(1))
	m.View()
	if drawings, notes := shownDiagrams(m); len(drawings) != 0 || len(notes) != 0 {
		t.Fatalf("drawings %q notes %q", drawings, notes)
	}
}

// contentVisible are the fences whose code or drawing rows are on screen.
func contentVisible(m *Model) map[fenceRef]bool {
	out := map[fenceRef]bool{}
	for _, r := range m.built.rows[min(m.scroll, len(m.built.rows)):min(m.scroll+m.lastBodyHeight, len(m.built.rows))] {
		if ref, ok := m.fenceOf(r); ok && r.kind == rowBody {
			out[ref] = true
		}
	}
	return out
}

// Scrolling inside one long message is a new screen too: a fence that left
// it gives its place to the next, in both directions.
func TestChatScrollingInsideAMessageRecounts(t *testing.T) {
	f := &fakeDiagrams{}
	m := diagramChat(t, f, 100, 160)
	var text strings.Builder
	for n := range 40 {
		fmt.Fprintln(&text, chatFence(n))
	}
	arriveText(m, "a", text.String())
	key := func(k rune, mod tea.KeyMod) {
		_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: k, Mod: mod}))
		settle(t, m, cmd)
	}
	settle(t, m, nil)
	for m.scroll > 0 {
		key(tea.KeyPgUp, 0)
	}
	check := func(step string) {
		t.Helper()
		visible := contentVisible(m)
		for ref := range m.diagrams.eligible {
			if !visible[ref] {
				t.Fatalf("%s: fence %d holds a place off the screen", step, ref.occ)
			}
		}
		if drawings, notes := shownDiagrams(m); len(notes) > 0 && len(m.diagrams.eligible) != maxScreenDiagrams {
			t.Fatalf("%s: %d drawings with too-many notes", step, len(drawings))
		}
	}
	for n := range 30 {
		key(tea.KeyDown, tea.ModCtrl)
		check(fmt.Sprintf("down %d", n))
	}
	for n := range 30 {
		key(tea.KeyUp, tea.ModCtrl)
		check(fmt.Sprintf("up %d", n))
	}
}

// Only the height changes: still a new screen, so a busy answer is asked
// again; on the same screen it is not.
func TestChatNewHeightRetriesABusyAnswer(t *testing.T) {
	f := &fakeDiagrams{busy: true}
	m := diagramChat(t, f, 80, 24)
	arriveText(m, "a", chatFence(1))
	settle(t, m, nil)
	f.busy = false
	settle(t, m, nil)
	if f.count() != 1 {
		t.Fatal("asked again on the same screen")
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 25})
	settle(t, m, cmd)
	if drawings, notes := shownDiagrams(m); len(drawings) != 1 || len(notes) != 0 || f.count() != 2 {
		t.Fatalf("drawings %q notes %q renders %d", drawings, notes, f.count())
	}
}

// A paused reading position stays on its line when a drawing arrives above
// it, taller or shorter than the code, and inside the fence on its row.
func TestChatReadingPositionSurvivesDrawings(t *testing.T) {
	for _, c := range []struct {
		rows   int
		target string
	}{{12, "TARGET"}, {1, "TARGET"}, {12, "intro"}, {3, "flowchart LR"}} {
		f := &fakeDiagrams{rows: c.rows}
		m := diagramChat(t, f, 80, 24)
		arriveText(m, "a", "intro\n"+chatFence(1)+"\nTARGET\n"+strings.Repeat("tail\n", 60))
		m.follow, m.scroll, m.anchor = false, 0, nil
		m.View()
		m.Update(diagramSettleMsg{})
		m.View()
		_, request := m.Update(diagramSettleMsg{})
		if request == nil {
			t.Fatal("no request")
		}
		body := m.bodyCache["server:a"]
		at := -1
		for i, l := range body.lines {
			if l.Text == c.target {
				at = i
			}
		}
		m.anchor = &viewAnchor{key: "server:a", line: at}
		m.View()
		settle(t, m, request)
		row := m.built.rows[m.scroll]
		got := m.bodyCache[row.key].lines[row.line].Text
		want := c.target
		if c.target == "flowchart LR" {
			want = "drawn n1" // the fence's first content row
		}
		if got != want {
			t.Fatalf("%d-row drawing: position on %q moved to %q", c.rows, c.target, got)
		}
	}
}

// A fence's note row belongs to it: a busy note that goes away on a new
// screen (replaced by the drawing) keeps a paused position after the fence.
func TestChatReadingPositionSurvivesANoteGoingAway(t *testing.T) {
	f := &fakeDiagrams{busy: true}
	m := diagramChat(t, f, 80, 24)
	arriveText(m, "a", "intro\n"+chatFence(1)+"\nTARGET\n"+strings.Repeat("tail\n", 60))
	m.follow, m.scroll, m.anchor = false, 0, nil
	settle(t, m, nil)
	body := m.bodyCache["server:a"]
	at := -1
	for i, l := range body.lines {
		if l.Text == "TARGET" {
			at = i
		}
	}
	if note := body.lines[at-1]; !strings.HasPrefix(note.Text, "mermaid: not drawn") || body.fenceAt[at-1] != 0 {
		t.Fatalf("line before TARGET %q, fence %d", note.Text, body.fenceAt[at-1])
	}
	m.anchor = &viewAnchor{key: "server:a", line: at}
	f.busy = false
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 25})
	settle(t, m, cmd)
	row := m.built.rows[m.scroll]
	if got := m.bodyCache[row.key].lines[row.line].Text; got != "TARGET" {
		t.Fatalf("position moved to %q", got)
	}
	if drawings, notes := shownDiagrams(m); len(drawings) != 0 && len(notes) != 0 {
		t.Fatalf("drawings %q notes %q", drawings, notes)
	}
}

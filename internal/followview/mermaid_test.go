package followview

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/mermaid"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// fakeRenderer draws "drawn N" rows and, like the client, answers a source
// it drew from its cache at once. Tests with a render cost use synctest so
// rendering and the refresh deadline share one clock.
type fakeRenderer struct {
	mu     sync.Mutex
	cost   time.Duration
	busy   bool
	cached map[string]bool
	rows   int // drawing rows; 0 means one
	calls  []string
	widths []int
	fresh  int // renders that were not answered from the cache
}

func (f *fakeRenderer) Render(ctx context.Context, source string, width int, _ ansi.Method) mermaid.Result {
	f.mu.Lock()
	f.calls, f.widths = append(f.calls, source), append(f.widths, width)
	if f.cached[source] {
		f.mu.Unlock()
		return mermaid.Result{Status: mermaid.OK, Lines: f.drawing(source)}
	}
	f.fresh++
	cost := f.cost
	f.mu.Unlock()
	if cost > 0 {
		select {
		case <-ctx.Done():
			return mermaid.Result{Status: mermaid.Busy}
		case <-time.After(cost):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return mermaid.Result{Status: mermaid.Busy}
	}
	if f.cached == nil {
		f.cached = map[string]bool{}
	}
	f.cached[source] = true
	return mermaid.Result{Status: mermaid.OK, Lines: f.drawing(source)}
}

func (f *fakeRenderer) freshRenders() int { f.mu.Lock(); defer f.mu.Unlock(); return f.fresh }

func (f *fakeRenderer) drawing(source string) []string {
	lines := []string{"drawn " + strings.Fields(source)[2]}
	for range f.rows - 1 {
		lines = append(lines, "│")
	}
	return lines
}

func fence(n int) string { return fmt.Sprintf("```mermaid\nflowchart LR\n n%d --> x\n```\n", n) }

func drawn(m *Model) (rows, notes []string) {
	for _, l := range m.view.doc.Lines {
		switch {
		case l.Diagram > 0:
			rows = append(rows, l.Text)
		case l.Decor && strings.HasPrefix(l.Text, "mermaid:"):
			notes = append(notes, l.Text)
		}
	}
	return rows, notes
}

func mermaidModel(t *testing.T, text string, r *fakeRenderer) *Model {
	t.Helper()
	path := fixtureDir(t) + "/doc.md"
	writeFixture(t, path, []byte(text))
	m := New(context.Background(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown, Mermaid: r})
	t.Cleanup(m.Close)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	applyWork(t, m, cmd)
	return m
}

func TestDrawAdmitsCountsAndDeduplicatesFences(t *testing.T) {
	r := &fakeRenderer{}
	var sources []string
	for n := range 22 {
		sources = append(sources, fmt.Sprintf("flowchart LR\n n%d --> x\n", n))
	}
	// Fence 0 again (the 23rd fence) is past the limit too, though its
	// drawing exists: the limit counts fences, the work distinct sources.
	sources = append([]string{sources[0]}, sources...)
	sources = append(sources, sources[0], "classDiagram\n A <|-- B\n")
	d := drawing{renderer: r, now: time.Now, budget: drawBudget}
	got := d.draw(t.Context(), sources, 80, ansi.GraphemeWidth)
	out := got.diagrams
	if got.undrawn != 0 || got.retry != 0 || len(r.calls) != maxDiagrams-1 {
		t.Fatalf("undrawn %d retry %d, %d renders", got.undrawn, got.retry, len(r.calls))
	}
	for _, w := range r.widths {
		if w != markdownview.DiagramWidth(80) {
			t.Fatalf("requested width %d, want the card's inner width", w)
		}
	}
	for _, n := range []int{0, 1, 19} {
		if got := out[n]; len(got.Lines) != 1 || got.Note != "" {
			t.Fatalf("fence %d: %+v", n, got)
		}
	}
	for _, n := range []int{20, 21, 22, 23} {
		if got := out[n]; got.Note != noteTooMany || got.Lines != nil {
			t.Fatalf("fence %d: %+v", n, got)
		}
	}
	if got := out[24].Note; got != "mermaid: not drawn (only flowchart and sequence diagrams are drawn)" {
		t.Fatalf("not admitted note %q", got)
	}
}

func TestFollowContinuesUndrawnFencesWhileItProgresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 3 * time.Second}
		var text strings.Builder
		for n := range 5 {
			text.WriteString(fence(n))
		}
		// Each refresh shows the document after 5 s: one 3 s render, and the
		// next still drawing; continuations wait for it and draw the rest
		// without any read, each source rendered once.
		m := mermaidModel(t, text.String(), r)
		rows, notes := drawn(m)
		if len(rows) != 5 || len(notes) != 0 || m.view.undrawn != 0 {
			t.Fatalf("rows %q notes %q", rows, notes)
		}
		if r.freshRenders() != 5 {
			t.Fatalf("%d renders for 5 sources: %q", r.freshRenders(), r.calls)
		}
	})
}

func TestDrawKeepsTheRefreshWithinItsTotalBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := &fakeRenderer{cost: 3 * time.Second}
		d := drawing{renderer: r, now: time.Now, budget: drawBudget}
		got := d.draw(t.Context(), []string{"flowchart LR\na-->b", "flowchart LR\nc-->d", "flowchart LR\ne-->f"}, 80, ansi.GraphemeWidth)
		if elapsed := time.Since(start); elapsed != drawBudget {
			t.Fatalf("refresh took %s, budget %s", elapsed, drawBudget)
		}
		if len(got.diagrams[0].Lines) != 1 || got.undrawn != 2 || got.diagrams[1].Note != noteDrawing || got.diagrams[2].Note != noteDrawing || len(r.calls) != 2 {
			t.Fatalf("%+v, %d renders", got, len(r.calls))
		}
	})
}

// A diagram that draws longer than a refresh (6 s here, under the renderer's
// 20 s) shows its source at 5 s and its drawing when the render ends: the
// render is not cut at the budget, and no continuation starts it again.
func TestFollowDrawsARenderThatOutlastsTheRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 6 * time.Second}
		path := fixtureDir(t) + "/doc.md"
		writeFixture(t, path, []byte(fence(1)+fence(2)+fence(3)))
		m := New(context.Background(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown, Mermaid: r})
		t.Cleanup(m.Close)
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
		start := time.Now()
		msg, ok := cmd().(resultMsg)
		if !ok || time.Since(start) != drawBudget {
			t.Fatalf("first view after %s, want %s", time.Since(start), drawBudget)
		}
		_, next := m.Update(msg)
		if rows, notes := drawn(m); len(rows) != 0 || strings.Join(notes, "|") != strings.Join([]string{noteDrawing, noteDrawing, noteDrawing}, "|") {
			t.Fatalf("first view: rows %q notes %q", rows, notes)
		}
		applyWork(t, m, next)
		if rows, notes := drawn(m); len(rows) != 3 || len(notes) != 0 || r.freshRenders() != 3 {
			t.Fatalf("rows %q notes %q renders %d", rows, notes, r.freshRenders())
		}
	})
}

// A render longer than two refreshes keeps its continuations going: one that
// draws nothing new while the render runs does not stop the chain.
func TestFollowWaitsForARenderLongerThanTwoRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 12 * time.Second}
		m := mermaidModel(t, fence(1), r)
		if rows, notes := drawn(m); len(rows) != 1 || len(notes) != 0 || r.freshRenders() != 1 {
			t.Fatalf("rows %q notes %q renders %d", rows, notes, r.freshRenders())
		}
	})
}

// A continuation that makes no progress, with nothing still drawing, stops
// the chain and says so; r starts afresh.
func TestDrawStallsOnlyWithoutProgressOrARunningRender(t *testing.T) {
	r := &fakeRenderer{}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	d := drawing{renderer: r, now: time.Now, budget: drawBudget, before: 1}
	got := d.draw(ended, []string{"flowchart LR\na-->b"}, 80, ansi.GraphemeWidth)
	if got.undrawn != 1 || got.inflight || got.diagrams[0].Note != noteStalled {
		t.Fatalf("%+v", got)
	}
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 6 * time.Second}
		d := drawing{renderer: r, now: time.Now, budget: drawBudget, before: 1}
		got := d.draw(t.Context(), []string{"flowchart LR\na-->b"}, 80, ansi.GraphemeWidth)
		if got.undrawn != 1 || !got.inflight || got.diagrams[0].Note != noteDrawing {
			t.Fatalf("a running render is progress: %+v", got)
		}
	})
}

// A transient fallback (busy) is attempted, not undrawn: no continuation,
// but r on the unchanged file draws it again.
func TestFollowRetriesATransientFallbackOnR(t *testing.T) {
	r := &fakeRenderer{busy: true}
	m := mermaidModel(t, fence(1), r)
	if rows, notes := drawn(m); len(rows) != 0 || len(notes) != 1 || notes[0] != "mermaid: not drawn (renderer busy)" || len(r.calls) != 1 {
		t.Fatalf("rows %q notes %q renders %d", rows, notes, len(r.calls))
	}
	r.busy = false
	applyWork(t, m, key(m, 'r', 0))
	if rows, notes := drawn(m); len(rows) != 1 || len(notes) != 0 {
		t.Fatalf("after r: rows %q notes %q", rows, notes)
	}
	calls := len(r.calls)
	applyWork(t, m, key(m, 'r', 0)) // nothing left to retry: a read only
	if len(r.calls) != calls {
		t.Fatal("r rendered a drawn document again")
	}
}

func TestFollowWithoutRendererKeepsCodeCards(t *testing.T) {
	path := fixtureDir(t) + "/doc.md"
	writeFixture(t, path, []byte(fence(1)))
	m := New(context.Background(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown})
	t.Cleanup(m.Close)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	applyWork(t, m, cmd)
	if rows, notes := drawn(m); len(rows) != 0 || len(notes) != 0 || m.view.doc.Lines[1].Text != "flowchart LR" {
		t.Fatalf("rows %q notes %q", rows, notes)
	}
}

// A drawing that replaces code rows at or above a selection clears it; one
// above the changed fence is kept.
func TestFollowDrawingClearsASelectionItMoves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 3 * time.Second}
		text := "intro\n" + fence(1) + "middle\n" + fence(2) + "end\n"
		path := fixtureDir(t) + "/doc.md"
		writeFixture(t, path, []byte(text))
		for _, c := range []struct {
			line int
			kept bool
		}{{0, true}, {6, false}} {
			m := New(context.Background(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown, Mermaid: r})
			t.Cleanup(m.Close)
			r.cached = nil
			_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
			// The first refresh draws fence 1 and leaves fence 2 for a continuation.
			_, next := m.Update(cmd())
			if m.view.undrawn != 1 || next == nil {
				t.Fatalf("undrawn %d", m.view.undrawn)
			}
			m.selection = &selection{anchor: point{c.line, 0}, focus: point{c.line, 2}}
			applyWork(t, m, next)
			if rows, _ := drawn(m); len(rows) != 2 || (m.selection != nil) != c.kept {
				t.Fatalf("line %d: rows %q selection %+v", c.line, rows, m.selection)
			}
		}
	})
}

// A paused reading position is a source byte: drawings that replace a
// fence's code rows with fewer or more rows, or code rows that replace a
// drawing, keep it on the same prose line; one inside a fence lands on its
// drawing's top.
func TestFollowAnchorSurvivesDrawingReplacement(t *testing.T) {
	theme := spaceview.NewTheme(true, nil, nil)
	raw := []byte("head\n" + fence(1) + "TARGET\n" + strings.Repeat("tail\n", 30))
	render := func(rows int) *rendered {
		if rows < 0 {
			return renderFile(context.Background(), raw, false, false, Markdown, 80, ansi.GraphemeWidth, theme, drawing{})
		}
		r := &fakeRenderer{rows: rows}
		return renderFile(context.Background(), raw, false, false, Markdown, 80, ansi.GraphemeWidth, theme, drawing{renderer: r, now: time.Now, budget: drawBudget})
	}
	at := func(v *rendered, text string) int {
		for i, row := range v.rows {
			if v.doc.Lines[row.Line].Text == text {
				return i
			}
		}
		t.Fatalf("no row %q", text)
		return 0
	}
	for _, c := range []struct{ from, to int }{{-1, 1}, {-1, 6}, {1, 6}, {6, 1}, {6, -1}} {
		for _, text := range []string{"TARGET", "head"} {
			m := New(context.Background(), Options{})
			m.height, m.following = 5, false
			m.view = render(c.from)
			m.top = at(m.view, text)
			m.captureAnchor()
			m.view = render(c.to)
			m.placeAnchor()
			if got := m.view.doc.Lines[m.view.rows[m.top].Line].Text; got != text {
				t.Fatalf("%d→%d rows: anchor on %q moved to %q", c.from, c.to, text, got)
			}
			m.Close()
		}
	}
	// Inside the fence's code: the drawing's first row.
	m := New(context.Background(), Options{})
	defer m.Close()
	m.height, m.following = 5, false
	m.view = render(-1)
	m.top = at(m.view, " n1 --> x")
	m.captureAnchor()
	m.view = render(3)
	m.placeAnchor()
	if got := m.view.doc.Lines[m.view.rows[m.top].Line].Text; got != "drawn n1" {
		t.Fatalf("anchor inside the fence moved to %q", got)
	}
}

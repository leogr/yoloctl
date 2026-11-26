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
	mu         sync.Mutex
	cost       time.Duration
	busy       bool
	busySource string
	cached     map[string]bool
	rows       int // drawing rows; 0 means one
	calls      []string
	widths     []int
	fresh      int // renders that were not answered from the cache
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
	if f.busy || f.busySource != "" && strings.Contains(source, f.busySource) {
		return mermaid.Result{Status: mermaid.Busy}
	}
	if f.cached == nil {
		f.cached = map[string]bool{}
	}
	f.cached[source] = true
	return mermaid.Result{Status: mermaid.OK, Lines: f.drawing(source)}
}

func (f *fakeRenderer) Lookup(source string, _ int, _ ansi.Method) (mermaid.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cached[source] {
		return mermaid.Result{}, false
	}
	return mermaid.Result{Status: mermaid.OK, Lines: f.drawing(source)}, true
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
	var text strings.Builder
	for _, source := range sources {
		fmt.Fprintf(&text, "```mermaid\n%s```\n", source)
	}
	m := mermaidModel(t, text.String(), r)
	out := m.view.drawn.diagrams
	if len(r.calls) != maxDiagrams-1 {
		t.Fatalf("%d renders, want %d", len(r.calls), maxDiagrams-1)
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

func TestFollowShowsSourceAtOriginalDeadlineAcrossAppends(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 8 * time.Second}
		path := fixtureDir(t) + "/doc.md"
		text := "initial prose\n" + fence(1) + fence(22)
		writeFixture(t, path, []byte(text))
		m := New(t.Context(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown, Mermaid: r})
		t.Cleanup(m.Close)
		d := newViewDriver(m)
		start := time.Now()
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
		d.start(cmd)
		d.ready()
		if time.Since(start) != 0 || !strings.Contains(m.view.text, "initial prose") || !m.view.drawn.diagrams[0].Pending {
			t.Fatal("first prose waited for the drawing")
		}
		for i := 1; i <= 8; i++ {
			time.Sleep(time.Second)
			added := fmt.Sprintf("append %d", i)
			text += added + "\n"
			writeFixture(t, path, []byte(text))
			d.start(m.work(true, false))
			d.ready()
			if !strings.Contains(m.view.text, added) {
				t.Fatalf("append %d was not displayed", i)
			}
			for j, card := range m.view.drawn.diagrams {
				if card.Pending != (i < 5) {
					t.Fatalf("second %d, fence %d: %+v", i, j, card)
				}
			}
			if want := 1 + i/8; r.freshRenders() != want {
				t.Fatalf("second %d: renders=%d, want %d", i, r.freshRenders(), want)
			}
		}
		if rows, _ := drawn(m); len(rows) != 1 || rows[0] != "drawn n1" {
			t.Fatalf("latest document: drawings %q", rows)
		}
	})
}

func TestFollowSmallestBusyAdvancesQueue(t *testing.T) {
	r := &fakeRenderer{busySource: "n1 -->"}
	m := mermaidModel(t, fence(22)+fence(1), r)
	rows, notes := drawn(m)
	if len(r.calls) != 2 || !strings.Contains(r.calls[0], "n1 -->") ||
		len(rows) != 1 || rows[0] != "drawn n22" || strings.Join(notes, "") != "mermaid: not drawn (renderer busy)" {
		t.Fatalf("calls=%q rows=%q notes=%q", r.calls, rows, notes)
	}
	applyWork(t, m, m.work(true, false))
	if len(r.calls) != 2 {
		t.Fatalf("poll retried the busy fence: %q", r.calls)
	}
	r.busySource = ""
	applyWork(t, m, key(m, 'r', 0))
	if rows, _ := drawn(m); len(r.calls) != 3 || len(rows) != 2 {
		t.Fatalf("explicit retry: calls=%q rows=%q", r.calls, rows)
	}
}

func TestFollowResizeRejectsStaleDiagramReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 8 * time.Second}
		path := fixtureDir(t) + "/doc.md"
		writeFixture(t, path, []byte(fence(1)))
		m := New(t.Context(), Options{Reader: HostReader{Path: path}, Format: Markdown, Mermaid: r})
		t.Cleanup(m.Close)
		d := newViewDriver(m)
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
		d.start(cmd)
		d.ready()
		old := m.diagramActive
		_, cmd = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
		d.start(cmd)
		d.ready()
		replacement := m.diagramActive
		if replacement == nil || replacement == old || replacement.key.width != markdownview.DiagramWidth(100) {
			t.Fatalf("replacement = %+v", replacement)
		}
		m.Update(diagramDoneMsg{old, mermaid.Result{Status: mermaid.Busy}})
		if m.diagramActive != replacement || m.view.drawn.diagrams[0].Note != noteDrawing {
			t.Fatal("obsolete cancellation changed the replacement")
		}
		time.Sleep(8 * time.Second)
		d.ready()
		if rows, _ := drawn(m); len(rows) != 1 || r.freshRenders() != 2 {
			t.Fatalf("rows=%q fresh=%d", rows, r.freshRenders())
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
			_, next := m.Update(cmd())
			if len(m.view.drawn.order) != 2 || next == nil {
				t.Fatal("missing diagram jobs")
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
			return renderFile(raw, false, false, Markdown, 80, ansi.GraphemeWidth, theme, drawing{})
		}
		r := &fakeRenderer{rows: rows, cached: map[string]bool{"flowchart LR\n n1 --> x\n": true}}
		return renderFile(raw, false, false, Markdown, 80, ansi.GraphemeWidth, theme, drawing{renderer: r, now: time.Now})
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

func TestFollowShowsSourceWhenFallbackWakeIsDue(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lateLayout bool
	}{
		{name: "late layout", lateLayout: true},
		{name: "unchanged read before queued wake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := &fakeRenderer{cost: 8 * time.Second}
				path := fixtureDir(t) + "/doc.md"
				writeFixture(t, path, []byte(fence(1)))
				m := New(t.Context(), Options{Reader: HostReader{Path: path}, Format: Markdown, Mermaid: r})
				t.Cleanup(m.Close)
				d := newViewDriver(m)
				_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
				first := cmd().(resultMsg)
				if !first.view.drawn.diagrams[0].Pending {
					t.Fatal("fixture did not produce a placeholder")
				}
				if tc.lateLayout {
					time.Sleep(drawBudget + time.Second)
				}
				_, cmd = m.Update(first)
				d.start(cmd)
				d.ready()

				if !tc.lateLayout {
					read := m.work(true, false)().(resultMsg)
					if read.snapshot.Changed || read.view != nil || read.err != nil {
						t.Fatalf("expected an unchanged read: %+v", read)
					}
					time.Sleep(drawBudget)
					synctest.Wait()
					select {
					case queued := <-d.in:
						if _, ok := queued.(diagramWakeMsg); !ok {
							t.Fatalf("expected queued wake, got %T", queued)
						}
						_, cmd = m.Update(read)
						d.start(cmd)
						d.accept(queued)
						d.ready()
					default:
						t.Fatal("fallback wake was not queued")
					}
				}

				if m.diagramActive == nil || m.view.drawn.diagrams[0].Pending ||
					!strings.Contains(ansi.Strip(m.View().Content), "n1 --> x") {
					t.Fatal("fallback did not show the source while the render was pending")
				}
				exposed := m.view
				d.start(m.work(true, false))
				d.ready()
				if m.view != exposed || m.diagramWake != nil {
					t.Fatal("unchanged poll scheduled another layout or fallback after exposure")
				}
				time.Sleep(r.cost)
				d.ready()
				if rows, _ := drawn(m); len(rows) != 1 || r.freshRenders() != 1 {
					t.Fatalf("drawings %q after %d renders", rows, r.freshRenders())
				}
			})
		})
	}
}

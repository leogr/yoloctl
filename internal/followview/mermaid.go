package followview

import (
	"context"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/mermaid"
	"github.com/leogr/yoloctl/internal/mermaid/admit"
)

// Renderer draws one Mermaid fence at a card's inner width (mermaid.Client).
// A ctx that ends while it renders gets a transient result at once.
type Renderer interface {
	Render(ctx context.Context, source string, width int, method ansi.Method) mermaid.Result
}

const (
	// drawBudget is how long one refresh waits for drawings before it shows
	// the document (leogr D4 and the sweep rule: the source after 5 s). A
	// render still running keeps going, up to the renderer's own limit, and a
	// continuation shows its drawing.
	drawBudget = 5 * time.Second
	// maxDiagrams is the fences one refresh draws (leogr D4).
	maxDiagrams = 20
)

// Notes shown after a mermaid fence that is not drawn.
const (
	noteDrawing = "mermaid: drawing…"
	noteStalled = "mermaid: not drawn (refresh budget; r retries)"
	noteTooMany = "mermaid: not drawn (too many diagrams)"
	notePrefix  = "mermaid: not drawn ("
	noteSuffix  = ")"
)

// drawing is how one render job draws mermaid fences.
type drawing struct {
	// reuse preserves the refresh outcome during link-only relayout.
	reuse    *drawResult
	renderer Renderer // nil: markdown.mermaid off, fences stay code cards
	now      func() time.Time
	budget   time.Duration // 0 means drawBudget
	// before is the undrawn fences of the view a continuation continues
	// (0 otherwise): one that leaves as many, with none still drawing, stops
	// the chain.
	before int
	// renders holds the renders that outlast a refresh; nil starts them under
	// the draw call's context, unshared.
	renders *renderSet
}

// drawResult is a refresh's outcome for each closed mermaid fence, in source order.
type drawResult struct {
	diagrams []markdownview.Diagram
	undrawn  int  // fences the budget left for a continuation
	retry    int  // undrawn fences plus transient fallbacks: what r redraws
	inflight bool // a render is still running: its continuation will show it
}

// renderSet holds the renders that outlast the refresh that started them. A
// render keeps going up to the renderer's own limit, and a continuation waits
// for it instead of starting another. The Model owns one for all its jobs.
type renderSet struct {
	// spawn runs a render in the background under the view's context; false
	// once the view has closed.
	spawn   func(func(context.Context)) bool
	mu      sync.Mutex
	running map[renderKey]*renderRun
}

type renderKey struct {
	source string
	width  int
	method ansi.Method
}

type renderRun struct {
	done   chan struct{}
	result mermaid.Result
}

// start returns the running render of k, or starts one.
func (s *renderSet) start(r Renderer, k renderKey) *renderRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.running[k]; ok {
		return run
	}
	run := &renderRun{done: make(chan struct{})}
	if !s.spawn(func(ctx context.Context) {
		run.result = r.Render(ctx, k.source, k.width, k.method)
		s.mu.Lock()
		delete(s.running, k)
		s.mu.Unlock()
		close(run.done)
	}) {
		run.result = mermaid.Result{Status: mermaid.Busy}
		close(run.done)
		return run
	}
	if s.running == nil {
		s.running = map[renderKey]*renderRun{}
	}
	s.running[k] = run
	return run
}

// draw decides every fence occurrence in source order: a refused one gets
// the admission reason, admitted ones past maxDiagrams a note, and the others
// a drawing or the renderer's reason. Each distinct source is rendered once.
// The refresh waits for drawings only within budget: a render still running
// at its end keeps going for a continuation, and later sources wait for one.
// Fences left over are undrawn: "drawing…" for a continuation, or stalled
// when this continuation made no progress and nothing is still drawing.
func (d drawing) draw(ctx context.Context, sources []string, width int, method ansi.Method) drawResult {
	if d.reuse != nil {
		return *d.reuse
	}
	out := drawResult{diagrams: make([]markdownview.Diagram, len(sources))}
	results := map[string]markdownview.Diagram{}
	skipped, transient := map[string]bool{}, map[string]bool{}
	var undrawn []int
	admitted := 0
	budget := d.budget
	if budget <= 0 {
		budget = drawBudget
	}
	deadline := d.now().Add(budget)
	renders := d.renders
	if renders == nil {
		renders = &renderSet{spawn: func(fn func(context.Context)) bool { go fn(ctx); return true }}
	}
	for i, source := range sources {
		if decision := admit.Check(source); !decision.Admitted() {
			out.diagrams[i] = markdownview.Diagram{Note: notePrefix + decision.Reason + noteSuffix}
			continue
		}
		if admitted++; admitted > maxDiagrams {
			out.diagrams[i] = markdownview.Diagram{Note: noteTooMany}
			continue
		}
		if r, ok := results[source]; ok {
			out.diagrams[i] = r
			if transient[source] {
				out.retry++
			}
			continue
		}
		remaining := deadline.Sub(d.now())
		if skipped[source] || ctx.Err() != nil || remaining <= 0 {
			skipped[source] = true
			undrawn = append(undrawn, i)
			continue
		}
		run := renders.start(d.renderer, renderKey{source, markdownview.DiagramWidth(width), method})
		timer := time.NewTimer(remaining)
		var result mermaid.Result
		finished := false
		select {
		case <-run.done:
			result, finished = run.result, true
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		if !finished {
			// Still drawing at the budget (or the view closed): shown on a
			// continuation, not attempted again here.
			out.inflight = out.inflight || ctx.Err() == nil
			skipped[source] = true
			undrawn = append(undrawn, i)
			continue
		}
		r := markdownview.Diagram{Lines: result.Lines}
		if result.Status != mermaid.OK {
			r = markdownview.Diagram{Note: notePrefix + result.Note() + noteSuffix}
		}
		if result.Transient() {
			transient[source] = true
			out.retry++
		}
		results[source], out.diagrams[i] = r, r
	}
	note := noteDrawing
	if d.before > 0 && len(undrawn) >= d.before && !out.inflight {
		note = noteStalled
	}
	for _, i := range undrawn {
		out.diagrams[i] = markdownview.Diagram{Note: note}
	}
	out.undrawn = len(undrawn)
	out.retry += len(undrawn)
	return out
}

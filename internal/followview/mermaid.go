package followview

import (
	"context"
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
	// drawBudget is one refresh's total time for rendering (leogr D4).
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
	renderer Renderer // nil: markdown.mermaid off, fences stay code cards
	now      func() time.Time
	budget   time.Duration // 0 means drawBudget
	// before is the undrawn fences of the view a continuation continues
	// (0 otherwise): a continuation that leaves as many stops the chain.
	before int
}

// drawResult is a refresh's outcome for each closed mermaid fence, in source order.
type drawResult struct {
	diagrams []markdownview.Diagram
	undrawn  int // fences the budget left for a continuation
	retry    int // undrawn fences plus transient fallbacks: what r redraws
}

// draw decides every fence occurrence in source order: a refused one gets
// the admission reason, admitted ones past maxDiagrams a note, and the others
// a drawing or the renderer's reason. Each distinct source is rendered once,
// all within budget: a render gets only the time left and one cut by it
// counts as not attempted. Fences left over are undrawn: "drawing…" for a
// continuation, or stalled when this continuation made no progress.
func (d drawing) draw(ctx context.Context, sources []string, width int, method ansi.Method) drawResult {
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
		left, cancel := context.WithTimeout(ctx, remaining)
		result := d.renderer.Render(left, source, markdownview.DiagramWidth(width), method)
		cancel()
		if result.Transient() && !d.now().Before(deadline) {
			skipped[source] = true // cut by the budget: not attempted
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
	if d.before > 0 && len(undrawn) >= d.before {
		note = noteStalled
	}
	for _, i := range undrawn {
		out.diagrams[i] = markdownview.Diagram{Note: note}
	}
	out.undrawn = len(undrawn)
	out.retry += len(undrawn)
	return out
}

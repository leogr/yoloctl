package mermaid

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/markdownview"
)

func TestWorkerFitsRecordedFlowchartAtFollowWidths(t *testing.T) {
	const source = `flowchart TD
A[Client request to /health] --> B{Method is GET}
B -->|yes| C[Respond 200 with body ok]
B -->|no| D[Respond 405 with Allow GET]
`
	const unshiftedWidth = 60
	method := ansi.GraphemeWidth
	// The first pane supplies the same compact drawing before any translation.
	panes := []int{62, 59, 60, 80}
	draw := testWorkerSession(t, workerLimits{1024, renderDeadline, MaxDrawing})
	var unshifted []string
	for i, pane := range panes {
		width := markdownview.DiagramWidth(pane)
		req := testRenderRequest(width, methodName(method), source)
		req.ID = uint64(i + 1)
		drawing := draw(req).Result
		if width == unshiftedWidth {
			unshifted = drawing.Lines
		}
		t.Run(fmt.Sprintf("pane-%d", pane), func(t *testing.T) {
			if drawing.Status != OK || !drawing.valid(width, method) {
				t.Fatalf("inner width %d: %+v", width, drawing)
			}
			if shift := unshiftedWidth - width; shift > 0 {
				if len(drawing.Lines) != len(unshifted) {
					t.Fatalf("shift changed the row count: %d → %d", len(unshifted), len(drawing.Lines))
				}
				prefix := strings.Repeat(" ", shift)
				for i, row := range unshifted {
					want, shared := strings.CutPrefix(row, prefix)
					if !shared || drawing.Lines[i] != want {
						t.Fatalf("row %d: got %q, want %q shifted by %d", i, drawing.Lines[i], row, shift)
					}
				}
			}
			doc := markdownview.Parse("```mermaid\n"+source+"```\n", markdownview.ParseOptions{
				Method: method,
				Diagrams: func(string) (markdownview.Diagram, bool) {
					return markdownview.Diagram{Lines: drawing.Lines}, true
				},
			})
			layout := markdownview.Layout(doc, pane, markdownview.LayoutOptions{Method: method})
			rows := 0
			for _, row := range layout.Rows {
				if doc.Lines[row.Line].Diagram > 0 {
					rows++
					if got := markdownview.RunsWidth(method, row.Runs); got > pane {
						t.Fatalf("drawing row occupies %d columns in a %d-column pane", got, pane)
					}
				}
			}
			if rows != len(drawing.Lines) {
				t.Fatalf("Markdown laid out %d drawing rows, worker returned %d", rows, len(drawing.Lines))
			}
		})
	}
}

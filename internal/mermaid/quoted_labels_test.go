package mermaid

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/leogr/yoloctl/internal/mermaid/admit"
)

// drawAdmitted draws an admitted source with the embedded artifact at a
// fidelity width, as golden text (drawing or status).
func drawAdmitted(t *testing.T) func(source string, width int) string {
	t.Helper()
	skipUnderRace(t)
	ctx := t.Context()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		t.Fatal(err)
	}
	compiled, err := rt.CompileModule(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	return func(source string, width int) string {
		t.Helper()
		if d := admit.Check(source); !d.Admitted() {
			t.Fatalf("%q not admitted: %s", source, d.Reason)
		}
		r := render(ctx, rt, compiled, request{protocol, moduleHash(), width, "grapheme", source}, ansi.GraphemeWidth, workerLimits{1024, 2 * time.Second, MaxDrawing})
		if r.Status == Failed || r.Status == Timeout || r.Status == ParseError {
			t.Fatalf("%q at %d: %s", source, width, r.Status)
		}
		return golden(r)
	}
}

// A quoted edge label draws exactly as the same text unquoted: the pinned
// renderer removes the quotes, in both admitted forms, for every edge style.
func TestQuotedEdgeLabelsDrawAsUnquoted(t *testing.T) {
	draw := drawAdmitted(t)
	labels := []string{"x", "two words", "❯ é ok", "one two three four five six seven eight nine ten"}
	var pairs [][2]string // quoted, unquoted
	for _, l := range labels {
		for _, op := range []string{"-->", "---", "-.->", "==>"} {
			pairs = append(pairs, [2]string{"flowchart LR\nA " + op + `|"` + l + `"| B`, "flowchart LR\nA " + op + "|" + l + "| B"})
		}
		pairs = append(pairs,
			[2]string{"flowchart LR\nA -- \"" + l + "\" --> B", "flowchart LR\nA -- " + l + " --> B"},
			[2]string{"flowchart TD\nA -->|\"" + l + "\"| B -- \"" + l + "\" --> C & D\nsubgraph s\nE -- \"" + l + "\" --> F\nend",
				"flowchart TD\nA -->|" + l + "| B -- " + l + " --> C & D\nsubgraph s\nE -- " + l + " --> F\nend"})
	}
	for _, p := range pairs {
		for _, width := range fidelityWidths {
			if quoted, plain := draw(p[0], width), draw(p[1], width); quoted != plain {
				t.Errorf("width %d: %q draws\n%s\nbut %q draws\n%s", width, p[0], quoted, p[1], plain)
			}
		}
	}
}

// A label with characters only its quoted form admits is drawn verbatim
// (#34; as a quote) where a plain label of the same shape is drawn, with the
// same edge: replacing the text by that plain label gives its drawing.
func TestQuotedEdgeLabelSpecialCharactersDrawVerbatim(t *testing.T) {
	draw := drawAdmitted(t)
	plain := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == ',' {
				return r
			}
			return 'x'
		}, s)
	}
	for _, c := range []struct {
		label, drawn string
		dash         bool // also admitted as -- "label" -->
	}{
		{`a (b) [c] {d}`, `a (b) [c] {d}`, true},
		{`x; y %% z`, `x; y %% z`, true},
		{`say #34;hi#34;`, `say "hi"`, true},
		{`a -- b -> c`, `a -- b -> c`, true},
		{`a | b`, `a | b`, false},
		{`a --> b`, `a --> b`, false},
		{`v1.-rc -.-> b`, `v1.-rc -.-> b`, false},
	} {
		p := plain(c.drawn)
		sources := [][2]string{}
		for _, op := range []string{"-->", "---", "-.->", "==>"} {
			sources = append(sources, [2]string{"flowchart LR\nA " + op + `|"` + c.label + `"| B`, "flowchart LR\nA " + op + "|" + p + "| B"})
		}
		if c.dash {
			sources = append(sources, [2]string{"flowchart LR\nA -- \"" + c.label + "\" --> B", "flowchart LR\nA -- " + p + " --> B"})
		}
		for _, s := range sources {
			for _, width := range fidelityWidths {
				got, want := draw(s[0], width), draw(s[1], width)
				if !strings.Contains(got, c.drawn) || strings.Replace(got, c.drawn, p, 1) != want {
					t.Errorf("width %d: %q draws\n%s\nwant %q where %q draws\n%s", width, s[0], got, c.drawn, s[1], want)
				}
			}
		}
	}
}

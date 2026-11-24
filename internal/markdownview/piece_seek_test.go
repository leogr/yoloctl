package markdownview

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func TestWrappedPiecesMatchFullScan(t *testing.T) {
	theme := spaceview.NewTheme(true, nil, nil)
	text := strings.Repeat("**bold** ~~strike~~ _italic_ <https://a.test/longer-than-a-wrapped-row> ", 24)
	for _, tc := range []struct {
		name     string
		decorate Decorator
	}{
		{"ordered", nil},
		{"empty-boundary", func(_ string, pieces []Piece, _ spaceview.Theme, _ bool) []Piece {
			at := pieces[0].End
			return slices.Insert(pieces, 1, Piece{Start: at, End: at})
		}},
		{"reordered", func(_ string, pieces []Piece, _ spaceview.Theme, _ bool) []Piece {
			out := slices.Clone(pieces)
			slices.Reverse(out)
			return out
		}},
		{"overlapping", func(_ string, pieces []Piece, _ spaceview.Theme, _ bool) []Piece {
			p := pieces[0]
			p.Act = Action{Kind: IntentURL, Value: "overlap"}
			return slices.Insert(pieces, 0, p)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := Parse(text, ParseOptions{Method: ansi.GraphemeWidth, Theme: theme, Decorate: tc.decorate})
			line := doc.Lines[0]
			result := Layout(doc, 18, LayoutOptions{Method: ansi.GraphemeWidth, Theme: theme})
			if len(result.Rows) < 4 || len(line.Pieces) < 16 {
				t.Fatal("fixture needs multiple wraps over many pieces")
			}
			for i, row := range result.Rows {
				var want []Run
				for _, p := range line.Pieces {
					start, end := max(p.Start, row.Start), min(p.End, row.End)
					if start < end {
						want = append(want, Run{Text: line.Text[start:end], Style: p.Style, Copy: true, Start: start, Act: p.Act, Strike: p.Strike})
					}
				}
				if !slices.Equal(row.Runs, want) {
					t.Fatalf("row %d at %d:%d = %#v, want %#v", i, row.Start, row.End, row.Runs, want)
				}
			}
		})
	}
}

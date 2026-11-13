package followview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func TestSixTableWidthCases(t *testing.T) {
	files, err := os.ReadDir("../markdownview/testdata/table-shapes")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join("../markdownview/testdata/table-shapes", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		prior, priorClosed := 0, 10000
		for width := 8; width <= 180; width++ {
			view := renderFile(raw, false, false, Markdown, width, ansi.GraphemeWidth, spaceview.NewTheme(true, nil, nil))
			rows := view.rows
			level := 2
			closed := 0
			var text strings.Builder
			for _, row := range rows {
				if row.Closed {
					closed++
				}
				for _, run := range row.Runs {
					text.WriteString(run.Text)
					if run.Act.Kind == markdownview.IntentToggleTableRow || run.Tail > 0 {
						level = 1
					}
				}
			}
			if strings.HasPrefix(text.String(), "Table ") {
				level = 0
			}
			if level < prior || level == 1 && prior == 1 && closed > priorClosed {
				t.Fatalf("%s width%d: %d->%d closed%d->%d", file.Name(), width, prior, level, priorClosed, closed)
			}
			prior, priorClosed = level, closed
		}
	}
}

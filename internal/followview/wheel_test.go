package followview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/filelink"
)

func TestWheelKeepsWrappedRowAfterRender(t *testing.T) {
	for _, mode := range []struct {
		name, down, up string
	}{
		{"sgr-cell", "\x1b[<65;3;4M", "\x1b[<64;3;4M"},
		{"sgr-pixel", "\x1b[<65;23;58M", "\x1b[<64;23;58M"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			m, _ := fileModel(t, strings.Repeat("wrap these words across several rows ", 24)+"\nnext line", Markdown)
			// The CLI enables navigation, so scrolling schedules a background render
			// even for unchanged text with no links to admit.
			m.opts.Navigation = &filelink.Navigator{}
			_, resize := m.Update(tea.WindowSizeMsg{Width: 20, Height: 6})
			applyWork(t, m, resize)
			applyWork(t, m, key(m, tea.KeyHome, 0))
			if a, b := m.view.rows[0], m.view.rows[1]; a.Line != b.Line || a.End != b.Start || b.Start == 0 {
				t.Fatalf("fixture did not create an adjacent wrap: line %d/%d, end %d, start %d", a.Line, b.Line, a.End, b.Start)
			}

			var decoder uv.EventDecoder
			for _, burst := range []struct {
				input string
				want  []int
			}{
				{mode.down, []int{1}},
				{mode.up, []int{0}},
				{mode.down + mode.down + mode.up + mode.down, []int{1, 2, 1, 2}},
				{mode.down + mode.down + mode.down, []int{3, 4, 5}},
			} {
				input := []byte(burst.input)
				var work tea.Cmd
				for _, top := range burst.want {
					n, event := decoder.Decode(input)
					wheel, ok := event.(uv.MouseWheelEvent)
					if !ok || n <= 0 {
						t.Fatalf("wheel decode failed: %T for %q", event, input)
					}
					input = input[n:]
					_, cmd := m.Update(tea.MouseWheelMsg(tea.Mouse(wheel)))
					if cmd != nil {
						if work != nil {
							t.Fatal("wheel burst started parallel renders")
						}
						work = cmd
					}
					if m.top != top || m.following {
						t.Fatalf("wheel placed top at %d following=%v, want %d paused", m.top, m.following, top)
					}
				}
				if len(input) != 0 {
					t.Fatalf("unconsumed wheel input %q", input)
				}
				applyWork(t, m, work)
				if want := burst.want[len(burst.want)-1]; m.top != want {
					t.Fatalf("unchanged render moved the top row from %d to %d", want, m.top)
				}
			}
		})
	}
}

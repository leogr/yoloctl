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

func TestWheelKeepsTableRowAcrossRenderAndRead(t *testing.T) {
	const doc = "| Ticket | Notes |\n|---|---|\n" +
		"| one two three four five | red green blue white black |\n" +
		"| six seven eight nine | indigo orange teal gold |\n"
	for _, appendWhileReading := range []bool{false, true} {
		name := "render"
		if appendWhileReading {
			name = "append"
		}
		t.Run(name, func(t *testing.T) {
			for _, shape := range []string{"overlap", "separator"} {
				t.Run(shape, func(t *testing.T) {
					raw := doc + "\n" + strings.Repeat("tail\n", 30)
					m, path := fileModel(t, raw, Markdown)
					m.opts.Navigation = &filelink.Navigator{}
					_, resize := m.Update(tea.WindowSizeMsg{Width: 27, Height: 6})
					applyWork(t, m, resize)
					m.pause()

					target := 0
					for i := 1; i < m.maxTop(); i++ {
						a, b := m.view.rows[i-1], m.view.rows[i]
						if a.Line != b.Line || m.view.doc.Lines[b.Line].Table == nil {
							continue
						}
						overlap := a.Start < b.Start && b.Start < a.End
						separator := a.Decor && a.Start == 0 && a.End == 0 && b.Start == 0 && b.End > 0
						if (shape == "overlap" && overlap) || (shape == "separator" && separator) {
							target = i
							break
						}
					}
					if target == 0 {
						t.Fatalf("fixture has no %s row", shape)
					}
					m.top = target - 1
					m.captureAnchor()
					var work tea.Cmd
					if appendWhileReading {
						_, poll := m.Update(pollMsg{})
						work = poll().(tea.BatchMsg)[1]
						writeFixture(t, path, []byte(raw+"\na later paragraph\n"))
					}
					_, wheel := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 2, Y: 2})
					if m.top != target || m.following {
						t.Fatalf("wheel put top at %d, want paused row %d", m.top, target)
					}
					if !appendWhileReading {
						work = wheel
					}
					if work == nil {
						t.Fatal("missing render work")
					}
					result := work().(resultMsg)
					if appendWhileReading && (!result.read || result.err != nil || !result.snapshot.Changed) {
						t.Fatalf("expected successful changed read, got read=%v changed=%v err=%v", result.read, result.snapshot.Changed, result.err)
					}
					_, next := m.Update(result)
					if m.top != target {
						t.Errorf("render moved top to %d, want %d", m.top, target)
					}
					if next != nil {
						applyWork(t, m, next)
					}
					if m.top != target {
						t.Errorf("settled top at %d, want %d", m.top, target)
					}
				})
			}
		})
	}
}

func TestViewportUsesSourceAnchorAfterReflow(t *testing.T) {
	m, _ := fileModel(t, strings.Repeat("word ", 100)+"\n"+strings.Repeat("tail\n", 20), Text)
	_, resize := m.Update(tea.WindowSizeMsg{Width: 20, Height: 6})
	applyWork(t, m, resize)
	applyWork(t, m, key(m, tea.KeyHome, 0))
	for range 4 {
		m.scroll(1)
	}
	before := m.top
	anchor := m.anchor
	_, resize = m.Update(tea.WindowSizeMsg{Width: 40, Height: 6})
	applyWork(t, m, resize)
	row := m.view.rows[m.top]
	if m.top == before || row.Line != 0 || int64(row.Start) > anchor || int64(row.End) <= anchor {
		t.Fatalf("reflow kept or missed the old row: top %d→%d, row %d [%d,%d), anchor %d", before, m.top, row.Line, row.Start, row.End, anchor)
	}
}

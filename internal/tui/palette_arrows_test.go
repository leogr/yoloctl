package tui

import (
	"fmt"
	"testing"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestPaletteTabArrowsThroughHostInputLoop(t *testing.T) {
	for _, encoding := range []struct {
		name, left, right string
		kitty             bool
	}{
		{"xterm Ctrl", "\x1b[1;5D", "\x1b[1;5C", false},
		{"xterm Alt", "\x1b[1;3D", "\x1b[1;3C", false},
		{"xterm Ctrl Alt", "\x1b[1;7D", "\x1b[1;7C", false},
		{"SS3 Ctrl", "\x1bO5D", "\x1bO5C", false},
		{"ESC-prefix Alt", "\x1b\x1b[D", "\x1b\x1b[C", false},
		{"ESC-prefix Alt with Ctrl", "\x1b\x1b[1;5D", "\x1b\x1b[1;5C", false},
		{"Kitty Ctrl", "\x1b[1;5:1D\x1b[1;5:3D", "\x1b[1;5:1C\x1b[1;5:3C", true},
		{"Kitty Alt", "\x1b[1;3:1D\x1b[1;3:3D", "\x1b[1;3:1C\x1b[1;3:3C", true},
		{"Kitty Ctrl Alt", "\x1b[1;7:1D\x1b[1;7:3D", "\x1b[1;7:1C\x1b[1;7:3C", true},
		{"Kitty functional CSI-u Ctrl", "\x1b[57350;5u", "\x1b[57351;5u", true},
		{"Kitty functional CSI-u Alt", "\x1b[57350;3u", "\x1b[57351;3u", true},
	} {
		for _, direction := range []struct {
			name, input string
			want        int
		}{{"left", encoding.left, 0}, {"right", encoding.right, 2}} {
			for _, focusedNotice := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/focused_notice=%t", encoding.name, direction.name, focusedNotice), func(t *testing.T) {
					m := newTestModel(t, 100, 30)
					for _, label := range []string{"first", "second", "third"} {
						m.tabs.Add(testTab(nil, label, true))
					}
					m.tabs.Select(1)
					m.pal.Insert("kept draft")
					if focusedNotice {
						m.postNotice(prettylog.KindWarn, "review")
						m.openToast()
						if !m.noticeFocused {
							t.Fatal("fixture did not focus the notice")
						}
					}
					input := direction.input
					if encoding.kitty {
						input = "\x1b[?31u" + input
					}
					runCompatibilityInput(t, m, input, false)
					if m.tabs.Active != direction.want || m.noticeFocused || !m.focusPalette {
						t.Fatalf("wire %q: active=%d want=%d, notice focused=%v, palette=%v", input, m.tabs.Active, direction.want, m.noticeFocused, m.focusPalette)
					}
					if line, _ := m.pal.Line(); line != "kept draft" {
						t.Fatalf("wire %q changed the draft to %q", input, line)
					}
				})
			}
		}
	}
}

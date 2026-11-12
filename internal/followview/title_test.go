package followview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The title and window title name the followed file only, in host and space
// mode alike (leogr: no space in the follow title).
func TestTitleNamesOnlyThePath(t *testing.T) {
	for _, space := range []string{"", "dev"} {
		m, path := fileModel(t, "hello", Text)
		m.opts.Space = space
		want := "follow · " + path
		// Size the view to the title, so a long temporary path is not clipped.
		m.Update(tea.WindowSizeMsg{Width: ansi.StringWidth(want) + 8, Height: 12})
		v := m.View()
		if v.WindowTitle != want {
			t.Fatalf("space %q: window title %q, want %q", space, v.WindowTitle, want)
		}
		first := ansi.Strip(strings.SplitN(v.Content, "\n", 2)[0])
		if !strings.HasPrefix(first, want) || strings.Contains(first, "host ·") || strings.Contains(first, "dev ·") {
			t.Fatalf("space %q: title row %q", space, first)
		}
	}
}

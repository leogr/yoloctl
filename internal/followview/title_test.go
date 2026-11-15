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
		want := path
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

func TestTitleUsesWorkdirRelativePathAndKeepsOutsideAbsolute(t *testing.T) {
	for _, tc := range []struct{ name, path, base, want string }{
		{"nested", "/work/project/docs/notes.md", "/work/project", "docs/notes.md"},
		{"root file", "/work/project/notes.md", "/work/project", "notes.md"},
		{"outside", "/var/log/service.log", "/work/project", "/var/log/service.log"},
		{"sibling prefix", "/work/project-other/notes.md", "/work/project", "/work/project-other/notes.md"},
		{"unavailable base", "/work/project/notes.md", "", "/work/project/notes.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, space := range []string{"", "dev"} {
				m, _ := fileModel(t, "hello", Text)
				m.opts.Path, m.opts.Workdir, m.opts.Space = tc.path, tc.base, space
				m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
				v := m.View()
				if v.WindowTitle != tc.want {
					t.Fatalf("title %q, want %q", v.WindowTitle, tc.want)
				}
				first := ansi.Strip(strings.SplitN(v.Content, "\n", 2)[0])
				if !strings.HasPrefix(first, tc.want) {
					t.Fatalf("title row %q", first)
				}
				if m.opts.Path != tc.path {
					t.Fatal("title changed the pinned reader path")
				}
			}
		})
	}
}

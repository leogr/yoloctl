package markdownview

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func Plain(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func ExpandTabs(wm ansi.Method, s string) string { return ExpandTabsMap(wm, s, nil) }

// expandTabsMap is expandTabs that, given src, records each tab's spaces as
// shown in place of it.
func ExpandTabsMap(wm ansi.Method, s string, src *SourceMap) string {
	var b strings.Builder
	x, at := 0, 0
	for at < len(s) {
		g, w := ansi.FirstGraphemeCluster(s[at:], wm)
		in := g
		if g == "\t" {
			w = 4 - x%4
			g = strings.Repeat(" ", w)
		}
		src.Add(g, s, at, at+len(in))
		b.WriteString(g)
		at += len(in)
		x += w
	}
	return b.String()
}

func DisplayWidth(wm ansi.Method, s string) int { return wm.StringWidth(ExpandTabs(wm, s)) }

func Fit(wm ansi.Method, s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = Truncate(wm, s, max(width, 0), "")
	return s + strings.Repeat(" ", max(width-wm.StringWidth(s), 0))
}

func FenceMarker(line string) string {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	i := 0
	for i < len(line) && line[i] == line[0] {
		i++
	}
	if i < 3 {
		return ""
	}
	return line[:i]
}

func ClosesFence(line, fence string) bool {
	marker := FenceMarker(line)
	return len(marker) >= len(fence) && marker[0] == fence[0] && strings.TrimSpace(line[len(marker):]) == ""
}

package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
)

func (m *model) tabWidthMethod() ansi.Method {
	if m.hostGraphemes {
		return ansi.GraphemeWidth
	}
	return ansi.WcWidth
}

// Titles are control-free plain text. Walk complete clusters using the same
// width method as the renderer, including when truncating in legacy wcwidth
// mode: ansi.TruncateWc's early width check uses grapheme width instead.
func truncateTabText(text string, width int, method ansi.Method) string {
	if method.StringWidth(text) <= width {
		return text
	}
	if width < 1 {
		return ""
	}
	end, used := 0, 0
	for end < len(text) {
		cluster, cols := ansi.FirstGraphemeCluster(text[end:], method)
		if used+cols > width-1 {
			break
		}
		end += len(cluster)
		used += cols
	}
	return text[:end] + "…"
}

// matchTabIcon keeps matching deterministic and never puts padding or SGR
// inside a grapheme (for example, between a symbol and its emoji selector).
func matchTabIcon(title string, icons map[string]config.AgentIcon) config.AgentIcon {
	var match config.AgentIcon
	var matchName string
	for name, icon := range icons {
		if icon.Icon == "" || !strings.HasPrefix(title, icon.Icon) {
			continue
		}
		end := 0
		for end < len(icon.Icon) {
			cluster, _ := ansi.FirstGraphemeCluster(title[end:], ansi.GraphemeWidth)
			end += len(cluster)
		}
		if end != len(icon.Icon) {
			continue
		}
		if len(icon.Icon) > len(match.Icon) || len(icon.Icon) == len(match.Icon) && name < matchName {
			match, matchName = icon, name
		}
	}
	return match
}

// tabIconTitle reserves at least two columns for the icon, plus a separator.
// This gives narrow symbols room for font overhang and aligns them with ASCII
// marks and wide graphemes. Padding precedes truncation and hit-test geometry;
// neither the holder's title nor a custom icon's grapheme sequence is changed.
func tabIconTitle(title string, icons map[string]config.AgentIcon, method ansi.Method) string {
	icon := matchTabIcon(title, icons).Icon
	if icon == "" || len(title) == len(icon) {
		return title
	}
	rest := title[len(icon):]
	padding := max(2-method.StringWidth(icon), 0)
	if !strings.HasPrefix(rest, " ") {
		padding++
	}
	return icon + strings.Repeat(" ", padding) + rest
}

// tabIconLabel adds color only; tabStrip calls this for the active tab.
// Inactive icons use the tab's foreground, including its underline.
func (m *model) tabIconLabel(label, style string, icons map[string]config.AgentIcon) string {
	num, title, ok := strings.Cut(label, " ")
	if !ok {
		return label
	}
	match := matchTabIcon(title, icons)
	color := match.Dark
	if m.bgDetected && !m.bgIsDark {
		color = match.Light
	}
	sgr, ok := bgSGR(color)
	if match.Icon == "" || !ok {
		return label
	}
	fg := strings.Replace(sgr, "\x1b[48;", "\x1b[38;", 1)
	return num + " " + fg + match.Icon + style + title[len(match.Icon):]
}

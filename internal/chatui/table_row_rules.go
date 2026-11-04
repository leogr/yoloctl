package chatui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// tableRowRule reuses one faint solid divider per aligned table. It is a
// physical decoration only: logical lines, source offsets and copied cells
// stay unchanged. Column dividers keep their vertical line with a gap on each
// side; a table that falls back to prose has no aligned divider.
func tableRowRule(wm ansi.Method, headerRule string, width int, theme spaceview.Theme) []run {
	if headerRule == "" || wm.StringWidth(headerRule) > width {
		return nil
	}
	return []run{deco(strings.ReplaceAll(headerRule, "─┼─", " │ "), spaceview.Style{FG: theme.Dim})}
}

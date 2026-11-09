package markdownview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// tableRowRule reuses one faint solid divider per aligned table. It is a
// physical decoration only: logical lines, source offsets and copied cells
// stay unchanged. Column dividers keep their vertical line with a gap on each
// side; a table that falls back to prose has no aligned divider.
func TableRowRule(wm ansi.Method, headerRule string, width int, theme spaceview.Theme) []Run {
	if headerRule == "" || wm.StringWidth(headerRule) > width {
		return nil
	}
	return []Run{Deco(strings.ReplaceAll(headerRule, "─┼─", " │ "), spaceview.Style{FG: theme.Dim})}
}

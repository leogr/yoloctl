package spaceview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// DialogFrame is shared by native participant and notice dialogs. Callers own
// placement, clipping, input and already-sanitized content; this owns theme and
// cell-exact border/background rendering only.
type DialogFrame struct {
	Theme  Theme
	Method ansi.Method
	Width  int
}

func (d DialogFrame) Header(title string) string {
	t := d.Theme
	frame := Style{FG: t.Frame, BG: t.Popup}
	title = d.Method.Truncate(title, max(d.Width-6, 1), "…")
	return StyleText("╭─ ", frame) + StyleText(title, Style{FG: t.Brand, BG: t.Popup, Bold: true}) + StyleText(" "+strings.Repeat("─", max(d.Width-d.Method.StringWidth(title)-5, 0))+"╮", frame)
}

func (d DialogFrame) Row(text string) string {
	t := d.Theme
	frame, bg := Style{FG: t.Frame, BG: t.Popup}, Style{BG: t.Popup}
	panel := strings.TrimSuffix(StyleText("", bg), "\x1b[0m")
	text = panel + strings.ReplaceAll(text, "\x1b[0m", "\x1b[0m"+panel)
	return StyleText("│ ", frame) + text + StyleText(strings.Repeat(" ", max(d.Width-4-d.Method.StringWidth(text), 0)), bg) + StyleText(" │", frame)
}

func (d DialogFrame) Footer(hint string) string {
	t := d.Theme
	frame := Style{FG: t.Frame, BG: t.Popup}
	text := d.Method.Truncate(" "+hint+" ", max(d.Width-2, 0), "")
	return StyleText("╰", frame) + StyleText(text, Style{FG: t.Dim, BG: t.Popup}) + StyleText(strings.Repeat("─", max(d.Width-2-d.Method.StringWidth(text), 0))+"╯", frame)
}

// DialogButton uses the participant dialog's primary/secondary faces. Its
// padding stays un-underlined on hover, and padding adds two cells to the rendered label width.
func DialogButton(t Theme, label string, primary, hovered, disabled bool) string {
	st := Style{FG: t.Text, BG: t.Raised}
	if primary {
		st = Style{FG: t.Bright, BG: t.Frame, Bold: true}
	}
	if disabled {
		st = Style{FG: t.Dim, BG: t.Raised}
	}
	face := st
	if hovered && !disabled {
		face.Underline, face.Bold = true, true
	}
	return StyleText(" ", st) + StyleText(label, face) + StyleText(" ", st)
}

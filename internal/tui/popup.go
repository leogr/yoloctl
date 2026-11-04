package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// popup renders plain text in a rounded, palette-colored frame. Callers own
// placement and input handling. Footer choices keep their space when body text
// overflows; clipping is marked inside the closing border.
type popup struct {
	Title  string
	Lines  []string
	Footer string
	// Selected is the footer text drawn highlighted (reverse video), such as
	// the default button.
	Selected string
	// Highlight is the 1-based body line drawn in reverse video, such as a
	// picker's current row; 0 for none.
	Highlight int
	// Dimmed marks body lines drawn grey, such as a picker's disabled rows.
	Dimmed []bool
}

// layout is where render puts the body for width×rows: its wrap width, the
// row of its first line, and how many body lines fit beside the footer as
// wrapped. Scrolling and hit-testing use it, so they match the screen.
func (p popup) layout(width, rows int) (inner, top, capacity int) {
	if width <= 0 || rows <= 0 {
		return 0, 0, 0
	}
	inner, capacity = width, rows
	if width >= 5 && rows >= 3 { // framed, as render decides
		inner, top, capacity = width-4, 1, rows-2
	}
	if p.Footer != "" {
		capacity -= min(len(strings.Split(ansi.Hardwrap(ansi.Strip(p.Footer), inner, true), "\n")), capacity)
	}
	return inner, top, capacity
}

// rowsFor is how many rows the popup needs at width to show body display
// rows beside its whole wrapped footer: the frame, the footer, the body and
// the blank row that separates them.
func (p popup) rowsFor(width, body int) int {
	framed := width >= 5
	inner := width
	if framed {
		inner -= 4
	}
	rows := body
	if p.Footer != "" {
		rows += len(strings.Split(ansi.Hardwrap(ansi.Strip(p.Footer), max(inner, 1), true), "\n"))
		if body > 0 {
			rows++ // the blank row above the footer
		}
	}
	if framed {
		rows += 2
	}
	return rows
}

func (p popup) render(width, rows int, bg string) []string {
	if width <= 0 || rows <= 0 {
		return nil
	}
	// On a tiny terminal, retain content without spending the entire area on
	// decoration. All other sizes reserve the complete frame before wrapping.
	framed := width >= 5 && rows >= 3
	inner, bodyRows := width, rows
	if framed {
		inner -= 4 // side borders and one space of inset on each side
		bodyRows -= 2
	}
	var body []string
	var dim []bool
	for i, line := range p.Lines {
		wrapped := strings.Split(ansi.Hardwrap(ansi.Strip(line), inner, true), "\n")
		body = append(body, wrapped...)
		for range wrapped {
			dim = append(dim, i < len(p.Dimmed) && p.Dimmed[i])
		}
	}
	var footer []string
	if p.Footer != "" {
		footer = strings.Split(ansi.Hardwrap(ansi.Strip(p.Footer), inner, true), "\n")
		if len(footer) > bodyRows {
			footer = footer[:bodyRows]
			footer[bodyRows-1] = ansi.Truncate(footer[bodyRows-1]+"…", inner, "…")
		}
	}
	bodyRows -= len(footer)
	if len(body) > bodyRows {
		body = body[:bodyRows]
		if bodyRows > 0 {
			body[bodyRows-1] = ansi.Truncate(body[bodyRows-1]+"…", inner, "…")
		}
	}
	if len(footer) > 0 && len(body) > 0 && len(body) < bodyRows {
		body = append(body, "") // separate actions when there is room
	}
	var lines []string
	if framed {
		title := strings.Join(strings.Fields(ansi.Strip(p.Title)), " ")
		label := ansi.Truncate(" "+title+" ", width-4, "…")
		lines = append(lines, paintRow(cDim+"╭─"+cOrange+cBold+label+cReset+cDim+
			strings.Repeat("─", width-3-ansi.StringWidth(label))+"╮"+cReset, bg))
	}
	for i, line := range append(body, footer...) {
		line = ansi.Truncate(line, inner, "…")
		padding := strings.Repeat(" ", max(inner-ansi.StringWidth(line), 0))
		if p.Highlight > 0 && i == p.Highlight-1 && i < len(body) {
			line = "\x1b[7m" + line + padding + "\x1b[27m"
			padding = ""
		}
		if i < len(body) && i < len(dim) && dim[i] {
			line = cDim + line + cReset
		}
		if i >= len(body) {
			if p.Selected != "" {
				line = strings.Replace(line, p.Selected, "\x1b[7m"+p.Selected+"\x1b[27m", 1)
			}
			line = cOrange + cBold + line + cReset
		}
		line += padding
		if framed {
			line = cDim + "│" + cReset + " " + line + " " + cDim + "│" + cReset
		}
		lines = append(lines, paintRow(line, bg))
	}
	if framed {
		lines = append(lines, paintRow(cDim+"╰"+strings.Repeat("─", width-2)+"╯"+cReset, bg))
	}
	return lines
}

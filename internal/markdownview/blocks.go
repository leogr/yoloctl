package markdownview

import "strings"

// Block state the parser carries from line to line besides an open fence
// (feat-markdown-tolerant-tables, plan section 3). Columns here are source
// columns: leading TABs expand to the next multiple of 4. They are kept apart
// from ListItem's offsets, which are bytes of the displayed line.

// indentCols is the column after line's leading spaces and TABs and the byte
// where its text starts.
func indentCols(line string) (col, at int) {
	for ; at < len(line) && (line[at] == ' ' || line[at] == '\t'); at++ {
		if line[at] == '\t' {
			col += 4 - col%4
		} else {
			col++
		}
	}
	return col, at
}

// listMarker reads a list item marker: '-', '*', '+' or 1 to 9 digits and
// '.' or ')', then at least one space, as today's items require. marker is
// its source column and content the column its text starts at: the marker's
// width plus the spaces after it when they are 1 to 4, and 1 when they are
// more (GFM 5.2).
func listMarker(line string) (marker, content int, ok bool) {
	marker, at := indentCols(line)
	rest := line[at:]
	width := 0
	switch {
	case rest != "" && strings.IndexByte("-*+", rest[0]) >= 0:
		width = 1
	default:
		digits := len(rest) - len(strings.TrimLeft(rest, "0123456789"))
		if digits == 0 || digits > 9 || digits == len(rest) || rest[digits] != '.' && rest[digits] != ')' {
			return 0, 0, false
		}
		width = digits + 1
	}
	spaces := len(rest[width:]) - len(strings.TrimLeft(rest[width:], " "))
	if spaces == 0 {
		return 0, 0, false
	}
	if spaces > 4 {
		spaces = 1
	}
	return marker, marker + width + spaces, true
}

// listContext is the stack of open list items, by source column.
type listContext struct{ open []listOpen }

type listOpen struct{ marker, content int }

// active reports whether a list is open.
func (c *listContext) active() bool { return len(c.open) > 0 }

// item opens an item at marker with its content column: it nests under an
// open item whose content column it reaches; otherwise the open items at or
// beyond its marker column are closed first.
func (c *listContext) item(marker, content int) {
	for n := len(c.open); n > 0; n = len(c.open) {
		top := c.open[n-1]
		if marker >= top.content || top.marker < marker {
			break
		}
		c.open = c.open[:n-1]
	}
	c.open = append(c.open, listOpen{marker, content})
}

// other continues the context over a blank line or a line indented to the
// outermost item's content column, and ends it at any other line.
func (c *listContext) other(line string) {
	if !c.active() || strings.TrimSpace(line) == "" {
		return
	}
	if col, _ := indentCols(line); col < c.open[0].content {
		c.open = c.open[:0]
	}
}

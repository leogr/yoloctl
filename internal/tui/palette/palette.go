// Package palette is the TUI's command line: a fixed,
// non-removable `yoloctl ` prompt and a single-line editor. Input whitespace
// becomes spaces and other terminal controls are discarded. The resulting line
// becomes subprocess argv through plain whitespace splitting, without shell
// interpretation or command expansion.
package palette

import (
	"strings"
	"unicode"
)

// Model is the palette's editing state. Zero value is ready.
type Model struct {
	buf    []rune
	cursor int     // rune index into buf
	hist   history // submitted lines, newest last
}

type history struct {
	lines []string
	pos   int // == len(lines) when not browsing
}

// Insert adds text at the cursor, keeping the editor on one display row.
func (m *Model) Insert(s string) {
	r := singleLine(s)
	if len(r) == 0 {
		return
	}
	m.buf = append(m.buf[:m.cursor], append(r, m.buf[m.cursor:]...)...)
	m.cursor += len(r)
	m.hist.pos = len(m.hist.lines)
}

// Backspace deletes the rune before the cursor.
func (m *Model) Backspace() {
	if m.cursor == 0 {
		return
	}
	m.buf = append(m.buf[:m.cursor-1], m.buf[m.cursor:]...)
	m.cursor--
}

// Delete removes the rune at the cursor (forward delete); the cursor
// stays put. No-op at end of line.
func (m *Model) Delete() {
	if m.cursor >= len(m.buf) {
		return
	}
	m.buf = append(m.buf[:m.cursor], m.buf[m.cursor+1:]...)
}

// Left / Right / Home / End move the cursor.
func (m *Model) Left() {
	if m.cursor > 0 {
		m.cursor--
	}
}
func (m *Model) Right() {
	if m.cursor < len(m.buf) {
		m.cursor++
	}
}
func (m *Model) Home() { m.cursor = 0 }
func (m *Model) End()  { m.cursor = len(m.buf) }

// HistPrev / HistNext browse submitted lines (newest-first walk),
// replacing the buffer. Editing resumes at line end.
func (m *Model) HistPrev() {
	if m.hist.pos == 0 || len(m.hist.lines) == 0 {
		return
	}
	m.hist.pos--
	m.set(m.hist.lines[m.hist.pos])
}

func (m *Model) HistNext() {
	if m.hist.pos >= len(m.hist.lines) {
		return
	}
	m.hist.pos++
	if m.hist.pos == len(m.hist.lines) {
		m.set("")
		return
	}
	m.set(m.hist.lines[m.hist.pos])
}

func (m *Model) set(s string) {
	m.buf = singleLine(s)
	m.cursor = len(m.buf)
}

// Pasted text and completion values can contain newlines or other controls.
// Normalize whitespace without joining argv words; keep printable Unicode,
// combining marks and joiners intact. This applies only to the palette editor,
// never to input forwarded to a running command or tab.
func singleLine(s string) []rune {
	return []rune(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, s))
}

// Submit returns the line as argv (whitespace-split, verbatim — no
// quoting, globbing, or expansion), records history, and clears the
// editor. nil for an empty line.
func (m *Model) Submit() []string {
	line := strings.TrimSpace(string(m.buf))
	m.set("")
	if line == "" {
		m.hist.pos = len(m.hist.lines)
		return nil
	}
	m.hist.lines = append(m.hist.lines, line)
	m.hist.pos = len(m.hist.lines)
	return strings.Fields(line)
}

// Draft is a line set aside with its cursor, to be put back whole.
type Draft struct {
	buf    []rune
	cursor int
}

// Empty reports whether the draft holds no text.
func (d Draft) Empty() bool { return len(d.buf) == 0 }

// Stash takes the line out of the editor, leaving it empty.
func (m *Model) Stash() Draft {
	d := Draft{buf: m.buf, cursor: m.cursor}
	m.buf, m.cursor = nil, 0
	m.hist.pos = len(m.hist.lines)
	return d
}

// Restore puts a stashed line back in place of the editor's line.
func (m *Model) Restore(d Draft) {
	m.buf, m.cursor = d.buf, d.cursor
	m.hist.pos = len(m.hist.lines)
}

// Keep records a stashed line in history without running it, so ↑
// recalls it when the editor holds other text.
func (m *Model) Keep(d Draft) {
	if line := strings.TrimSpace(string(d.buf)); line != "" {
		m.hist.lines = append(m.hist.lines, line)
	}
	m.hist.pos = len(m.hist.lines)
}

// Line returns the current buffer and cursor position for rendering.
func (m *Model) Line() (string, int) { return string(m.buf), m.cursor }

// CompletionInput splits the line for a completion request: the
// finished words, and the token under the cursor (empty when the line
// ends in whitespace). Completion applies at end-of-line only — ok is
// false when the cursor sits mid-line.
func (m *Model) CompletionInput() (words []string, last string, ok bool) {
	if m.cursor != len(m.buf) {
		return nil, "", false
	}
	line := string(m.buf)
	words = strings.Fields(line)
	if len(line) > 0 && !strings.HasSuffix(line, " ") && len(words) > 0 {
		last = words[len(words)-1]
		words = words[:len(words)-1]
	}
	return words, last, true
}

// ApplyCompletion replaces the token under the cursor with cand. A
// full single match should arrive with a trailing space so typing
// flows on.
func (m *Model) ApplyCompletion(cand string) {
	words, _, ok := m.CompletionInput()
	if !ok || cand == "" {
		return
	}
	m.set(strings.Join(append(words, cand), " "))
}

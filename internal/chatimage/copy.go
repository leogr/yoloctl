// Package chatimage holds the host side of native chat image previews that is
// independent of the terminal UI: the bounded ledger of preview image IDs and
// the mapping of a copied native selection back to attachment chips.
package chatimage

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Placeholder is the Kitty Unicode placeholder that marks image cells.
const Placeholder = '\U0010EEEE'

// GenericChip replaces selected image cells whose preview is no longer known.
// It asserts nothing about which attachment they showed.
const GenericChip = "[image attachment]"

// Ledger bounds: preview IDs remembered for copy, and the longest chip text.
const (
	MaxLedgerEntries = 256
	maxChipBytes     = 512
)

// Ledger remembers which attachment chip each preview image ID showed, so a
// copy of scrolled-back history can still name it. It is bounded: the oldest
// IDs are forgotten first, and their cells copy as GenericChip.
type Ledger struct {
	mu    sync.Mutex
	chips map[uint32]string
	order []uint32
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger { return &Ledger{chips: map[uint32]string{}} }

// Remember records the chip text for a 24-bit image ID.
func (l *Ledger) Remember(id uint32, chip string) {
	if l == nil || id == 0 || id > 0xffffff {
		return
	}
	chip = sanitizeChip(chip)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.chips[id]; !ok {
		l.order = append(l.order, id)
		for len(l.order) > MaxLedgerEntries {
			delete(l.chips, l.order[0])
			l.order = l.order[1:]
		}
	}
	l.chips[id] = chip
}

// Chip returns the chip text for id, or GenericChip when it is not known.
func (l *Ledger) Chip(id uint32) string {
	if l == nil {
		return GenericChip
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if chip, ok := l.chips[id]; ok {
		return chip
	}
	return GenericChip
}

// sanitizeChip keeps a chip to one bounded line of printable text.
func sanitizeChip(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) || r == Placeholder {
			r = ' '
		}
		if b.Len()+utf8.RuneLen(r) > maxChipBytes {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return GenericChip
	}
	return b.String()
}

// ErrUnexpectedControl means the styled selection contained something other
// than text, line breaks and SGR, so it is not mapped.
var ErrUnexpectedControl = errors.New("chatimage: unexpected control in styled selection")

// MapSelection turns a native styled selection (text, CRLF line breaks and SGR
// only) into clipboard text: ordinary text and line breaks are kept, and each
// maximal run of placeholder cells drawn with one image ID becomes that
// image's chip from the ledger (GenericChip when unknown or not a 24-bit ID).
// A line that holds only the same image as the line before it (the next rows
// of one preview) is dropped, so one image copies as one chip.
func MapSelection(styled string, ledger *Ledger) (string, error) {
	lines, err := tokenize(styled)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	prevImage := int64(-1) // image ID of the previous line if it held only that image
	wrote := false
	for _, line := range lines {
		if only := line.onlyImage(); only >= 0 && only == prevImage {
			continue
		}
		if wrote {
			out.WriteByte('\n')
		}
		wrote = true
		for _, it := range line {
			if it.image {
				if it.id == 0 {
					out.WriteString(GenericChip)
				} else {
					out.WriteString(ledger.Chip(it.id))
				}
			} else {
				out.WriteString(it.text)
			}
		}
		prevImage = line.onlyImage()
	}
	return out.String(), nil
}

// item is ordinary text or one run of placeholder cells with one image ID.
type item struct {
	text  string
	image bool
	id    uint32
}

type line []item

// onlyImage is the line's image ID when it holds exactly one image run and
// nothing else, else -1.
func (l line) onlyImage() int64 {
	if len(l) == 1 && l[0].image && l[0].id != 0 {
		return int64(l[0].id)
	}
	return -1
}

func tokenize(styled string) ([]line, error) {
	var lines []line
	var cur line
	var text strings.Builder
	var fg uint32
	flushText := func() {
		if text.Len() > 0 {
			cur = append(cur, item{text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(styled); {
		c := styled[i]
		switch {
		case c == 0x1b:
			n, next, ok := parseSGR(styled[i:], fg)
			if !ok {
				return nil, ErrUnexpectedControl
			}
			fg, i = next, i+n
			continue
		case c == '\r' && i+1 < len(styled) && styled[i+1] == '\n':
			i++
			fallthrough
		case c == '\n':
			flushText()
			lines = append(lines, cur)
			cur = nil
			i++
			continue
		case c < 0x20 && c != '\t' || c == 0x7f:
			return nil, ErrUnexpectedControl
		}
		r, size := utf8.DecodeRuneInString(styled[i:])
		if r != Placeholder {
			text.WriteString(styled[i : i+size])
			i += size
			continue
		}
		// The placeholder and its combining row/column/ID marks are one cell.
		j := i + size
		for j < len(styled) {
			m, msize := utf8.DecodeRuneInString(styled[j:])
			if !unicode.Is(unicode.Mn, m) {
				break
			}
			j += msize
		}
		flushText()
		// Contiguous cells with one ID are one run; so are contiguous cells
		// without an ID (no colour or a palette colour), which copy as one
		// generic chip without implying which attachment they showed.
		if n := len(cur); n == 0 || !cur[n-1].image || cur[n-1].id != fg {
			cur = append(cur, item{image: true, id: fg})
		}
		i = j
	}
	flushText()
	return append(lines, cur), nil
}

// parseSGR reads one CSI ... m sequence at the start of s and returns its
// length and the resulting 24-bit foreground. Any other escape is rejected.
func parseSGR(s string, fg uint32) (int, uint32, bool) {
	if len(s) < 3 || s[1] != '[' {
		return 0, fg, false
	}
	end := 2
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == ';' || s[end] == ':') {
		end++
	}
	if end >= len(s) || s[end] != 'm' {
		return 0, fg, false
	}
	body := s[2:end]
	if body == "" {
		return end + 1, 0, true
	}
	params := strings.FieldsFunc(body, func(r rune) bool { return r == ';' || r == ':' })
	if strings.Contains(body, "::") { // 38:2::r:g:b has an empty colour-space field
		params = strings.Split(strings.ReplaceAll(body, ";", ":"), ":")
	}
	num := func(i int) (int, bool) {
		if i >= len(params) {
			return 0, false
		}
		if params[i] == "" {
			return 0, true
		}
		n, err := strconv.Atoi(params[i])
		return n, err == nil
	}
	for i := 0; i < len(params); i++ {
		p, ok := num(i)
		if !ok {
			return 0, fg, false
		}
		switch {
		case p == 0 || p == 39:
			fg = 0
		case p == 38 || p == 48 || p == 58:
			kind, _ := num(i + 1)
			switch kind {
			case 2:
				off := i + 2
				if strings.Contains(body, "::") && off < len(params) && params[off] == "" {
					off++ // skip the empty colour-space id
				}
				r, ok1 := num(off)
				g, ok2 := num(off + 1)
				b, ok3 := num(off + 2)
				if !ok1 || !ok2 || !ok3 || r > 255 || g > 255 || b > 255 {
					return 0, fg, false
				}
				if p == 38 {
					fg = uint32(r)<<16 | uint32(g)<<8 | uint32(b)
				}
				i = off + 2
			case 5:
				if p == 38 {
					fg = 0 // palette colours are not image IDs
				}
				i += 2
			default:
				return 0, fg, false
			}
		case p >= 30 && p <= 37 || p >= 90 && p <= 97:
			fg = 0
		}
	}
	return end + 1, fg, true
}

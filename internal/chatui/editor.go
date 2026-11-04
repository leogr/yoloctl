package chatui

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const (
	// DefaultMessageBytes is the default logical draft byte budget.
	DefaultMessageBytes = 64 << 10
	// MaxMessageBytes bounds both configured drafts and received logical bodies.
	MaxMessageBytes = 1 << 20
	maxDraftLines   = 8192
	maxUndoBytes    = 4 << 20
	maxUndoEntries  = 64
)

type editState struct {
	text   string
	cursor int // UTF-8 byte offset at a cursor stop (see stops)
	// folds are collapsed pastes, sorted and disjoint. A new slice replaces
	// them on every change, so undo snapshots can share them.
	folds []fold
	// remainder marks a draft that is what a send left of it: it is always a
	// message, never a slash command. Snapshots keep it, so undo and redo
	// restore the protection with the text.
	remainder bool
}

// fold collapses one long paste: bytes [start, end) of the draft show as a
// single chip but stay in the text, so the payload, its limits, the byte
// count and the send are exactly those of the expanded draft. n numbers the
// chip within the draft.
//
// A fold with img is an image placeholder instead: its bytes are only the
// "[Image #n]" label, it never expands, and sending splits the draft around
// it (see attachments.go). Snapshots share the pointer, so undo restores the
// same payload; images and pastes are numbered separately.
type fold struct {
	start, end, n int
	img           *draftImage
}

// Pastes of at least foldLines lines or foldBytes bytes collapse.
const (
	foldLines = 6
	foldBytes = 1 << 10
)

type editor struct {
	editState
	limit    int
	revision uint64
	killed   string
	undo     []editState
	redo     []editState
	column   *int // preferred display column across successive vertical moves
}

// cleanInput preserves textual newlines, tabs, indentation and joiners. Other
// terminal controls cannot enter a draft through paste or key text.
func cleanInput(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("input is not valid UTF-8")
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s), nil
}

// clusters uses only byte boundaries; the measured widths are discarded, so
// editing boundaries remain independent of terminal width negotiation.
func clusters(s string) []int {
	positions := []int{0}
	for offset := 0; offset < len(s); {
		g, _ := ansi.FirstGraphemeCluster(s[offset:], ansi.WcWidth)
		offset += len(g)
		positions = append(positions, offset)
	}
	return positions
}

// stops are the cursor positions: grapheme boundaries outside folds and
// both ends of every fold, so a collapsed paste moves, deletes and wraps as
// one unit. A fold end stays a stop even when a combining mark typed after
// it joins the paste's last grapheme.
func (e *editState) stops() []int {
	positions := clusters(e.text)
	if len(e.folds) == 0 {
		return positions
	}
	out := make([]int, 0, len(positions)+2*len(e.folds))
	for _, p := range positions {
		if _, inside := e.foldAt(p); !inside {
			out = append(out, p)
		}
	}
	for _, f := range e.folds {
		out = append(out, f.start, f.end)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// foldAt returns the fold strictly containing offset p (start < p < end).
func (e *editState) foldAt(p int) (fold, bool) {
	for _, f := range e.folds {
		if f.start < p && p < f.end {
			return f, true
		}
	}
	return fold{}, false
}

// foldStarting returns the fold that begins at offset p.
func (e *editState) foldStarting(p int) (int, bool) {
	for i, f := range e.folds {
		if f.start == p {
			return i, true
		}
	}
	return 0, false
}

// widen grows [start, end) to whole folds and moves an insertion point
// inside a fold to its end, so no edit splits a collapsed paste.
func (e *editState) widen(start, end int) (int, int) {
	for _, f := range e.folds {
		switch {
		case start == end && f.start < start && start < f.end:
			start, end = f.end, f.end
		case start < end && start < f.end && f.start < end:
			start, end = min(start, f.start), max(end, f.end)
		}
	}
	return start, end
}

// shifted returns the folds after replacing [start, end) with inserted
// bytes: folds before stay, folds after move, covered folds are removed.
func shifted(folds []fold, start, end, inserted int) []fold {
	var out []fold
	delta := inserted - (end - start)
	for _, f := range folds {
		switch {
		case f.end <= start:
			out = append(out, f)
		case f.start >= end:
			f.start, f.end = f.start+delta, f.end+delta
			out = append(out, f)
		}
	}
	return out
}

func (e *editor) previous() int {
	previous := 0
	for _, p := range e.stops() {
		if p >= e.cursor {
			break
		}
		previous = p
	}
	return previous
}

func (e *editor) next() int {
	for _, p := range e.stops() {
		if p > e.cursor {
			return p
		}
	}
	return len(e.text)
}

func boundHistory(states []editState) []editState {
	bytes := 0
	for i := len(states) - 1; i >= 0; i-- {
		bytes += len(states[i].text)
		if bytes > maxUndoBytes || len(states)-i > maxUndoEntries {
			return append([]editState(nil), states[i+1:]...)
		}
	}
	return states
}

func (e *editor) replace(start, end int, text string) error {
	start, end = e.widen(start, end)
	for _, f := range e.folds {
		if f.img != nil && f.img.state == "uploading" && start < f.end && f.start < end {
			return errImageSending
		}
	}
	if len(e.text)-(end-start)+len(text) > e.limit {
		return errors.New("draft exceeds the message byte limit")
	}
	next := e.text[:start] + text + e.text[end:]
	if strings.Count(next, "\n") >= maxDraftLines {
		return errors.New("draft exceeds the 8192-line editor limit")
	}
	if next == e.text {
		return nil
	}
	e.undo = boundHistory(append(e.undo, e.editState))
	e.redo = nil
	e.text, e.cursor, e.folds = next, start+len(text), shifted(e.folds, start, end, len(text))
	if next == "" {
		e.remainder = false
	}
	// Inserting a combining mark or joiner can merge neighboring clusters.
	for _, p := range e.stops() {
		if p >= e.cursor {
			e.cursor = p
			break
		}
	}
	e.revision++
	e.column = nil
	return nil
}

// remove deletes [start, end) for a change the user did not type, such as
// a sent part of the draft, keeping the cursor on the same text.
func (e *editor) remove(start, end int) error {
	cursor := e.cursor
	start, end = e.widen(start, end)
	if err := e.replace(start, end, ""); err != nil {
		return err
	}
	switch {
	case cursor >= end:
		e.cursor = cursor - (end - start)
	case cursor > start:
		e.cursor = start
	default:
		e.cursor = cursor
	}
	return nil
}

func (e *editor) insert(text string) error {
	text, err := cleanInput(text)
	if err != nil {
		return err
	}
	return e.replace(e.cursor, e.cursor, text)
}

// lineStart and lineEnd find the draft line around the cursor. A newline
// inside a collapsed paste does not end a line: the chip shows inline.
func (e *editor) lineStart() int {
	p := e.cursor
	for {
		i := strings.LastIndex(e.text[:p], "\n")
		if i < 0 {
			return 0
		}
		if f, ok := e.foldCovering(i); ok {
			p = f.start
			continue
		}
		return i + 1
	}
}

func (e *editor) lineEnd() int {
	p := e.cursor
	for {
		n := strings.IndexByte(e.text[p:], '\n')
		if n < 0 {
			return len(e.text)
		}
		if f, ok := e.foldCovering(p + n); ok {
			p = f.end
			continue
		}
		return p + n
	}
}

// foldCovering returns the fold whose bytes include offset p.
func (e *editState) foldCovering(p int) (fold, bool) {
	for _, f := range e.folds {
		if f.start <= p && p < f.end {
			return f, true
		}
	}
	return fold{}, false
}

// word returns the whitespace-delimited word around the cursor; it never
// reaches into a collapsed paste, whose text is not being typed.
func (e *editor) word() (int, int) {
	start, end := wordAt(e.text, e.cursor)
	for _, f := range e.folds {
		if f.end <= e.cursor && f.end > start {
			start = f.end
		}
		if f.start >= e.cursor && f.start < end {
			end = f.start
		}
	}
	return start, end
}

// kill cuts [start, end) for Ctrl+Y. An image placeholder in the range is
// removed but never yanked back as its label text.
func (e *editor) kill(start, end int) error {
	start, end = e.widen(start, end)
	if start == end {
		return nil
	}
	var b strings.Builder
	at := start
	for _, f := range e.folds {
		if f.img != nil && f.start >= start && f.end <= end {
			b.WriteString(e.text[at:f.start])
			at = f.end
		}
	}
	b.WriteString(e.text[at:end])
	if err := e.replace(start, end, ""); err != nil { // deletion cannot exceed the limit
		return err
	}
	e.killed = b.String()
	return nil
}

func (e *editor) wordStart() int {
	stops := e.stops()
	i := len(stops) - 1
	for i > 0 && stops[i] > e.cursor {
		i--
	}
	// space reports whether the unit before stop j is whitespace; a
	// collapsed paste counts as part of a word.
	space := func(j int) bool {
		if _, ok := e.foldStarting(stops[j-1]); ok {
			return false
		}
		r, _ := utf8.DecodeRuneInString(e.text[stops[j-1]:])
		return unicode.IsSpace(r)
	}
	for i > 0 && space(i) {
		i--
	}
	for i > 0 && !space(i) {
		i--
	}
	return stops[i]
}

func (e *editor) restore(redo bool) error {
	from, to := &e.undo, &e.redo
	if redo {
		from, to = to, from
	}
	if len(*from) == 0 {
		return nil
	}
	target := (*from)[len(*from)-1]
	for _, f := range e.folds {
		if f.img != nil && f.img.state == "uploading" && !slices.ContainsFunc(target.folds, func(g fold) bool { return g.img == f.img }) {
			return errImageSending
		}
	}
	*to = boundHistory(append(*to, e.editState))
	text := e.text
	e.editState = (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	// Undoing an expansion changes only the folds: like expanding, it is
	// not an edit, so a pending send still clears on confirmation.
	if e.text != text {
		e.revision++
	}
	e.column = nil
	return nil
}

// vertical moves by display rows, sharing layout with the composer renderer.
func (e *editor) vertical(wm ansi.Method, delta, width int) {
	rows, row, col := draftLayout(wm, e.text, e.folds, e.cursor, width)
	if e.column == nil {
		e.column = &col
	} else {
		col = *e.column
	}
	target := row + delta
	if target < 0 || target >= len(rows) {
		return
	}
	e.cursor = rows[target].stopAt(col)
}

// expand removes the fold at or next to the cursor, showing its text in
// place. The text, payload and revision are unchanged, so a pending send
// still clears on confirmation; undo collapses the paste again.
func (e *editor) expand() bool {
	for i, f := range e.folds {
		if f.img == nil && f.start <= e.cursor && e.cursor <= f.end {
			return e.expandFold(i)
		}
	}
	return false
}

func (e *editor) expandFold(i int) bool {
	if i < 0 || i >= len(e.folds) || e.folds[i].img != nil {
		return false
	}
	e.undo = boundHistory(append(e.undo, e.editState))
	e.redo = nil
	e.folds = slices.Delete(slices.Clone(e.folds), i, i+1)
	e.column = nil
	return true
}

// foldPaste collapses the text just pasted at [start, e.cursor) when it is
// long enough. The chip number is one more than any in the draft.
func (e *editor) foldPaste(start int) {
	// A paste that begins with a combining mark or joiner merges into the
	// grapheme before it; the chip starts at the next grapheme boundary so
	// that merged grapheme stays visible as text.
	for _, p := range clusters(e.text) {
		if p >= start {
			start = p
			break
		}
	}
	if start >= e.cursor {
		return
	}
	body := e.text[start:e.cursor]
	if len(body) < foldBytes && strings.Count(body, "\n")+1 < foldLines {
		return
	}
	n := 1
	for _, f := range e.folds {
		if f.img == nil {
			n = max(n, f.n+1)
		}
	}
	folds := append(slices.Clone(e.folds), fold{start: start, end: e.cursor, n: n})
	slices.SortFunc(folds, func(a, b fold) int { return a.start - b.start })
	e.folds = folds
}

var errImageSending = errors.New("the image is being sent; wait for the result")

// insertImage puts img's placeholder at the cursor as one unit, numbered one
// more than any image in the draft. Typing around it keeps their order.
func (e *editor) insertImage(img *draftImage) error {
	n := 1
	for _, f := range e.folds {
		if f.img != nil {
			n = max(n, f.n+1)
		}
	}
	label := imageLabel(n)
	start := e.cursor
	if err := e.replace(start, start, label); err != nil {
		return err
	}
	folds := append(slices.Clone(e.folds), fold{start: start, end: start + len(label), n: n, img: img})
	slices.SortFunc(folds, func(a, b fold) int { return a.start - b.start })
	e.folds = folds
	return nil
}

// imageFold finds img's placeholder in the current draft.
func (e *editState) imageFold(img *draftImage) (fold, bool) {
	for _, f := range e.folds {
		if f.img == img {
			return f, true
		}
	}
	return fold{}, false
}

// images lists the current draft's images in draft order.
func (e *editState) images() []*draftImage {
	var out []*draftImage
	for _, f := range e.folds {
		if f.img != nil {
			out = append(out, f.img)
		}
	}
	return out
}

// held lists every image the draft or its undo history refers to.
func (e *editor) held() []*draftImage {
	out := e.images()
	for _, states := range [][]editState{e.undo, e.redo} {
		for _, s := range states {
			for _, f := range s.folds {
				if f.img != nil && !slices.Contains(out, f.img) {
					out = append(out, f.img)
				}
			}
		}
	}
	return out
}

// commit makes the current draft the start of its undo history: whatever a
// send published can never be undone back into the draft and sent again.
func (e *editor) commit() { e.undo, e.redo = nil, nil }

// forget drops the undo and redo states that refer to img, and every state
// reachable only through them, so no undo can bring back an image that was
// sent, refused or spent.
func (e *editor) forget(img *draftImage) {
	for _, h := range []*[]editState{&e.undo, &e.redo} {
		for i := len(*h) - 1; i >= 0; i-- {
			if _, ok := (*h)[i].imageFold(img); ok {
				*h = slices.Clone((*h)[i+1:])
				break
			}
		}
	}
}

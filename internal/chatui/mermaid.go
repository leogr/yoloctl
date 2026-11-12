package chatui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/mermaid"
	"github.com/leogr/yoloctl/internal/mermaid/admit"
)

// DiagramRenderer draws Mermaid fences (mermaid.Client): Lookup answers from
// its cache without waiting, Render runs one diagram off the UI goroutine.
type DiagramRenderer interface {
	Render(ctx context.Context, source string, width int, method ansi.Method) mermaid.Result
	Lookup(source string, width int, method ansi.Method) (mermaid.Result, bool)
}

// maxScreenDiagrams is the fence occurrences one screen draws (leogr D4).
const maxScreenDiagrams = 20

const (
	diagramTooMany = "mermaid: not drawn (too many diagrams)"
	diagramPrefix  = "mermaid: not drawn ("
)

// fenceRef is one closed mermaid fence of a message: its order among them.
type fenceRef struct {
	key string
	occ int
}

// viewport identifies what a screen shows for diagram eligibility: a new
// one recomputes it; within one, eligibility only grows, so drawings that
// change row heights cannot make it oscillate.
type viewport struct {
	width, height int
	follow        bool
	moves         uint64 // user scrolls and message selections (diagramState.moves)
	messages      int    // while following: new messages make a new screen
	filter        *messageFilter
}

// diagramState is a channel view's diagram eligibility and request state.
type diagramState struct {
	// eligible are the occurrences, at most maxScreenDiagrams, counted on
	// the visible rows top to bottom, that may show a drawing; tooMany are
	// the visible ones past them, shown as source with a note.
	eligible, tooMany map[fenceRef]bool
	// transient are this viewport's busy/unavailable/internal outcomes by
	// source and width: shown, not requested again until the next one.
	transient map[diagramKey]mermaid.Result
	at        viewport
	epoch     uint64 // changes with at; a transient result of another is not kept
	inflight  bool
	moves     uint64 // counts the user's scrolls; layout changes never do
}

type diagramKey struct {
	source string
	width  int
}

// diagramResultMsg is one rendering's outcome; the client cached it when it
// is cacheable.
type diagramResultMsg struct {
	channel string
	epoch   uint64
	key     diagramKey
	result  mermaid.Result
}

// diagramSettleMsg runs settleDiagrams again after it changed bodies.
type diagramSettleMsg struct{}

// diagramFor is the outcome a message's body shows for occurrence occ of a
// closed mermaid fence at body width: an admission refusal, a note past the
// screen's 20, a drawing or reason when eligible and known, else today's
// code card (and the occurrence is pending when eligible).
func (m *Model) diagramFor(ref fenceRef, source string, width int) (markdownview.Diagram, bool, bool, int8) {
	if d := admit.Check(source); !d.Admitted() {
		return markdownview.Diagram{Note: diagramPrefix + d.Reason + ")"}, true, false, fenceRefused
	}
	s := &m.diagrams
	class := s.class(ref)
	switch class {
	case fenceTooMany:
		return markdownview.Diagram{Note: diagramTooMany}, true, false, class
	case fencePlain:
		return markdownview.Diagram{}, false, false, class
	}
	key := diagramKey{source, markdownview.DiagramWidth(width)}
	r, ok := s.transient[key]
	if !ok {
		r, ok = m.opts.Mermaid.Lookup(source, key.width, m.widthMethod)
	}
	switch {
	case !ok:
		return markdownview.Diagram{}, false, true, class
	case r.Status == mermaid.OK:
		return markdownview.Diagram{Lines: r.Lines}, true, false, class
	default:
		return markdownview.Diagram{Note: diagramPrefix + r.Note() + ")"}, true, false, class
	}
}

// parseDiagrams parses a message body with its diagram outcomes, recording
// each closed mermaid fence's source, outcome and pending state in body.
func (m *Model) parseDiagrams(key, text string, width int, body *cachedBody) []displayLine {
	body.fences, body.outcomes, body.pending, body.class, body.transient = nil, nil, nil, nil, nil
	body.drawWidth = width
	lookup := func(source string) (markdownview.Diagram, bool) {
		ref := fenceRef{key, len(body.fences)}
		d, ok, pending, class := m.diagramFor(ref, source, width)
		body.class = append(body.class, class)
		body.fences = append(body.fences, source)
		body.outcomes = append(body.outcomes, d)
		body.pending = append(body.pending, pending)
		body.transient = append(body.transient, class == fenceEligible && !pending && d.Lines == nil && m.diagrams.transient[diagramKey{source, markdownview.DiagramWidth(width)}].Status != "")
		return d, ok
	}
	lines := markdownview.Parse(text, markdownview.ParseOptions{Method: m.widthMethod, Theme: m.theme, Decorate: chatDecorator(m.self), Diagrams: lookup}).Lines
	body.fenceAt = fenceLines(lines, len(body.fences))
	return lines
}

// fenceLines maps each display line of a closed mermaid fence (opener,
// rows, closer and its note) to the fence's occurrence, -1 elsewhere.
func fenceLines(lines []displayLine, fences int) []int {
	at := make([]int, len(lines))
	for i := range at {
		at[i] = -1
	}
	occ, open := 0, -1
	for i := range lines {
		switch {
		case lines[i].Fence == markdownview.FenceOpen:
			open = -1
			if markdownview.IsMermaid(lines[i].Text) && occ < fences {
				open = i
			}
		case lines[i].Fence == markdownview.FenceClose && open >= 0:
			for k := open; k <= i; k++ {
				at[k] = occ
			}
			if i+1 < len(lines) && lines[i+1].Decor && lines[i+1].Fence == 0 && len(lines[i+1].Pieces) == 1 && lines[i+1].Pieces[0].Style.Italic {
				at[i+1] = occ // its note row
			}
			occ, open = occ+1, -1
		}
	}
	return at
}

// diagramLookup replays a body's outcomes in fence order, so another parse
// of the same text (Quote's source map) shows the same lines.
func diagramLookup(body cachedBody) func(string) (markdownview.Diagram, bool) {
	next := 0
	return func(string) (markdownview.Diagram, bool) {
		if next >= len(body.outcomes) {
			return markdownview.Diagram{}, false
		}
		d := body.outcomes[next]
		next++
		return d, d.Lines != nil || d.Note != ""
	}
}

// diagramBySource shows a fragment's fences as the body drew them: a quoted
// fence renders to the drawing its message shows.
func diagramBySource(body cachedBody) func(string) (markdownview.Diagram, bool) {
	return func(source string) (markdownview.Diagram, bool) {
		for i, s := range body.fences {
			if s == source && body.outcomes[i].Lines != nil {
				return body.outcomes[i], true
			}
		}
		return markdownview.Diagram{}, false
	}
}

// fence classes: how a body showed an occurrence when it was built.
const (
	fencePlain    int8 = iota // not counted on the screen: today's code card
	fenceEligible             // within the screen's 20: drawing, reason or pending
	fenceTooMany              // visible past the 20
	fenceRefused              // not admitted: its reason, whatever the screen
)

func (s *diagramState) class(ref fenceRef) int8 {
	switch {
	case s.eligible[ref]:
		return fenceEligible
	case s.tooMany[ref]:
		return fenceTooMany
	}
	return fencePlain
}

// settleDiagrams runs after every update while chat draws diagrams. From one
// layout of the current view it counts the admitted mermaid fences on the
// visible rows. A new viewport recomputes eligibility from that layout;
// within one it only grows, so drawings that change row heights settle.
// Bodies whose fence classes changed, or whose pending drawing is now known,
// are rebuilt (and settle runs again); otherwise the next missing eligible
// drawing on the screen is requested, one at a time.
func (m *Model) settleDiagrams() tea.Cmd {
	if m.opts.Mermaid == nil || m.source || m.layout.transcriptWidth == 0 || m.lastBodyHeight == 0 {
		return nil
	}
	tw, height := m.layout.transcriptWidth, m.lastBodyHeight
	rows := m.buildTranscript(tw)
	m.place(rows, height)
	s := &m.diagrams
	at := viewport{width: tw, height: height, follow: m.follow, moves: s.moves, filter: m.filter}
	if m.follow {
		at.messages = len(m.messages)
	}
	if s.eligible == nil || at != s.at {
		s.at, s.epoch = at, s.epoch+1
		s.transient = map[diagramKey]mermaid.Result{}
		s.eligible, s.tooMany = nil, nil
	}
	s.eligible, s.tooMany = m.visibleFences(rows[min(m.scroll, len(rows)):min(m.scroll+height, len(rows))], s.eligible, s.tooMany)
	if m.rebuildDiagramBodies() {
		return func() tea.Msg { return diagramSettleMsg{} }
	}
	if s.inflight {
		return nil
	}
	return m.requestDiagram(rows[min(m.scroll, len(rows)):min(m.scroll+height, len(rows))])
}

// visibleFences counts the admitted fence occurrences whose code or drawing
// rows are among rows (not only a frame or note row), top to bottom, once each: while fewer than maxScreenDiagrams are eligible
// the next one is, the others are too many. Both sets keep what they had.
func (m *Model) visibleFences(rows []transcriptRow, eligible, tooMany map[fenceRef]bool) (map[fenceRef]bool, map[fenceRef]bool) {
	next, over := map[fenceRef]bool{}, map[fenceRef]bool{}
	for ref := range eligible {
		next[ref] = true
	}
	for ref := range tooMany {
		over[ref] = true
	}
	for _, row := range rows {
		ref, ok := m.fenceOf(row)
		if !ok || row.kind != rowBody || next[ref] || over[ref] {
			continue // only a fence's code or drawing rows put it on the screen
		}
		if m.bodyCache[row.key].class[ref.occ] == fenceRefused {
			continue
		}
		if len(next) < maxScreenDiagrams {
			next[ref] = true
		} else {
			over[ref] = true
		}
	}
	return next, over
}

// fenceOf is the mermaid fence occurrence a transcript row shows, if any.
func (m *Model) fenceOf(row transcriptRow) (fenceRef, bool) {
	if row.kind != rowBody && row.kind != rowDecor || row.line < 0 {
		return fenceRef{}, false
	}
	body, ok := m.bodyCache[row.key]
	if !ok || row.line >= len(body.fenceAt) || body.fenceAt[row.line] < 0 {
		return fenceRef{}, false
	}
	return fenceRef{row.key, body.fenceAt[row.line]}, true
}

// rebuildDiagramBodies drops the bodies built for other fence classes than
// the current ones, or waiting for a drawing that is now known.
func (m *Model) rebuildDiagramBodies() bool {
	s := &m.diagrams
	changed := false
	for key, body := range m.bodyCache {
		if body.stale {
			continue // rebuilt at the next layout
		}
		for occ := range body.fences {
			ref := fenceRef{key, occ}
			stale := body.class[occ] != fenceRefused && body.class[occ] != s.class(ref)
			k := diagramKey{body.fences[occ], markdownview.DiagramWidth(body.drawWidth)}
			_, transient := s.transient[k]
			switch {
			case stale:
			case body.pending[occ]:
				_, known := m.opts.Mermaid.Lookup(k.source, k.width, m.widthMethod)
				stale = transient || known
			case body.transient[occ]:
				stale = !transient // its answer belongs to an earlier screen
			}
			if stale {
				if !changed {
					m.holdReadingPosition()
				}
				m.dropBody(key)
				changed = true
				break
			}
		}
	}
	return changed
}

// holdReadingPosition anchors a paused view to its top row before bodies
// change height, so drawings above it do not move what is read.
func (m *Model) holdReadingPosition() {
	rows := m.built.rows
	if !m.follow && m.anchor == nil && m.scroll < len(rows) {
		row := rows[m.scroll]
		m.anchor = &viewAnchor{key: row.key, line: row.line, offset: row.offset}
	}
}

// dropBody marks a message's body for rebuilding at the next layout, which
// carries a reading position on it over (carryLine). A highlight in it clears.
func (m *Model) dropBody(key string) {
	body, ok := m.bodyCache[key]
	if !ok {
		return
	}
	body.stale = true
	m.bodyCache[key] = body
	m.touch()
	if m.sel != nil && (m.sel.anchor.key == key || m.sel.focus.key == key) {
		m.sel = nil
	}
}

// carryLine maps a display line of a body to the rebuilt body of the same
// text, whose mermaid fences may show other rows: a line outside the fences
// keeps its place among those lines (and its offset); a fence's opener,
// closer or note stays that line, and a code or drawing row stays the
// same row of the fence's content while it has as many.
func carryLine(old, next cachedBody, line, offset int) (int, int) {
	if line < 0 || line >= len(old.fenceAt) {
		return line, offset
	}
	if occ := old.fenceAt[line]; occ >= 0 {
		from, to := fenceSpan(old.fenceAt, occ)
		newFrom, newTo := fenceSpan(next.fenceAt, occ)
		if newFrom < 0 {
			return line, 0
		}
		oldContent, newContent := contentSpan(old.lines, from, to), contentSpan(next.lines, newFrom, newTo)
		switch {
		case line == from:
			return newFrom, offset // the opener
		case line > from+oldContent:
			return min(newTo, newFrom+newContent+(line-from-oldContent)), offset // closer, note
		}
		return newFrom + min(line-from, max(newContent, 1)), 0
	}
	k := 0
	for _, o := range old.fenceAt[:line] {
		if o < 0 {
			k++
		}
	}
	for i, o := range next.fenceAt {
		if o >= 0 {
			continue
		}
		if k == 0 {
			return i, offset
		}
		k--
	}
	return line, offset
}

// requestDiagram asks the renderer for the first pending eligible
// occurrence on the visible rows.
func (m *Model) requestDiagram(rows []transcriptRow) tea.Cmd {
	for _, row := range rows {
		ref, ok := m.fenceOf(row)
		if !ok {
			continue
		}
		body := m.bodyCache[row.key]
		if !body.pending[ref.occ] {
			continue
		}
		s := &m.diagrams
		s.inflight = true
		key := diagramKey{body.fences[ref.occ], markdownview.DiagramWidth(body.drawWidth)}
		renderer, method, channel, epoch := m.opts.Mermaid, m.widthMethod, m.name, s.epoch
		return func() tea.Msg {
			return diagramResultMsg{channel: channel, epoch: epoch, key: key, result: renderer.Render(context.Background(), key.source, key.width, method)}
		}
	}
	return nil
}

// diagramResult records one answer (the renderer cached a cacheable one);
// settleDiagrams then rebuilds the bodies waiting for it and asks for the
// next. A transient answer is shown until the viewport changes.
func (m *Model) diagramResult(msg diagramResultMsg) {
	m.within(msg.channel, func() {
		s := &m.diagrams
		s.inflight = false
		if msg.epoch == s.epoch && msg.result.Transient() {
			s.transient[msg.key] = msg.result
		}
	})
}

// fenceSpan is the first and last display line of occurrence occ.
func fenceSpan(at []int, occ int) (int, int) {
	from, to := -1, -1
	for i, o := range at {
		if o == occ {
			if from < 0 {
				from = i
			}
			to = i
		}
	}
	return from, to
}

// contentSpan is the number of code or drawing lines of the fence whose
// opener is lines[from] and whose lines end at to.
func contentSpan(lines []displayLine, from, to int) int {
	n := 0
	for i := from + 1; i <= to && i < len(lines); i++ {
		if lines[i].Code {
			n++
		}
	}
	return n
}

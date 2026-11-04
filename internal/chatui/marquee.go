package chatui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// Hovering a participant whose card's second row is clipped scrolls that
// row, one grapheme at a time, so all of it can be read. Only that drawn row
// moves: fitting rows, empty rows, compact one-row lists and hidden panels
// never start a tick, and the row's width, glyph, name and dot stay put.
//
// One tick chain serves the hovered row. Its generation binds every tick to
// the channel, participant, text and width it started for; settleMarquee
// resets it after any update that changes them, such as a hover elsewhere, a
// press, a resize, a modal, a channel switch or a new label, so a tick of an
// earlier chain neither moves nor schedules anything. Terminals report no
// pointer leave: a row keeps scrolling while the last reported pointer stays
// on it. With NoMotion nothing moves and the footer hint shows the full row.

const (
	// marqueeHold precedes the first step, and each return to the start.
	marqueeHold = 600 * time.Millisecond
	marqueeStep = 120 * time.Millisecond
	// marqueeEnd keeps the end of the text shown before the return.
	marqueeEnd = time.Second
	// marqueeMin is the fewest cells the moving text needs to stay legible;
	// a narrower row keeps static clipping.
	marqueeMin = 6
)

// marqueeTarget is a clipped second row: the channel and participant it
// belongs to, its text, and the cells its text is drawn in.
type marqueeTarget struct {
	channel, key string
	text         string
	width        int
}

// marquee is the scrolling row. A target is set exactly while its tick chain
// runs; offset counts the graphemes scrolled out.
type marquee struct {
	target marqueeTarget
	gen    uint64
	offset int
}

type marqueeTickMsg struct{ gen uint64 }

// marqueeAfter schedules a marquee tick; tests step the chain through it
// without waiting.
var marqueeAfter = tea.Tick

func marqueeTick(gen uint64, d time.Duration) tea.Cmd {
	return marqueeAfter(d, func(time.Time) tea.Msg { return marqueeTickMsg{gen} })
}

// roleLine is the text of a participant card's second row: a box's model
// and effort, then the away reason, each left out when unknown. A trusted
// human is the one exception and reads "human". Nothing uncertain stands in
// for a missing value, so the row may be empty.
func (m *Model) roleLine(w who, presence spacetelemetry.Presence) string {
	var parts []string
	if w.trusted && w.human {
		parts = append(parts, "human")
	} else if runtime := m.runtimeSummary(w, time.Now()); runtime != "" {
		parts = append(parts, runtime)
	}
	if label := presenceLabel(presence); label != "" {
		// Keep the model visible before a long away reason is truncated.
		parts = append(parts, label)
	}
	return strings.Join(parts, " · ")
}

// hoveredLabel is the clipped second row of the participant under the
// pointer, as the last frame drew its card.
func (m *Model) hoveredLabel() (marqueeTarget, bool) {
	h := m.activeHover()
	l := m.layout
	if h.area != hitPanel || l.cardWidth <= 0 || h.item < 0 || h.item >= len(l.panelMembers) {
		return marqueeTarget{}, false
	}
	mem := l.panelMembers[h.item]
	if memberKey(mem) != h.id {
		return marqueeTarget{}, false
	}
	presence, _ := m.memberPresence(mem)
	text := m.roleLine(m.identify(mem.Nick, mem.Account), presence)
	cells := roleCells(l.cardWidth)
	if m.widthMethod.StringWidth(text) <= cells {
		return marqueeTarget{}, false
	}
	return marqueeTarget{channel: m.name, key: h.id, text: text, width: cells}, true
}

// roleCells is the width of a card's second-row text in a panel this wide.
func roleCells(panel int) int { return max(panel-8, 1) }

// marqueeSteps is how many graphemes the row scrolls until its end shows,
// or 0 when the row is too narrow to scroll legibly.
func marqueeSteps(wm ansi.Method, t marqueeTarget) int {
	room := t.width - 1 // a leading ellipsis
	if room < marqueeMin {
		return 0
	}
	rest := t.text
	for k := 1; rest != ""; k++ {
		g, _ := ansi.FirstGraphemeCluster(rest, wm)
		if g == "" {
			break
		}
		rest = rest[len(g):]
		if wm.StringWidth(rest) <= room {
			return k
		}
	}
	return 0
}

// marqueeWindow is the row scrolled offset graphemes: an ellipsis marks
// text cut on either side, and no grapheme is split.
func marqueeWindow(wm ansi.Method, t marqueeTarget, offset int) string {
	rest := t.text
	for range offset {
		g, _ := ansi.FirstGraphemeCluster(rest, wm)
		if g == "" {
			break
		}
		rest = rest[len(g):]
	}
	// Truncation can measure a cluster, such as an emoji with a variation
	// selector, narrower than the whole row does; the row decides.
	for room := t.width - 1; room > 0; room-- {
		if row := "…" + truncate(wm, rest, room, "…"); wm.StringWidth(row) <= t.width {
			return row
		}
	}
	return "…"
}

// roleText fits a card's second row to cells: scrolled while the marquee is
// past its start on exactly this row, clipped with an ellipsis otherwise.
func (m *Model) roleText(mem Member, text string, cells int) string {
	q := &m.marquee
	t := marqueeTarget{channel: m.name, key: memberKey(mem), text: text, width: cells}
	if q.offset == 0 || q.target != t || !m.panelHovered(mem) {
		return truncate(m.widthMethod, text, cells, "…")
	}
	return marqueeWindow(m.widthMethod, t, q.offset)
}

// settleMarquee runs after every update. A newly hovered clipped row starts
// a chain with a hold; a chain whose row is no longer hovered, or changed,
// stops and returns to the start.
func (m *Model) settleMarquee() tea.Cmd {
	t, ok := m.hoveredLabel()
	ok = ok && !m.opts.NoMotion && marqueeSteps(m.widthMethod, t) > 0
	q := &m.marquee
	if ok && q.target == t || !ok && q.target == (marqueeTarget{}) {
		return nil
	}
	q.gen++
	q.offset = 0
	q.target = marqueeTarget{}
	if !ok {
		return nil
	}
	q.target = t
	return marqueeTick(q.gen, marqueeHold)
}

// marqueeAdvance moves the hovered row one step, holds at its end, then
// returns to the start. A tick of another chain, or for a row that changed,
// does nothing: settleMarquee resets that chain after this update.
func (m *Model) marqueeAdvance(msg marqueeTickMsg) tea.Cmd {
	q := &m.marquee
	if msg.gen != q.gen || q.target == (marqueeTarget{}) {
		return nil
	}
	if t, ok := m.hoveredLabel(); !ok || t != q.target {
		return nil
	}
	steps := marqueeSteps(m.widthMethod, q.target)
	switch {
	case steps == 0:
		q.target, q.offset = marqueeTarget{}, 0
		return nil
	case q.offset >= steps:
		q.offset = 0
		return marqueeTick(q.gen, marqueeHold)
	}
	q.offset++
	if q.offset == steps {
		return marqueeTick(q.gen, marqueeEnd)
	}
	return marqueeTick(q.gen, marqueeStep)
}

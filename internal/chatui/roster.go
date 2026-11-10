package chatui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// sortedMembers orders participants as IRC clients do: channel operators,
// then voiced members, then everyone else, alphabetically (case-folded)
// within each group. Ties keep a stable account order.
func sortedMembers(members []Member) []Member {
	out := slices.Clone(members)
	rank := func(m Member) int {
		switch {
		case m.Op:
			return 0
		case m.Voice:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(out, func(a, b Member) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		if c := strings.Compare(strings.ToLower(a.Nick), strings.ToLower(b.Nick)); c != 0 {
			return c
		}
		return strings.Compare(a.Account, b.Account)
	})
	return out
}

// panelWidth is the side panel width at a terminal width, or 0 when the
// transcript would keep fewer than 48 columns: the panel then overlays.
func panelWidth(width int) int {
	switch {
	case width >= 100:
		return 28
	case width >= 80:
		return 22
	}
	return 0
}

// panelRow is one side panel row; member indexes sortedMembers, and a
// positive channel is 1 + the index of a channel in m.order.
type panelRow struct {
	runs    []run
	member  int
	channel int
}

// channelRail lists the open channels at the top of a side panel,
// with unread counts and a marker on the visible one. One channel needs no
// rail; the top band still names it.
func (m *Model) channelRail(width int) []panelRow {
	t := m.theme
	if len(m.order) < 2 {
		return nil
	}
	count := fmt.Sprint(len(m.order))
	rows := []panelRow{{member: -1, runs: []run{
		deco(" CHANNELS", spaceview.Style{FG: t.Dim, Bold: true}),
		deco(strings.Repeat(" ", max(width-9-len(count)-1, 1)), spaceview.Style{}),
		deco(count, spaceview.Style{FG: t.Dim}),
	}}}
	for i, name := range m.order {
		v := m.views[name]
		bg := spaceview.Color(0)
		mark := deco("  ", spaceview.Style{})
		nameStyle := spaceview.Style{FG: t.Room, Bold: v.unread > 0}
		if v == m.cur {
			bg = t.Raised
			mark = deco(" ▌", spaceview.Style{FG: t.Brand, BG: bg})
			nameStyle.BG, nameStyle.Bold = bg, true
		}
		runs := []run{mark, m.hoverRun(deco(name, nameStyle), m.hovered(hitChannel, i, ""))}
		var badge []run
		switch {
		case v.unreadMentions > 0:
			badge = []run{deco(fmt.Sprintf(" @%d ", v.unreadMentions), spaceview.Style{FG: t.Bright, BG: t.OperatorBG, Bold: true})}
		case v.unread > 0:
			badge = []run{deco(fmt.Sprintf(" %d ", v.unread), spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true})}
		case v.deleted:
			badge = []run{deco(" deleted", spaceview.Style{FG: t.Faint, BG: bg})}
		case v.membership == "left" || v.membership == "kicked":
			badge = []run{deco(" "+v.membership, spaceview.Style{FG: t.Faint, BG: bg})}
		}
		pad := width - runsWidth(m.widthMethod, runs) - runsWidth(m.widthMethod, badge)
		if pad < 1 {
			runs[1].Text = truncate(m.widthMethod, runs[1].Text, max(m.widthMethod.StringWidth(runs[1].Text)+pad-1, 1), "…")
			pad = 1
		}
		runs = append(runs, deco(strings.Repeat(" ", pad), spaceview.Style{BG: bg}))
		rows = append(rows, panelRow{member: -1, channel: i + 1, runs: append(runs, badge...)})
	}
	return append(rows, panelRow{member: -1})
}

// participantRows renders the participants panel: a quiet heading with the
// count, then operators, voiced members and everyone else, each as a
// two-row card (a glyph tile, the name, and a role line), and a closing
// note. Cards keep their height however many participants there are: when
// they do not fit, the list scrolls by whole cards (m.rosterScroll, clamped
// here), with a dim row naming how many are above or below. The heading
// never scrolls.
func (m *Model) participantRows(members []Member, width, height int, bg spaceview.Color) []panelRow {
	t := m.theme
	m.layout.rosterPage = 0
	if height <= 0 {
		return nil
	}
	count := fmt.Sprint(len(members))
	head := " PARTICIPANTS"
	rows := []panelRow{{member: -1, runs: []run{
		deco(head, spaceview.Style{FG: t.Dim, BG: bg, Bold: true}),
		deco(strings.Repeat(" ", max(width-len(head)-len(count)-1, 1)), spaceview.Style{BG: bg}),
		deco(count, spaceview.Style{FG: t.Dim, BG: bg}),
	}}, {member: -1}}
	if m.members == nil {
		return append(rows, panelRow{member: -1, runs: []run{deco(" waiting for the room…", spaceview.Style{FG: t.Dim, BG: bg})}})
	}
	m.layout.cardWidth = width
	var cards []panelRow
	for i, mem := range members {
		name, role := m.memberCard(mem, width, bg)
		cards = append(cards, panelRow{member: i, runs: name}, panelRow{member: i, runs: role})
	}
	avail := height - len(rows)
	if avail <= 0 {
		// Only the heading fits (the channel rail took the rest): no card
		// shows and nothing scrolls until the panel grows again.
		return rows[:min(height, len(rows))]
	}
	if len(cards) <= avail {
		m.rosterScroll = 0
		rows = append(rows, cards...)
		// The closing note shows only with room for all of it.
		note := []string{" Everyone in " + m.name, " sees its messages.", " Click a box to attach."}
		if len(cards)+1+len(note) <= avail {
			rows = append(rows, panelRow{member: -1})
			for _, line := range note {
				rows = append(rows, panelRow{member: -1, runs: []run{deco(line, spaceview.Style{FG: t.Faint, BG: bg})}})
			}
		}
		return rows
	}
	// Scrolled: the first k cards are hidden, the second heading row names
	// them, and the last row names the cards below. The last position shows
	// the last card.
	more := func(text string) panelRow {
		return panelRow{member: -1, runs: []run{deco(text, spaceview.Style{FG: t.Dim, BG: bg})}}
	}
	// A page is the cards one screen shows while more are below (one row
	// names them), so paging never skips a card.
	m.layout.rosterPage = max((avail-1)/2, 1)
	k := min(max(m.rosterScroll, 0), (len(cards)-avail+1)/2)
	m.rosterScroll = k
	if k > 0 {
		rows[1] = more(fmt.Sprintf(" ▲ %d above", k))
	}
	rest := cards[2*k:]
	if len(rest) <= avail {
		return append(rows, rest...)
	}
	// One row names what is below; a card is not split across that edge
	// unless no whole card fits, when its name row still shows.
	n := max(avail-1, 0)
	if n > 1 && rest[n].member == rest[n-1].member {
		n--
	}
	if n == 0 {
		n = min(avail, 1)
	}
	seen := map[int]bool{}
	for _, r := range rest[:n] {
		seen[r.member] = true
	}
	rows = append(rows, rest[:n]...)
	if n < avail {
		rows = append(rows, more(fmt.Sprintf(" ▼ %d more", len(members)-k-len(seen))))
	}
	return rows
}

// scrollRoster moves the participants panel by n cards (negative: up)
// when the last frame showed it scrolled; the next frame clamps it. It
// reports whether it moved.
func (m *Model) scrollRoster(n int) bool {
	if !m.rosterShown() || m.layout.rosterPage == 0 {
		return false
	}
	m.rosterScroll = max(m.rosterScroll+n, 0)
	m.hover = hoverKey{} // the cards moved under the pointer
	return true
}

// rosterShown reports whether the participants panel is on screen: the side
// panel on a wide screen, or the overlay on a narrow one.
func (m *Model) rosterShown() bool {
	if panelWidth(m.width) > 0 {
		return m.rosterOpen
	}
	return m.rosterOverlay
}

// roleMark is the cell right before a participant's name: @ for a channel
// operator, + for voice, otherwise a space so names stay aligned. It comes
// only from the observed channel roles, never from the identity map.
func (m *Model) roleMark(mem Member, bg spaceview.Color) run {
	switch {
	case mem.Op:
		return deco("@", spaceview.Style{FG: m.theme.OperatorFG, BG: bg, Bold: true})
	case mem.Voice:
		return deco("+", spaceview.Style{FG: m.theme.OK, BG: bg, Bold: true})
	}
	return deco(" ", spaceview.Style{BG: bg})
}

// memberCard is a participant's two panel rows: a tinted glyph tile, the
// role mark and name with presence, then a dim line with a box's observed
// model or a human's "human", and any away reason; it stays blank when
// nothing is known.
func (m *Model) memberCard(mem Member, width int, bg spaceview.Color) ([]run, []run) {
	t := m.theme
	w := m.identify(mem.Nick, mem.Account)
	tile := w.glyph + strings.Repeat(" ", max(2-m.widthMethod.StringWidth(w.glyph), 0))
	presence, active := m.memberPresence(mem)
	nameStyle := spaceview.Style{FG: w.color, BG: bg, Bold: true}
	if mem.Away {
		nameStyle.FG = t.Dim
	}
	name := []run{
		deco(" ", spaceview.Style{BG: bg}),
		deco(" "+tile+" ", spaceview.Style{FG: w.color, BG: t.Raised, Bold: true}),
		deco(" ", spaceview.Style{BG: bg}),
		m.roleMark(mem, bg),
		m.hoverRun(run{Text: w.name, Style: nameStyle, Act: action{Kind: actAuthor, Value: mem.Account, Extra: mem.Nick}}, m.panelHovered(mem)),
	}
	if w.me {
		name = append(name, deco(" · you", spaceview.Style{FG: t.Dim, BG: bg}))
	}
	if tag, ok := m.stateTag(w, time.Now()); ok {
		name = append(name, m.stateTagRun(tag, bg))
	}
	dot := m.presenceDot(presence, active, bg)
	dot.Text += " "
	if room := width - runsWidth(m.widthMethod, name) - runsWidth(m.widthMethod, []run{dot}); room < 0 {
		name[4].Text = truncate(m.widthMethod, name[4].Text, max(m.widthMethod.StringWidth(name[4].Text)+room, 1), "…")
	}
	name = append(name, deco(strings.Repeat(" ", max(width-runsWidth(m.widthMethod, name)-2, 0)), spaceview.Style{BG: bg}), dot)

	text := m.roleLine(w, presence)
	role := []run{deco("       "+m.roleText(mem, text, roleCells(width)), spaceview.Style{FG: t.Dim, BG: bg})}
	return name, role
}

// presenceDot is a participant's own IRC availability, always a solid ●:
// green connected, yellow away, gray not connected. Box activity (output
// or sustained load) makes whichever dot is shown breathe with space view's
// shared tone; activity never changes the glyph, hue or state.
func (m *Model) presenceDot(p spacetelemetry.Presence, active bool, bg spaceview.Color) run {
	t := m.theme
	c := t.OK
	switch p.Availability {
	case spacetelemetry.AvailabilityAway:
		c = t.Warn
	case spacetelemetry.AvailabilityUnknown:
		c = t.Dim
	}
	if active {
		c = t.ActiveTone(c, !m.opts.NoMotion, m.tele.frame)
	}
	return deco("●", spaceview.Style{FG: c, BG: bg})
}

// presenceLabel is the words beside a participant. The yellow dot already
// marks away, so only its reason is needed.
func presenceLabel(p spacetelemetry.Presence) string {
	switch {
	case p.Availability == spacetelemetry.AvailabilityAway:
		return oneLine(p.Detail)
	case p.Label != "" && p.Detail != "":
		return p.Label + ": " + oneLine(p.Detail)
	case p.Label != "":
		return p.Label
	}
	return p.Detail
}

// stateTagRun draws a state tag beside a name: a wait in the warning tone,
// an API error in the error tone.
func (m *Model) stateTagRun(tag string, bg spaceview.Color) run {
	t := m.theme
	fg := t.Capability
	if strings.HasPrefix(tag, "⚠") {
		fg = t.Bad
	}
	return deco(" "+tag, spaceview.Style{FG: fg, BG: bg, Bold: true})
}

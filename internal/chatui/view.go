package chatui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// viewLayout records where the last frame drew each region, so mouse
// events map to the same rows and targets the user saw.
type viewLayout struct {
	bodyTop, bodyHeight int
	transcriptWidth     int
	panelMembers        []Member
	// cardWidth is the participants panel's width when it drew two-row
	// cards, else 0.
	cardWidth     int
	inputTop      int
	inputRows     int
	firstDraftRow int
	draftRows     []draftRow
}

type hitArea int

const (
	hitTranscript hitArea = iota + 1
	hitTop
	hitPanel
	hitSuggest
	hitFooter
	hitReplyCancel
	hitComposer
	hitOverlay
	hitOverlayRow
	hitOverlayButton
	hitChannel
	hitFold   // a collapsed paste or image in the composer; id is its number
	hitFilter // the divider's For me / filter toggle
)

// hit is one click target of the last frame, covering cells [x0, x1) of
// row y (or rows [y, y1) when y1 > y).
type hit struct {
	x0, x1, y, y1 int
	area          hitArea
	item          int
	id            string
}

func (m *Model) hitAt(x, y int) (hit, bool) {
	for i := len(m.hits) - 1; i >= 0; i-- {
		h := m.hits[i]
		rows := y == h.y || h.y1 > h.y && y >= h.y && y < h.y1
		if rows && x >= h.x0 && x < h.x1 {
			return h, true
		}
	}
	return hit{}, false
}

// selectionStyle highlights selected text in both themes.
func (m *Model) selectionStyle() spaceview.Style {
	if m.theme.Dark {
		return spaceview.Style{FG: m.theme.Bright, BG: spaceview.PaletteColor(25)}
	}
	return spaceview.Style{FG: m.theme.Bright, BG: spaceview.PaletteColor(153)}
}

func (m *Model) View() tea.View {
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		return tea.NewView("")
	}
	if w < 20 || h < 7 {
		return tea.NewView(spaceview.StyleText(fit(m.widthMethod, "Resize to at least 20×7", w), spaceview.Style{FG: m.theme.FirstRow().Text, BG: spaceview.FirstRowBG}))
	}
	t := m.theme
	m.hits = m.hits[:0]
	lines := []string{m.topBar(w)}
	if h >= 16 {
		lines = append(lines, spaceview.StyleText(strings.Repeat("─", w), spaceview.Style{FG: t.Faint}))
	}
	top := len(lines)
	draft, cursorRow, cursorCol := m.layoutDraft()
	// boxRows are the composer's rows besides the draft: with room, a blank
	// row above it; the reply being written; and always a blank row below
	// its last visible line. statusRows keep a notice readable above a
	// selected message's actions.
	padRows, blankRows, statusRows := 0, 1, 0
	if h >= 16 {
		padRows = 1
	}
	if m.statusRow() {
		statusRows = 1
	}
	boxRows := padRows + blankRows
	if m.replyTo != "" {
		boxRows++
	}
	inputRows := min(max(len(draft), 1), 8, max(h-top-boxRows-statusRows-4, 1))
	// Only at the minimum height, replying with a notice beside the actions,
	// does the transcript give up its last row rather than overflow.
	bodyHeight := max(h-top-1-boxRows-statusRows-inputRows-1, 0)

	panel := 0
	if m.rosterOpen {
		panel = panelWidth(w)
	}
	tw := w
	if panel > 0 {
		tw = w - panel - 1
	}
	m.lastTextWidth = max(tw-bodyIndent-1, 1)
	if m.built.rows == nil || m.built.generation != m.generation || m.built.width != tw {
		m.built.rows, m.built.generation, m.built.width = m.transcript(tw), m.generation, tw
	}
	transcript := m.built.rows
	m.lastRows, m.lastBodyHeight = len(transcript), bodyHeight
	m.place(transcript, bodyHeight)
	m.lastTranscript = transcript
	m.layout = viewLayout{bodyTop: top, bodyHeight: bodyHeight, transcriptWidth: tw}

	var hl func(row transcriptRow) func(run) (int, int)
	if m.sel != nil {
		if ia, la, oa, ib, lb, ob, ok := m.ordered(*m.sel); ok {
			hl = func(row transcriptRow) func(run) (int, int) {
				lo, hi, ok := m.rowRange(row, ia, la, oa, ib, lb, ob)
				if !ok {
					return nil
				}
				return func(r run) (int, int) { return lo - r.start, hi - r.start }
			}
		}
	}
	members := sortedMembers(m.members)
	m.layout.panelMembers = members
	var side []panelRow
	if panel > 0 {
		// A wide panel lists open channels above the participants.
		rail := m.channelRail(panel)
		side = append(rail, m.participantRows(members, panel, bodyHeight-len(rail), 0)...)
	}
	selStyle := m.selectionStyle()
	for i := range bodyHeight {
		line := strings.Repeat(" ", tw)
		if j := m.scroll + i; j < len(transcript) {
			row := transcript[j]
			var f func(run) (int, int)
			if hl != nil {
				f = hl(row)
			}
			line = paint(m.widthMethod, m.hoverRow(row), tw, row.fill, f, selStyle)
		}
		if panel > 0 {
			line += spaceview.StyleText("│", spaceview.Style{FG: t.Faint})
			var runs []run
			if i < len(side) {
				runs = side[i].runs
				switch {
				case side[i].channel > 0:
					m.hits = append(m.hits, hit{x0: tw + 1, x1: w, y: top + i, area: hitChannel, item: side[i].channel - 1})
				case side[i].member >= 0:
					m.hits = append(m.hits, hit{x0: tw + 1, x1: w, y: top + i, area: hitPanel, item: side[i].member, id: memberKey(members[side[i].member])})
				}
			}
			line += paint(m.widthMethod, runs, panel, 0, nil, selStyle)
		}
		lines = append(lines, line)
	}
	if panel == 0 && m.rosterOverlay {
		m.participantOverlay(members, lines, w, top, bodyHeight)
	}

	composerTop := len(lines)
	lines = append(lines, m.composerDivider(w, composerTop))
	if padRows > 0 {
		// Visual padding only: it is not a line of the draft.
		m.hits = append(m.hits, hit{x0: 0, x1: w, y: len(lines), area: hitComposer})
		lines = append(lines, m.inputRow(nil, 0, false, w, len(lines)))
	}
	if m.replyTo != "" {
		lines = append(lines, m.replyRow(w, len(lines)))
	}
	first := max(cursorRow-inputRows+1, 0)
	m.layout.inputTop, m.layout.inputRows, m.layout.firstDraftRow, m.layout.draftRows = len(lines), inputRows, first, draft
	m.hits = append(m.hits, hit{x0: 0, x1: w, y: len(lines), y1: len(lines) + inputRows, area: hitComposer})
	for i := range inputRows {
		lines = append(lines, m.inputRow(draft, first+i, i == 0 && first == 0, w, len(lines)))
	}
	if blankRows > 0 {
		// Visual padding below the last visible line, like the row above.
		m.hits = append(m.hits, hit{x0: 0, x1: w, y: len(lines), area: hitComposer})
		lines = append(lines, m.inputRow(nil, 0, false, w, len(lines)))
	}
	if statusRows > 0 {
		lines = append(lines, m.noticeLine(w))
	}
	lines = append(lines, m.footer(w, len(lines)))
	if m.suggest != nil && m.overlay == nil {
		m.suggestPopup(lines, w, composerTop)
	}
	if m.overlay != nil {
		lines = m.overlayLines(lines, w)
	}

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	// All motion, so a buttonless pointer can highlight click targets.
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = "yoloctl — chat"
	if m.focus == focusComposer && m.overlay == nil {
		v.Cursor = tea.NewCursor(composerIndent+cursorCol, m.layout.inputTop+cursorRow-first)
	}
	// Request repeat metadata where supported so holding Ctrl+O cannot
	// confirm the browser prompt that the first press just opened.
	v.KeyboardEnhancements.ReportEventTypes = true
	if m.linkPrompt != nil {
		v.Content = m.linkPromptView()
		v.Cursor = nil
	}
	return v
}

// layoutDraft wraps the composer draft, reusing the last layout while the
// text, cursor and width are unchanged (for example while scrolling).
func (m *Model) layoutDraft() ([]draftRow, int, int) {
	e, d, w := &m.editor, &m.draft, m.composerWidth()
	// Expanding a paste changes the folds but not the text or revision.
	if d.rows == nil || d.revision != e.revision || d.cursor != e.cursor || d.width != w || !slices.Equal(d.folds, e.folds) {
		d.rows, d.row, d.col = draftLayout(m.widthMethod, e.text, e.folds, e.cursor, w)
		d.revision, d.cursor, d.width, d.folds = e.revision, e.cursor, w, e.folds
	}
	return d.rows, d.row, d.col
}

// place resolves the scroll position: follow the latest messages, keep a
// logical anchor, or reveal an explicitly selected message. An anchor on a
// wrapped table goes to the row whose cell fragment holds its byte; one in
// the table's padding or separators, which no row shows, goes to the row
// showing the next cell text. Rows whose offset precedes it are only the
// fallback, as for prose.
func (m *Model) place(transcript []transcriptRow, bodyHeight int) {
	if m.anchor != nil && !m.follow {
		held, next, nextAt := -1, -1, 0
		for i, row := range transcript {
			if row.key != m.anchor.key {
				continue
			}
			if m.anchor.line == lineTop {
				m.scroll = i
				break
			}
			if row.line != m.anchor.line {
				continue
			}
			if row.offset <= m.anchor.offset {
				m.scroll = i
			}
			if held < 0 && row.holds(m.anchor.offset) {
				held = i
			}
			if at := row.nextFrag(m.anchor.offset); at >= 0 && (next < 0 || at < nextAt) {
				next, nextAt = i, at
			}
		}
		switch {
		case held >= 0:
			m.scroll = held
		case next >= 0:
			m.scroll = next
		}
		if m.anchor.line == lineTop {
			m.anchor = nil
			m.reveal(transcript, bodyHeight)
		}
	}
	if m.follow {
		m.scroll = max(len(transcript)-bodyHeight, 0)
	}
	m.scroll = min(max(m.scroll, 0), max(len(transcript)-bodyHeight, 0))
}

// scrolledBack reports whether the last frame left transcript rows below
// the view: the reading position is away from the live tail.
func (m *Model) scrolledBack() bool {
	return !m.follow && m.scroll < m.lastRows-m.lastBodyHeight
}

// reveal scrolls as little as possible to show the selected message.
func (m *Model) reveal(transcript []transcriptRow, bodyHeight int) {
	key := m.selectedKey()
	first, last := -1, -1
	for i, row := range transcript {
		if row.key == key && row.kind != rowSpacer && row.kind != rowEvent {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return
	}
	switch {
	case last-first+1 > bodyHeight || first < m.scroll:
		m.scroll = first
	case last >= m.scroll+bodyHeight:
		m.scroll = last - bodyHeight + 1
	}
}

// topBar follows the space dashboard: plain summary text on the first
// row's black, with connection state anchored on the right.
func (m *Model) topBar(w int) string {
	t := m.theme.FirstRow()
	bg := spaceview.FirstRowBG
	st := func(fg spaceview.Color, bold bool) spaceview.Style {
		return spaceview.Style{FG: fg, BG: bg, Bold: bold}
	}
	state, stateFG := "◉ LIVE", t.OK
	switch {
	case m.reconnecting:
		state, stateFG = "◌ CONNECTING", t.Brand
	case !m.connected:
		state, stateFG = "◌ OFFLINE", t.Bad
	}
	type part struct {
		runs []run
		id   string
	}
	base := []run{deco(" "+spaceview.ChatIcon+" ", st(t.Room, true)), deco(oneLine(m.opts.Space), st(t.Bright, true)), deco(" ", st(0, false))}
	channel := part{[]run{deco(m.name, st(t.Room, true))}, "channels"}
	if len(m.order) > 1 {
		channel.runs = append(channel.runs, deco(" ▾", st(t.Dim, false)))
	}
	var extras []part
	if m.members != nil {
		count := fmt.Sprintf("  %d online", len(m.members))
		away := 0
		for _, mem := range m.members {
			if mem.Away {
				away++
			}
		}
		if away > 0 {
			count += fmt.Sprintf(" · %d away", away)
		}
		extras = append(extras, part{[]run{deco(count, st(t.Dim, false))}, ""})
	}
	if unread, mentions := m.otherUnread(); unread > 0 {
		label := fmt.Sprintf("  %d elsewhere", unread)
		if mentions > 0 {
			label += fmt.Sprintf(" · %d for you", mentions)
		}
		extras = append(extras, part{[]run{deco(label, st(t.Brand, true))}, "channels"})
	}
	if m.unread > 0 {
		label := fmt.Sprintf("  ↓ %d new", m.unread)
		if m.unreadMentions > 0 {
			label += fmt.Sprintf(" · %d for you", m.unreadMentions)
		}
		extras = append(extras, part{[]run{deco(label, st(t.Brand, true))}, "live"})
	}
	if m.source {
		extras = append(extras, part{[]run{deco("  source view", st(t.Capability, true))}, ""})
	}
	if m.topic != "" {
		extras = append(extras, part{[]run{deco("  "+truncate(m.widthMethod, m.topic, 48, "…"), st(t.Text, false))}, ""})
	}
	right := []run{deco(state+" ", st(stateFG, true))}
	budget := max(w-runsWidth(m.widthMethod, right)-1, 0)
	parts := []part{{runs: base}, channel}
	used := runsWidth(m.widthMethod, base) + runsWidth(m.widthMethod, channel.runs)
	for _, p := range extras {
		if used+runsWidth(m.widthMethod, p.runs) > budget {
			break
		}
		parts = append(parts, p)
		used += runsWidth(m.widthMethod, p.runs)
	}
	// Clip the summary before registering its visible targets. The status
	// keeps one separating column even with a long space or channel name.
	leftWidth := min(used, budget)
	var left []run
	x := 0
	for _, p := range parts {
		pw := runsWidth(m.widthMethod, p.runs)
		if p.id != "" && x < leftWidth {
			m.hits = append(m.hits, hit{x0: x, x1: min(x+pw, leftWidth), y: 0, area: hitTop, id: p.id})
		}
		left = append(left, hoverRuns(p.runs, p.id != "" && m.hovered(hitTop, 0, p.id))...)
		x += pw
	}
	gap := w - leftWidth - runsWidth(m.widthMethod, right)
	return paint(m.widthMethod, left, leftWidth, bg, nil, spaceview.Style{}) +
		spaceview.StyleText(strings.Repeat(" ", max(gap, 0)), spaceview.Style{BG: bg}) +
		paint(m.widthMethod, right, runsWidth(m.widthMethod, right), bg, nil, spaceview.Style{})
}

// composerDivider labels the composer: the channel and, right after it, the
// message filter control, a jump to the latest messages centered on the
// screen while the transcript is scrolled back, and the draft's byte count,
// coloured as it nears the limit.
func (m *Model) composerDivider(w, y int) string {
	t := m.theme
	frame := spaceview.Style{FG: t.Frame}
	room := func(name string) []run {
		return []run{deco("─ ", frame), deco(name+" ", spaceview.Style{FG: t.Room, Bold: true})}
	}
	runs := room(m.name)
	var count []run
	if n := len(m.editor.text); n > 0 {
		st := spaceview.Style{FG: t.Dim}
		switch {
		case n >= m.editor.limit:
			st = spaceview.Style{FG: t.Bad, Bold: true}
		case n*10 >= m.editor.limit*8:
			st = spaceview.Style{FG: t.Capability, Bold: true}
		}
		count = append(count, deco(" "+byteCount(n)+" ", st))
	}
	right := append(count, deco("─", frame))
	// withChip puts the filter control right after the channel name, in the
	// cells before limit the rest left, past one frame cell: it gives way
	// first, so the jump and the count behave as without it. Narrower, the
	// keys, /filter and a filtered transcript's scope rows remain.
	withChip := func(left []run, limit int) []run {
		chip, ids, ok := m.filterChip(limit - runsWidth(m.widthMethod, left) - 1)
		if !ok {
			return left
		}
		out, x := slicesCloneRuns(left), runsWidth(m.widthMethod, left)
		for i, c := range chip {
			cw := runsWidth(m.widthMethod, []run{c})
			m.hits = append(m.hits, hit{x0: x, x1: x + cw, y: y, area: hitFilter, id: ids[i]})
			out = append(out, hoverRuns([]run{c}, m.hovered(hitFilter, 0, ids[i]))...)
			x += cw
		}
		return out
	}
	// The jump is centered on the whole screen, at least one frame cell
	// from the room name and the count. It shrinks to a short label, then
	// the room name shortens, then the jump gives way before the count.
	var labels []string
	switch {
	case !m.behindLatest(): // at the live tail
	case m.unread > 0:
		labels = []string{fmt.Sprintf(" ↓ %d new · Jump to latest ", m.unread), fmt.Sprintf(" ↓ %d new ", m.unread)}
	default:
		labels = []string{" ↓ Jump to latest ", " ↓ Latest "}
	}
	type try struct {
		label string
		short bool
	}
	var tries []try
	for _, label := range labels {
		tries = append(tries, try{label, false})
	}
	if len(labels) > 0 {
		tries = append(tries, try{labels[len(labels)-1], true})
	}
	for _, c := range tries {
		pw := m.widthMethod.StringWidth(c.label)
		x := (w - pw) / 2
		left := runs
		if c.short {
			// "─ " and the space after the name, then at least one frame cell.
			name := x - 4
			if name < 2 || m.widthMethod.StringWidth(m.name) <= name {
				continue
			}
			left = room(truncate(m.widthMethod, m.name, name, "…"))
		}
		if runsWidth(m.widthMethod, left)+1 > x || x+pw+1+runsWidth(m.widthMethod, right) > w {
			continue
		}
		live := hoverRuns([]run{deco(c.label, spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true})}, m.hovered(hitTop, 0, "live"))
		m.hits = append(m.hits, hit{x0: x, x1: x + pw, y: y, area: hitTop, id: "live"})
		left = withChip(left, x)
		out := append(slicesCloneRuns(left), deco(strings.Repeat("─", x-runsWidth(m.widthMethod, left)), frame))
		out = append(append(out, live...), deco(strings.Repeat("─", w-x-pw-runsWidth(m.widthMethod, right)), frame))
		return paint(m.widthMethod, append(out, right...), w, 0, nil, spaceview.Style{})
	}
	fill := w - runsWidth(m.widthMethod, runs) - runsWidth(m.widthMethod, right)
	if fill < 1 {
		right = right[len(right)-1:]
		fill = w - runsWidth(m.widthMethod, runs) - 1
	} else {
		runs = withChip(runs, w-runsWidth(m.widthMethod, right))
		fill = w - runsWidth(m.widthMethod, runs) - runsWidth(m.widthMethod, right)
	}
	runs = append(runs, deco(strings.Repeat("─", max(fill, 0)), frame))
	return paint(m.widthMethod, append(runs, right...), w, 0, nil, spaceview.Style{})
}

func byteCount(n int) string {
	if n < 10<<10 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

func (m *Model) replyRow(w, y int) string {
	t := m.theme
	bg := spaceview.Style{BG: t.Raised}
	label := strings.TrimPrefix(m.replyLabel(m.replyTo), "↳ ")
	cancel := " ✕ "
	m.hits = append(m.hits, hit{x0: w - 4, x1: w - 1, y: y, area: hitReplyCancel})
	runs := []run{
		deco("▌", spaceview.Style{FG: t.Label, BG: t.Raised}),
		deco(" ↳ Replying to ", spaceview.Style{FG: t.Label, BG: t.Raised, Bold: true}),
		deco(truncate(m.widthMethod, label, max(w-24, 1), "…"), spaceview.Style{FG: t.Dim, BG: t.Raised}),
	}
	pad := w - runsWidth(m.widthMethod, runs) - m.widthMethod.StringWidth(cancel) - 1
	runs = append(runs, deco(strings.Repeat(" ", max(pad, 0)), bg))
	runs = append(runs, hoverRuns([]run{deco(cancel, spaceview.Style{FG: t.Text, BG: t.Surface, Bold: true})}, m.hovered(hitReplyCancel, 0, ""))...)
	return paint(m.widthMethod, runs, w, t.Raised, nil, spaceview.Style{})
}

type composerStatus struct {
	ready       bool
	placeholder string
	hintKey     string
	hintAction  string
}

func (m *Model) composerStatus() composerStatus {
	switch {
	case m.deleted:
		return composerStatus{
			placeholder: m.name + " was deleted" + m.membershipNote(),
			hintKey:     "deleted",
			hintAction:  "read-only",
		}
	case m.reconnecting:
		return composerStatus{
			placeholder: "Connecting to " + m.name + "… drafts are preserved",
			hintKey:     "connecting…",
			hintAction:  "draft kept",
		}
	case !m.connected:
		return composerStatus{
			placeholder: "Offline · drafts are preserved",
			hintKey:     "offline",
			hintAction:  "draft kept",
		}
	case m.opts.Send == nil:
		return composerStatus{
			placeholder: "Sending unavailable · view is read-only",
			hintKey:     "sending unavailable",
			hintAction:  "draft kept",
		}
	case m.inflight != "" || m.pending != nil:
		return composerStatus{
			placeholder: "A send or channel action is unresolved · draft kept",
			hintKey:     "unresolved",
			hintAction:  "draft kept",
		}
	case m.imageUnresolved() != "":
		return composerStatus{
			placeholder: m.imageUnresolved() + " · draft kept",
			hintKey:     "image",
			hintAction:  "needs attention",
		}
	case m.membership == "joining":
		return composerStatus{
			placeholder: "Joining " + m.name + "… drafts are preserved",
			hintKey:     "joining…",
			hintAction:  "draft kept",
		}
	case !m.canSend():
		return composerStatus{
			placeholder: "You are not in " + m.name + m.membershipNote(),
			hintKey:     "/join " + m.name,
			hintAction:  "to send",
		}
	default:
		return composerStatus{
			ready:       true,
			placeholder: "Message " + m.name + " · @name notifies · /help",
			hintKey:     "Enter",
			hintAction:  "send",
		}
	}
}

func (m *Model) inputRow(draft []draftRow, n int, first bool, w, y int) string {
	t := m.theme
	bg := t.Raised
	accent := spaceview.Style{FG: t.Brand, BG: bg}
	if m.focus != focusComposer {
		accent.FG = t.Faint
	}
	prompt := "  "
	if first {
		prompt = composerPrompt
	}
	runs := []run{deco("▌ ", accent), deco(prompt, spaceview.Style{FG: accent.FG, BG: bg, Bold: true})}
	switch {
	case m.editor.text == "" && first:
		status := m.composerStatus()
		hint := status.placeholder
		if m.focus == focusMessages {
			hint = "Type to return to the composer"
		}
		runs = append(runs, deco(truncate(m.widthMethod, hint, max(w-composerIndent-1, 0), "…"), spaceview.Style{FG: t.Dim, BG: bg}))
	case n < len(draft):
		for _, p := range draft[n].parts {
			if p.fold < 0 {
				runs = append(runs, deco(p.text, spaceview.Style{FG: t.Bright, BG: bg}))
				continue
			}
			// A collapsed paste: click (or Alt+P beside it) shows its text.
			f := m.editor.folds[p.fold]
			id := strconv.Itoa(f.n)
			st := spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true}
			if f.img != nil {
				id, st = imageHitID+id, m.imageChipStyle(f.img)
			}
			x := composerIndent + p.col
			m.hits = append(m.hits, hit{x0: x, x1: x + m.widthMethod.StringWidth(p.text), y: y, area: hitFold, item: p.fold, id: id})
			runs = append(runs, m.hoverRun(deco(p.text, st), m.hovered(hitFold, p.fold, id)))
		}
	}
	return paint(m.widthMethod, runs, w, bg, nil, spaceview.Style{})
}

// composerPrompt marks the draft's first line.
const composerPrompt = "❯ "

// actionsShown reports whether the footer holds the explicitly selected
// message's actions. Neither composer focus nor a notice hides them.
func (m *Model) actionsShown() bool { return m.picked && m.selectedMessage() != nil }

// statusRow reports whether a notice needs its own row above the actions.
func (m *Model) statusRow() bool { return m.notice != "" && m.actionsShown() }

// noticeLine draws the current notice with its level's icon.
func (m *Model) noticeLine(w int) string {
	return paint(m.widthMethod, m.noticeRuns(), w, 0, nil, spaceview.Style{})
}

func (m *Model) noticeRuns() []run {
	t := m.theme
	icon, st := "› ", spaceview.Style{FG: t.Label}
	switch m.noticeLevel {
	case levelOK:
		icon, st = "✓ ", spaceview.Style{FG: t.OK}
	case levelWarn:
		icon, st = "! ", spaceview.Style{FG: t.Capability, Bold: true}
	case levelError:
		icon, st = "✕ ", spaceview.Style{FG: t.Bad, Bold: true}
	}
	return []run{deco(" "+icon, st), deco(oneLine(m.notice), st)}
}

// quoteChip leads the footer while a highlight can be quoted, before the
// notice or hints, so a release's copy notice does not hide it.
func (m *Model) quoteChip(y int) []run {
	if m.sel == nil || !m.quotable() {
		return nil
	}
	t := m.theme
	chip := " " + quoteLabel + " "
	m.hits = append(m.hits, hit{x0: 1, x1: 1 + m.widthMethod.StringWidth(chip), y: y, area: hitFooter, id: "quote"})
	return append([]run{deco(" ", spaceview.Style{})}, hoverRuns([]run{deco(chip, spaceview.Style{FG: t.Text, BG: t.Raised})}, m.hovered(hitFooter, 0, "quote"))...)
}

// footer shows the selected message's actions, a notice, or key hints.
func (m *Model) footer(w, y int) string {
	t := m.theme
	dim := spaceview.Style{FG: t.Dim}
	key := spaceview.Style{FG: t.Label, Bold: true}
	if m.actionsShown() {
		msg := m.selectedMessage()
		who := m.author(msg)
		runs := []run{deco(" ", dim), deco(iconSlot(m.widthMethod, who.glyph), spaceview.Style{FG: who.color}), deco(who.name, spaceview.Style{FG: who.color, Bold: true}), deco(" "+msg.Time.Local().Format("15:04")+"  ", dim)}
		x := runsWidth(m.widthMethod, runs)
		for i, a := range m.messageActions() {
			st := spaceview.Style{FG: t.Text, BG: t.Raised}
			if i == m.actionIndex && m.focus == focusMessages {
				st = spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true}
			}
			chip := " " + a.label + " "
			if x+m.widthMethod.StringWidth(chip)+1 > w {
				break
			}
			m.hits = append(m.hits, hit{x0: x, x1: x + m.widthMethod.StringWidth(chip), y: y, area: hitFooter, id: a.id})
			runs = append(runs, hoverRuns([]run{deco(chip, st)}, m.hovered(hitFooter, 0, a.id))...)
			runs = append(runs, deco(" ", dim))
			x += m.widthMethod.StringWidth(chip) + 1
		}
		if m.focus == focusMessages {
			runs = append(runs, deco(" ←→ Enter · Esc", dim))
		}
		return paint(m.widthMethod, runs, w, 0, nil, spaceview.Style{})
	}
	chip := m.quoteChip(y)
	if m.notice != "" {
		return paint(m.widthMethod, append(chip, m.noticeRuns()...), w, 0, nil, spaceview.Style{})
	}
	if m.suggest == nil && m.sel == nil {
		if hint := m.hoverHint(); hint != nil {
			return paint(m.widthMethod, hint, w, 0, nil, spaceview.Style{})
		}
	}
	type hint struct{ keys, what string }
	var hints []hint
	defaultHints := false
	switch {
	case m.suggest != nil:
		hints = []hint{{"↑↓", "choose"}, {"Tab/Enter", "insert"}, {"Esc", "close"}}
	case m.sel != nil && chip != nil:
		hints = []hint{{"Alt+Q", "quote"}, {"Esc", "clear highlight"}, {"/help", "keys"}}
	case m.sel != nil:
		hints = []hint{{"Esc", "clear highlight"}, {"drag", "select again"}, {"/help", "keys"}}
	default:
		defaultHints = true
		status := m.composerStatus()
		firstHint := hint{status.hintKey, status.hintAction}
		hints = []hint{firstHint, {"Alt+Enter", "newline"}, {"@", "mention"}, {"Alt+↑", "messages"}, {"Ctrl+P", "participants"}, {"/help", "keys"}}
	}
	build := func(hs []hint) []run {
		runs := append(slices.Clone(chip), deco(" ", dim))
		for i, h := range hs {
			if i > 0 {
				runs = append(runs, deco(" · ", spaceview.Style{FG: t.Faint}))
			}
			runs = append(runs, deco(h.keys, key), deco(" "+h.what, dim))
		}
		return runs
	}
	runs := build(hints)
	for runsWidth(m.widthMethod, runs) > w && len(hints) > 1 {
		if defaultHints && len(hints) == 2 {
			hints = hints[:1] // preserve availability reason, drop ancillary /help
			runs = build(hints)
			if runsWidth(m.widthMethod, runs) > w && w > 0 {
				avail := max(w-2-m.widthMethod.StringWidth(hints[0].keys), 0)
				hints[0].what = truncate(m.widthMethod, hints[0].what, avail, "…")
				runs = build(hints)
			}
			break
		}
		hints = append(hints[:len(hints)-2], hints[len(hints)-1])
		runs = build(hints)
	}
	if len(hints) > 0 && hints[len(hints)-1].keys == "/help" {
		last := hints[len(hints)-1]
		x := runsWidth(m.widthMethod, runs) - m.widthMethod.StringWidth(last.keys+" "+last.what)
		m.hits = append(m.hits, hit{x0: x, x1: runsWidth(m.widthMethod, runs), y: y, area: hitFooter, id: "help"})
	}
	return paint(m.widthMethod, runs, w, 0, nil, spaceview.Style{})
}

// suggestPopup draws the suggestions above the composer, aligned with the
// word being completed.
func (m *Model) suggestPopup(lines []string, w, composerTop int) {
	t := m.theme
	s := m.suggest
	inner := 0
	for _, it := range s.items {
		inner = max(inner, m.widthMethod.StringWidth(it.label)+2+m.widthMethod.StringWidth(it.detail)+7)
	}
	title := "Mention"
	switch {
	case s.items[0].command != nil:
		title = "Commands"
	case strings.HasPrefix(s.word, "#"):
		title = "Channels"
	}
	head := []run{deco(" "+title, spaceview.Style{FG: t.Brand, BG: t.Popup, Bold: true}), deco("  Tab/Enter inserts · Esc closes", spaceview.Style{FG: t.Dim, BG: t.Popup})}
	boxW := min(max(inner, runsWidth(m.widthMethod, head)+1, 24), w)
	rows := len(s.items) + 1
	y := composerTop - rows
	if y < m.layout.bodyTop {
		return
	}
	col := 0
	if s.start <= len(m.editor.text) {
		_, _, col = draftLayout(m.widthMethod, m.editor.text, m.editor.folds, s.start, m.composerWidth())
	}
	x := min(max(composerIndent+col-2, 0), w-boxW)
	lines[y] = overlayLine(m.widthMethod, lines[y], x, paint(m.widthMethod, head, boxW, t.Popup, nil, spaceview.Style{}), w)
	for i, it := range s.items {
		bg := t.Popup
		marker := deco("  ", spaceview.Style{BG: bg})
		if i == s.index {
			bg = t.Raised
			marker = deco("› ", spaceview.Style{FG: t.Brand, BG: bg, Bold: true})
		}
		labelStyle := spaceview.Style{FG: t.Text, BG: bg, Bold: true}
		if it.member != nil {
			labelStyle.FG = m.identify(it.member.Nick, it.member.Account).color
		}
		if it.group {
			labelStyle.FG = t.Capability
		}
		runs := []run{marker}
		if it.member != nil {
			who := m.identify(it.member.Nick, it.member.Account)
			runs = append(runs, deco(iconSlot(m.widthMethod, who.glyph), spaceview.Style{FG: who.color, BG: bg, Bold: true}))
		}
		runs = append(runs, m.hoverRun(deco(it.label, labelStyle), m.hovered(hitSuggest, i, "")), deco("  "+it.detail, spaceview.Style{FG: t.Dim, BG: bg}))
		row := y + 1 + i
		lines[row] = overlayLine(m.widthMethod, lines[row], x, paint(m.widthMethod, runs, boxW, bg, nil, spaceview.Style{}), w)
		m.hits = append(m.hits, hit{x0: x, x1: x + boxW, y: row, area: hitSuggest, item: i})
	}
}

// participantOverlay opens the participants panel over the right side of
// the transcript on screens too narrow for a side panel.
func (m *Model) participantOverlay(members []Member, lines []string, w, top, height int) {
	pw := min(30, w)
	x := w - pw
	rows := m.participantRows(members, pw, height, m.theme.Popup)
	for i := range height {
		var runs []run
		if i < len(rows) {
			runs = rows[i].runs
			if rows[i].member >= 0 {
				m.hits = append(m.hits, hit{x0: x, x1: w, y: top + i, area: hitPanel, item: rows[i].member, id: memberKey(members[rows[i].member])})
			}
		}
		lines[top+i] = overlayLine(m.widthMethod, lines[top+i], x, paint(m.widthMethod, runs, pw, m.theme.Popup, nil, spaceview.Style{}), w)
	}
}

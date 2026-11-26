package chatui

import (
	"crypto/rand"
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"
)

// WindowPageMsg completes one window page request: the whole page, as its
// history batch closed, or Err. It reuses HistoryRequest.ID.
type WindowPageMsg struct {
	ID, Channel string
	Messages    []Message
	Err         error
}

// A window is a page of older history a channel shows in place of its live
// list, because a jump's target is not in that list and loading back to it
// is bounded by the display limits. The live list is set aside and keeps
// receiving messages (see onLive); the window pages at its own edges.
type window struct {
	// live is the list not shown: the live list, except while onLive runs
	// on it (swapped), when it holds the window's own list.
	live    messageList
	swapped bool
	// edge is the one pending edge request; a reply with another ID, or for
	// a window shown since, is ignored.
	edge *windowEdge
	// start: no older history. end: an AFTER page reached nothing and no
	// live row; a later live message re-arms it.
	start, end bool
}

type windowEdge struct {
	id    string
	after bool
}

// onLive runs f on the live list, also when a window shows in its place.
func (m *Model) onLive(f func()) {
	w := m.win
	if w == nil || w.swapped {
		f()
		return
	}
	m.messageList, w.live, w.swapped = w.live, m.messageList, true
	defer func() { m.messageList, w.live, w.swapped = w.live, m.messageList, false }()
	f()
}

// liveMessages is the live list's messages, shown or set aside.
func (v *channelView) liveMessages() []Message {
	if v.win != nil && !v.win.swapped {
		return v.win.live.messages
	}
	return v.messages
}

// edgeID is the message at the window's bottom (after) or top edge.
func (m *Model) edgeID(after bool) string {
	if len(m.messages) == 0 {
		return ""
	}
	if after {
		return m.messages[len(m.messages)-1].ID
	}
	return m.messages[0].ID
}

// lists is the channel's lists: the one in the view, and the one beside it.
func (v *channelView) lists() [][]Message {
	if v.win == nil {
		return [][]Message{v.messages}
	}
	return [][]Message{v.messages, v.win.live.messages}
}

// holdsKey reports whether either list of the channel has a message with key.
func (v *channelView) holdsKey(key string) bool {
	for _, list := range v.lists() {
		if slices.ContainsFunc(list, func(msg Message) bool { return messageKey(msg) == key }) {
			return true
		}
	}
	return false
}

// holds reports whether either list of the channel has message id.
func (v *channelView) holds(id string) bool {
	for _, list := range v.lists() {
		if slices.ContainsFunc(list, func(msg Message) bool { return msg.ID == id }) {
			return true
		}
	}
	return false
}

// showWindow shows page in place of the live list. A window shown already is
// replaced; its live list stays set aside. The reading position is the
// caller's to set.
func (m *Model) showWindow(page []Message) {
	var replaced []Message
	if m.win == nil {
		m.captureAnchor()
		live := m.messageList
		// While set aside the live list is not at its tail: arrivals count
		// as unread (incoming) until the reader returns to it.
		live.follow = false
		m.win = &window{live: live}
	} else {
		replaced = m.messages
	}
	m.win.edge, m.win.start, m.win.end = nil, false, false
	m.messageList = messageList{messages: slices.Clone(page), selected: -1}
	m.carryRows(m.win.live.messages, m.messages)
	m.release(replaced)
	m.drag = dragState{}
	m.touch()
}

// carryRows gives the copies in to of the confirmed messages in from their
// opened table rows where the two keys differ: the reader's own message has
// its client key in the live list and its server key in a history page.
func (m *Model) carryRows(from, to []Message) {
	if len(m.tableRows) == 0 {
		return
	}
	keys := make(map[string]string, len(to))
	for _, msg := range to {
		if msg.ID != "" {
			keys[msg.ID] = messageKey(msg)
		}
	}
	for _, msg := range from {
		src := messageKey(msg)
		dst, ok := keys[msg.ID]
		rows, open := m.tableRows[src]
		if msg.ID == "" || !ok || dst == src || !open {
			continue
		}
		m.tableRows[dst] = maps.Clone(rows)
		if b, ok := m.bodyCache[dst]; ok {
			b.width = -1 // lay it out again with those rows open
			m.bodyCache[dst] = b
		}
	}
}

// release drops the rendered bodies and table-row state of messages that
// neither list holds any more, as eviction does for one message.
func (m *Model) release(gone []Message) {
	if len(gone) == 0 {
		return
	}
	held := map[string]bool{}
	for _, list := range m.lists() {
		for _, msg := range list {
			held[messageKey(msg)] = true
		}
	}
	for _, msg := range gone {
		if key := messageKey(msg); !held[key] {
			delete(m.bodyCache, key)
			delete(m.tableRows, key)
		}
	}
}

// endWindow shows the live list again. Its reading position is the caller's
// to set; the layout it had is stale.
func (m *Model) endWindow() {
	gone := m.closeWindow()
	m.carryRows(gone, m.messages)
	m.release(gone)
}

// closeWindow shows the live list again and returns the window's messages,
// for the caller to release once what the live list holds is complete.
func (m *Model) closeWindow() []Message {
	if m.win == nil {
		return nil
	}
	gone := m.messages
	m.messageList, m.win = m.win.live, nil
	m.lastTranscript, m.lastRows = nil, 0
	m.drag = dragState{}
	m.touch()
	return gone
}

// heldBeside reports whether the list beside the one in use, if any, has a
// message with key: its rendered body and table rows are still needed.
func (m *Model) heldBeside(key string) bool {
	return m.win != nil && slices.ContainsFunc(m.win.live.messages, func(msg Message) bool { return messageKey(msg) == key })
}

// liveJoin finds where page meets live: page[s:] is the run of live
// messages from p on, by ID. Only a suffix of the page can meet the live
// list without a hole between them.
func liveJoin(page, live []Message) (s, p int, ok bool) {
	at := make(map[string]int, len(live))
	for i, msg := range live {
		if msg.ID != "" {
			at[msg.ID] = i
		}
	}
	s = len(page)
	for s > 0 {
		j, in := at[page[s-1].ID]
		if !in || page[s-1].ID == "" || s < len(page) && at[page[s].ID] != j+1 {
			break
		}
		s--
	}
	if s == len(page) {
		return 0, 0, false
	}
	return s, at[page[s].ID], true
}

// listFits reports whether a and b together fit one view's display bounds.
func listFits(a, b []Message) bool {
	n, bytes, lines := len(a)+len(b), 0, 0
	for _, list := range [][]Message{a, b} {
		for _, msg := range list {
			bytes += len(msg.Text)
			lines += logicalLines(msg.Text)
		}
	}
	return n <= maxMessages && bytes <= maxHistoryBytes && lines <= maxLogicalLines
}

// mergeable reports the rows of page to put in front of live: only when
// every live row is newer (page meets the live list at its first row) and
// all of them fit, so nothing the page shows is lost.
func mergeable(page, live []Message) ([]Message, bool) {
	s, p, ok := liveJoin(page, live)
	if !ok || p != 0 || !listFits(page[:s], live) {
		return nil, false
	}
	return page[:s], true
}

// prependLive puts older rows in front of the shown live list.
func (m *Model) prependLive(rows []Message) {
	for i := range rows {
		rows[i].Delivery = Confirmed
	}
	m.messages = append(slices.Clone(rows), m.messages...)
	if m.selected >= 0 {
		m.selected += len(rows)
	}
	m.touch()
}

// position is a reading position and selection carried between lists by
// message ID, since a list's keys can differ (own messages carry a client key).
type position struct {
	anchorID          string
	line, offset      int
	follow            bool
	selectedID        string
	picked            bool
	anchorKey, selKey string
}

func (m *Model) idOf(key string) string {
	if i := m.indexOf(key); i >= 0 {
		return m.messages[i].ID
	}
	return ""
}

func (m *Model) indexOfID(id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(m.messages, func(msg Message) bool { return msg.ID == id })
}

// here captures the shown list's position.
func (m *Model) here() position {
	m.captureAnchor()
	p := position{follow: m.follow, picked: m.picked}
	if a := m.anchor; a != nil && !m.follow {
		p.anchorKey, p.anchorID, p.line, p.offset = a.key, m.idOf(a.key), a.line, a.offset
	}
	if m.selected >= 0 && m.selected < len(m.messages) {
		p.selKey, p.selectedID = messageKey(m.messages[m.selected]), m.messages[m.selected].ID
	}
	return p
}

// placeAt restores p on the shown list. It reports whether the anchor was
// found; otherwise the view goes to the oldest message.
func (m *Model) placeAt(p position) bool {
	found := true
	switch {
	case p.follow:
		m.follow, m.anchor = true, nil
	default:
		m.follow = false
		i := m.indexOf(p.anchorKey)
		if i < 0 {
			i = m.indexOfID(p.anchorID)
		}
		if i >= 0 {
			m.anchor = &viewAnchor{key: messageKey(m.messages[i]), line: p.line, offset: p.offset}
		} else {
			m.anchor, m.scroll, found = nil, 0, false
		}
	}
	i := m.indexOf(p.selKey)
	if i < 0 {
		i = m.indexOfID(p.selectedID)
	}
	if i >= 0 {
		m.selected, m.picked = i, p.picked
	} else {
		m.selected, m.picked = max(len(m.messages)-1, -1), false
		if m.focus == focusMessages {
			m.focus = focusComposer
		}
	}
	m.touch()
	return found
}

// mergeWindow ends the window when its rows can go in front of the live
// list, keeping the reading position and selection.
func (m *Model) mergeWindow() bool {
	w := m.win
	if w == nil {
		return false
	}
	rows, ok := mergeable(m.messages, w.live.messages)
	if !ok {
		return false
	}
	at := m.here()
	rows = slices.Clone(rows)
	gone := m.closeWindow()
	m.prependLive(rows)
	m.carryRows(gone, m.messages)
	m.release(gone) // only what the merged list does not hold
	m.placeAt(at)
	m.trimAggregate()
	return true
}

// switchAtShared ends the window when the top row on screen is in the run
// the window shares with the live list: the live list shows from that row
// on, at the same place, so no row is skipped or shown twice.
func (m *Model) switchAtShared() bool {
	w := m.win
	if w == nil || m.scroll >= len(m.lastTranscript) {
		return false
	}
	s, _, ok := liveJoin(m.messages, w.live.messages)
	if !ok {
		return false
	}
	top := m.lastTranscript[m.scroll]
	if i := m.indexOf(top.key); i < s {
		return false
	}
	at := m.here()
	at.anchorKey, at.anchorID, at.line, at.offset = top.key, m.idOf(top.key), top.line, top.offset
	at.follow = false
	m.endWindow()
	m.placeAt(at)
	return true
}

// scrollDown moves toward newer messages. In a window, its bottom asks for
// the next page instead of returning to the live tail.
func (m *Model) scrollDown(rows int) tea.Cmd {
	m.moveScroll(rows)
	if m.win == nil || m.mergeWindow() || m.switchAtShared() {
		return nil
	}
	if m.scroll >= max(m.lastRows-m.lastBodyHeight, 0) {
		return m.windowEdge(true)
	}
	return nil
}

// windowEdge requests the page after or before the window, once at a time.
func (m *Model) windowEdge(after bool) tea.Cmd {
	w := m.win
	if w == nil || w.edge != nil || len(m.messages) == 0 {
		return nil
	}
	if !after && w.start {
		m.say(levelInfo, "Beginning of retained history")
		return nil
	}
	if after && w.end || m.opts.LoadWindow == nil || !m.connected || m.deleted || m.membership != "joined" {
		return nil
	}
	w.edge = &windowEdge{id: rand.Text(), after: after}
	r := HistoryRequest{ID: w.edge.id, Channel: m.name, Window: true}
	if after {
		r.After = m.edgeID(true)
	} else {
		r.Before = m.edgeID(false)
	}
	return m.opts.LoadWindow(r)
}

// edgePage applies a correlated edge page. Its rows are what the server
// returned after or before the anchor, which they exclude; rows the window
// holds already are dropped.
func (m *Model) edgePage(msg WindowPageMsg) {
	w := m.win
	if w == nil || w.edge == nil || msg.ID != w.edge.id {
		return
	}
	after := w.edge.after
	w.edge = nil
	if msg.Err != nil {
		m.say(levelWarn, "More history is unavailable: "+oneLine(msg.Err.Error()))
		return
	}
	var rows []Message
	for _, r := range msg.Messages {
		if r.ID != "" && m.indexOfID(r.ID) < 0 {
			r.Delivery = Confirmed
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		if after {
			w.end = true
		} else {
			w.start = true
			m.say(levelInfo, "Beginning of retained history")
		}
		return
	}
	at := m.here()
	if after {
		m.messages = append(m.messages, rows...)
	} else {
		m.messages = append(rows, m.messages...)
	}
	m.touch()
	m.boundWindow()
	m.placeAt(at)
	if after {
		m.mergeWindow()
	}
	m.trimAggregate()
}

// visibleKeys are the messages the last frame showed.
func (m *Model) visibleKeys() map[string]bool {
	keys := map[string]bool{}
	for i := m.scroll; i >= 0 && i < len(m.lastTranscript) && i < m.scroll+m.lastBodyHeight; i++ {
		keys[m.lastTranscript[i].key] = true
	}
	return keys
}

// windowEvictOne drops one row of the shown window through the list's own
// eviction: from the edge farther from the reading position, never a row
// on screen or the anchored one (an offscreen pick is released). Dropping
// an edge row re-arms paging at that edge. It reports false when no row may
// go.
func (m *Model) windowEvictOne(keep map[string]bool) bool {
	n := len(m.messages)
	if m.win == nil || m.win.swapped || n <= 1 {
		return false
	}
	m.captureAnchor()
	at := n / 2
	if m.anchor != nil {
		if i := m.indexOf(m.anchor.key); i >= 0 {
			at = i
		}
	}
	edges := []int{0, n - 1}
	if at < n-1-at {
		edges = []int{n - 1, 0}
	}
	for _, i := range edges {
		key := messageKey(m.messages[i])
		if keep[key] || m.anchor != nil && key == m.anchor.key {
			continue
		}
		m.evict(i)
		if i == 0 {
			m.win.start = false
		} else {
			m.win.end = false
		}
		if e := m.win.edge; e != nil && e.after == (i != 0) {
			// Its page would continue from the row just dropped and leave a
			// hole: the read is void, and the next continues from the new edge.
			m.win.edge = nil
		}
		return true
	}
	return false
}

// boundWindow keeps the shown window within one view's display bounds.
func (m *Model) boundWindow() {
	m.captureAnchor()
	keep := m.visibleKeys()
	for !listFits(m.messages, nil) && m.windowEvictOne(keep) {
	}
}

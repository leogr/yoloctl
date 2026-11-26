package chatui

import (
	"crypto/rand"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// backEntry is the view a jump left, identified by messages, not rows: Esc
// returns to it. A window source keeps only its anchor's ID; going back to
// a window replaced since fetches the page around it again.
type backEntry struct {
	channel string
	filter  *messageFilter
	focus   focusMode
	at      position
	window  bool
	seq     uint64 // identifies the entry while a return to it is fetched
}

// snapshot is the visible view as a back entry.
func (m *Model) snapshot() backEntry {
	return backEntry{channel: m.name, filter: m.filter, focus: m.focus, at: m.here(), window: m.win != nil}
}

// pushBack records where a jump started.
func (m *Model) pushBack(e backEntry) {
	m.backSeq++
	e.seq = m.backSeq
	m.back = append(m.back, e)
}

// dropBack forgets where jumps started, and a return to one being fetched.
func (m *Model) dropBack() {
	m.back = nil
	if r := m.reference; r != nil && r.back != nil {
		m.clearReference()
	}
}

// goBack returns to the last back entry. It reports false with nothing to
// go back to. An entry stays until the return applies: a return that must
// fetch its page leaves the view as it is until that page arrives.
func (m *Model) goBack() (tea.Cmd, bool) {
	if len(m.back) == 0 {
		return nil, false
	}
	e := m.back[len(m.back)-1]
	v := m.views[strings.ToLower(e.channel)]
	id := e.at.anchorID
	if id == "" {
		id = e.at.selectedID
	}
	switch {
	case v == nil:
		m.back = m.back[:len(m.back)-1]
		return nil, true
	case !e.window || e.at.follow || v.holds(id) || v.holdsKey(e.at.anchorKey):
		m.back = m.back[:len(m.back)-1]
		if v != m.cur {
			m.switchTo(v.name)
			m.clearInfo()
		}
		if !e.window || e.at.follow || m.indexOf(e.at.anchorKey) < 0 && m.indexOfID(id) < 0 {
			m.endWindow() // the place is in the live list
		}
		m.restore(e)
		return nil, true
	case id == "" || m.opts.LoadWindow == nil:
		// Nothing to fetch it with: its earlier place is gone.
		m.back = m.back[:len(m.back)-1]
		return nil, true
	case !m.connected || v.deleted || v.membership != "joined":
		m.say(levelInfo, "Can't go back to that message until "+v.name+" is joined and connected")
		return nil, true
	}
	r := &referenceLookup{id: rand.Text(), target: id, back: &e}
	m.reference = r
	m.say(levelInfo, "Going back…")
	return m.requestPage(r, v.name), true
}

// restore puts the shown list back at e: its filter first, then the
// reading position, selection and focus.
func (m *Model) restore(e backEntry) {
	if m.filter != e.filter {
		m.applyFilter(e.filter)
	}
	m.focus = e.focus
	if !m.placeAt(e.at) && e.at.follow {
		m.follow = true
	}
	if m.follow {
		m.unread, m.unreadMentions = 0, 0
	}
	if m.focus == focusMessages && !m.picked {
		m.focus = focusComposer
	}
}

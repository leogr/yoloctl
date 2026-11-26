package chatui

import (
	"crypto/rand"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ReferenceLookupMsg completes one request. Empty Message.ID and nil Err
// mean a definite miss in Channel.
type ReferenceLookupMsg struct {
	ID, Channel string
	Message     Message
	Err         error
}

// referenceLookup is the one jump being fetched: first the exact search of
// the joined channels, then the page around the target in the channel that
// has it. Nothing changes on screen until that page arrives complete.
type referenceLookup struct {
	id, target string
	channels   []string // current request first, then the rest of the snapshot
	// page and pageChannel name the page request once the search found it.
	page, pageChannel string
	// origin is the view the click left, recorded when the jump completes;
	// back is set instead when the fetch returns to a back entry.
	origin backEntry
	back   *backEntry
}

const (
	noticeOpening = "Opening that message…"
	noticeMissing = "That message is no longer in the history of your joined channels"
)

// clearReference cancels the fetch in progress, if any, and its line.
func (m *Model) clearReference() {
	if m.reference != nil {
		m.clearInfo()
	}
	m.reference = nil
}

func (m *Model) lookupReference(target string) tea.Cmd {
	switch {
	case m.reference != nil:
		m.say(levelInfo, "Still opening the previous message")
		return nil
	case m.opts.LookupReference == nil || m.opts.LoadWindow == nil:
		m.say(levelInfo, "That message isn't in this chat view")
		return nil
	case !m.connected:
		m.say(levelInfo, "Can't open that message while the chat is disconnected")
		return nil
	case m.deleted || m.membership != "joined":
		m.say(levelInfo, "Can't open that message until you join a channel")
		return nil
	}
	r := &referenceLookup{id: rand.Text(), target: target, channels: []string{m.name}, origin: m.snapshot()}
	for _, name := range m.order {
		if v := m.views[name]; v != nil && v != m.cur && v.membership == "joined" && !v.deleted {
			r.channels = append(r.channels, name)
		}
	}
	m.reference = r
	m.say(levelInfo, noticeOpening)
	return m.nextReference()
}

func (m *Model) nextReference() tea.Cmd {
	r := m.reference
	if len(r.channels) == 0 {
		m.reference = nil
		m.say(levelInfo, noticeMissing)
		return nil
	}
	channel := r.channels[0]
	if v := m.views[channel]; v == nil || v.deleted || v.membership != "joined" {
		m.reference = nil
		m.say(levelWarn, "Couldn't open that message: "+channel+" is no longer joined")
		return nil
	}
	return m.opts.LookupReference(HistoryRequest{ID: r.id, Channel: channel, Lookup: r.target})
}

func (m *Model) referenceResult(msg ReferenceLookupMsg) tea.Cmd {
	r := m.reference
	if r == nil || r.page != "" || msg.ID != r.id || len(r.channels) == 0 || msg.Channel != r.channels[0] {
		return nil
	}
	if msg.Err == nil && msg.Message.ID == "" {
		r.channels = r.channels[1:]
		return m.nextReference()
	}
	switch {
	case msg.Err != nil:
		m.reference = nil
		m.say(levelWarn, "Couldn't open that message in "+msg.Channel+": "+oneLine(msg.Err.Error()))
		return nil
	case msg.Message.ID != r.target || msg.Message.Channel != msg.Channel:
		m.reference = nil
		m.say(levelWarn, "Couldn't open that message: "+msg.Channel+" returned a different record")
		return nil
	}
	return m.requestPage(r, msg.Channel)
}

// requestPage asks for the page around r.target in channel.
func (m *Model) requestPage(r *referenceLookup, channel string) tea.Cmd {
	r.page, r.pageChannel = rand.Text(), channel
	return m.opts.LoadWindow(HistoryRequest{ID: r.page, Channel: channel, Around: r.target, Window: true})
}

// windowPage routes a completed page: the pending jump's, else a window's
// edge page.
func (m *Model) windowPage(msg WindowPageMsg) {
	if r := m.reference; r != nil && r.page != "" && msg.ID == r.page && strings.EqualFold(msg.Channel, r.pageChannel) {
		m.openPage(r, msg)
		return
	}
	if v := m.views[strings.ToLower(msg.Channel)]; v != nil && v.win != nil && v.win.edge != nil && v.win.edge.id == msg.ID {
		m.within(v.name, func() { m.edgePage(msg) })
	}
}

// openPage completes a jump with the page around its target: the channel
// that has it is shown with the target selected, and the view the click
// left becomes the back entry. A page without the exact target, a failure
// or a channel no longer joined changes nothing.
func (m *Model) openPage(r *referenceLookup, msg WindowPageMsg) {
	m.reference = nil
	m.clearInfo()
	if r.back != nil && (len(m.back) == 0 || m.back[len(m.back)-1].seq != r.back.seq) {
		return // the places to go back to changed meanwhile
	}
	if msg.Err != nil {
		m.say(levelWarn, "Couldn't open that message: "+oneLine(msg.Err.Error()))
		return
	}
	if !slices.ContainsFunc(msg.Messages, func(x Message) bool { return x.ID == r.target }) {
		m.say(levelInfo, noticeMissing)
		return
	}
	v := m.views[strings.ToLower(msg.Channel)]
	if v == nil || v.deleted || v.membership != "joined" {
		m.say(levelWarn, "Couldn't open that message: "+msg.Channel+" is no longer joined")
		return
	}
	if v != m.cur {
		m.switchTo(v.name)
		m.clearInfo()
	}
	page := slices.Clone(msg.Messages)
	for i := range page {
		page[i].Channel, page[i].Delivery = v.name, Confirmed
	}
	if rows, ok := mergeable(page, v.liveMessages()); ok {
		gone := m.closeWindow()
		m.prependLive(rows)
		m.carryRows(gone, m.messages)
		m.release(gone)
	} else {
		m.showWindow(page)
	}
	if r.back != nil {
		m.back = m.back[:len(m.back)-1] // the return applies only now
		m.restore(*r.back)
	} else {
		m.selectLoaded(r.target)
		m.pushBack(r.origin)
	}
	if m.win != nil {
		m.boundWindow()
	}
	m.trimAggregate()
}

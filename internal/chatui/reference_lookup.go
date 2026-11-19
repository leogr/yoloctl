package chatui

import (
	"crypto/rand"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// ReferenceLookupMsg completes one request. Empty Message.ID and nil Err
// mean a definite miss in Channel.
type ReferenceLookupMsg struct {
	ID, Channel string
	Message     Message
	Err         error
}

type referenceLookup struct {
	id, target string
	channels   []string // current request first, then the rest of the snapshot
}

func (m *Model) clearReference() {
	m.reference = nil
	if m.overlay != nil && m.overlay.kind == overlayReference {
		m.overlay = nil
	}
}

func (m *Model) lookupReference(target string) tea.Cmd {
	if m.opts.LookupReference == nil || !m.connected || m.deleted || m.membership != "joined" {
		m.say(levelInfo, "That message is not loaded in any open channel; history lookup requires a joined channel and a connection")
		return nil
	}
	if m.reference != nil {
		m.say(levelInfo, "A message lookup is already in progress")
		return nil
	}
	r := &referenceLookup{id: rand.Text(), target: target, channels: []string{m.name}}
	for _, name := range m.order {
		if v := m.views[name]; v != nil && v != m.cur && v.membership == "joined" && !v.deleted {
			r.channels = append(r.channels, name)
		}
	}
	m.reference = r
	return m.nextReference()
}

func (m *Model) nextReference() tea.Cmd {
	r := m.reference
	if len(r.channels) == 0 {
		m.reference = nil
		m.say(levelInfo, "That message is no longer retained in the joined channels")
		return nil
	}
	channel := r.channels[0]
	if v := m.views[channel]; v == nil || v.deleted || v.membership != "joined" {
		m.reference = nil
		m.say(levelWarn, "Message lookup incomplete: "+channel+" is no longer joined")
		return nil
	}
	m.say(levelInfo, "Looking up the message in "+channel+"…")
	return m.opts.LookupReference(HistoryRequest{ID: r.id, Channel: channel, Lookup: r.target})
}

func (m *Model) referenceResult(msg ReferenceLookupMsg) tea.Cmd {
	r := m.reference
	if r == nil || msg.ID != r.id || msg.Channel != r.channels[0] {
		return nil
	}
	switch {
	case msg.Err != nil:
		m.say(levelWarn, "Message lookup incomplete in "+msg.Channel+": "+oneLine(msg.Err.Error()))
	case msg.Message.ID == "":
		r.channels = r.channels[1:]
		return m.nextReference()
	case msg.Message.ID != r.target || msg.Message.Channel != msg.Channel:
		m.say(levelWarn, "Message lookup returned a different record in "+msg.Channel)
	case m.overlay == nil:
		m.clearInfo()
		m.openOverlay(overlayState{kind: overlayReference, quote: msg.Message})
	}
	m.reference = nil
	return nil
}

func (m *Model) referenceRows(msg Message, width int) []string {
	author := oneLine(m.identify(msg.Nick, msg.Account).name)
	if !msg.Time.IsZero() {
		author += " · " + msg.Time.Local().Format("2006-01-02 15:04")
	}
	rows := wrapText(m.widthMethod, author, width, spaceview.Style{FG: m.theme.Dim})
	rows = append(rows, "")
	for line := range strings.SplitSeq(plain(msg.Text), "\n") {
		if line == "" {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, wrapText(m.widthMethod, markdownview.ExpandTabs(m.widthMethod, line), width, spaceview.Style{FG: m.theme.Text})...)
	}
	return rows
}

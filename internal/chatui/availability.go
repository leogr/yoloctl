package chatui

import (
	"crypto/rand"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// AvailabilityMsg is confirmed connection-scoped state, independent of the
// currently selected channel. Unknown is not a successful /back.
type AvailabilityMsg struct{ State, Reason string }

func (m *Model) availabilityCommand(text string, fields []string) (tea.Cmd, bool) {
	kind := strings.TrimPrefix(fields[0], "/")
	var reason *string
	if kind == "back" {
		if len(fields) != 1 {
			m.say(levelWarn, "Usage: /back")
			return nil, true
		}
	} else {
		value := strings.TrimSpace(strings.TrimPrefix(text, "/away"))
		if value == "" {
			value = "away"
		}
		if !utf8.ValidString(value) || len(value) > 200 || strings.ContainsAny(value, "\x00\r\n") {
			m.say(levelWarn, "Away reason must be valid UTF-8, at most 200 bytes, without CR, LF or NUL")
			return nil, true
		}
		reason = &value
	}
	if !m.controlReady() {
		return nil, true
	}
	action := Action{ID: rand.Text(), Kind: kind, Reason: reason}
	m.pending = &pendingAction{Action: action, origin: m.name, draftText: m.editor.text, draftRevision: m.editor.revision}
	return m.opts.Control(action), true
}

func (m *Model) availability(msg AvailabilityMsg) {
	if !m.connected {
		return
	}
	if msg.State != "away" && msg.State != "back" && msg.State != "unknown" {
		return
	}
	m.ownState, m.ownReason = msg.State, msg.Reason
	if msg.State != "away" {
		m.ownReason = ""
	}
	m.eachView(func() {
		for i := range m.members {
			member := &m.members[i]
			if m.opts.Account != "" && member.Account == m.opts.Account && member.Nick == m.opts.Nick {
				member.Away = m.ownState == "away"
				member.AwayReason = m.ownReason
			}
		}
		m.touch()
	})
}

func (m *Model) availabilitySummary() string {
	if !m.connected {
		return ""
	}
	if m.pending != nil && (m.pending.Kind == "away" || m.pending.Kind == "back") {
		if m.pending.uncertain {
			return "unknown"
		}
		return "pending"
	}
	if m.ownState == "away" {
		return "away"
	}
	if m.ownState == "unknown" {
		return "unknown"
	}
	return ""
}
func (m *Model) availabilityDetail() string {
	if !m.connected {
		return "unavailable (disconnected)"
	}
	if summary := m.availabilitySummary(); summary == "pending" || summary == "unknown" {
		return summary
	}
	if m.ownState == "away" {
		return "away: " + oneLine(m.ownReason)
	}
	return "back"
}

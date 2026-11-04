package chatui

import (
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

type presenceKind int

const (
	presenceJoin presenceKind = iota
	presenceLeave
	presenceRename
	presenceOp
	presenceDeop
	presenceVoice
	presenceDevoice
	presenceTopic // names[0] is "setter\x00topic"
	presenceSelf  // names[0] is the whole notice, about this connection
	presenceGap   // names[0] describes lost continuity
	// presenceGuide is guidance that must stay readable, such as a host
	// operation to inspect: unlike other notices it wraps to the width.
	presenceGuide
)

// presenceEvent is a visible room change: joins, departures, nick and role
// changes. It is display-only, follows the latest message at the time, and
// is never part of a copy selection.
type presenceEvent struct {
	after string // key of the latest message when it happened
	at    time.Time
	kind  presenceKind
	names []string
}

// maxPresenceEvents bounds the retained notices.
const maxPresenceEvents = 200

// presenceWindow folds a burst of the same change into one notice.
const presenceWindow = 10 * time.Second

// observeMembers derives notices from consecutive roster snapshots. The
// first snapshot after connecting is the existing room, not a change.
func (m *Model) observeMembers(next []Member) {
	prev := m.members
	m.members = next
	if prev == nil {
		return
	}
	byNick := func(ms []Member) map[string]Member {
		out := map[string]Member{}
		for _, mem := range ms {
			out[strings.ToLower(mem.Nick)] = mem
		}
		return out
	}
	old, cur := byNick(prev), byNick(next)
	// One authenticated account may hold several connections (a human with
	// two chat views). Opening or closing one while another stays is neither
	// an arrival nor a departure; the roster still lists each connection.
	stays := func(account string) bool {
		if account == "" {
			return false
		}
		for _, mem := range prev {
			if _, still := cur[strings.ToLower(mem.Nick)]; still && mem.Account == account {
				return true
			}
		}
		return false
	}
	renamed := map[string]bool{}
	for _, mem := range next {
		if _, ok := old[strings.ToLower(mem.Nick)]; ok {
			continue
		}
		name := m.identify(mem.Nick, mem.Account).name
		if mem.Account != "" {
			for _, was := range prev {
				if was.Account == mem.Account && !renamed[strings.ToLower(was.Nick)] {
					if _, still := cur[strings.ToLower(was.Nick)]; !still {
						renamed[strings.ToLower(was.Nick)] = true
						m.addPresence(presenceRename, oneLine(was.Nick)+" → "+oneLine(mem.Nick))
						name = ""
						break
					}
				}
			}
		}
		if name != "" && !stays(mem.Account) {
			m.addPresence(presenceJoin, name)
		}
	}
	for _, was := range prev {
		folded := strings.ToLower(was.Nick)
		if _, ok := cur[folded]; !ok && !renamed[folded] && !stays(was.Account) {
			m.addPresence(presenceLeave, m.identify(was.Nick, was.Account).name)
		}
	}
	for _, mem := range next {
		was, ok := old[strings.ToLower(mem.Nick)]
		if !ok {
			continue
		}
		name := m.identify(mem.Nick, mem.Account).name
		switch {
		case mem.Op && !was.Op:
			m.addPresence(presenceOp, name)
		case !mem.Op && was.Op:
			m.addPresence(presenceDeop, name)
		}
		switch {
		case mem.Voice && !was.Voice:
			m.addPresence(presenceVoice, name)
		case !mem.Voice && was.Voice:
			m.addPresence(presenceDevoice, name)
		}
	}
}

func (m *Model) addPresence(kind presenceKind, name string) {
	after := ""
	if n := len(m.messages); n > 0 {
		after = messageKey(m.messages[n-1])
	}
	now := time.Now()
	if n := len(m.presence); n > 0 {
		last := &m.presence[n-1]
		if last.kind == kind && last.after == after && now.Sub(last.at) < presenceWindow && kind != presenceRename && kind < presenceTopic {
			last.names = append(last.names, name)
			last.at = now
			return
		}
	}
	m.presence = append(m.presence, presenceEvent{after: after, at: now, kind: kind, names: []string{name}})
	// Drop notices whose message left the display, then bound the rest.
	kept := m.presence[:0]
	for _, ev := range m.presence {
		if ev.after == "" || m.indexOf(ev.after) >= 0 {
			kept = append(kept, ev)
		}
	}
	m.presence = kept
	if len(m.presence) > maxPresenceEvents {
		m.presence = append([]presenceEvent(nil), m.presence[len(m.presence)-maxPresenceEvents:]...)
	}
}

func (ev presenceEvent) text() (string, string) {
	who := strings.Join(ev.names, ", ")
	if len(ev.names) > 4 {
		who = strings.Join(ev.names[:3], ", ") + " and " + strconv.Itoa(len(ev.names)-3) + " others"
	}
	switch ev.kind {
	case presenceJoin:
		return "→", who + " joined"
	case presenceLeave:
		return "←", who + " left"
	case presenceRename:
		return "~", who
	case presenceOp:
		return "@", who + " became an operator"
	case presenceDeop:
		return "@", who + " is no longer an operator"
	case presenceVoice:
		return "+", who + " was given voice"
	case presenceSelf, presenceGuide:
		return "•", ev.names[0]
	case presenceGap:
		return "!", "Continuity unknown: " + ev.names[0]
	case presenceTopic:
		by, topic, _ := strings.Cut(ev.names[0], "\x00")
		if by == "" {
			by = "Someone"
		}
		if topic == "" {
			return "#", by + " cleared the topic"
		}
		return "#", by + " set the topic: " + topic
	}
	return "+", who + " lost voice"
}

// presenceRows renders the notices that followed the message key as event
// rows of their own, after a spacer that separates them from that message.
// Each is one row; guidance wraps within width, each continuation anchored
// by its byte offset. A message filter keeps only transport notices.
func (m *Model) presenceRows(key string, lines int, indent []run, width int) []transcriptRow {
	t := m.theme
	var rows []transcriptRow
	n := 0
	for _, ev := range m.presence {
		if ev.after != key || m.filter != nil && !ev.kind.isTransport() {
			continue
		}
		if n == 0 && key != "" {
			rows = append(rows, transcriptRow{key: key, line: lines + lineEvents, kind: rowSpacer})
		}
		icon, text := ev.text()
		st := spaceview.Style{FG: t.Dim}
		iconStyle := spaceview.Style{FG: t.OK, Bold: true}
		switch ev.kind {
		case presenceLeave:
			iconStyle.FG = t.Faint
		case presenceOp, presenceDeop:
			iconStyle.FG = t.OperatorFG
		case presenceRename, presenceTopic, presenceSelf:
			iconStyle.FG = t.Label
		case presenceGap, presenceGuide:
			iconStyle.FG = t.Capability
		}
		at := deco("  "+ev.at.Local().Format("15:04"), spaceview.Style{FG: t.Faint})
		if ev.kind != presenceGuide {
			runs := append(slicesCloneRuns(indent), deco(icon+" ", iconStyle), deco(text, st), at)
			rows = append(rows, transcriptRow{key: key, line: lines + linePresence + n, kind: rowEvent, runs: runs})
			n++
			continue
		}
		st.FG = t.Text
		// The icon takes two cells; one more keeps text off the panel divider.
		ranges := wrapRanges(m.widthMethod, text, max(width-runsWidth(m.widthMethod, indent)-3, 8), true)
		for j, r := range ranges {
			lead := deco(icon+" ", iconStyle)
			if j > 0 {
				lead = deco("  ", spaceview.Style{})
			}
			runs := append(slicesCloneRuns(indent), lead, deco(strings.TrimRight(text[r[0]:r[1]], " "), st))
			if j == len(ranges)-1 && runsWidth(m.widthMethod, runs)+runsWidth(m.widthMethod, []run{at}) < width {
				runs = append(runs, at)
			}
			rows = append(rows, transcriptRow{key: key, line: lines + linePresence + n, offset: r[0], kind: rowEvent, runs: runs})
		}
		n++
	}
	return rows
}

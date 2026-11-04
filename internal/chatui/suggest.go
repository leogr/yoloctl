package chatui

import (
	"fmt"
	"slices"
	"strings"
)

// suggestion is one row of the popup that opens while typing an @mention
// or a leading /command. insert replaces the word being typed.
type suggestion struct {
	insert, label, detail string
	member                *Member
	group                 bool // @all/@here: notifies everyone
	command               *command
}

type suggestState struct {
	start, end int
	word       string
	items      []suggestion
	index      int
}

// command is a slash command. Only an exact known command, as the whole
// draft, is intercepted; any other text starting with / is sent as typed.
type command struct {
	name, args, detail string
}

var commands = []command{
	{"/help", "", "Keys, actions and what each one targets"},
	{"/settings", "", "Nickname, panels and host policies"},
	{"/nick", " NAME", "Change your nickname (saves, then reconnects)"},
	{"/channels", "", "Switch channel; see unread and membership"},
	{"/join", " NAME", "Follow a channel and switch to it"},
	{"/part", " [NAME]", "Leave a channel (its draft and history stay)"},
	{"/topic", " [set TEXT | clear]", "Show, set or clear this channel's topic"},
	{"/participants", "", "Show or hide the participants panel"},
	{"/filter", " [me | all | NAME …]", "Show only messages from or mentioning someone"},
	{"/thread", "", "Show only the selected message's thread"},
	{"/source", "", "Switch between Markdown and source view"},
	{"/quit", "", "Leave the chat"},
}

// wordAt returns the token around the cursor: [start, end) with no spaces.
func wordAt(text string, cursor int) (int, int) {
	start, end := cursor, cursor
	for start > 0 && !strings.ContainsRune(" \t\n", rune(text[start-1])) {
		start--
	}
	for end < len(text) && !strings.ContainsRune(" \t\n", rune(text[end])) {
		end++
	}
	return start, end
}

// refreshSuggest recomputes the popup after every edit or cursor move. It
// closes when nothing matches, when the typed word already is the only
// match (so Enter sends), and while the user dismissed this exact word.
func (m *Model) refreshSuggest() {
	e := &m.editor
	m.suggest = nil
	if m.pasting || m.focus != focusComposer {
		return
	}
	start, end := e.word()
	word := e.text[start:end]
	if word == "" || word == m.dismissed {
		return
	}
	m.dismissed = ""
	typed := strings.ToLower(e.text[start:e.cursor])
	var items []suggestion
	switch {
	case strings.HasPrefix(word, "@"):
		// A trusted account is one row under its saved nick, however many
		// connections it has; an unverified session keeps its own nick.
		prefix := strings.TrimPrefix(typed, "@")
		accounts := map[string]bool{}
		for _, member := range sortedMembers(m.members) {
			nick := m.mentionToken(member.Nick, member.Account)
			who := m.identify(member.Nick, member.Account)
			// Canonical names remain input aliases, never display labels.
			name := m.opts.Identities[member.Account].Name
			if !strings.HasPrefix(strings.ToLower(nick), prefix) && (!who.trusted || !strings.HasPrefix(strings.ToLower(name), prefix)) {
				continue
			}
			if who.trusted && accounts[member.Account] {
				continue
			}
			accounts[member.Account] = who.trusted
			mem := member
			// An account outside the identity map gets no kind.
			var detail string
			switch {
			case !who.trusted:
			case who.me:
				detail = "you"
			case who.human:
				detail = "human"
			default:
				detail = "agent"
			}
			if who.trusted && m.accountAway(member.Account) || !who.trusted && member.Away {
				detail = joinDetail(detail, "away")
			}
			item := suggestion{insert: "@" + nick + " ", label: "@" + nick, detail: detail, member: &mem}
			if i := slices.IndexFunc(items, func(s suggestion) bool { return s.insert == item.insert }); i >= 0 {
				// One row per @name; a trusted account keeps its own row.
				if who.trusted && !m.identify(items[i].member.Nick, items[i].member.Account).trusted {
					items[i] = item
				}
				continue
			}
			items = append(items, item)
		}
		for _, group := range []string{"all", "here"} {
			if strings.HasPrefix(group, prefix) {
				items = append(items, suggestion{insert: "@" + group + " ", label: "@" + group, detail: "notifies everyone in the room", group: true})
			}
		}
	case strings.HasPrefix(word, "#"):
		// Channel names are inserted as text; nothing is joined.
		prefix := strings.TrimPrefix(typed, "#")
		for _, name := range m.order {
			if strings.HasPrefix(strings.TrimPrefix(name, "#"), prefix) {
				detail := "channel"
				if v := m.views[name]; v.unread > 0 {
					detail = fmt.Sprintf("%d new", v.unread)
				}
				items = append(items, suggestion{insert: name + " ", label: name, detail: detail})
			}
		}
	case start == 0 && strings.HasPrefix(word, "/") && !strings.HasPrefix(word, "//"):
		for i := range commands {
			c := &commands[i]
			if strings.HasPrefix(c.name, typed) {
				insert := c.name
				if c.args != "" {
					insert += " "
				}
				items = append(items, suggestion{insert: insert, label: c.name + c.args, detail: c.detail, command: c})
			}
		}
	default:
		return
	}
	if len(items) == 0 || len(items) == 1 && strings.EqualFold(strings.TrimSpace(items[0].insert), word) {
		return
	}
	if len(items) > 6 {
		items = items[:6]
	}
	m.suggest = &suggestState{start: start, end: end, word: word, items: items}
}

// refreshOpenSuggest refreshes labels after an identity update without changing
// the selected account or command. A vanished item leaves the old index clamped
// to the remaining list. Ordinary edits still start at the first match.
func (m *Model) refreshOpenSuggest() {
	previous := m.suggest
	if previous == nil {
		return
	}
	m.refreshSuggest()
	current := m.suggest
	if current == nil {
		return
	}
	current.index = max(0, min(previous.index, len(current.items)-1))
	if previous.index < 0 || previous.index >= len(previous.items) {
		return
	}
	selected := previous.items[previous.index]
	for i, item := range current.items {
		matches := item.insert == selected.insert
		switch {
		case selected.member != nil && selected.member.Account != "":
			matches = item.member != nil && item.member.Account == selected.member.Account
		case selected.command != nil:
			matches = item.command != nil && item.command.name == selected.command.name
		}
		if matches {
			current.index = i
			return
		}
	}
}

// accountAway reports whether every connection of account is away.
func (m *Model) accountAway(account string) bool {
	away := false
	for _, mem := range m.members {
		if mem.Account == account {
			if !mem.Away {
				return false
			}
			away = true
		}
	}
	return away
}

// acceptSuggestion replaces the typed word with the highlighted item. A
// command without arguments chosen with Enter runs at once.
func (m *Model) acceptSuggestion(i int, run bool) {
	s := m.suggest
	if s == nil || i < 0 || i >= len(s.items) {
		return
	}
	item := s.items[i]
	e := &m.editor
	insert := item.insert
	// Keep a single space when the text after the word already has one.
	if strings.HasSuffix(insert, " ") && s.end < len(e.text) && e.text[s.end] == ' ' {
		insert = strings.TrimSuffix(insert, " ")
		m.edit(e.replace(s.start, s.end, insert))
		e.cursor = min(s.start+len(insert)+1, len(e.text))
	} else {
		m.edit(e.replace(s.start, s.end, insert))
	}
	m.suggest = nil
	// Run only when the command is the whole draft; anything else typed
	// around it stays text for the user to finish.
	if run && item.command != nil && item.command.args == "" && strings.TrimSpace(e.text) == item.command.name {
		m.pendingRun = true
	}
	m.refreshSuggest()
}

// joinDetail joins two parts of a suggestion's detail, leaving out an
// empty one.
func joinDetail(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " · " + b
}

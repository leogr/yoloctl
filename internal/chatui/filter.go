package chatui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// messageFilter keeps a channel's transcript to the messages that any of
// the selected authenticated accounts sent or that mention one of them, and
// the loaded replies to those (see shown), or else to one thread. It hides
// rows only: messages, IDs, receipts, drafts, the reply and the history
// cursor stay.
type messageFilter struct {
	// picks are the selected accounts, each once, in the order chosen.
	picks []filterPick
	// thread, instead of picks, is the server ID of the top-most loaded
	// message of the chosen thread when it was chosen (see threadTop).
	thread string
	// hiddenNew counts live arrivals the filter hid since it was set.
	hiddenNew int
}

// filterPick is one selected account; nick names it when the identity map
// does not know the account.
type filterPick struct{ account, nick string }

func (f *messageFilter) has(account string) bool {
	return slices.ContainsFunc(f.picks, func(p filterPick) bool { return p.account == account })
}

// nameBindings map case-folded @mention tokens to the account they name.
// The trusted catalog binds account IDs, names and saved nicks. Nicks
// observed with an authenticated account, in any channel's roster or loaded
// messages, bind only tokens the catalog does not: an unverified session
// cannot take over a trusted name. A token two accounts claim is conflicting
// and names nobody, so a reused or ambiguous alias is never guessed; "" is a
// nick seen only on sessions whose account is unknown. This connection's live
// nick is observed evidence for the local account like any other: an earlier
// use of it by another account still makes it name nobody.
type nameBindings struct {
	catalog, observed map[string]string
}

// conflicting marks a token bound to more than one account.
const conflicting = "\x00"

func bindName(table map[string]string, token, account string) {
	token = strings.ToLower(token)
	if token == "" {
		return
	}
	if old, ok := table[token]; ok && old != account {
		account = conflicting
	}
	table[token] = account
}

func (m *Model) bindings() nameBindings {
	b := nameBindings{catalog: map[string]string{}, observed: map[string]string{}}
	for account, id := range m.opts.Identities {
		if account == "" {
			continue
		}
		bindName(b.catalog, account, account)
		bindName(b.catalog, id.Name, account)
		bindName(b.catalog, id.Nick, account)
	}
	if m.opts.Account != "" {
		bindName(b.observed, m.opts.Nick, m.opts.Account)
	}
	for _, name := range m.order {
		v := m.views[name]
		for _, mem := range v.members {
			bindName(b.observed, mem.Nick, mem.Account)
		}
		for _, msg := range v.messages {
			if msg.Account == "" && msg.ClientID != "" {
				continue // my send, not echoed yet: shown under my live nick
			}
			bindName(b.observed, msg.Nick, msg.Account)
		}
	}
	return b
}

type nameState int

const (
	nameUnbound nameState = iota
	nameUnique
	nameConflict
)

// resolve names the account one mention token binds: unique, conflicting,
// or unbound (also when only sessions with unknown accounts use it).
func (b nameBindings) resolve(token string) (string, nameState) {
	token = strings.ToLower(token)
	table := b.observed
	if _, ok := b.catalog[token]; ok {
		table = b.catalog
	}
	switch a, ok := table[token]; {
	case !ok || a == "":
		return "", nameUnbound
	case a == conflicting:
		return "", nameConflict
	default:
		return a, nameUnique
	}
}

// selfNames lists, NUL-separated and sorted, the tokens that mention the
// local user: every token that names the local account, or, while that
// account is unknown, the live nick. Cached bodies are keyed by it.
func (m *Model) selfNames() string {
	names := map[string]bool{}
	if m.opts.Account == "" && m.opts.Nick != "" {
		names[strings.ToLower(m.opts.Nick)] = true
	}
	if m.opts.Account != "" {
		b := m.bindings()
		for _, table := range []map[string]string{b.catalog, b.observed} {
			for token := range table {
				if a, state := b.resolve(token); state == nameUnique && a == m.opts.Account {
					names[token] = true
				}
			}
		}
	}
	return strings.Join(slices.Sorted(maps.Keys(names)), "\x00")
}

// mentionToken is how a participant is @mentioned: a trusted account by its
// saved nick, so every connection of one human is the same @name; anyone
// else by the nick their session actually uses.
func (m *Model) mentionToken(nick, account string) string {
	if id, ok := m.opts.Identities[account]; ok && account != "" && id.Nick != "" {
		return oneLine(id.Nick)
	}
	return oneLine(nick)
}

// isTransport reports the notices a filter never hides: lost continuity,
// this connection's own membership and guidance to act on.
func (k presenceKind) isTransport() bool {
	return k == presenceGap || k == presenceSelf || k == presenceGuide
}

// matches reports whether the visible view's filter keeps msg by itself: a
// selected account sent or is mentioned in it. A send that is pending,
// refused or uncertain always stays, so its outcome is never hidden. A
// thread keeps nothing by itself: shown walks to its top.
func (m *Model) matches(msg *Message, b nameBindings) bool {
	f := m.filter
	if f == nil || msg.Delivery == Pending || msg.Delivery == Rejected || msg.Delivery == Uncertain || msg.Error != "" {
		return true
	}
	if f.thread != "" {
		return false
	}
	sender := msg.Account
	if sender == "" && msg.ClientID != "" {
		sender = m.opts.Account
	}
	if sender != "" && f.has(sender) {
		return true
	}
	found := false
	eachMention(msg.Text, func(token string) bool {
		a, state := b.resolve(token)
		found = state == nameUnique && f.has(a)
		return !found
	})
	return found
}

// shown reports, for each of the visible view's messages in one pass,
// whether its filter keeps it: a message it matches, or a reply whose
// parent is loaded in this view with exactly that server ID and is itself
// shown. The reply ID is participant-supplied, so a parent outside the
// loaded messages is never fetched or guessed, and a forged chain (a
// self-reply, a cycle, an unknown ID) shows nothing. A thread keeps its
// top-most loaded message and so every reply under it, all branches; a
// chain whose loaded part ends in a reply to the chosen top, evicted since,
// is still that thread.
func (m *Model) shown(b nameBindings) []bool {
	out := make([]bool, len(m.messages))
	if m.filter == nil {
		for i := range out {
			out[i] = true
		}
		return out
	}
	parent := m.parents()
	thread := m.filter.thread
	top := -1
	if thread != "" {
		top = m.threadTop(parent)
	}
	const (
		_ = iota // not visited yet
		walking
		decided
	)
	state := make([]uint8, len(m.messages))
	var chain []int
	for i := range m.messages {
		chain = chain[:0]
		keep := false
		for j := i; ; {
			if state[j] == decided {
				keep = out[j]
				break
			}
			if state[j] == walking {
				break // a cycle: nothing on it matched
			}
			state[j] = walking
			chain = append(chain, j)
			msg := &m.messages[j]
			if j == top || m.matches(msg, b) {
				keep = true
				break
			}
			p, ok := parent[msg.ReplyTo]
			if msg.ReplyTo == "" || !ok {
				keep = thread != "" && msg.ReplyTo == thread
				break
			}
			j = p
		}
		for _, j := range chain {
			out[j], state[j] = keep, decided
		}
	}
	return out
}

// parents maps each server ID loaded in the visible view to the first
// message with it: the message a reply label names.
func (m *Model) parents() map[string]int {
	parent := make(map[string]int, len(m.messages))
	for i := range m.messages {
		if id := m.messages[i].ID; id != "" {
			if _, dup := parent[id]; !dup {
				parent[id] = i
			}
		}
	}
	return parent
}

// rootOf walks a reply chain up from message i through loaded parents to
// its top-most loaded message. A cycle stops at the first message it would
// visit again.
func (m *Model) rootOf(i int, parent map[string]int) int {
	seen := map[int]bool{i: true}
	for {
		p, ok := parent[m.messages[i].ReplyTo]
		if m.messages[i].ReplyTo == "" || !ok || seen[p] {
			return i
		}
		seen[p] = true
		i = p
	}
}

// threadTop is the index of the thread's top-most loaded message: the
// chosen top, or an ancestor an older page has loaded since. It is -1 once
// the chosen top is no longer loaded.
func (m *Model) threadTop(parent map[string]int) int {
	i, ok := parent[m.filter.thread]
	if !ok {
		return -1
	}
	return m.rootOf(i, parent)
}

// threadMayStartEarlier reports whether the visible thread may continue
// above the loaded messages: its top replies to a message not loaded here,
// or the chosen top itself is no longer loaded.
func (m *Model) threadMayStartEarlier() bool {
	parent := m.parents()
	top := m.threadTop(parent)
	if top < 0 {
		return true
	}
	_, ok := parent[m.messages[top].ReplyTo]
	return m.messages[top].ReplyTo != "" && !ok
}

// reconcile follows identity evidence across channels after an event: a
// roster, message or catalog change in one channel can change whom a token
// names in every other. When the bindings change, every view rebuilds its
// rows from the reading position it showed. In a filtered view, an explicit
// pick that is no longer shown (a confirmed send, a newly conflicting name)
// ends rather than moving to a neighbour, and a reading anchor on a hidden
// message moves to the nearest shown one.
func (m *Model) reconcile() {
	b := m.bindings()
	changed := !maps.Equal(b.catalog, m.bound.catalog) || !maps.Equal(b.observed, m.bound.observed)
	m.bound = b
	m.eachView(func() {
		if changed {
			m.captureAnchor()
			m.touch()
		}
		if m.filter == nil {
			return
		}
		shown := m.shown(b)
		if m.picked && m.selected >= 0 && m.selected < len(m.messages) && !shown[m.selected] {
			m.picked = false
			if m.isCurrent() {
				m.focus = focusComposer
			}
			m.touch()
		}
		if m.anchor != nil && !m.follow {
			if i := m.indexOf(m.anchor.key); i >= 0 && !shown[i] {
				m.anchor = m.shownAnchor(*m.anchor, shown)
				m.follow = m.anchor == nil
			}
		}
	})
}

// shownIndexes lists the visible view's messages its filter keeps.
func (m *Model) shownIndexes() []int {
	out := make([]int, 0, len(m.messages))
	for i, keep := range m.shown(m.bindings()) {
		if keep {
			out = append(out, i)
		}
	}
	return out
}

// arrivalShown reports whether the filter keeps a live arrival just
// appended, as the newest message, with the rest of the loaded thread; one
// the display bounds already evicted is judged by itself.
func (m *Model) arrivalShown(msg Message) bool {
	n := len(m.messages)
	if n == 0 || messageKey(m.messages[n-1]) != messageKey(msg) {
		return m.matches(&msg, m.bindings())
	}
	return m.shown(m.bindings())[n-1]
}

// pickName is how notices, the picker and the scope rows name one
// selected account.
func (m *Model) pickName(p filterPick) string {
	if p.account == m.opts.Account {
		return "you"
	}
	return "@" + m.mentionToken(p.nick, p.account)
}

// filterName names every filtered account, in the order chosen.
func (m *Model) filterName() string {
	names := make([]string, 0, len(m.filter.picks))
	for _, p := range m.filter.picks {
		names = append(names, m.pickName(p))
	}
	return strings.Join(names, ", ")
}

// selfOnly reports whether the visible view's filter is exactly For me.
func (m *Model) selfOnly() bool {
	f := m.filter
	return f != nil && m.opts.Account != "" && len(f.picks) == 1 && f.picks[0].account == m.opts.Account
}

// setFilter shows only account's messages and mentions in the visible
// channel, or every message for "".
func (m *Model) setFilter(account, nick string) {
	var picks []filterPick
	if account != "" {
		picks = []filterPick{{account, nick}}
	}
	m.setFilterPicks(picks)
}

// setFilterPicks shows only the messages any of picks sent or mentions, and
// replies to those, each account once, or every message when there are
// none (see applyFilter).
func (m *Model) setFilterPicks(picks []filterPick) {
	var unique []filterPick
	for _, p := range picks {
		if p.account != "" && !slices.ContainsFunc(unique, func(u filterPick) bool { return u.account == p.account }) {
			unique = append(unique, p)
		}
	}
	if len(unique) == 0 && m.filter == nil {
		return
	}
	if len(unique) == 0 {
		m.applyFilter(nil)
		m.say(levelInfo, "Showing all messages")
		return
	}
	m.applyFilter(&messageFilter{picks: unique})
	m.say(levelInfo, "Showing only messages from or mentioning "+m.filterName()+", and replies to those · click ✕ for all messages")
}

// setThread shows only the thread of message i: its top-most loaded
// message and every loaded reply under that, all branches, so any message
// of a thread shows the same messages. It replaces any other filter.
func (m *Model) setThread(i int) {
	top := m.rootOf(i, m.parents())
	if m.messages[top].ID == "" {
		m.say(levelWarn, "Not confirmed yet; its thread shows once the room echoes it")
		return
	}
	m.applyFilter(&messageFilter{thread: m.messages[top].ID})
	m.say(levelInfo, "Showing only the thread of "+m.messageName(&m.messages[top])+", with all its replies · click ✕ for all messages")
}

// applyFilter replaces the visible view's filter. The reading position
// stays on the nearest message still shown; a text highlight clears, and a
// picked message the filter hides is no longer the target of actions.
func (m *Model) applyFilter(f *messageFilter) {
	m.captureAnchor()
	m.filter = f
	m.sel = nil
	shown := m.shown(m.bindings())
	if m.picked && m.selected >= 0 && m.selected < len(m.messages) && !shown[m.selected] {
		m.picked, m.focus = false, focusComposer
	}
	if m.anchor != nil && !m.follow {
		m.anchor = m.shownAnchor(*m.anchor, shown)
		m.follow = m.anchor == nil
	}
	m.touch()
}

// threadName names the visible thread by its top-most loaded message.
func (m *Model) threadName() string {
	if top := m.threadTop(m.parents()); top >= 0 {
		return "the thread of " + m.messageName(&m.messages[top])
	}
	return "the loaded part of a thread"
}

// shownAnchor keeps a reading anchor on a row the new transcript still has:
// a shown message's own rows, else the header of the next shown message
// (or, past the last, the previous one). Separators and notices depend on
// what is shown around them, so an anchor on one moves to its header.
func (m *Model) shownAnchor(a viewAnchor, shown []bool) *viewAnchor {
	i := m.indexOf(a.key)
	if i < 0 {
		return &a
	}
	if shown[i] {
		if a.line < lineHeader || a.line >= len(m.body(&m.messages[i], max(m.lastTextWidth, 1)).lines)+lineEvents {
			a.line, a.offset = lineHeader, 0
		}
		return &a
	}
	for _, j := range append(rangeIndexes(i+1, len(m.messages), 1), rangeIndexes(i-1, -1, -1)...) {
		if shown[j] {
			return &viewAnchor{key: messageKey(m.messages[j]), line: lineHeader}
		}
	}
	return nil
}

func rangeIndexes(from, to, step int) []int {
	var out []int
	for i := from; i != to; i += step {
		out = append(out, i)
	}
	return out
}

// filterScopeRows head a filtered transcript: what is shown, a click target
// for all messages, and how much history the filter has seen. The filter
// covers loaded messages only; Page Up loads the page before the oldest
// underlying message, whether or not it matched.
func (m *Model) filterScopeRows(shown, width int) []transcriptRow {
	t := m.theme
	button := run{text: " ✕ All messages ", style: spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true}, act: action{kind: actFilter}}
	label := []run{deco("▌ ", spaceview.Style{FG: t.Brand, Bold: true}), deco("Only messages from or mentioning ", spaceview.Style{FG: t.Text}),
		deco(m.filterName(), spaceview.Style{FG: t.Brand, Bold: true}), deco(", and replies to those", spaceview.Style{FG: t.Text})}
	open := false
	if m.filter.thread != "" {
		label = []run{deco("▌ ", spaceview.Style{FG: t.Brand, Bold: true}), deco("Only ", spaceview.Style{FG: t.Text}),
			deco(m.threadName(), spaceview.Style{FG: t.Brand, Bold: true}), deco(", with all its replies", spaceview.Style{FG: t.Text})}
		open = m.threadMayStartEarlier()
	}
	label = fitRuns(m.widthMethod, label, max(width-runsWidth(m.widthMethod, []run{button})-1, 4))
	pad := deco(strings.Repeat(" ", max(width-runsWidth(m.widthMethod, label)-runsWidth(m.widthMethod, []run{button}), 1)), spaceview.Style{})
	scope := fmt.Sprintf("%d of %d loaded messages", shown, len(m.messages))
	switch {
	case m.olderSkipped > 0:
		scope += " · older history is not shown in this view"
	case m.filter.thread != "" && !open:
		scope += " · from the start of the thread"
	case m.historyEnd:
		scope += " · from the beginning of retained history"
	case m.loadingOlder:
		scope += " · loading older history…"
	case len(m.messages) > maxMessages-50:
		scope += " · display history is full"
	case open:
		scope += " · the thread may start earlier: Page Up loads older history"
	default:
		scope += " · Page Up loads older history"
	}
	gaps := 0
	for _, ev := range m.presence {
		if ev.kind == presenceGap {
			gaps++
		}
	}
	if gaps > 0 {
		scope += fmt.Sprintf(" · %d continuity gaps marked", gaps)
	}
	return []transcriptRow{
		{line: lineFilter, kind: rowDecor, runs: fitRuns(m.widthMethod, append(label, pad, button), width)},
		{line: lineFilter + 1, kind: rowDecor, runs: []run{deco("  "+truncate(m.widthMethod, scope, max(width-3, 1), "…"), spaceview.Style{FG: t.Dim})}},
	}
}

// filterChip is the divider's filter control in at most room cells, as runs
// and their hit IDs: For me while every message is shown; else For me,
// Thread or the filtered name or count (shortened to fit) with its hidden
// arrivals, then ✕. It reports false when no readable chip fits.
func (m *Model) filterChip(room int) ([]run, []string, bool) {
	t := m.theme
	if m.filter == nil {
		r := deco(" For me ", spaceview.Style{FG: t.Label, BG: t.Raised, Bold: true})
		return []run{r}, []string{"toggle"}, runsWidth(m.widthMethod, []run{r}) <= room
	}
	id, name := "pick", m.filterName()
	switch {
	case m.filter.thread != "":
		name = "Thread"
	case m.selfOnly():
		id, name = "toggle", "For me"
	case len(m.filter.picks) > 1:
		name = fmt.Sprintf("%d people", len(m.filter.picks))
	}
	extra := ""
	if n := m.filter.hiddenNew; n > 0 {
		extra = fmt.Sprintf(" +%d", n)
	}
	const clear = " ✕ "
	fit := min(16, room-1-m.widthMethod.StringWidth(extra+clear))
	if fit < 3 {
		return nil, nil, false
	}
	st := spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true}
	return []run{deco(" "+truncate(m.widthMethod, name, fit, "…")+extra, st), deco(clear, st)}, []string{id, "clear"}, true
}

// toggleForMe is Alt+M and the For me chip: exactly For me goes back to all
// messages; all messages or another selection becomes For me.
func (m *Model) toggleForMe() {
	switch {
	case m.selfOnly():
		m.setFilter("", "")
	case m.opts.Account == "":
		m.say(levelWarn, "Your account is not known yet; nothing to filter by")
	default:
		m.setFilter(m.opts.Account, m.opts.Nick)
	}
}

// filterCommand runs /filter: no argument opens the picker; me, all (or
// off) choose alone, and one or more participants' @names choose them
// together. Every name resolves like a mention click before anything
// changes; an unknown name or one naming more than one account refuses the
// whole command. It reports whether the command did something, so the
// draft clears.
func (m *Model) filterCommand(args []string) bool {
	if len(args) == 0 {
		m.openFilterPicker()
		return true
	}
	if len(args) == 1 {
		switch strings.ToLower(strings.TrimPrefix(args[0], "@")) {
		case "me":
			if m.opts.Account == "" {
				m.say(levelWarn, "Your account is not known yet; nothing to filter by")
				return false
			}
			m.setFilter(m.opts.Account, m.opts.Nick)
			return true
		case "all", "off":
			m.setFilter("", "")
			m.say(levelInfo, "Showing all messages")
			return true
		}
	}
	picks := make([]filterPick, 0, len(args))
	for _, raw := range args {
		arg := strings.TrimPrefix(raw, "@")
		switch strings.ToLower(arg) {
		case "me", "all", "off":
			m.say(levelWarn, "/filter "+plain(arg)+" stands alone; name participants with /filter NAME [NAME …]")
			return false
		}
		p, ok := m.filterTarget(arg)
		if !ok {
			return false
		}
		picks = append(picks, p)
	}
	m.setFilterPicks(picks)
	return true
}

// filterTarget resolves one /filter name like a mention click, else a
// connected session with exactly that nick, explaining a refusal.
func (m *Model) filterTarget(arg string) (filterPick, bool) {
	account, state := m.bindings().resolve(arg)
	switch state {
	case nameConflict:
		m.say(levelWarn, "@"+plain(arg)+" names more than one participant; choose with /filter")
		return filterPick{}, false
	case nameUnique:
		return filterPick{account, arg}, true
	}
	for _, mem := range m.members {
		if strings.EqualFold(mem.Nick, arg) && mem.Account != "" {
			return filterPick{mem.Account, mem.Nick}, true
		}
	}
	m.say(levelWarn, "No participant @"+plain(arg)+" is known here; /filter lists them")
	return filterPick{}, false
}

// filterChoice is one row of the /filter picker; thread is the visible
// thread's row.
type filterChoice struct {
	account, nick, label, detail string
	thread                       bool
}

// filterChoices are All messages, the visible thread while one is shown,
// For me, then the channel's connected participants with an authenticated
// account, one row per account, and any selected account no longer
// connected, so it can be removed.
func (m *Model) filterChoices() []filterChoice {
	out := []filterChoice{{label: "All messages", detail: "no filter"}}
	if m.filter != nil && m.filter.thread != "" {
		out = append(out, filterChoice{label: "Thread", detail: m.threadName() + ", with all its replies", thread: true})
	}
	if m.opts.Account != "" {
		out = append(out, filterChoice{account: m.opts.Account, nick: m.opts.Nick, label: "For me", detail: "messages from or mentioning you, and replies to those"})
	}
	seen := map[string]bool{m.opts.Account: true}
	for _, mem := range sortedMembers(m.members) {
		if mem.Account == "" || seen[mem.Account] {
			continue
		}
		seen[mem.Account] = true
		w := m.identify(mem.Nick, mem.Account)
		// An account outside the identity map gets no detail at all.
		var detail string
		switch {
		case w.human:
			detail = "human"
		case w.trusted:
			detail = "agent"
		}

		// A canonical identity name is only an input alias.
		// The picker displays the saved mention token and role.
		token := m.mentionToken(mem.Nick, mem.Account)
		out = append(out, filterChoice{account: mem.Account, nick: mem.Nick, label: "@" + token, detail: detail})
	}
	if m.filter != nil {
		for _, p := range m.filter.picks {
			if !seen[p.account] {
				seen[p.account] = true
				out = append(out, filterChoice{account: p.account, nick: p.nick, label: m.pickName(p), detail: "not connected"})
			}
		}
	}
	return out
}

// openFilterPicker opens Alt+F's picker on a private copy of the current
// selection: nothing changes until Enter applies it.
func (m *Model) openFilterPicker() {
	o := overlayState{kind: overlayFilter, choices: m.filterChoices()}
	if m.filter != nil {
		o.staged = slices.Clone(m.filter.picks)
		o.stagedThread = m.filter.thread != ""
		for i, c := range o.choices {
			if c.account != "" && o.stagedHas(c.account) || c.thread && o.stagedThread {
				o.index = i
				break
			}
		}
	}
	m.openOverlay(o)
}

func (o *overlayState) stagedHas(account string) bool {
	return slices.ContainsFunc(o.staged, func(p filterPick) bool { return p.account == account })
}

// toggleFilterChoice checks or unchecks the highlighted picker row; All
// messages clears the staged selection. The thread and people exclude
// each other: checking one unchecks the other.
func (m *Model) toggleFilterChoice() {
	o := m.overlay
	if o.index < 0 || o.index >= len(o.choices) {
		return
	}
	c := o.choices[o.index]
	switch {
	case c.thread:
		o.stagedThread = !o.stagedThread
		o.staged = nil
	case c.account == "":
		o.staged, o.stagedThread = nil, false
	case o.stagedHas(c.account):
		o.staged = slices.DeleteFunc(o.staged, func(p filterPick) bool { return p.account == c.account })
	default:
		o.staged, o.stagedThread = append(o.staged, filterPick{c.account, c.nick}), false
	}
}

// applyFilterChoices applies the picker's staged selection: the thread
// stays as it is, else the checked people, or all messages.
func (m *Model) applyFilterChoices(o *overlayState) {
	if o.stagedThread && m.filter != nil && m.filter.thread != "" {
		return
	}
	m.setFilterPicks(o.staged)
}

func (m *Model) filterPickerRows(inner int) []string {
	t := m.theme
	o := m.overlay
	var rows []string
	for i, c := range o.choices {
		marker := "  "
		if i == o.index {
			marker = spaceview.StyleText("› ", spaceview.Style{FG: t.Brand, Bold: true})
		}
		box := "[ ] "
		switch {
		case c.thread && o.stagedThread,
			!c.thread && c.account == "" && len(o.staged) == 0 && !o.stagedThread,
			c.account != "" && o.stagedHas(c.account):
			box = "[x] "
		}
		note := c.detail
		switch {
		case c.thread && m.filter != nil && m.filter.thread != "",
			!c.thread && c.account == "" && m.filter == nil,
			m.filter != nil && c.account != "" && m.filter.has(c.account):
			note += " · shown"
		}
		rows = append(rows, marker+spaceview.StyleText(box, spaceview.Style{FG: t.Brand, Bold: true})+spaceview.StyleText(fitPlain(m.widthMethod, c.label, 22), spaceview.Style{FG: t.Text, Bold: true})+spaceview.StyleText(truncate(m.widthMethod, note, max(inner-30, 8), "…"), spaceview.Style{FG: t.Dim}))
	}
	return append(rows, "", spaceview.StyleText("A message shows when anyone checked sent or is mentioned in it, or when it replies to a loaded message shown. A selected message's Thread or /thread shows its whole thread instead. A filter shows loaded messages only; Page Up loads older ones. Each channel keeps its own.", spaceview.Style{FG: t.Dim}))
}

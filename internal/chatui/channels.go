package chatui

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

// channelView is one channel's view: its draft and reply, messages and
// rendering caches, reading position and selection, older-history cursor,
// roster, notices, topic, unread counts and membership. The Model embeds the
// visible view; events for another channel update that channel's view
// without changing what is shown.
type channelView struct {
	name           string
	editor         editor
	messages       []Message
	replyTo        string
	replyRevision  uint64
	selected       int
	picked         bool
	actionIndex    int
	follow         bool
	scroll         int
	unread         int
	unreadMentions int
	lastRows       int
	lastBodyHeight int
	lastTextWidth  int
	anchor         *viewAnchor
	lastTranscript []transcriptRow
	bodyCache      map[string]cachedBody
	tableRows      map[string]map[markdownview.TableRowID]bool
	members        []Member
	loadingOlder   bool
	olderRequest   string
	historyEnd     bool
	olderAt        int
	olderSkipped   int
	sel            *selection
	joinedAt       time.Time
	presence       []presenceEvent
	topic, topicBy string
	topicKnown     bool
	// membership is joining, joined, left, kicked, deleted or unknown as the
	// stream reports it; "" means never reported (the legacy single channel).
	membership string
	// deleted: the channel was deleted while this chat was open. The view
	// keeps its history and draft read-only until chat closes, also across
	// an in-view reconnect; a later channel with the name is another one.
	deleted bool
	// channelID is the immutable identity the view was first bound to by a
	// catalog; another identity under the same name is never shown here.
	channelID string
	// filter shows only one account's messages and mentions (filter.go);
	// each channel keeps its own across switches.
	filter *messageFilter
	// generation counts changes to transcript rows (see touch); scrolling
	// reuses the rows built for the same generation and width.
	generation uint64
	draft      struct {
		revision      uint64
		cursor, width int
		folds         []fold
		rows          []draftRow
		row, col      int
	}
	built struct {
		generation uint64
		width      int
		elsewhere  uint64 // otherLoaded when built: previews of other channels' messages
		rows       []transcriptRow
	}
}

func newChannelView(name string, limit int) *channelView {
	return &channelView{name: name, editor: editor{limit: limit}, selected: -1, follow: true, bodyCache: map[string]cachedBody{}}
}

// Bounds across all channels; each channel keeps its own display bounds.
const (
	maxChannels         = 16
	maxTotalMessages    = 1024
	maxTotalBytes       = 8 << 20
	maxTotalLines       = 65536
	maxChannelNameBytes = 48
)

// canonicalChannel normalizes a typed channel name ("build", "#Build") to
// the canonical "#build" form, or reports false for anything else.
func canonicalChannel(name string) (string, bool) {
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "#"))
	if name == "" || len(name)+1 > maxChannelNameBytes {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return "", false
		}
	}
	return "#" + name, true
}

// view returns the view for a channel, creating it within the channel bound.
// An empty name is the default channel.
func (m *Model) view(channel string) *channelView {
	if channel == "" {
		channel = m.opts.Channel
	}
	key := strings.ToLower(channel)
	if v, ok := m.views[key]; ok {
		return v
	}
	if len(m.views) >= maxChannels {
		return nil
	}
	v := newChannelView(key, m.opts.MaxMessageBytes)
	if m.cur != nil {
		v.editor.limit = m.cur.editor.limit
	}
	if m.connected {
		v.joinedAt = time.Now()
	}
	m.views[key] = v
	m.order = append(m.order, key)
	return v
}

// within runs f with the channel's view embedded, as if it were visible.
// Notices from a hidden channel are dropped unless they report an error or
// were said with sayEverywhere, which then name the channel.
func (m *Model) within(channel string, f func()) {
	v := m.view(channel)
	if v == nil {
		return
	}
	if v == m.channelView {
		f()
		return
	}
	prev, notice, level, owner, read := m.channelView, m.notice, m.noticeLevel, m.noticeOwner, m.noticeRead
	m.channelView = v
	m.carry = nil
	f()
	m.channelView = prev
	carry := m.carry
	m.carry = nil
	switch {
	case v == m.cur:
	case carry != nil:
		m.notice, m.noticeLevel, m.noticeOwner, m.noticeRead = v.name+": "+carry.text, carry.level, carry.owner, carry.read
	case m.noticeLevel < levelError:
		m.notice, m.noticeLevel, m.noticeOwner, m.noticeRead = notice, level, owner, read
	}
}

// carried is a notice that must reach the user from a hidden channel.
type carried struct {
	owner noticeOwner
	read  CatalogRead
	level noticeLevel
	text  string
}

// sayEverywhere is say for guidance the user must not miss, such as the
// operation to inspect after an uncertain upload: from a hidden channel it
// is shown prefixed with that channel instead of being dropped.
func (m *Model) sayEverywhere(level noticeLevel, text string) {
	m.say(level, text)
	m.carry = &carried{level: level, text: text, owner: m.noticeOwner, read: m.noticeRead}
}

// eachView runs f on every channel view, the visible one last.
func (m *Model) eachView(f func()) {
	for _, name := range m.order {
		if v := m.views[name]; v != m.cur {
			m.within(name, f)
		}
	}
	f()
}

func (m *Model) isCurrent() bool { return m.channelView == m.cur }

func (m *Model) isDefault() bool { return strings.EqualFold(m.name, m.opts.Channel) }

// canSend reports whether the visible channel accepts a message from this user:
// the channel is not deleted and membership is joined. A channel the stream never
// reported (the legacy single channel) counts as joined when it is the default.
// Overall view send readiness also requires an active connection, Send callback,
// and absence of unresolved actions (see composerStatus).
func (m *Model) canSend() bool {
	return !m.deleted && (m.membership == "joined" || m.membership == "" && m.isDefault())
}

// switchTo shows another channel. Nothing is sent or reconnected; the old
// channel keeps its draft, reading position and selection.
func (m *Model) switchTo(name string) {
	v := m.view(name)
	if v == nil || v == m.cur {
		return
	}
	m.captureAnchor()
	m.channelView, m.cur = v, v
	m.drag, m.suggest, m.dismissed = dragState{}, nil, ""
	m.focus = focusComposer
	if m.follow {
		m.unread, m.unreadMentions = 0, 0
	}
	m.touch()
	m.refreshSuggest()
	m.say(levelInfo, "Now in "+v.name+m.membershipNote())
}

// selectChannel is a deliberate selection in the human host chat view.
// It uses the ordinary typed join/admission path; catalog events and join
// completion keep using switchTo, which never starts a channel action.
func (m *Model) selectChannel(name string) tea.Cmd {
	key, ok := canonicalChannel(name)
	if !ok {
		return nil
	}
	v := m.views[key]
	if v == nil {
		return nil
	}
	if v.deleted || v.membership == "joined" || v.membership == "" && strings.EqualFold(key, m.opts.Channel) {
		m.switchTo(key)
		return nil
	}
	if m.joinSwitch.id != "" && strings.EqualFold(m.joinSwitch.channel, key) {
		m.say(levelInfo, "Joining "+key+"…")
		return nil
	}
	cmd, started := m.control("join", key, nil)
	if started {
		m.say(levelInfo, "Joining "+key+"…")
	}
	return cmd
}

func (m *Model) membershipNote() string {
	if m.deleted {
		return " (deleted: read-only, no restore)"
	}
	switch m.membership {
	case "joining":
		return " (joining…)"
	case "left":
		return " (left: /join " + m.name + " to write here)"
	case "kicked":
		return " (you were removed from this channel)"
	case "unknown":
		return " (membership unknown after a gap)"
	}
	return ""
}

// cycle moves to the next (delta 1) or previous (-1) channel, preferring
// channels with unread messages.
func (m *Model) cycle(delta int) tea.Cmd {
	if len(m.order) < 2 {
		m.say(levelInfo, "Only "+m.name+" is open; /join NAME adds a channel")
		return nil
	}
	at, n := slices.Index(m.order, m.name), len(m.order)
	for _, unreadOnly := range []bool{true, false} {
		for step := 1; step < n; step++ {
			name := m.order[((at+delta*step)%n+n)%n]
			if !unreadOnly || m.views[name].unread > 0 {
				return m.selectChannel(name)
			}
		}
	}
	return nil
}

// otherUnread counts unread messages and mentions outside the visible view.
func (m *Model) otherUnread() (int, int) {
	unread, mentions := 0, 0
	for name, v := range m.views {
		if name != m.name {
			unread += v.unread
			mentions += v.unreadMentions
		}
	}
	return unread, mentions
}

// CatalogMsg is the host's trusted channel catalog snapshot. A lower
// generation than the last one is ignored; nothing is joined automatically.
type CatalogMsg struct {
	Read       CatalogRead // local accepted-refresh correlation, never native authority
	Generation uint64
	Default    string
	Channels   []ChannelInfo
	// Deleted lists the identities of channels deleted from the room.
	Deleted []string
	// RuntimeUnobserved means no server application check was made.
	RuntimeUnobserved bool
	// Applied is meaningful only for an observed catalog.
	Applied bool
	// Identities, when set, replaces the trusted identity map: members
	// enrolled since the view opened become trusted, revoked ones lose it.
	Identities map[string]Identity
}

// identityRefreshGap bounds automatic catalog refreshes triggered by an
// authenticated account the view does not know yet.
const identityRefreshGap = 15 * time.Second

// refreshForUnknown asks the host for its trusted catalog when an
// authenticated account is not in the identity map, at most once per
// identityRefreshGap. Trust only ever comes from that catalog, never from
// the nick or the roster.
func (m *Model) refreshForUnknown(accounts ...string) tea.Cmd {
	if m.opts.RefreshChannels == nil || time.Since(m.lastRefresh) < identityRefreshGap {
		return nil
	}
	for _, account := range accounts {
		if _, known := m.opts.Identities[account]; account != "" && !known {
			m.lastRefresh = time.Now()
			return m.opts.RefreshChannels()
		}
	}
	return nil
}

// ChannelInfo describes one catalog channel.
type ChannelInfo struct {
	Name    string
	ID      string // immutable identity; empty when the host has none
	Members int    // members with access, as the controller declares
}

// MembershipMsg reports this connection's membership in a channel:
// joining, joined, left, kicked, deleted or unknown. ID names the Action that
// asked for it, if any. Only an authenticated self JOIN reports joined.
// Deleted is final for the view: later reports cannot reopen it.
type MembershipMsg struct {
	Channel, State, ID string
}

// Action is one channel control: join, part, topic-read, topic-set or
// topic-clear. Topic is set only for topic-set (it may be empty).
type Action struct {
	ID, Kind, Channel string
	Topic             *string
}

// ControlMsg finishes an Action: confirmed, refused or uncertain. An
// uncertain outcome keeps further controls blocked until reconnect.
type ControlMsg struct {
	ID, Channel, Kind, State string
	// Code classifies a refused join: CodeUnknownChannel or CodeACLDenied;
	// empty for every other outcome, which Err then explains.
	Code string
	Err  error
}

// ControlMsg codes for a refused join.
const (
	CodeUnknownChannel = "unknown-channel"
	CodeACLDenied      = "acl-denied"
)

// HistoryRequest asks for the page of a channel before message Before.
type HistoryRequest struct {
	ID, Channel, Before string
}

func (m *Model) catalog(msg CatalogMsg) {
	if msg.Read.Link != 0 && !m.refreshLink(msg.Read.Link) {
		return
	}
	if msg.Generation < m.catalogGeneration {
		if msg.Read.Link == 0 {
			m.say(levelWarn, "Ignored an older channel catalog")
		}
		return
	}
	m.catalogGeneration, m.catalogApplied = msg.Generation, msg.Applied && !msg.RuntimeUnobserved
	m.catalogUnobserved = msg.RuntimeUnobserved
	m.refreshAccepted(msg.Read)
	if msg.Identities != nil {
		identities := make(map[string]Identity, len(msg.Identities))
		for account, id := range msg.Identities {
			if old, ok := m.opts.Identities[account]; ok && id.Agent == "" {
				id.Agent = old.Agent // agent glyphs come from a separate read
			}
			identities[account] = id
		}
		m.opts.Identities = identities
		m.refreshParticipantLabels()
		m.eachView(m.touch)
		m.attachIdentityChanged()
	}
	m.catalogInfo = map[string]ChannelInfo{}
	var names []string
	for _, ch := range msg.Channels {
		name, ok := canonicalChannel(ch.Name)
		if !ok {
			continue
		}
		m.catalogInfo[name] = ch
		names = append(names, name)
	}
	for _, name := range names {
		m.view(name)
	}
	m.bindIdentities(msg.Deleted)
	// Catalog order (default first), then any other open views.
	order := []string{}
	if d, ok := canonicalChannel(msg.Default); ok && m.views[d] != nil {
		order = append(order, d)
	}
	for _, name := range append(names, m.order...) {
		if m.views[name] != nil && !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	m.order = order
}

// bindIdentities ties each view to the channel identity it first showed. A
// catalog recording that identity's deletion, or naming another identity
// for the view's name, makes the view read-only: the channel it showed was
// deleted, also if that happened while this chat was disconnected.
func (m *Model) bindIdentities(deleted []string) {
	var gone []string
	for _, name := range m.order {
		v, info := m.views[name], m.catalogInfo[name]
		switch {
		case v == nil || v.deleted || strings.EqualFold(name, m.opts.Channel):
		case v.channelID != "" && (slices.Contains(deleted, v.channelID) || info.ID != "" && info.ID != v.channelID):
			gone = append(gone, name)
		case v.channelID == "":
			v.channelID = info.ID
		}
	}
	for _, name := range gone {
		m.membershipChanged(MembershipMsg{Channel: name, State: "deleted"})
	}
}

// deletedView reports a view made read-only by a deletion: stream content
// for its name belongs to another channel and is never shown there.
func (m *Model) deletedView(channel string) bool {
	v := m.views[strings.ToLower(channel)]
	return v != nil && v.deleted
}

// membershipChanged records a membership report in its channel's view.
// A report for this view's own join finishes it: joined shows the channel
// unless the user moved elsewhere meanwhile, and a refused join (reported
// as left) is not a departure.
func (m *Model) membershipChanged(msg MembershipMsg) {
	join := msg.ID != "" && msg.ID == m.joinSwitch.id
	m.within(msg.Channel, func() {
		if m.deleted {
			return
		}
		before := m.membership
		m.membership = msg.State
		m.touch()
		switch msg.State {
		case "deleted":
			m.deleted = true
			m.addPresence(presenceSelf, m.name+" was deleted; this view is read-only")
			m.sayEverywhere(levelWarn, m.name+" was deleted with its history; this view stays read-only until chat closes, and a channel created later with this name needs a new chat")
		case "joined":
			if m.joinedAt.IsZero() {
				m.joinedAt = time.Now()
			}
			if before != "joined" {
				m.addPresence(presenceSelf, "You joined "+m.name)
			}
		case "left":
			if !join {
				m.addPresence(presenceSelf, "You left "+m.name)
			}
		case "kicked":
			m.addPresence(presenceSelf, "You were removed from "+m.name)
			m.say(levelWarn, "You were removed from "+m.name+"; it is not rejoined automatically")
		case "unknown":
			m.addPresence(presenceSelf, "Membership in "+m.name+" is unknown after a gap")
		}
	})
	if join && msg.State == "joined" {
		m.showJoined(msg.Channel)
	}
}

// showJoined shows a channel this view asked to join, once, if the user is
// still where the request started.
func (m *Model) showJoined(channel string) {
	js := m.joinSwitch
	m.joinSwitch = joinSwitch{}
	if m.name == js.from {
		m.switchTo(channel)
		if js.ref != "" && strings.EqualFold(m.name, channel) {
			m.selectLoaded(js.ref)
		}
	}
}

// joinSwitch remembers an explicit join until the server confirms it, so
// the view follows the confirmation whichever report arrives first. ref is a
// message a reference click asked to show once this join switches; it lives
// and ends with the join intent.
type joinSwitch struct{ id, from, channel, ref string }

// controlReady reports whether a channel action may start now. One send
// or action is outstanding at a time, and an uncertain action blocks the
// next until reconnect.
func (m *Model) controlReady() bool {
	switch {
	case m.opts.Control == nil:
		m.say(levelWarn, "Channel actions are not available in this view")
	case !m.connected:
		m.say(levelWarn, "Chat is disconnected")
	case m.pending != nil && m.pending.uncertain:
		m.say(levelWarn, "An earlier "+m.pending.Kind+" in "+m.pending.Channel+" is uncertain; reconnect to resolve it")
	case m.pending != nil || m.inflight != "":
		m.say(levelWarn, "Another send or channel action is still unresolved")
	default:
		return true
	}
	return false
}

// control starts a channel action once controlReady allows it, and
// reports whether it started.
func (m *Model) control(kind, channel string, topic *string) (tea.Cmd, bool) {
	if v := m.views[strings.ToLower(channel)]; v != nil && v.deleted {
		m.say(levelWarn, channel+" was deleted while this chat was open; quit and run yoloctl chat again to use a channel created later with that name")
		return nil, false
	}
	if !m.controlReady() {
		return nil, false
	}
	a := Action{ID: rand.Text(), Kind: kind, Channel: channel, Topic: topic}
	m.pending = &pendingAction{Action: a, created: m.views[strings.ToLower(channel)] == nil}
	if kind == "join" {
		m.joinSwitch = joinSwitch{id: a.ID, from: m.name, channel: channel}
	} else if kind == "part" && strings.EqualFold(m.joinSwitch.channel, channel) {
		// Leaving supersedes an acknowledged join whose membership event has
		// not arrived yet; it must not keep selection stuck on “Joining…”.
		m.joinSwitch = joinSwitch{}
	}
	return m.opts.Control(a), true
}

// pendingAction is the outstanding channel action. created records that no
// view of its channel existed, so a refused join leaves nothing behind.
type pendingAction struct {
	Action
	uncertain bool
	created   bool
}

func (m *Model) controlDone(msg ControlMsg) {
	if m.pending == nil || msg.ID != m.pending.ID {
		return
	}
	kind, channel := m.pending.Kind, m.pending.Channel
	what := map[string]string{"join": "Joining " + channel, "part": "Leaving " + channel, "topic-read": "Reading the topic of " + channel, "topic-set": "Setting the topic of " + channel, "topic-clear": "Clearing the topic of " + channel}[kind]
	switch msg.State {
	case "confirmed":
		m.pending = nil
		switch kind {
		case "join":
			// The stream may confirm before reporting membership; switch only
			// once the view is actually joined.
			if v := m.views[strings.ToLower(channel)]; v != nil && v.membership == "joined" && m.joinSwitch.id == msg.ID {
				m.showJoined(channel)
			}
		case "topic-set", "topic-clear":
			m.say(levelOK, "Topic updated in "+channel)
		case "part":
			m.say(levelOK, "Left "+channel+"; its draft and history stay here")
		}
	case "refused":
		created := m.pending.created
		m.pending = nil
		reason := "refused"
		if msg.Err != nil {
			reason = msg.Err.Error()
		}
		switch {
		case kind == "join" && msg.Code == CodeUnknownChannel:
			// The bare name: in a shell, a leading # would start a comment.
			m.say(levelError, channel+" is not a channel of this room; create it with yoloctl chat channel create "+strings.TrimPrefix(channel, "#")+", then /join "+channel)
		case kind == "join" && msg.Code == CodeACLDenied:
			m.say(levelError, "The room does not give you access to "+channel+"; yoloctl chat channel list shows who may join")
		default:
			m.say(levelError, what+" failed: "+reason)
		}
		if kind == "join" {
			m.noticeOwner = noticeJoin
			m.joinSwitch = joinSwitch{}
			m.unjoined(channel, created)
		}
	case "uncertain":
		m.pending.uncertain = true
		m.say(levelWarn, what+" is uncertain; inspect the channel before trying again")
		if kind == "join" {
			m.noticeOwner = noticeJoin
			m.joinSwitch = joinSwitch{}
			m.within(channel, func() {
				if m.membership != "joined" {
					m.membership = "unknown"
					m.touch()
				}
			})
		}
	}
}

// unjoined tidies after a refused join. Membership stays whatever the stream
// reported; a view that only this attempt created, and that holds nothing,
// is removed again. The visible view is never removed.
func (m *Model) unjoined(channel string, created bool) {
	key := strings.ToLower(channel)
	v := m.views[key]
	if v == nil {
		return
	}
	if v.membership == "joining" {
		v.membership = "unknown"
	}
	if !created || v == m.cur || len(v.messages) > 0 || v.editor.text != "" {
		return
	}
	delete(m.views, key)
	m.order = slices.DeleteFunc(m.order, func(name string) bool { return name == key })
}

// channelCommand handles /channels, /join, /part and /topic. Arguments keep
// their bytes after one separator; only a complete single-line draft gets here.
func (m *Model) channelCommand(text string, fields []string) (tea.Cmd, bool) {
	clear := func() { m.edit(m.editor.replace(0, len(m.editor.text), "")) }
	switch fields[0] {
	case "/channels":
		if len(fields) != 1 {
			return nil, false
		}
		clear()
		m.openOverlay(overlayState{kind: overlayChannels, index: max(slices.Index(m.order, m.name), 0)})
		if m.opts.RefreshChannels != nil {
			return m.opts.RefreshChannels(), true
		}
		return nil, true
	case "/join":
		if len(fields) != 2 {
			m.say(levelWarn, "Usage: /join NAME")
			return nil, true
		}
		name, ok := canonicalChannel(fields[1])
		if !ok {
			m.say(levelWarn, "Channel names use letters, digits, _ or - (at most 47)")
			return nil, true
		}
		if v := m.views[name]; v != nil && !v.deleted && (v.membership == "joined" || v.membership == "" && strings.EqualFold(name, m.opts.Channel)) {
			clear()
			m.switchTo(name)
			return nil, true
		}
		if m.views[name] == nil && len(m.views) >= maxChannels {
			m.say(levelWarn, fmt.Sprintf("At most %d channels can be open", maxChannels))
			return nil, true
		}
		if !m.controlReady() {
			return nil, true
		}
		// Only the stream reports membership: the view switches once the
		// server confirms the join, never on the request alone.
		clear()
		cmd, _ := m.control("join", name, nil)
		m.say(levelInfo, "Joining "+name+"…")
		return cmd, true
	case "/part":
		name := m.name
		if len(fields) == 2 {
			var ok bool
			if name, ok = canonicalChannel(fields[1]); !ok {
				m.say(levelWarn, "Channel names use letters, digits, _ or -")
				return nil, true
			}
		} else if len(fields) != 1 {
			m.say(levelWarn, "Usage: /part [NAME]")
			return nil, true
		}
		cmd, started := m.control("part", name, nil)
		if started {
			clear()
		}
		return cmd, true
	case "/topic":
		rest := strings.TrimPrefix(text, "/topic")
		switch {
		case len(fields) == 1:
			clear()
			switch {
			case m.topic != "" && m.topicBy != "":
				m.say(levelInfo, "Topic of "+m.name+": "+m.topic+" (set by "+m.topicBy+")")
			case m.topic != "":
				m.say(levelInfo, "Topic of "+m.name+": "+m.topic)
			case m.topicKnown:
				m.say(levelInfo, "No topic is set for "+m.name)
			default:
				cmd, _ := m.control("topic-read", m.name, nil)
				return cmd, true
			}
		case fields[1] == "clear" && len(fields) == 2:
			// The driver encodes the clear itself; a topic-clear carries no text.
			if cmd, started := m.control("topic-clear", m.name, nil); started {
				clear()
				return cmd, true
			}
		case fields[1] == "set" && strings.HasPrefix(rest, " set "):
			topic := strings.TrimPrefix(rest, " set ")
			if len(topic) > 200 {
				m.say(levelWarn, "A topic has at most 200 bytes")
				return nil, true
			}
			if cmd, started := m.control("topic-set", m.name, &topic); started {
				clear()
				return cmd, true
			}
		default:
			m.say(levelWarn, "Usage: /topic, /topic set TEXT or /topic clear")
		}
		return nil, true
	}
	return nil, false
}

// trimAggregate keeps all channels within the whole-view bounds: hidden
// channels lose their oldest messages first, then the visible one. Drafts
// and an unresolved send are never dropped; trimmed channels show where
// older history stops.
func (m *Model) trimAggregate() {
	for {
		messages, bytes, lines := 0, 0, 0
		for _, v := range m.views {
			messages += len(v.messages)
			for _, msg := range v.messages {
				bytes += len(msg.Text)
				lines += logicalLines(msg.Text)
			}
		}
		if messages <= maxTotalMessages && bytes <= maxTotalBytes && lines <= maxTotalLines {
			return
		}
		var victim *channelView
		for _, name := range m.order {
			v := m.views[name]
			if len(v.messages) > 1 && v != m.cur && (victim == nil || len(v.messages) > len(victim.messages)) {
				victim = v
			}
		}
		if victim == nil {
			victim = m.cur
			if len(victim.messages) <= 1 {
				return
			}
		}
		m.within(victim.name, func() {
			i := 0
			if m.messages[0].ClientID == m.inflight && m.inflight != "" {
				i = 1
			}
			m.evict(i)
			m.historyEnd = true
			m.olderSkipped++
		})
	}
}

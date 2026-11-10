// Package chatui is a transport-neutral human chat component. Its caller owns
// the confined AIRC client, credentials, lifecycle, and host integration policy.
package chatui

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Delivery describes a whole logical message, never an individual wire chunk.
type Delivery string

const (
	Pending   Delivery = "pending"
	Confirmed Delivery = "confirmed"
	Rejected  Delivery = "rejected"
	Uncertain Delivery = "uncertain"
)

// Outgoing is one send attempt to Channel. The adapter must preserve ID for
// correlation. Attach publishes staged images with this one message.
type Outgoing struct {
	ID, Channel, Text, ReplyTo string
	Attach                     []AttachRef
}

// Message preserves the original logical text, independent of its rendering.
// Account must come from authenticated transport metadata; Nick is display text.
type Message struct {
	ID, ClientID, Account, Nick, Text, ReplyTo string
	// Channel is the message's channel; empty means the default channel.
	Channel string
	Time    time.Time
	Replay  bool
	// Older inserts a message from an explicitly requested older page.
	Older    bool
	Delivery Delivery
	Error    string
	// Artifact is set only from validated server-reserved artifact tags.
	Artifact *ArtifactRef
	// Attachments are the images this one message published, each at its
	// marker's byte offset in Text, from validated reserved tags only.
	Attachments []AttachmentRef
}

// Identity is a trusted room mapping for one authenticated account.
type Identity struct {
	Name string // canonical identity name, used only for input matching/comparison
	// Nick is the saved room nickname: how the account is @mentioned, also
	// while each connection of one human uses its own live alias.
	Nick  string
	Human bool
	// Box marks an enrolled box member: the host can resolve it for
	// attachment, re-validating membership and declaration identity.
	Box bool
	// Agent names the box's agent, for its configured glyph and color.
	Agent string
}

// Member is one connected channel session. Account is empty until the
// transport observes it; such members display as unverified.
type Member struct {
	Nick, Account string
	Op, Voice     bool
	// Away is explicit AWAY state with its reason; it is never inferred.
	Away       bool
	AwayReason string
}

// MembersMsg replaces a channel's roster snapshot.
type MembersMsg struct {
	Channel string
	Members []Member
}

// HistoryPageMsg finishes an older-history request: Count messages arrived;
// 0 means no older retained history, negative means the request failed. ID
// is the HistoryRequest's, when the stream reports it.
type HistoryPageMsg struct {
	Channel, ID string
	Count       int
}

// IncomingMsg carries one assembled message from the caller's client stream.
type IncomingMsg struct{ Message Message }

// DeliveryMsg correlates a complete receipt to an Outgoing.ID. Queued/accepted
// responses are Pending; only an observed complete echo/receipt is Confirmed.
type DeliveryMsg struct {
	ID, MessageID, Channel string
	State                  Delivery
	Err                    error
	// Refusal details a definite refusal of a message with attachments.
	Refusal *AttachmentRefusal
}

// ConnectionMsg reports the client connection, not container/server liveness.
type ConnectionMsg struct {
	Connected bool
	// Negotiated per-message bounds; zero keeps the current limits.
	MaxBytes, MaxLines int
	// Attachments: this connection can send text and images as one message.
	Attachments bool
	Err         error
}

// GapMsg makes bounded history loss visible without inventing missing
// context. An empty Channel means the whole connection: every channel's
// continuity, roster, topic and membership evidence becomes unknown.
type GapMsg struct{ Channel, Text string }

// ActionResultMsg reports a host copy/link callback outcome without coupling
// the component to a desktop opener or a clipboard implementation.
type ActionResultMsg struct {
	Text string
	Err  error
}

// AttachTarget is the host's action-time resolution of a participant's box.
// State is running, stopped, missing or revoked; Box is set only when the
// current declaration matches the member's immutable identity. The IDs go
// back to Attach unchanged, so the host can verify them again.
type AttachTarget struct {
	Account, Box, BoxID, State, Reason string
	RoomID, MemberID                   string
}

// AttachTargetMsg answers Options.ResolveAttach.
type AttachTargetMsg struct {
	Target AttachTarget
	Err    error
}

// NickSavedMsg answers Options.SaveNick.
type NickSavedMsg struct {
	Nick string
	Err  error
}

// TopicMsg reports the channel topic: the state on joining, or a Live
// change made by By. An empty Text means no topic is set.
type TopicMsg struct {
	Channel, Text, By string
	Live              bool
	// Unknown marks a topic the stream could not establish (for example
	// after a gap); it is distinct from an unset (empty) topic.
	Unknown bool
}

// AgentsMsg names the agent of trusted members' boxes, by account, for
// their glyphs and colors. It never adds or changes an identity.
type AgentsMsg struct {
	Agents map[string]string
	// Runtime is each box's model and effort evidence by account, read at
	// At; nil leaves the current values. Err reports a failed read: the
	// earlier values stay and show as stale.
	Runtime map[string]AgentRuntime
	At      time.Time
	Err     error
}

// NickHeldMsg reports that the stream refused, before connecting, because
// another session of this account is connected: the view asks whether to
// take that session over (Options.TakeOver) or close.
type NickHeldMsg struct{}

// SessionMsg updates the local identity after an explicit reconnect.
type SessionMsg struct {
	Nick       string
	Identities map[string]Identity
}

// Options injects every effect. Callbacks return promptly with a tea.Cmd; send
// acceptance alone must not return Confirmed. Copy/OpenLink are explicit user
// actions and must apply the outer host's clipboard/browser policy.
type Options struct {
	Space, Channel, Nick string
	// Account is the local human's authenticated account.
	Account         string
	MaxMessageBytes int
	Send            func(Outgoing) tea.Cmd
	Copy, OpenLink  func(string) tea.Cmd
	// ConfirmLinks requires a second explicit Ctrl+O for the fixed URL shown
	// in a local prompt. The caller still owns URL validation and opening.
	ConfirmLinks bool
	// Clipboard and Browser describe the host's seams.clipboard and
	// seams.browser policies for Help and Settings; "" means unknown.
	Clipboard, Browser string
	// Identities maps authenticated accounts to trusted display names.
	Identities map[string]Identity
	// Icons are the TUI agent_icons, for agent glyphs and colors.
	Icons map[string]config.AgentIcon
	// LoadOlder requests the page of a channel before its oldest message ID;
	// the caller answers with older IncomingMsgs and one HistoryPageMsg.
	LoadOlder func(HistoryRequest) tea.Cmd
	// Control runs one channel action (join, part, topic) and answers with
	// ControlMsg; membership and topic changes arrive as their own messages.
	Control func(Action) tea.Cmd
	// RefreshChannels asks the host for its trusted catalog (CatalogMsg).
	RefreshChannels func() tea.Cmd
	// ResolveAttach checks, at action time, which box an authenticated
	// account's membership currently names and answers AttachTargetMsg.
	ResolveAttach func(account string) tea.Cmd
	// Attach opens a resolved target's session; it reports ActionResultMsg.
	Attach func(AttachTarget) tea.Cmd
	// SaveNick persists a new display nickname and answers NickSavedMsg.
	SaveNick func(nick string) tea.Cmd
	// Reconnect restarts the client stream with saved settings. The host
	// sends ConnectionMsg updates and a SessionMsg; nothing is resent.
	Reconnect func() tea.Cmd
	// TakeOver connects again, replacing this account's other session that
	// holds the nickname; the user chose it after NickHeldMsg. The host sends
	// ConnectionMsg updates; nothing is resent.
	TakeOver func() tea.Cmd
	// ReadImage reads one host clipboard PNG after a local Ctrl+V and
	// answers ImagePreparedMsg; nil when seams.clipboard is off or image
	// paste is unavailable, so no desktop read happens.
	ReadImage func(ImageRequest) tea.Cmd
	// UploadImage publishes one prepared image and answers
	// ImageUploadedMsg; it never retries.
	UploadImage func(ImageUpload) tea.Cmd
	// NoMotion holds active participant dots steady instead of breathing.
	NoMotion bool
}

const maxMessages = 256
const maxHistoryBytes = 2 << 20
const maxLogicalLines = 32768

type focusMode int

const (
	focusComposer focusMode = iota
	// focusMessages gives arrows and Enter to the selected message's
	// actions. Typing returns to the composer, so text is never a command.
	focusMessages
)

type noticeLevel int

const (
	levelInfo noticeLevel = iota
	levelOK
	levelWarn
	levelError
)

// Model implements tea.Model. Feed transport messages via the hosting program.
type Model struct {
	// channelView is the visible channel; within embeds another one while
	// applying its events. Per-channel state lives there.
	*channelView
	cur               *channelView
	views             map[string]*channelView
	order             []string
	catalogGeneration uint64
	catalogApplied    bool
	catalogUnobserved bool
	catalogInfo       map[string]ChannelInfo
	pending           *pendingAction
	lastRefresh       time.Time
	opts              Options
	width, height     int
	// Zero is WcWidth, as in Bubble Tea before a supported mode-2027 report.
	widthMethod       ansi.Method
	connected         bool
	pasting           bool
	inflight          string
	inflightChannel   string
	sentRevision      uint64
	sentReplyRevision uint64
	sentText          string
	notice            string
	noticeLevel       noticeLevel
	noticeOwner       noticeOwner
	noticeRead        CatalogRead
	refresh           refreshState
	focus             focusMode
	source            bool
	theme             spaceview.Theme
	tele              boxTelemetry
	rosterOpen        bool
	rosterOverlay     bool
	maxLines          int
	quitArmed         bool
	linkPrompt        *linkPrompt
	suggest           *suggestState
	dismissed         string
	pendingRun        bool
	drag              dragState
	copyOnSelect      bool
	overlay           *overlayState
	layout            viewLayout
	hits              []hit
	reconnecting      bool
	// rosterScroll is how many participant cards the panel has scrolled
	// past; each frame clamps it to the cards that do not fit.
	rosterScroll int
	// gesture identifies the current mouse press, so a drag tick scheduled
	// by an earlier gesture is ignored rather than doubling auto-scroll.
	gesture uint64
	// hover is the click target under a buttonless pointer (see hover.go).
	hover hoverKey
	// marquee scrolls the hovered participant's clipped row (marquee.go).
	marquee marquee
	// joinSwitch is the explicit join whose confirmation shows its channel.
	joinSwitch joinSwitch
	// imageRead and imageUpload are the outstanding clipboard read and image
	// upload, one of each at a time (see attachments.go).
	imageRead   *imageRead
	imageUpload *imageUpload
	// late lists images that left their draft while a host outcome was due.
	late []lateImage
	// attachments: this connection sends text and images as one message
	// (staging.go); sentImages are the images of that message in flight.
	attachments bool
	sentImages  []*draftImage
	// carry is a notice said with sayEverywhere inside within.
	carry *carried
	// sentDraft and sentEnd are the draft when the inflight text left and
	// the length of its prefix that text covers; a draft holding images
	// sends its parts in order (see sendDraft).
	sentDraft string
	sentEnd   int
	// resume names the channel whose confirmed part leaves more of an
	// unchanged draft to send.
	resume string
	// attaching is the one participant lookup in flight (see requestAttach);
	// attachSeq numbers lookups so a superseded answer is ignored.
	attaching *attachLookup
	attachSeq uint64
	// runtime is the members' model and effort by account, as last read at
	// runtimeAt; runtimeErr is the failure of a later read (see runtime.go).
	runtime    map[string]AgentRuntime
	runtimeAt  time.Time
	runtimeErr string
	// self is the local user's mention tokens as the last transcript build
	// resolved them (see selfNames); bound is the name bindings the views
	// last reconciled with (see reconcile).
	self  string
	bound nameBindings
}

// wireLineBytes approximates the text one IRC line carries after the relay
// prefix; the AIRC client remains authoritative and refuses what does not fit.
const wireLineBytes = 440

// New creates a disconnected view. The caller supplies ConnectionMsg once its
// authenticated client stream is ready; no network connection is opened here.
func New(opts Options) *Model {
	if opts.Channel == "" {
		opts.Channel = "#space"
	}
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = DefaultMessageBytes
	}
	opts.MaxMessageBytes = min(opts.MaxMessageBytes, MaxMessageBytes)
	if name, ok := canonicalChannel(opts.Channel); ok {
		opts.Channel = name
	}
	v := newChannelView(opts.Channel, opts.MaxMessageBytes)
	return &Model{channelView: v, cur: v, views: map[string]*channelView{v.name: v}, order: []string{v.name},
		opts: opts, rosterOpen: true, copyOnSelect: true, theme: spaceview.NewTheme(true, nil, opts.Icons)}
}

// Draft returns unrendered editable text.
func (m *Model) Draft() string { return m.editor.text }

// SetDraft restores a textual draft, subject to the same bounds as paste.
func (m *Model) SetDraft(text string) error {
	text, err := cleanInput(text)
	if err != nil {
		return err
	}
	return m.editor.replace(0, len(m.editor.text), text)
}

// Messages returns a detached snapshot, including local delivery state.
func (m *Model) Messages() []Message { return slices.Clone(m.messages) }

func (m *Model) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m *Model) say(level noticeLevel, text string) {
	m.notice, m.noticeLevel = text, level
	m.noticeOwner, m.noticeRead = noticeGeneric, CatalogRead{}
}

// touch marks the transcript rows stale. Scrolling, typing, key releases
// and pointer motion do not touch, so they repaint only visible rows.
func (m *Model) touch() { m.generation++ }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.update(msg)
	switch msg.(type) {
	case MembersMsg, IncomingMsg, DeliveryMsg, CatalogMsg, SessionMsg, ConnectionMsg, GapMsg, MembershipMsg:
		m.reconcile()
	}
	m.settleImages()
	if next := m.settleMarquee(); next != nil {
		cmd = tea.Batch(cmd, next)
	}
	if channel := m.resume; channel != "" {
		m.resume = ""
		var next tea.Cmd
		m.within(channel, func() { next = m.sendDraft() })
		if next != nil {
			cmd = tea.Batch(cmd, next)
		}
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.ModeReportMsg:
		// Mirror Bubble Tea v2.0.10's renderer predicate, including Reset
		// (supported but currently off). The renderer never reverts.
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeReset || msg.Value == ansi.ModeSet || msg.Value == ansi.ModePermanentlySet) {
			m.setWidthMethod(ansi.GraphemeWidth)
		}
	case tea.WindowSizeMsg:
		m.captureAnchor()
		m.width, m.height = max(msg.Width, 0), max(msg.Height, 0)
		m.hover = hoverKey{} // every target may have moved
	case tea.BackgroundColorMsg:
		m.theme = spaceview.NewTheme(msg.IsDark(), msg.Color, m.opts.Icons)
		m.eachView(func() {
			m.bodyCache = map[string]cachedBody{}
			m.touch()
		})
	case tea.PasteStartMsg:
		m.pasting = true
		m.suggest = nil
	case tea.PasteEndMsg:
		m.pasting = false
		m.refreshSuggest()
	case tea.PasteMsg:
		if m.linkPrompt != nil || m.overlay != nil {
			break
		}
		m.quitArmed = false
		m.focus = focusComposer
		start := m.editor.cursor
		if err := m.editor.insert(msg.Content); err != nil {
			m.edit(err)
		} else {
			m.editor.foldPaste(start)
		}
		m.refreshSuggest()
	case tea.KeyPressMsg:
		return m, m.runPending(m.key(msg))
	case tea.MouseMsg:
		return m, m.runPending(m.mouse(msg))
	case dragTickMsg:
		return m, m.dragStep(msg)
	case TelemetryMsg, TelemetryLostMsg:
		return m, m.applyTelemetry(msg)
	case pulseTickMsg:
		return m, m.pulseTick(msg)
	case marqueeTickMsg:
		return m, m.marqueeAdvance(msg)
	case ImagePreparedMsg:
		m.imagePrepared(msg)
	case ImageUploadedMsg:
		return m, m.imageUploaded(msg)
	case MembersMsg:
		if m.deletedView(msg.Channel) {
			break
		}
		m.within(msg.Channel, func() {
			m.observeMembers(slices.Clone(msg.Members))
			m.touch()
		})
		m.refreshSuggest()
		accounts := make([]string, 0, len(msg.Members))
		for _, member := range msg.Members {
			accounts = append(accounts, member.Account)
		}
		return m, m.refreshForUnknown(accounts...)
	case HistoryPageMsg:
		m.within(msg.Channel, func() { m.historyPage(msg) })
	case IncomingMsg:
		if m.deletedView(msg.Message.Channel) {
			break
		}
		m.within(msg.Message.Channel, func() {
			m.captureAnchor()
			m.incoming(msg.Message)
			m.published(msg.Message)
			m.touch()
		})
		if !msg.Message.Replay {
			return m, m.refreshForUnknown(msg.Message.Account)
		}
	case DeliveryMsg:
		channel := msg.Channel
		if channel == "" && msg.ID == m.inflight {
			channel = m.inflightChannel
		}
		m.within(channel, func() {
			m.captureAnchor()
			m.delivery(msg)
			m.touch()
		})
	case ConnectionMsg:
		m.connection(msg)
	case NickHeldMsg:
		m.nickHeld()
	case GapMsg:
		m.gap(msg)
	case CatalogMsg:
		m.catalog(msg)
	case MembershipMsg:
		m.membershipChanged(msg)
	case ControlMsg:
		m.controlDone(msg)
	case RefreshLinkMsg:
		m.refreshLink(msg.Link)
	case RefreshResultMsg:
		m.refreshResult(msg)
	case ActionResultMsg:
		if msg.Err != nil {
			m.say(levelError, msg.Err.Error())
		} else if msg.Text != "" {
			m.say(levelOK, msg.Text)
		}
	case AttachTargetMsg:
		// Answers come wrapped by requestAttach; a bare one acts only when
		// no lookup is pending, so it cannot stand in for another's answer.
		if m.attaching == nil {
			return m, m.attachResolved(msg)
		}
	case attachDoneMsg:
		return m, m.attachDone(msg)
	case NickSavedMsg:
		return m, m.nickSaved(msg)
	case AgentsMsg:
		m.agents(msg)
	case TopicMsg:
		if m.deletedView(msg.Channel) {
			break
		}
		m.within(msg.Channel, func() {
			m.topic, m.topicBy, m.topicKnown = oneLine(msg.Text), oneLine(msg.By), !msg.Unknown
			if msg.Unknown {
				m.topic, m.topicBy = "", ""
			}
			if msg.Live {
				m.addPresence(presenceTopic, m.topicBy+"\x00"+m.topic)
			}
			m.touch()
		})
	case SessionMsg:
		m.opts.Nick = msg.Nick
		if msg.Identities != nil {
			m.opts.Identities = msg.Identities
		}
		m.refreshParticipantLabels()
		// A new session may map accounts differently: nothing read or asked
		// under the old one applies.
		m.runtime, m.runtimeAt, m.runtimeErr = nil, time.Time{}, ""
		m.cancelAttach("the chat session changed")
		m.eachView(func() {
			m.bodyCache = map[string]cachedBody{}
			m.touch()
		})
	}
	return m, nil
}

// historyPage finishes the visible-or-hidden view's older-page request.
func (m *Model) historyPage(msg HistoryPageMsg) {
	if msg.ID != "" && m.olderRequest != "" && msg.ID != m.olderRequest {
		return // a page this view did not ask for
	}
	m.captureAnchor()
	m.loadingOlder, m.olderRequest = false, ""
	m.touch()
	switch {
	case msg.Count == 0:
		m.historyEnd = true
		m.say(levelInfo, "Beginning of retained history")
	case msg.Count < 0:
		m.say(levelWarn, "Older history is unavailable")
	case m.olderSkipped > 0:
		m.say(levelWarn, fmt.Sprintf("Display history is full; %d older messages were not shown", m.olderSkipped))
	case m.filter != nil:
		// The page is the one before the oldest underlying message, matched
		// or not; nothing else is fetched until the next explicit Page Up.
		keep, shown := m.shown(m.bindings()), 0
		for i := range min(m.olderAt, len(m.messages)) {
			if keep[i] {
				shown++
			}
		}
		m.say(levelInfo, fmt.Sprintf("Loaded %d older messages; %d shown by the filter · Page Up loads more", msg.Count, shown))
	default:
		m.say(levelInfo, fmt.Sprintf("Loaded %d older messages", msg.Count))
	}
}

// gap marks continuity lost in one channel or, for an empty channel, in all
// of them: their roster, topic and membership are unknown until reported.
func (m *Model) gap(msg GapMsg) {
	mark := func() {
		m.addPresence(presenceGap, oneLine(msg.Text))
		m.touch()
	}
	if msg.Channel != "" {
		m.within(msg.Channel, mark)
	} else {
		m.eachView(mark)
	}
	m.say(levelWarn, "History gap: "+msg.Text)
}

// runPending runs a command chosen from the suggestions with Enter or a
// click, after the input that chose it.
func (m *Model) runPending(cmd tea.Cmd) tea.Cmd {
	if !m.pendingRun {
		return cmd
	}
	m.pendingRun = false
	return tea.Batch(cmd, m.submit())
}

func (m *Model) connection(msg ConnectionMsg) {
	m.captureAnchor()
	m.connected = msg.Connected
	if msg.Connected {
		m.attachments = msg.Attachments
	}
	m.eachView(func() {
		if msg.Connected && msg.MaxBytes > 0 {
			m.editor.limit = min(msg.MaxBytes, m.opts.MaxMessageBytes)
		}
		if msg.Connected && m.joinedAt.IsZero() && m.canSend() {
			m.joinedAt = time.Now()
		}
		if !msg.Connected {
			// Unknown until the next stream lists the room again.
			m.members = nil
			if m.reconnecting && msg.Err == nil {
				m.loadingOlder, m.historyEnd = false, false
			}
		}
		m.touch()
	})
	if msg.Connected && msg.MaxLines > 0 {
		m.maxLines = msg.MaxLines
	}
	if msg.Connected {
		if m.reconnecting {
			m.reconnecting = false
			m.say(levelOK, "Reconnected as "+m.opts.Nick)
		} else if msg.Err != nil {
			m.say(levelWarn, msg.Err.Error())
		}
		return
	}
	m.linkPrompt = nil
	m.pending, m.joinSwitch = nil, joinSwitch{} // a new stream starts with no outstanding action
	uncertain := m.inflight != ""
	if uncertain {
		m.within(m.inflightChannel, func() {
			m.delivery(DeliveryMsg{ID: m.inflight, State: Uncertain, Err: errors.New("connection lost; delivery must be inspected")})
		})
	}
	m.lostImage()
	if m.reconnecting && msg.Err == nil {
		m.say(levelInfo, "Reconnecting…")
		return
	}
	m.reconnecting = false
	// Reconnect is explicit: a new view loads history; nothing is resent.
	notice := "Disconnected; rerun yoloctl chat to reconnect"
	if msg.Err != nil {
		notice += " (" + msg.Err.Error() + ")"
	}
	if uncertain {
		notice += "; the last send is uncertain, check history before resending"
	}
	m.say(levelError, notice)
}

// nickHeld asks whether to replace this account's other session holding the
// nickname. The refused stream connected nothing; without a host TakeOver
// the refusal is only reported.
func (m *Model) nickHeld() {
	m.reconnecting = false
	if m.opts.TakeOver == nil {
		m.say(levelError, "@"+oneLine(m.opts.Nick)+" is in use by another chat session of your account")
		return
	}
	m.openOverlay(overlayState{kind: overlayTakeOver})
}

// nickSaved continues Save and reconnect once the host saved the nickname.
func (m *Model) nickSaved(msg NickSavedMsg) tea.Cmd {
	if m.overlay != nil && m.overlay.kind == overlaySettings {
		m.overlay.saving = false
	}
	if msg.Err != nil {
		if m.overlay != nil && m.overlay.kind == overlaySettings {
			m.overlay.problem = msg.Err.Error()
		}
		m.say(levelError, "Nickname not saved: "+msg.Err.Error())
		return nil
	}
	m.overlay = nil
	if m.opts.Reconnect == nil {
		m.say(levelOK, "Saved nickname "+msg.Nick+"; it applies the next time you open chat")
		return nil
	}
	if m.inflight != "" {
		m.say(levelWarn, "Saved nickname "+msg.Nick+"; reconnect after the unresolved send settles (/settings)")
		return nil
	}
	m.reconnecting = true
	m.say(levelInfo, "Saved nickname "+msg.Nick+"; reconnecting…")
	return m.opts.Reconnect()
}

func (m *Model) edit(err error) {
	if err != nil {
		m.say(levelError, err.Error())
	}
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	e := &m.editor
	k.Mod &^= tea.ModCapsLock | tea.ModNumLock
	if m.linkPrompt != nil {
		return m.linkPromptKey(k)
	}
	if m.overlay != nil {
		return m.overlayKey(k)
	}
	if m.noticeLevel <= levelOK {
		m.clearNotice()
	}
	if m.pasting {
		m.quitArmed = false
		m.focus = focusComposer
		switch k.Code {
		case tea.KeyEnter:
			m.edit(e.insert("\n"))
		case tea.KeyTab:
			m.edit(e.insert("\t"))
		default:
			m.edit(e.insert(k.Text))
		}
		return nil
	}
	// Text-only/extended events are committed data, including payloads that
	// spell a command name. Printable text with Shift is data too.
	if k.Text != "" && (k.Code == 0 || k.Code == tea.KeyExtended || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModMeta|tea.ModHyper) == 0) {
		m.quitArmed = false
		m.focus = focusComposer
		m.edit(e.insert(k.Text))
		m.refreshSuggest()
		return nil
	}
	// String() prefers committed Text, which may literally be "enter" or
	// "ctrl+c". Only physical keystrokes may dispatch commands.
	key := strings.ToLower(k.Keystroke())
	if key != "up" && key != "down" {
		e.column = nil
	}
	if key != "ctrl+c" {
		m.quitArmed = false
	}
	switch key {
	case "ctrl+c":
		// Quit is explicit: a second Ctrl+C confirms, so a draft is not lost.
		if m.quitArmed {
			return tea.Quit
		}
		m.quitArmed = true
		notice := "Press Ctrl+C again to quit"
		if strings.TrimSpace(e.text) != "" {
			notice += "; the unsent draft will be lost"
		}
		m.say(levelWarn, notice)
		return nil
	case "ctrl+p":
		m.toggleParticipants()
		return nil
	case "ctrl+g":
		m.openOverlay(overlayState{kind: overlayChannels, index: max(slices.Index(m.order, m.name), 0)})
		return nil
	case "alt+m":
		if !k.IsRepeat {
			m.toggleForMe()
		}
		return nil
	case "alt+f":
		if !k.IsRepeat {
			m.openFilterPicker()
		}
		return nil
	case quoteKey:
		if k.IsRepeat {
			return nil
		}
		return m.quoteSelection()
	case "alt+left":
		return m.cycle(-1)
	case "alt+right":
		return m.cycle(1)
	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		if i := int(key[len(key)-1] - '1'); i < len(m.order) {
			return m.selectChannel(m.order[i])
		}
		return nil
	case "ctrl+s":
		m.toggleSource()
		return nil
	case "pgup":
		return m.scrollUp(m.page())
	case "pgdown":
		m.moveScroll(m.page())
		return nil
	case "alt+pgup", "shift+pgup":
		m.scrollRoster(-max(m.layout.rosterPage, 1))
		return nil
	case "alt+pgdown", "shift+pgdown":
		m.scrollRoster(max(m.layout.rosterPage, 1))
		return nil
	case "ctrl+up":
		return m.scrollUp(1)
	case "ctrl+down":
		m.moveScroll(1)
		return nil
	case "alt+end":
		m.jumpToLatest()
		return nil
	case "alt+up":
		m.selectMessage(-1)
		return nil
	case "alt+down":
		m.selectMessage(1)
		return nil
	case "ctrl+r":
		return m.runAction("reply")
	case "ctrl+shift+c", "alt+y":
		return m.runAction("copy")
	case "alt+c":
		return m.runAction("code")
	case "ctrl+o":
		if k.IsRepeat {
			return nil
		}
		return m.runAction("link")
	}
	if m.focus == focusMessages {
		if cmd, handled := m.messageFocusKey(key); handled {
			return cmd
		}
		m.focus = focusComposer
	}
	if m.suggest != nil {
		if cmd, handled := m.suggestKey(key); handled {
			return cmd
		}
	}
	switch key {
	case "enter":
		return m.submit()
	case "alt+enter", "shift+enter", "ctrl+j":
		m.edit(e.insert("\n"))
	case "tab":
		if !m.complete() {
			m.edit(e.insert("\t"))
		}
	case "left", "ctrl+b":
		e.cursor = e.previous()
	case "right", "ctrl+f":
		e.cursor = e.next()
	case "home", "ctrl+a":
		e.cursor = e.lineStart()
	case "end", "ctrl+e":
		e.cursor = e.lineEnd()
	case "ctrl+home":
		e.cursor = 0
	case "ctrl+end":
		e.cursor = len(e.text)
	case "up":
		e.vertical(m.widthMethod, -1, m.composerWidth())
	case "down":
		e.vertical(m.widthMethod, 1, m.composerWidth())
	case "backspace", "ctrl+h":
		m.edit(e.replace(e.previous(), e.cursor, ""))
	case "ctrl+v":
		return m.pasteImage()
	case "delete", "ctrl+d":
		m.edit(e.replace(e.cursor, e.next(), ""))
	case "ctrl+k":
		end := e.lineEnd()
		if end == e.cursor && end < len(e.text) {
			end++ // at EOL, kill its newline
		}
		m.edit(e.kill(e.cursor, end))
	case "ctrl+u":
		m.edit(e.kill(e.lineStart(), e.cursor))
	case "ctrl+w", "alt+backspace":
		m.edit(e.kill(e.wordStart(), e.cursor))
	case "ctrl+y":
		m.edit(e.insert(e.killed))
	case "ctrl+z":
		m.edit(e.restore(false))
	case "ctrl+shift+z":
		m.edit(e.restore(true))
	case "alt+p":
		if !e.expand() {
			m.say(levelInfo, "No collapsed paste beside the cursor")
		}
	case "esc":
		m.escape()
	default:
		if k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) == 0 && k.Text != "" {
			m.edit(e.insert(k.Text))
		}
	}
	m.refreshSuggest()
	return nil
}

// escape steps back one level: highlight, the jump back to the latest
// messages, reply, then selection; any notice clears. The jump keeps the
// draft, reply and selection.
func (m *Model) escape() {
	switch {
	case m.sel != nil:
		// The pick stays; Enter returns to the first action, Reply.
		m.sel, m.actionIndex = nil, 0
	case m.behindLatest():
		m.jumpToLatest()
	case m.replyTo != "":
		m.replyRevision++
		m.replyTo = ""
	case m.picked:
		m.picked = false
		m.touch()
	}
	m.clearNotice()
}

// suggestKey routes keys while the suggestion popup is open. Enter accepts
// only an open popup; Alt/Shift+Enter still insert a newline.
func (m *Model) suggestKey(key string) (tea.Cmd, bool) {
	s := m.suggest
	switch key {
	case "up":
		s.index = (s.index + len(s.items) - 1) % len(s.items)
	case "down":
		s.index = (s.index + 1) % len(s.items)
	case "tab":
		m.acceptSuggestion(s.index, false)
	case "enter":
		// Enter inserts only a row that differs from the typed word, so an
		// already complete command or nick still runs or sends.
		if strings.EqualFold(strings.TrimSpace(s.items[s.index].insert), s.word) {
			m.suggest = nil
			return nil, false
		}
		m.acceptSuggestion(s.index, true)
	case "esc":
		m.dismissed, m.suggest = s.word, nil
	default:
		return nil, false
	}
	return nil, true
}

// messageFocusKey handles keys while a message is selected for actions.
func (m *Model) messageFocusKey(key string) (tea.Cmd, bool) {
	actions := m.messageActions()
	switch key {
	case "up":
		m.selectMessage(-1)
	case "down":
		m.selectMessage(1)
	case "home":
		m.selectMessage(-len(m.messages))
	case "end":
		m.selectMessage(len(m.messages))
	case "left", "shift+tab":
		if len(actions) > 0 {
			m.actionIndex = (m.actionIndex + len(actions) - 1) % len(actions)
		}
	case "right", "tab":
		if len(actions) > 0 {
			m.actionIndex = (m.actionIndex + 1) % len(actions)
		}
	case "enter":
		if m.actionIndex < len(actions) {
			return m.runAction(actions[m.actionIndex].id), true
		}
	case "esc":
		m.focus, m.picked = focusComposer, false
		m.clearNotice()
		m.touch()
	default:
		return nil, false
	}
	return nil, true
}

type messageAction struct{ id, label, keys string }

// messageActions lists what applies to the selected message, so the footer
// never offers an action that would silently do nothing.
func (m *Model) messageActions() []messageAction {
	msg := m.selectedMessage()
	if msg == nil {
		return nil
	}
	var out []messageAction
	// A highlight in this message makes Quote the first action, so Enter
	// quotes it; without one, Reply comes first and Enter replies.
	if i, _, why := m.quoteTarget(); why == "" && &m.messages[i] == msg {
		out = append(out, messageAction{"quote", quoteLabel, "Alt+Q"})
	}
	if msg.ID != "" {
		out = append(out, messageAction{"reply", "↩ Reply", "Ctrl+R"})
	}
	out = append(out, messageAction{"copy", "Copy", "Alt+Y"})
	if messageID(msg.ID) {
		out = append(out, messageAction{"reference", "Copy reference", ""})
	}
	if msg.ID != "" {
		out = append(out, messageAction{"thread", "Thread", ""})
	}
	if _, ok := firstCode(msg.Text); ok {
		out = append(out, messageAction{"code", "Copy code", "Alt+C"})
	}
	switch {
	case msg.Artifact != nil:
		out = append(out, messageAction{"artifact", "Copy file id", ""})
	case len(msg.Attachments) == 1:
		out = append(out, messageAction{"artifact", "Copy file id", ""})
	case len(msg.Attachments) > 1:
		out = append(out, messageAction{"artifact", "Copy file ids", ""})
	}
	if msg.Artifact == nil && firstLink(msg.Text) != "" && m.opts.OpenLink != nil {
		out = append(out, messageAction{"link", "↗ Open link", "Ctrl+O"})
	}
	w := m.author(msg)
	if w.trusted && w.box && !w.human && !w.me {
		out = append(out, messageAction{"attach", "Attach " + w.name, ""})
	}
	if !w.me && msg.Nick != "" {
		out = append(out, messageAction{"mention", "@" + m.mentionToken(msg.Nick, msg.Account), ""})
	}
	return out
}

// copyCode copies one fenced block's code, from its copy button, under the
// same seams.clipboard policy as every other explicit copy.
func (m *Model) copyCode(code string) tea.Cmd {
	if m.opts.Copy == nil || m.opts.Clipboard == "off" {
		m.say(levelWarn, "Host clipboard is off (seams.clipboard); nothing was copied")
		return nil
	}
	m.say(levelOK, "Copied the code block")
	return m.opts.Copy(code)
}

// runAction acts on the selected message and names it in the outcome, or
// explains why the action does not apply.
func (m *Model) runAction(id string) tea.Cmd {
	msg := m.selectedMessage()
	if msg == nil {
		m.say(levelWarn, "No message selected: Alt+↑ or click one")
		return nil
	}
	target := m.messageName(msg)
	switch id {
	case "reply":
		if msg.ID == "" {
			m.say(levelWarn, "Not confirmed yet; you can reply once the room echoes it")
			return nil
		}
		m.replyTo = msg.ID
		m.replyRevision++
		m.focus = focusComposer
		m.say(levelInfo, "Replying to "+target+" · Esc cancels")
	case "copy":
		if m.opts.Copy == nil || m.opts.Clipboard == "off" {
			m.say(levelWarn, "Host clipboard is off (seams.clipboard); nothing was copied")
			return nil
		}
		m.say(levelOK, "Copied the original text of "+target)
		return m.opts.Copy(msg.Text)
	case "reference":
		switch {
		case !messageID(msg.ID):
			m.say(levelWarn, "Not confirmed yet; it has a reference once the room echoes it")
			return nil
		case m.opts.Copy == nil || m.opts.Clipboard == "off":
			m.say(levelWarn, "Host clipboard is off (seams.clipboard); nothing was copied")
			return nil
		}
		ref, _ := compactRef(msg.ID)
		m.say(levelOK, "Copied a reference to "+target+"; in a message, a click on it shows that message")
		return m.opts.Copy(ref)
	case "thread":
		if msg.ID == "" {
			m.say(levelWarn, "Not confirmed yet; its thread shows once the room echoes it")
			return nil
		}
		for i := range m.messages {
			if &m.messages[i] == msg {
				m.setThread(i)
			}
		}
	case "artifact":
		var ids []string
		if msg.Artifact != nil {
			ids = append(ids, msg.Artifact.ID)
		}
		for _, a := range msg.Attachments {
			ids = append(ids, a.ID)
		}
		switch {
		case len(ids) == 0:
			m.say(levelWarn, "No file in "+target)
			return nil
		case m.opts.Copy == nil || m.opts.Clipboard == "off":
			m.say(levelWarn, "Host clipboard is off (seams.clipboard); nothing was copied")
			return nil
		case len(ids) > 1:
			m.say(levelOK, fmt.Sprintf("Copied the %d file ids of %s, one per line", len(ids), target))
			return m.opts.Copy(strings.Join(ids, "\n"))
		}
		m.say(levelOK, "Copied the file id of "+target)
		return m.opts.Copy(ids[0])
	case "code":
		code, ok := firstCode(msg.Text)
		switch {
		case !ok:
			m.say(levelWarn, "No fenced code block in "+target)
			return nil
		case m.opts.Copy == nil || m.opts.Clipboard == "off":
			m.say(levelWarn, "Host clipboard is off (seams.clipboard); nothing was copied")
			return nil
		}
		m.say(levelOK, "Copied the first code block of "+target)
		return m.opts.Copy(code)
	case "link":
		link := firstLink(msg.Text)
		switch {
		case msg.Artifact != nil:
			m.say(levelInfo, "A file announcement has no web link to open; copy its file id instead")
			return nil
		case link == "":
			m.say(levelWarn, "No web link in "+target)
			return nil
		case m.opts.OpenLink == nil:
			m.say(levelWarn, "Opening links is off (seams.browser)")
			return nil
		}
		return m.openLink(link)
	case "quote":
		return m.quoteSelection()
	case "attach":
		return m.requestAttach(msg.Nick, msg.Account)
	case "mention":
		m.focus = focusComposer
		m.edit(m.editor.insert("@" + m.mentionToken(msg.Nick, msg.Account) + " "))
	}
	return nil
}

// messageName is how notices name a message: its author and time.
func (m *Model) messageName(msg *Message) string {
	w := m.author(msg)
	if w.me {
		return "your message from " + msg.Time.Local().Format("15:04")
	}
	return fmt.Sprintf("%s's message from %s", w.name, msg.Time.Local().Format("15:04"))
}

func (m *Model) openLink(link string) tea.Cmd {
	if m.opts.OpenLink == nil {
		m.say(levelWarn, "Opening links is off (seams.browser)")
		return nil
	}
	if m.opts.ConfirmLinks {
		m.linkPrompt = &linkPrompt{url: link}
		return nil
	}
	return m.opts.OpenLink(link)
}

// page is one screen of transcript minus two rows of overlap, so the
// reader keeps context across PgUp/PgDn.
func (m *Model) page() int { return max(m.lastBodyHeight-2, 1) }

func (m *Model) toggleParticipants() {
	// A side panel narrows the transcript; keep the reading position.
	m.captureAnchor()
	if panelWidth(m.width) == 0 {
		m.rosterOverlay = !m.rosterOverlay
		return
	}
	m.rosterOpen = !m.rosterOpen
	m.rosterOverlay = false
}

func (m *Model) toggleSource() {
	m.captureAnchor()
	m.touch()
	m.source = !m.source
	m.sel = nil // display text changes, so a selection would point elsewhere
}

func (m *Model) composerWidth() int { return max(m.width-composerIndent-1, 1) }

// composerIndent is the accent bar, a space, the prompt and a space.
const composerIndent = 4

func (m *Model) submit() tea.Cmd {
	text := m.editor.text
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if cmd, ok := m.slashCommand(text); ok {
		return cmd
	}
	return m.sendDraft()
}

// sendDraft sends the part that leads the draft. A draft without images is
// one message. Otherwise its text and images go in draft order, one part at
// a time: each confirmed part leaves the draft, and the next follows only if
// the draft and reply did not change meanwhile. A refused or uncertain part
// stops there, keeping the rest as the draft.
func (m *Model) sendDraft() tea.Cmd {
	e := &m.editor
	if !m.connected || m.opts.Send == nil {
		m.say(levelWarn, "Chat is disconnected; draft retained")
		return nil
	}
	if !m.canSend() {
		if m.deleted {
			m.say(levelWarn, m.name+" was deleted; this view is read-only. Draft retained")
			return nil
		}
		m.say(levelWarn, "You are not in "+m.name+"; /join "+m.name+" to write here. Draft retained")
		return nil
	}
	if m.inflight != "" || m.pending != nil {
		m.say(levelWarn, "A send or channel action is unresolved; inspect it before sending again")
		return nil
	}
	if why := m.imageUnresolved(); why != "" {
		m.say(levelWarn, why)
		return nil
	}
	if m.attachments && m.hasImages() {
		return m.sendStaged()
	}
	text, end := e.text, len(e.text)
	if i := slices.IndexFunc(e.folds, func(f fold) bool { return f.img != nil }); i >= 0 {
		end = e.folds[i].start
		text = strings.TrimRight(e.text[:end], " \t\n")
		if strings.TrimSpace(text) == "" {
			return m.uploadImage(e.folds[i].img)
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if lines := wireLines(text); m.maxLines > 0 && lines > m.maxLines {
		m.say(levelWarn, fmt.Sprintf("Message needs about %d lines; the room accepts %d per message. Shorten it or share a file", lines, m.maxLines))
		return nil
	}
	out := Outgoing{ID: rand.Text(), Channel: m.name, Text: text, ReplyTo: m.replyTo}
	m.inflight, m.inflightChannel, m.sentText, m.sentRevision = out.ID, m.name, out.Text, e.revision
	m.sentDraft, m.sentEnd, m.sentReplyRevision = e.text, end, m.replyRevision
	m.follow, m.anchor, m.picked = true, nil, false
	m.append(Message{ClientID: out.ID, Channel: m.name, Nick: m.opts.Nick, Text: out.Text, ReplyTo: out.ReplyTo, Time: time.Now(), Delivery: Pending})
	m.clearNotice()
	return m.opts.Send(out)
}

// slashCommand runs an exact known command typed as the whole draft. Any
// other text, including paths such as /workdir/x, is an ordinary message.
func (m *Model) slashCommand(text string) (tea.Cmd, bool) {
	fields := strings.Fields(text)
	// A collapsed paste is content the user did not see typed out: a draft
	// holding one is always a message, never a command.
	// So is what remains of a draft whose other parts were sent or dropped.
	if strings.Contains(strings.TrimSpace(text), "\n") || len(fields) == 0 || len(m.editor.folds) > 0 || m.editor.remainder {
		return nil, false
	}
	clear := func() { m.edit(m.editor.replace(0, len(m.editor.text), "")) }
	switch {
	case len(fields) == 1 && fields[0] == "/help":
		clear()
		m.openHelp()
	case len(fields) == 1 && fields[0] == "/settings":
		clear()
		m.openSettings()
	case len(fields) == 1 && fields[0] == "/participants":
		clear()
		m.toggleParticipants()
	case fields[0] == "/filter":
		if m.filterCommand(fields[1:]) {
			clear()
		}
	case fields[0] == "/thread":
		if len(fields) > 1 {
			m.say(levelWarn, "/thread takes no arguments; it shows the selected message's thread")
			return nil, true
		}
		clear()
		m.runAction("thread")
	case len(fields) == 1 && fields[0] == "/source":
		clear()
		m.toggleSource()
	case len(fields) == 1 && fields[0] == "/quit":
		return tea.Quit, true
	case fields[0] == "/channels" || fields[0] == "/join" || fields[0] == "/part" || fields[0] == "/topic":
		// Channel actions go through typed, bounded host operations; this
		// view never sends raw IRC.
		return m.channelCommand(strings.TrimSpace(text), fields)
	case fields[0] == "/nick" && len(fields) == 2:
		if !validNick(fields[1]) {
			m.say(levelWarn, "Nicknames use "+nickRule)
			return nil, true
		}
		cmd := m.saveNick(fields[1])
		if cmd != nil {
			clear()
		}
		return cmd, true
	case fields[0] == "/nick":
		m.say(levelWarn, "Usage: /nick NAME, with "+nickRule)
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) delivery(d DeliveryMsg) {
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.ClientID != d.ID || d.ID == "" || msg.Delivery == Confirmed || msg.Delivery == Rejected {
			continue
		}
		if d.State != Pending && d.State != Confirmed && d.State != Rejected && d.State != Uncertain {
			return
		}
		if msg.Delivery == Uncertain && d.State == Pending {
			return
		}
		msg.Delivery = d.State
		msg.Error = ""
		if d.Err != nil {
			msg.Error = d.Err.Error()
			m.say(levelError, d.Err.Error())
		} else if d.State == Rejected || d.State == Uncertain {
			msg.Error = "Send " + string(d.State) + "; draft retained"
			m.say(levelError, msg.Error)
		}
		if d.State == Confirmed {
			msg.ID = d.MessageID
			msg.Error = ""
			if m.inflight == d.ID && m.editor.revision == m.sentRevision && m.replyRevision == m.sentReplyRevision && m.editor.text == m.sentDraft {
				mixed := m.editor.remainder
				if m.sentEnd < len(m.editor.text) {
					m.edit(m.editor.remove(0, m.sentEnd))
					m.editor.remainder = true
					m.resume = m.name // the rest of the unchanged draft follows
				} else {
					m.edit(m.editor.replace(0, len(m.editor.text), ""))
				}
				if mixed || m.editor.remainder {
					// A part of a draft with images was published: no undo
					// brings it back to be sent again.
					m.editor.commit()
				}
				m.replyTo = ""
				m.suggest = nil
			}
			if m.inflight == d.ID {
				m.clearNotice()
			}
		}
		if m.inflight == d.ID && m.sentImages != nil {
			m.stagedOutcome(d, *msg)
		}
		if m.inflight == d.ID && (d.State == Confirmed || d.State == Rejected) {
			m.inflight, m.inflightChannel, m.sentText, m.sentDraft = "", "", "", ""
		}
		return
	}
}

func (m *Model) incoming(msg Message) {
	if msg.ID == "" || len(msg.Text) > MaxMessageBytes {
		m.say(levelWarn, "Invalid or oversized incoming logical message")
		return
	}
	for i, old := range m.messages {
		if old.ID == msg.ID {
			if old.Text != msg.Text {
				m.say(levelWarn, "Conflicting text for an existing message ID; original retained")
			} else if old.ClientID != "" {
				m.messages[i].Account, m.messages[i].Nick = msg.Account, msg.Nick
			}
			return // history/live replay overlap must not duplicate or wake unread
		}
		if msg.ClientID != "" && old.ClientID == msg.ClientID {
			if old.Text != msg.Text {
				m.say(levelWarn, "Correlated echo text differs; delivery remains unresolved")
				return
			}
			m.delivery(DeliveryMsg{ID: msg.ClientID, MessageID: msg.ID, State: Confirmed})
			msg.Delivery = Confirmed
			m.messages[i] = msg
			return
		}
	}
	msg.Delivery = Confirmed
	if msg.Older {
		// Older pages arrive oldest first, before the previously oldest message.
		// They never displace newer messages: past the display bounds this
		// page's oldest messages give way, so what is shown stays contiguous,
		// and paging stops visibly for this view.
		for !m.olderFits(msg) && m.olderAt > 0 {
			m.evict(0)
			m.olderSkipped++
		}
		fits := m.olderFits(msg)
		if !fits {
			m.olderSkipped++
		}
		if m.olderSkipped > 0 {
			m.historyEnd = true
			m.say(levelWarn, "Display history is full; some older messages were not shown")
		}
		if !fits {
			return
		}
		at := min(m.olderAt, len(m.messages))
		m.messages = slices.Insert(m.messages, at, msg)
		m.olderAt = at + 1
		if m.selected >= at {
			m.selected++
		}
		return
	}
	m.append(msg)
	if m.filter != nil && !msg.Replay && !m.arrivalShown(msg) {
		m.filter.hiddenNew++
	}
	if (!m.follow || !m.isCurrent()) && !msg.Replay {
		m.unread++
		if msg.Account != m.opts.Account && mentions(msg.Text, m.selfNames()) {
			m.unreadMentions++
		}
	}
}

// scrollUp moves toward older messages; at the top it requests the page before
// the oldest message once, while the display has room for it.
func (m *Model) scrollUp(rows int) tea.Cmd {
	atTop := !m.follow && m.scroll == 0 || m.lastRows <= m.lastBodyHeight
	m.moveScroll(-rows)
	if atTop && m.historyEnd {
		m.say(levelInfo, "Beginning of retained history")
	}
	if !atTop || m.loadingOlder || m.historyEnd || m.opts.LoadOlder == nil || !m.connected || m.deleted {
		return nil
	}
	oldest := ""
	for _, msg := range m.messages {
		if msg.ID != "" {
			oldest = msg.ID
			break
		}
	}
	switch {
	case oldest == "":
		return nil
	case len(m.messages) > maxMessages-50:
		m.say(levelInfo, "Display history is full; older pages are not loaded in this view")
		return nil
	}
	m.loadingOlder, m.olderAt, m.olderSkipped = true, 0, 0
	m.olderRequest = rand.Text()
	m.say(levelInfo, "Loading older history…")
	return m.opts.LoadOlder(HistoryRequest{ID: m.olderRequest, Channel: m.name, Before: oldest})
}

// wireLines estimates the IRC lines a logical message needs.
func wireLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		n += max((len(line)+wireLineBytes-1)/wireLineBytes, 1)
	}
	return n
}

// complete is the Tab fallback when no popup is open: it expands an
// "@prefix" before the cursor to a unique roster nickname. It reports false
// when there is nothing to complete, so Tab still indents.
func (m *Model) complete() bool {
	e := &m.editor
	start, _ := e.word()
	word := e.text[start:e.cursor]
	if !strings.HasPrefix(word, "@") {
		return false
	}
	prefix := strings.ToLower(word[1:])
	var matches []string
	for _, member := range sortedMembers(m.members) {
		token := m.mentionToken(member.Nick, member.Account)
		if strings.HasPrefix(strings.ToLower(token), prefix) && !slices.Contains(matches, token) {
			matches = append(matches, token)
		}
	}
	switch len(matches) {
	case 0:
		m.say(levelWarn, "No connected participant matches "+word)
	case 1:
		m.edit(e.replace(start, e.cursor, "@"+matches[0]+" "))
	default:
		m.say(levelInfo, "Matches: @"+strings.Join(matches, " @"))
	}
	return true
}

func (m *Model) append(msg Message) {
	m.captureAnchor()
	m.touch()
	m.messages = append(m.messages, msg)
	bytes := 0
	lines := 0
	for _, message := range m.messages {
		bytes += len(message.Text)
		lines += logicalLines(message.Text)
	}
	for (len(m.messages) > maxMessages || bytes > maxHistoryBytes || lines > maxLogicalLines) && len(m.messages) > 1 {
		i := 0
		if m.messages[0].ClientID == m.inflight && m.inflight != "" {
			// Never evict the only receipt correlation while a send is unresolved.
			i = 1
		}
		bytes -= len(m.messages[i].Text)
		lines -= logicalLines(m.messages[i].Text)
		m.evict(i)
		m.say(levelInfo, "Older display history was evicted; transport history is unchanged")
	}
	if len(m.bodyCache) > maxMessages {
		m.bodyCache = map[string]cachedBody{}
	}
	// A deliberately picked message stays the target of its actions.
	if m.follow && !m.picked || m.selected < 0 {
		m.selected = len(m.messages) - 1
	}
	m.trimAggregate()
}

// evict drops the displayed message at i with its cached body and anchor. It
// keeps olderAt counting only this page's inserted messages, so overflow never
// evicts a newer one. A selection touching it is dropped with it.
func (m *Model) evict(i int) {
	evicted := messageKey(m.messages[i])
	m.touch()
	delete(m.bodyCache, evicted)
	m.messages = slices.Delete(m.messages, i, i+1)
	if i < m.olderAt {
		m.olderAt--
	}
	if i == m.selected && m.picked {
		// Its actions must not move to a neighbour.
		m.picked, m.focus = false, focusComposer
	}
	m.selected = max(m.selected-1, 0)
	if m.anchor == nil || m.anchor.key == evicted {
		m.anchor = nil
		m.scroll = 0
	}
	if m.sel != nil && (m.sel.anchor.key == evicted || m.sel.focus.key == evicted) {
		m.sel = nil
		if m.isCurrent() {
			m.drag.active = false
		}
	}
}

// olderFits reports whether an older message fits the display bounds.
func (m *Model) olderFits(msg Message) bool {
	bytes, lines := len(msg.Text), logicalLines(msg.Text)
	for _, held := range m.messages {
		bytes += len(held.Text)
		lines += logicalLines(held.Text)
	}
	return len(m.messages) < maxMessages && bytes <= maxHistoryBytes && lines <= maxLogicalLines
}

func logicalLines(text string) int { return strings.Count(text, "\n") + 1 }

func (m *Model) selectedMessage() *Message {
	if m.selected < 0 || m.selected >= len(m.messages) {
		return nil
	}
	if m.filter != nil && !m.picked && !m.shown(m.bindings())[m.selected] {
		// The implicit target is the latest message shown, never a hidden one.
		shown := m.shownIndexes()
		if len(shown) == 0 {
			return nil
		}
		return &m.messages[shown[len(shown)-1]]
	}
	return &m.messages[m.selected]
}

// selectMessage moves the explicit selection over the shown messages and
// gives arrows and Enter to its actions; the transcript scrolls to keep it
// in view.
func (m *Model) selectMessage(delta int) {
	shown := m.shownIndexes()
	switch {
	case len(shown) == 0 && len(m.messages) > 0:
		m.say(levelInfo, "No loaded message matches the filter")
		return
	case len(shown) == 0:
		m.say(levelInfo, "No messages yet")
		return
	}
	// From the current message, or the nearest shown one before it.
	at := -1
	for i, n := range shown {
		if n <= m.selected {
			at = i
		}
	}
	m.selected = shown[min(max(at+delta, 0), len(shown)-1)]
	m.picked, m.focus, m.actionIndex = true, focusMessages, 0
	m.clearInfo()
	m.follow = false
	m.anchor = &viewAnchor{key: messageKey(m.messages[m.selected]), line: lineTop}
	m.suggest = nil
	m.touch()
}

// pick selects a clicked message without moving the reading position.
func (m *Model) pick(key string) {
	if i := m.indexOf(key); i >= 0 {
		m.captureAnchor()
		m.selected, m.picked, m.focus, m.actionIndex = i, true, focusMessages, 0
		m.suggest = nil
		m.clearInfo()
		m.touch()
	}
}

// clearInfo drops an informational or success notice, as any key does, so
// an explicit selection is not followed by an outdated message. Warnings
// and errors stay until read.
func (m *Model) clearInfo() {
	if m.noticeLevel <= levelOK {
		m.clearNotice()
	}
}

// behindLatest reports whether the reader is away from the live tail with
// something to return to: the condition for the jump to the latest messages.
func (m *Model) behindLatest() bool {
	return !m.follow && (m.unread > 0 || m.scrolledBack())
}

// jumpToLatest returns to the live tail, from a click on the jump, Alt+End
// or Esc. It changes only the reading position: the draft, reply, staged
// images and a picked message stay.
func (m *Model) jumpToLatest() {
	m.follow, m.unread, m.unreadMentions, m.anchor = true, 0, 0, nil
}

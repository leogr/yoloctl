package chatui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

type overlayKind int

const (
	overlayHelp overlayKind = iota + 1
	overlaySettings
	overlayCard
	overlayConfirm
	overlayChannels
	overlayFilter
	overlayTakeOver
)

// overlayState is the one modal panel drawn over the transcript. While it
// is open, keys and clicks reach only the panel; the room keeps updating.
type overlayState struct {
	kind   overlayKind
	scroll int
	index  int
	// settings: the nickname field while editing it, and its feedback
	nick    *editor
	saving  bool
	problem string
	// card and confirm
	who    who
	member *Member
	target AttachTarget
	// filter: the picker's rows, fixed while it is open, and the selection
	// Enter applies
	choices      []filterChoice
	staged       []filterPick
	stagedThread bool
}

func (m *Model) openOverlay(o overlayState) {
	m.overlay = &o
	m.suggest = nil
}

func (m *Model) openHelp() { m.openOverlay(overlayState{kind: overlayHelp}) }

func (m *Model) openSettings() { m.openOverlay(overlayState{kind: overlaySettings}) }

// openCard shows who a participant is and what can be done with them.
func (m *Model) openCard(nick, account string) {
	o := overlayState{kind: overlayCard, who: m.identify(nick, account)}
	for i := range m.members {
		if m.members[i].Account == account && account != "" || account == "" && m.members[i].Nick == nick {
			mem := m.members[i]
			o.member = &mem
			break
		}
	}
	m.openOverlay(o)
}

// refreshParticipantLabels updates open surfaces after the trusted identity
// map changes, keeping the card on the same account and live connection.
func (m *Model) refreshParticipantLabels() {
	if o := m.overlay; o != nil && o.kind == overlayCard {
		o.who = m.identify(o.who.nick, o.who.account)
	}
	if m.suggest != nil {
		m.refreshOpenSuggest()
	}
}

// helpSections is the reference shown by /help. Each entry names
// its keys and what they act on, including the cases where they cannot.
var helpSections = []struct {
	title string
	rows  [][2]string
}{
	{"Sending", [][2]string{
		{"Enter", "Send the draft. It shows “sending” until the room echoes it back; nothing is ever resent automatically."},
		{"Alt+Enter  Shift+Enter  Ctrl+J", "Insert a new line"},
		{"@name", "Notify someone. Agents wake only for an explicit @name or @all; a plain name notifies nobody."},
		{"Tab  ↑↓  Enter", "Pick from the suggestions that open while you type @ or a leading /"},
		{"Esc", "Close suggestions or clear a highlight, then jump to the latest messages, then cancel a reply or the selection. The draft stays."},
	}},
	{"Editing", [][2]string{
		{"Ctrl+K  Ctrl+U  Ctrl+W", "Cut to the end of the line, to its start, or the previous word"},
		{"Ctrl+Z  Ctrl+Shift+Z", "Undo and redo, including a cut"},
		{"Ctrl+A  Ctrl+E  Home  End", "Start and end of the line"},
		{"Alt+P  click [Pasted #n]", "A long paste shows as one chip but sends in full; this shows its text. Backspace removes it whole."},
		{"Ctrl+V", "Paste a PNG from the host clipboard (seams.clipboard) as [Image #n] at the cursor. Enter sends text and images in draft order, each after the previous one is confirmed; Backspace removes an image whole."},
	}},
	{"Reading", [][2]string{
		{"PgUp  PgDn  wheel", "Scroll; at the top, the page before the oldest message loads"},
		{"Alt+End  Esc  click ↓", "Jump to the latest messages, keeping the draft, reply and selection. Scrolled back, the view stays put as messages arrive."},
		{"Ctrl+S", "Show messages as rendered Markdown or as their source"},
		{"For me  Alt+M", "Show only the loaded messages you sent or that @mention you, and loaded replies to those: For me beside the channel name on the composer divider, or Alt+M; again shows everything."},
		{"Alt+F  /filter", "Choose one or more people: a message shows when any of them sent it or is @mentioned in it, or when it replies to a loaded message shown. In the picker Space checks, Enter applies, Esc cancels. A card's Filter messages chooses its participant. Each channel keeps its own; ✕ or All messages shows everything again. Page Up still loads the page before the oldest message."},
		{"Thread  /thread", "Show only the selected message's thread: the first loaded message of its reply chain and every loaded reply under it, all branches, whichever of them you select. It replaces For me or people, and they replace it. If the thread starts above the loaded messages, Page Up loads older ones; nothing loads by itself."},
	}},
	{"Messages", [][2]string{
		{"Alt+↑  Alt+↓  click", "Select a message. Its actions stay at the bottom, naming it, until you send, press Esc or select another; clicking them works while typing."},
		{"← →  Enter  Esc", "With a message selected: choose an action, run it, or return to typing. Typing returns too. Enter starts on Reply, or on Quote while a highlight lies in the selected message."},
		{"Ctrl+R", "Reply to the selected message"},
		{"Alt+Q  ❝ Quote", "Quote the highlighted part of one message in the draft at the cursor, as > lines of its original Markdown, and reply to that message. Uses no clipboard. Ctrl+Z removes the quote; a highlight that cuts formatting the view adds is refused (source view, Ctrl+S, quotes any part). Table Quote keeps selected cell text, separates cells with TAB and rows with newlines; use source view for original Markdown."},
		{"Alt+Y  Ctrl+Shift+C", "Copy the selected message's original text"},
		{"Alt+C", "Copy its first fenced code block"},
		{"Ctrl+O  click a link", "Open a web link with the host browser policy (seams.browser)"},
		{"Copy reference  m:…", "Copies the selected message's shared reference, the same in every client; pasted into a message, a click on it shows that message if it is loaded in the same channel (Page Up loads older ones). msgid: and the full ID works the same. A loaded message a reference names shows in its place as ↗ author: text…; click it to show the message. Copy and Quote keep the reference."},
	}},
	{"Selecting text", [][2]string{
		{"Drag", "Select message text; it also selects the message where it starts, so Enter quotes it. Releasing copies it. Names, times, gutters and the participants panel are never copied. Wrapped lines rejoin and messages are separated by a blank line."},
		{"", "Copy uses what is displayed: rendered Markdown, tabs as spaces. Use Alt+Y for the exact original, or Ctrl+S to select source."},
		{"Alt+Q  ❝ Quote", "Quote the highlight in the draft and reply to its message (see Messages); with that message selected, Enter quotes too"},
		{"Esc", "Clear the highlight"},
	}},
	{"Channels", [][2]string{
		{"Ctrl+G  click the channel", "Choose a channel; if not joined, join before showing it when permitted. Each keeps its draft, reply, reading position, selection and unread count"},
		{"Alt+1…9  Alt+← →", "Switch by position, or to the previous/next channel (unread first)"},
		{"/join NAME  /part [NAME]", "Follow or leave a channel; leaving keeps its draft and history but disables sending until you deliberately select or join it again"},
		{"/topic  /topic set TEXT  /topic clear", "Show or change this channel's topic, as the room allows"},
		{"#name", "Suggests channel names as you type; inserting one only adds text"},
	}},
	{"Participants", [][2]string{
		{"Ctrl+P", "Show or hide the panel; on narrow screens it opens over the messages"},
		{"wheel  Alt+PgUp  Alt+PgDn", "Scroll the participants when their cards do not fit: the wheel over the panel moves one card, the keys a page (Shift+PgUp and Shift+PgDn too, where the terminal passes them)"},
		{"Click a box", "Attach that box's session, after its membership and declaration are checked again"},
		{"Right-click a name", "Details. Only boxes can be attached; direct messages are not supported."},
		{"@  +", "Channel operator · voiced"},
		{"Opus 5.5 (max)", "Model and effort the agent reported for its running session. A value that is unknown, or whose last read failed or is over two minutes old, is left out; details show the context window and when it was read."},
	}},
	{"Commands", [][2]string{
		{"/help  /settings", "This reference · nickname, panels and host policies"},
		{"/nick NAME", "Change your nickname; the view saves it, then reconnects"},
		{"/participants  /source  /quit", "Toggle the panel · toggle source view · leave"},
		{"/filter [me | all | NAME …]", "Choose whose messages and mentions to show, or show only yours, everyone's, or those of the participants named"},
		{"/thread", "Show only the thread of the selected message, or of the latest shown one"},
		{"", "Only these exact commands are intercepted; other text starting with / is sent as typed."},
	}},
	{"Leaving", [][2]string{
		{"Ctrl+C  Ctrl+C", "Quit. The first press warns, so an unsent draft is not lost by accident."},
	}},
}

// settingRow is one Settings line. Only real options appear: read-only
// rows explain where a host policy is changed instead of faking a control.
type settingRow struct {
	id, label, value, hint string
	editable               bool
}

func (m *Model) settingRows() []settingRow {
	onOff := func(v bool, on, off string) string {
		if v {
			return on
		}
		return off
	}
	policy := func(v string) string {
		if v == "" {
			return "unknown"
		}
		return v
	}
	account := m.opts.Account
	if account == "" {
		account = "unknown"
	}
	nick := m.savedNick()
	switch {
	case nick == "":
		nick = "—"
	case !strings.EqualFold(nick, m.opts.Nick) && m.opts.Nick != "":
		nick += " · this view: @" + oneLine(m.opts.Nick)
	}
	return []settingRow{
		{"nick", "Nickname", nick, "Enter to change. Saving reconnects this view; your draft and reply stay.", m.opts.SaveNick != nil},
		{"account", "Signed in as", account, "Authenticated room account. A nickname never changes it.", false},
		{"panel", "Participants panel", onOff(m.rosterOpen, "shown", "hidden"), "Enter toggles (also Ctrl+P)", true},
		{"view", "Message view", onOff(m.source, "source", "Markdown"), "Enter toggles (also Ctrl+S)", true},
		{"copy", "Copy on select", onOff(m.copyOnSelect, "on", "off"), "Releasing a drag copies the selection", true},
		{"clipboard", "Host clipboard", policy(m.opts.Clipboard), "seams.clipboard in ~/.config/yoloctl/tui.yaml", false},
		{"browser", "Open links", policy(m.opts.Browser), "seams.browser (on · ask · off) in ~/.config/yoloctl/tui.yaml", false},
	}
}

func (m *Model) overlayKey(k tea.KeyPressMsg) tea.Cmd {
	o := m.overlay
	key := strings.ToLower(k.Keystroke())
	if o.kind == overlaySettings && o.nick != nil {
		return m.nickKey(k, key)
	}
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 && key != "space" {
		return nil // text never acts inside a panel
	}
	if o.kind == overlayFilter {
		switch key {
		case "alt+f":
			if !k.IsRepeat {
				m.overlay = nil
			}
			return nil
		case "space":
			m.toggleFilterChoice()
			return nil
		}
	}
	switch key {
	case "esc", "ctrl+c":
		if o.kind == overlayTakeOver {
			return m.overlayActivate(1)
		}
		m.overlay = nil
		if o.kind == overlayConfirm {
			m.say(levelInfo, "Attach canceled")
		}
		return nil
	case "up":
		switch o.kind {
		case overlaySettings, overlayChannels, overlayFilter:
			o.index = max(o.index-1, 0)
		default:
			o.scroll = max(o.scroll-1, 0)
		}
	case "down":
		switch o.kind {
		case overlaySettings:
			o.index = min(o.index+1, len(m.settingRows())-1)
		case overlayChannels:
			o.index = min(o.index+1, len(m.order)-1)
		case overlayFilter:
			o.index = min(o.index+1, len(o.choices)-1)
		default:
			o.scroll++
		}
	case "ctrl+g":
		if o.kind == overlayChannels {
			m.overlay = nil
		}
	case "pgup":
		o.scroll = max(o.scroll-max(m.layout.bodyHeight-2, 1), 0)
	case "pgdown":
		o.scroll += max(m.layout.bodyHeight-2, 1)
	case "enter", "space":
		return m.overlayActivate(-1)
	}
	return nil
}

// overlayActivate runs the overlay's primary action, or the numbered button.
func (m *Model) overlayActivate(button int) tea.Cmd {
	o := m.overlay
	switch o.kind {
	case overlayHelp:
		m.overlay = nil
	case overlaySettings:
		return m.activateSetting(m.settingRows()[o.index].id)
	case overlayChannels:
		m.overlay = nil
		if o.index < len(m.order) {
			return m.selectChannel(m.order[o.index])
		}
	case overlayFilter:
		m.overlay = nil
		m.applyFilterChoices(o)
	case overlayCard:
		actions := m.cardActions()
		if button < 0 {
			button = 0
		}
		if button < len(actions) {
			return m.cardAction(actions[button])
		}
	case overlayTakeOver:
		m.overlay = nil
		if button == 1 {
			// Cancel: this view does not join, and the other session stays.
			return tea.Quit
		}
		m.reconnecting = true
		m.say(levelInfo, "Taking over @"+oneLine(m.opts.Nick)+"…")
		return m.opts.TakeOver()
	case overlayConfirm:
		if button == 1 {
			m.overlay = nil
			m.say(levelInfo, "Attach canceled")
			return nil
		}
		target := o.target
		m.overlay = nil
		if m.opts.Attach == nil {
			m.say(levelWarn, "Attaching is unavailable in this view")
			return nil
		}
		m.say(levelInfo, "Starting and attaching "+target.Box+"…")
		return m.opts.Attach(target)
	}
	return nil
}

func (m *Model) activateSetting(id string) tea.Cmd {
	switch id {
	case "nick":
		if m.opts.SaveNick == nil {
			m.overlay.problem = "Nickname changes are unavailable in this view"
			return nil
		}
		m.overlay.nick = &editor{limit: 24}
		_ = m.overlay.nick.replace(0, 0, m.savedNick())
		m.overlay.problem = ""
	case "panel":
		m.toggleParticipants()
	case "view":
		m.toggleSource()
	case "copy":
		m.copyOnSelect = !m.copyOnSelect
	}
	return nil
}

// nickKey edits the nickname field; Enter saves and then reconnects.
func (m *Model) nickKey(k tea.KeyPressMsg, key string) tea.Cmd {
	o := m.overlay
	e := o.nick
	if o.saving {
		return nil
	}
	if k.Text != "" && (k.Code == 0 || k.Code == tea.KeyExtended || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModMeta|tea.ModHyper) == 0) {
		if err := e.insert(k.Text); err != nil {
			o.problem = "A nickname has at most 24 characters"
		}
		return nil
	}
	switch key {
	case "esc":
		o.nick, o.problem = nil, ""
	case "enter":
		return m.saveNick(e.text)
	case "backspace", "ctrl+h":
		_ = e.replace(e.previous(), e.cursor, "")
	case "delete", "ctrl+d":
		_ = e.replace(e.cursor, e.next(), "")
	case "left":
		e.cursor = e.previous()
	case "right":
		e.cursor = e.next()
	case "home", "ctrl+a":
		e.cursor = 0
	case "end", "ctrl+e":
		e.cursor = len(e.text)
	case "ctrl+u":
		_ = e.replace(0, e.cursor, "")
	}
	return nil
}

// validNick mirrors the room's nickname grammar so mistakes are explained
// before saving: 1–24 ASCII letters, digits, _ or -, starting with a letter
// or _. The host remains authoritative, including for collisions.
func validNick(nick string) bool {
	if len(nick) == 0 || len(nick) > 24 {
		return false
	}
	for i := 0; i < len(nick); i++ {
		c := nick[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		if !letter && (i == 0 || (c < '0' || c > '9') && c != '-') {
			return false
		}
	}
	return true
}

const nickRule = "1–24 ASCII letters, digits, _ or -, starting with a letter or _"

// savedNick is the local human's saved room nickname from the trusted
// identity map, never derived from the live alias this view connects as;
// without one it is the live nickname.
func (m *Model) savedNick() string {
	if id, ok := m.opts.Identities[m.opts.Account]; ok && m.opts.Account != "" && id.Nick != "" {
		return id.Nick
	}
	return m.opts.Nick
}

// saveNick starts Save and reconnect. It refuses while a send is unresolved,
// because reconnecting must never hide or replay its outcome.
func (m *Model) saveNick(nick string) tea.Cmd {
	problem := ""
	switch {
	case !validNick(nick):
		problem = "Use " + nickRule
	case nick == m.savedNick():
		problem = "That is already your nickname"
	case m.inflight != "":
		problem = "A send is unresolved; wait for its outcome before reconnecting"
	case m.opts.SaveNick == nil:
		problem = "Nickname changes are unavailable in this view"
	}
	if problem != "" {
		if m.overlay != nil && m.overlay.kind == overlaySettings {
			m.overlay.problem = problem
		} else {
			m.say(levelWarn, problem)
		}
		return nil
	}
	if m.overlay != nil && m.overlay.kind == overlaySettings {
		m.overlay.saving, m.overlay.problem = true, ""
	}
	m.say(levelInfo, "Saving nickname "+nick+"…")
	return m.opts.SaveNick(nick)
}

type cardAction string

func (m *Model) cardActions() []cardAction {
	o := m.overlay
	var out []cardAction
	if o.who.trusted && o.who.box && !o.who.human {
		out = append(out, "attach")
	}
	if o.member != nil {
		out = append(out, "mention")
	}
	if o.who.account != "" {
		out = append(out, "filter")
	}
	return append(out, "close")
}

func (m *Model) cardAction(a cardAction) tea.Cmd {
	o := m.overlay
	m.overlay = nil
	switch a {
	case "attach":
		return m.requestAttach(o.who.nick, o.who.account)
	case "mention":
		m.focus = focusComposer
		m.edit(m.editor.insert("@" + m.mentionToken(o.member.Nick, o.member.Account) + " "))
	case "filter":
		m.setFilter(o.who.account, o.who.nick)
	}
	return nil
}

// panelBox lays a framed panel of the given inner rows over the body
// region, centered, and records the button targets.
func (m *Model) overlayLines(base []string, width int) []string {
	o := m.overlay
	l := m.layout
	t := m.theme
	// The panel's own hit goes beneath its rows and buttons: hitAt prefers
	// the latest hit, so appending it last would swallow every click on them.
	under := len(m.hits)
	inner := min(width-4, 84)
	var title string
	var rows []string
	var buttons []string
	var hint string
	// rowItems maps a row to the clickable item it shows: the channel or
	// setting index, or -1 for wrapped hints and notes.
	var rowItems []int
	switch o.kind {
	case overlayHelp:
		title = "Help"
		rows = m.helpRows(inner)
		hint = "↑↓ PgUp PgDn scroll · Esc closes"
	case overlaySettings:
		title = "Settings"
		rows, rowItems = m.settingsView(inner)
		hint = "↑↓ choose · Enter change · Esc closes"
		if o.nick != nil {
			hint = "Enter saves and reconnects · Esc cancels"
		}
	case overlayCard:
		title = o.who.name
		rows = m.cardRows(inner)
		for _, a := range m.cardActions() {
			switch a {
			case "attach":
				buttons = append(buttons, "Attach session")
			case "mention":
				buttons = append(buttons, "Mention @"+m.mentionToken(o.member.Nick, o.member.Account))
			case "filter":
				buttons = append(buttons, "Filter messages")
			case "close":
				buttons = append(buttons, "Close")
			}
		}
		hint = "Enter runs the first · Esc closes"
	case overlayChannels:
		title = "Channels"
		rows = m.channelPickerRows(inner)
		for i := range rows {
			item := -1
			if i < len(m.order) {
				item = i
			}
			rowItems = append(rowItems, item)
		}
		hint = "↑↓ choose · Enter switch · Alt+1…9 · Esc closes"
	case overlayFilter:
		title = "Filter messages in " + m.name
		rows = m.filterPickerRows(inner)
		for i := range rows {
			item := -1
			if i < len(o.choices) {
				item = i
			}
			rowItems = append(rowItems, item)
		}
		hint = "↑↓ move · Space select · Enter apply · Esc cancel"
	case overlayTakeOver:
		title = "@" + m.opts.Nick + " is in use"
		rows = wrapText(m.widthMethod, "Another chat session of your account is connected as @"+m.opts.Nick+". Take over disconnects that session; anything it was still sending becomes uncertain. Cancel closes this view.", inner, spaceview.Style{FG: t.Text})
		buttons = []string{"Take over", "Cancel"}
		hint = "Enter takes over · Esc cancels"
	case overlayConfirm:
		title = "Start and attach " + o.target.Box + "?"
		id := o.target.BoxID
		if len(id) > 12 {
			id = id[:12] + "…"
		}
		rows = wrapText(m.widthMethod, "Box "+o.target.Box+" (declaration "+id+") is not running. Attaching starts it and opens its session in a new tab.", inner, spaceview.Style{FG: t.Text})
		buttons = []string{"Start and attach", "Cancel"}
		hint = "Enter starts · Esc cancels"
	}
	height := min(len(rows)+4, l.bodyHeight)
	if len(buttons) > 0 {
		height = min(len(rows)+6, l.bodyHeight)
	}
	visible := height - 4
	if len(buttons) > 0 {
		visible = height - 6
	}
	visible = max(visible, 0)
	o.scroll = min(o.scroll, max(len(rows)-visible, 0))
	boxW := inner + 4
	x := max((width-boxW)/2, 0)
	y := l.bodyTop + max((l.bodyHeight-height)/2, 0)
	frame := spaceview.Style{FG: t.Frame, BG: t.Popup}
	bg := spaceview.Style{BG: t.Popup}
	title = truncate(m.widthMethod, oneLine(title), max(boxW-6, 1), "…")
	box := []string{spaceview.StyleText("╭─ ", frame) + spaceview.StyleText(title, spaceview.Style{FG: t.Brand, BG: t.Popup, Bold: true}) +
		spaceview.StyleText(" "+strings.Repeat("─", max(boxW-m.widthMethod.StringWidth(title)-5, 0))+"╮", frame)}
	// Rows are styled by helpers that reset after each span; re-apply the
	// panel background after every reset so no cell shows through.
	panelBG := strings.TrimSuffix(spaceview.StyleText("", bg), "\x1b[0m")
	line := func(s string) string {
		s = panelBG + strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+panelBG)
		return spaceview.StyleText("│ ", frame) + s + spaceview.StyleText(strings.Repeat(" ", max(inner-m.widthMethod.StringWidth(s), 0)), bg) + spaceview.StyleText(" │", frame)
	}
	box = append(box, line(""))
	for i := range visible {
		s := ""
		if n := o.scroll + i; n < len(rows) {
			s = truncate(m.widthMethod, rows[n], inner, "")
			if n < len(rowItems) && rowItems[n] >= 0 && m.hovered(hitOverlayRow, rowItems[n], "") {
				s = hoverLine(s)
			}
		}
		box = append(box, line(s))
	}
	if len(buttons) > 0 {
		box = append(box, line(""))
		var b strings.Builder
		bx := x + 2
		for i, label := range buttons {
			st := spaceview.Style{FG: t.Text, BG: t.Raised}
			if i == 0 {
				st = spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true}
			}
			chip := " " + label + " "
			gap := 0
			if b.Len() > 0 {
				gap = 2
			}
			if m.widthMethod.StringWidth(b.String())+gap+m.widthMethod.StringWidth(chip) > inner {
				break
			}
			// Put separation before the next button, so no trailing gap can
			// overflow a row whose last button fills the available columns.
			b.WriteString(spaceview.StyleText(strings.Repeat(" ", gap), bg))
			bx += gap
			m.hits = append(m.hits, hit{x0: bx, x1: bx + m.widthMethod.StringWidth(chip), y: y + len(box), area: hitOverlayButton, item: i})
			face := st
			if m.hovered(hitOverlayButton, i, "") {
				face = hoverStyle(st)
			}
			b.WriteString(spaceview.StyleText(" ", st) + spaceview.StyleText(label, face) + spaceview.StyleText(" ", st))
			bx += m.widthMethod.StringWidth(chip)
		}
		box = append(box, line(b.String()))
	}
	footer := " " + hint + " "
	box = append(box, spaceview.StyleText("╰", frame)+spaceview.StyleText(truncate(m.widthMethod, footer, boxW-2, ""), spaceview.Style{FG: t.Dim, BG: t.Popup})+spaceview.StyleText(strings.Repeat("─", max(boxW-2-m.widthMethod.StringWidth(truncate(m.widthMethod, footer, boxW-2, "")), 0))+"╯", frame))
	for n, item := range rowItems {
		if row := 2 + n - o.scroll; item >= 0 && row >= 2 && row < 2+visible {
			m.hits = append(m.hits, hit{x0: x, x1: x + boxW, y: y + row, area: hitOverlayRow, item: item})
		}
	}
	m.hits = slices.Insert(m.hits, under, hit{x0: x, x1: x + boxW, y: y, y1: y + len(box), area: hitOverlay})
	out := slicesClone(base)
	for i, s := range box {
		if r := y + i; r >= 0 && r < len(out) {
			out[r] = overlayLine(m.widthMethod, out[r], x, s, width)
		}
	}
	return out
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// overlayLine draws box over base starting at cell x. A wide grapheme cut
// by either edge is replaced with spaces, so the row keeps its width.
func overlayLine(wm ansi.Method, base string, x int, box string, width int) string {
	left := truncate(wm, base, x, "")
	left += strings.Repeat(" ", max(x-wm.StringWidth(left), 0))
	end := x + wm.StringWidth(box)
	right := truncateLeft(wm, base, end)
	if pad := width - end - wm.StringWidth(right); pad > 0 {
		right = strings.Repeat(" ", pad) + right
	}
	return left + "\x1b[0m" + box + "\x1b[0m" + right
}

func wrapText(wm ansi.Method, text string, width int, st spaceview.Style) []string {
	var out []string
	for _, r := range wrapRanges(wm, text, width, true) {
		out = append(out, spaceview.StyleText(strings.TrimRight(text[r[0]:r[1]], " "), st))
	}
	return out
}

func (m *Model) helpRows(inner int) []string {
	t := m.theme
	keyW := min(32, inner*2/5)
	var rows []string
	for i, sec := range helpSections {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, spaceview.StyleText(sec.title, spaceview.Style{FG: t.Brand, Bold: true}))
		for _, r := range sec.rows {
			desc := wrapRanges(m.widthMethod, r[1], max(inner-keyW-2, 10), true)
			for j, d := range desc {
				k := ""
				if j == 0 {
					k = r[0]
				}
				keys := spaceview.StyleText(fitPlain(m.widthMethod, k, keyW), spaceview.Style{FG: t.Label, Bold: true})
				rows = append(rows, keys+"  "+spaceview.StyleText(strings.TrimRight(r[1][d[0]:d[1]], " "), spaceview.Style{FG: t.Text}))
			}
		}
	}
	return rows
}

func fitPlain(wm ansi.Method, s string, width int) string {
	s = truncate(wm, s, width, "…")
	return s + strings.Repeat(" ", max(width-wm.StringWidth(s), 0))
}

func (m *Model) settingsView(inner int) ([]string, []int) {
	t := m.theme
	o := m.overlay
	labelW := min(22, inner/3)
	var rows []string
	var items []int
	for i, s := range m.settingRows() {
		items = append(items, i)
		marker := "  "
		st := spaceview.Style{FG: t.Text}
		if i == o.index {
			marker = spaceview.StyleText("› ", spaceview.Style{FG: t.Brand, Bold: true})
			st.Bold = true
		}
		value := spaceview.StyleText(s.value, spaceview.Style{FG: t.Bright, Bold: true})
		if !s.editable {
			value = spaceview.StyleText(s.value, spaceview.Style{FG: t.Label})
		}
		if i == o.index && o.nick != nil {
			value = m.nickField()
		}
		rows = append(rows, marker+spaceview.StyleText(fitPlain(m.widthMethod, s.label, labelW), st)+"  "+value)
		if i == o.index {
			hint := s.hint
			hintStyle := spaceview.Style{FG: t.Dim}
			switch {
			case o.saving:
				hint, hintStyle = "Saving…", spaceview.Style{FG: t.Label}
			case o.problem != "":
				hint, hintStyle = o.problem, spaceview.Style{FG: t.Bad}
			case o.nick != nil:
				hint = nickRule
			}
			for _, w := range wrapText(m.widthMethod, hint, max(inner-labelW-4, 10), hintStyle) {
				rows = append(rows, strings.Repeat(" ", labelW+4)+w)
				items = append(items, -1)
			}
		}
	}
	return rows, items
}

func (m *Model) nickField() string {
	t := m.theme
	e := m.overlay.nick
	st := spaceview.Style{FG: t.Bright, BG: t.Raised, Bold: true}
	before, after := e.text[:e.cursor], e.text[e.cursor:]
	cursor := " "
	if after != "" {
		g, _ := ansi.FirstGraphemeCluster(after, m.widthMethod)
		cursor, after = g, after[len(g):]
	}
	return spaceview.StyleText(" "+before, st) + spaceview.StyleText(cursor, spaceview.Style{FG: t.Raised, BG: t.Bright}) + spaceview.StyleText(after+strings.Repeat(" ", max(25-m.widthMethod.StringWidth(e.text), 0)), st)
}

// channelPickerRows lists the open channels with unread counts, mentions,
// membership and topic, plus how to join another.
func (m *Model) channelPickerRows(inner int) []string {
	t := m.theme
	var rows []string
	for i, name := range m.order {
		v := m.views[name]
		marker := "  "
		st := spaceview.Style{FG: t.Room, Bold: true}
		if i == m.overlay.index {
			marker = spaceview.StyleText("› ", spaceview.Style{FG: t.Brand, Bold: true})
		}
		row := marker + spaceview.StyleText(fitPlain(m.widthMethod, fmt.Sprintf("%d %s", i+1, name), 18), st)
		var notes []string
		if v.unread > 0 {
			notes = append(notes, fmt.Sprintf("%d new", v.unread))
		}
		if v.unreadMentions > 0 {
			notes = append(notes, fmt.Sprintf("%d for you", v.unreadMentions))
		}
		switch {
		case v.deleted:
			notes = append(notes, "deleted")
		case v.membership == "joining" || v.membership == "left" || v.membership == "kicked" || v.membership == "unknown":
			notes = append(notes, v.membership)
		}
		if v == m.cur {
			notes = append(notes, "shown")
		}
		if v.topic != "" {
			notes = append(notes, truncate(m.widthMethod, v.topic, max(inner-40, 8), "…"))
		}
		rows = append(rows, row+spaceview.StyleText(strings.Join(notes, " · "), spaceview.Style{FG: t.Dim}))
	}
	rows = append(rows, "", spaceview.StyleText("/join NAME follows another channel; /part leaves one, keeping its draft.", spaceview.Style{FG: t.Dim}))
	if m.catalogUnobserved {
		rows = append(rows, spaceview.StyleText("Server application not checked; use chat status.", spaceview.Style{FG: t.Dim}))
	} else if !m.catalogApplied && m.catalogGeneration > 0 {
		rows = append(rows, spaceview.StyleText("The server has not loaded the latest channel catalog yet.", spaceview.Style{FG: t.Capability}))
	}
	return rows
}

func (m *Model) cardRows(inner int) []string {
	t := m.theme
	o := m.overlay
	w := o.who
	field := func(label, value string, st spaceview.Style) string {
		return spaceview.StyleText(fitPlain(m.widthMethod, label, 14), spaceview.Style{FG: t.Dim}) + spaceview.StyleText(value, st)
	}
	role := "agent"
	if w.human {
		role = "human"
	}
	if w.me {
		role += " (you)"
	}
	token := m.mentionToken(w.nick, w.account)
	rows := []string{
		spaceview.StyleText(iconSlot(m.widthMethod, w.glyph), spaceview.Style{FG: w.color}) + spaceview.StyleText(w.name, spaceview.Style{FG: w.color, Bold: true}),
		"",
		field("Nick", "@"+token, spaceview.Style{FG: t.Text}),
	}
	if o.member != nil && !strings.EqualFold(o.member.Nick, token) {
		// One connection's live alias; mentions use the saved nick.
		rows = append(rows, field("Connection", "@"+oneLine(o.member.Nick), spaceview.Style{FG: t.Dim}))
	}
	if w.trusted {
		// An unconfirmed identity has no row rather than a qualifier.
		rows = append(rows, field("Identity", role, spaceview.Style{FG: t.Text}))
	}
	if w.agent != "" {
		rows = append(rows, field("Agent", w.agent, spaceview.Style{FG: t.Text}))
	}
	for _, r := range m.runtimeRows(w, time.Now()) {
		// Values carry their provenance and may wrap under their label.
		for i, line := range wrapText(m.widthMethod, r[1], max(inner-14, 10), spaceview.Style{FG: t.Text}) {
			label := strings.Repeat(" ", 14)
			if i == 0 {
				label = spaceview.StyleText(fitPlain(m.widthMethod, r[0], 14), spaceview.Style{FG: t.Dim})
			}
			rows = append(rows, label+line)
		}
	}
	// Someone not connected has no channel role: the row is left out.
	presence, channel := "not connected", ""
	if mem := o.member; mem != nil {
		presence = "online"
		if mem.Away {
			presence = "away"
			if mem.AwayReason != "" {
				presence += ": " + oneLine(mem.AwayReason)
			}
		}
		channel = "member"
		switch {
		case mem.Op:
			channel = "operator (@)"
		case mem.Voice:
			channel = "voiced (+)"
		}
	}
	rows = append(rows, field("Presence", presence, spaceview.Style{FG: t.Text}))
	if channel != "" {
		rows = append(rows, field("Channel role", channel, spaceview.Style{FG: t.Text}))
	}
	if w.account != "" {
		rows = append(rows, field("Account", w.account, spaceview.Style{FG: t.Dim}))
	}
	rows = append(rows, "")
	var note string
	switch {
	case !w.trusted:
		note = "This session's account is not in the room's identity map, so its name cannot be trusted and nothing can be attached."
	case w.human:
		note = "Humans have no box session to attach. Direct messages are not supported in managed chat; use @" + token + " in the room."
	case w.box:
		note = "Attach opens this box's session in a new tab after its current membership and declaration are checked again."
	default:
		note = "No box is recorded for this identity, so there is nothing to attach."
	}
	return append(rows, wrapText(m.widthMethod, note, inner, spaceview.Style{FG: t.Text})...)
}

// attachLookup is the one participant lookup in flight: the account asked
// about, its original live nick, trusted identity at that time and request number.
type attachLookup struct {
	account string
	nick    string
	id      Identity
	seq     uint64
}

// attachDoneMsg carries a lookup's answer with the request it answers.
type attachDoneMsg struct {
	seq     uint64
	account string
	msg     tea.Msg
}

// requestAttach starts attachment for a participant, resolved only by the
// authenticated account. The host re-validates membership and declaration
// identity at action time; anything else gets an explanatory card. One
// lookup runs at a time: repeated clicks never queue more.
func (m *Model) requestAttach(nick, account string) tea.Cmd {
	w := m.identify(nick, account)
	switch {
	case !w.trusted || w.human || !w.box:
		m.openCard(nick, account)
		return nil
	case m.opts.ResolveAttach == nil:
		m.say(levelWarn, "Attaching is unavailable in this view")
		return nil
	case m.attaching != nil && m.attaching.account == account:
		m.say(levelInfo, fmt.Sprintf("Still checking %s…", w.name))
		return nil
	case m.attaching != nil:
		m.say(levelInfo, fmt.Sprintf("Still checking %s; try %s again when it finishes", m.identify(m.attaching.nick, m.attaching.account).name, w.name))
		return nil
	}
	m.say(levelInfo, fmt.Sprintf("Checking %s before attaching…", w.name))
	cmd := m.opts.ResolveAttach(account)
	if cmd == nil {
		return nil
	}
	m.attachSeq++
	seq := m.attachSeq
	m.attaching = &attachLookup{account: account, nick: nick, id: m.opts.Identities[account], seq: seq}
	return func() tea.Msg { return attachDoneMsg{seq: seq, account: account, msg: cmd()} }
}

// attachDone ends the lookup it answers. An answer to an earlier or
// canceled lookup opens nothing.
func (m *Model) attachDone(done attachDoneMsg) tea.Cmd {
	if m.attaching == nil || done.seq != m.attaching.seq {
		return nil
	}
	m.attaching = nil
	switch msg := done.msg.(type) {
	case nil:
		return nil
	case AttachTargetMsg:
		if msg.Err == nil && msg.Target.Account != done.account {
			msg = AttachTargetMsg{Err: errors.New("the host answered for another participant")}
		}
		return m.attachResolved(msg)
	default:
		return func() tea.Msg { return msg }
	}
}

// cancelAttach abandons the lookup in flight; its answer is then ignored.
func (m *Model) cancelAttach(why string) {
	if m.attaching == nil {
		return
	}
	name := m.identify(m.attaching.nick, m.attaching.account).name
	m.attaching = nil
	m.say(levelInfo, "Stopped checking "+name+": "+why)
}

// attachIdentityChanged cancels the lookup in flight when a new identity map
// no longer maps its account to the same trusted box member.
func (m *Model) attachIdentityChanged() {
	if m.attaching == nil {
		return
	}
	asked, now := m.attaching.id, m.opts.Identities[m.attaching.account]
	asked.Agent, now.Agent = "", ""
	asked.Nick, now.Nick = "", "" // a nickname is not membership
	if asked != now {
		m.cancelAttach("its room membership changed")
	}
}

// attachResolved acts on the host's fresh resolution: a running box opens
// at once, a stopped one asks first, and anything else explains itself.
func (m *Model) attachResolved(msg AttachTargetMsg) tea.Cmd {
	target := msg.Target
	if msg.Err != nil {
		m.say(levelError, "Cannot attach: "+msg.Err.Error())
		return nil
	}
	switch target.State {
	case "running":
		if m.opts.Attach == nil {
			m.say(levelWarn, "Attaching is unavailable in this view")
			return nil
		}
		m.say(levelInfo, "Opening "+target.Box+"…")
		return m.opts.Attach(target)
	case "stopped":
		m.openOverlay(overlayState{kind: overlayConfirm, target: target})
		return nil
	}
	reason := target.Reason
	if reason == "" {
		reason = "its box is " + target.State
	}
	m.say(levelWarn, "Nothing to attach: "+reason)
	return nil
}

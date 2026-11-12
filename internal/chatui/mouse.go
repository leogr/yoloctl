package chatui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

// mouse routes one event of the last frame. A click is a press and release
// on the same target without moving; a drag in the transcript selects text
// and copies it on release. Nothing acts on press alone, and buttonless
// motion only highlights the target under the pointer.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	mo := msg.Mouse()
	if m.linkPrompt != nil {
		m.hover = hoverKey{}
		return nil
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		// One row per wheel event, like the TUI's own scrollback: hosts
		// already apply their scroll multiplier and send high-resolution
		// trackpad gestures as many events, so more rows would jump.
		if m.overlay != nil {
			switch mo.Button {
			case tea.MouseWheelUp:
				m.overlay.scroll = max(m.overlay.scroll-1, 0)
			case tea.MouseWheelDown:
				m.overlay.scroll++
			}
			m.hover = hoverKey{} // the rows moved under the pointer
			return nil
		}
		if m.inRoster(mo.X, mo.Y) {
			// One card per wheel event over the participants panel.
			switch mo.Button {
			case tea.MouseWheelUp:
				m.scrollRoster(-1)
			case tea.MouseWheelDown:
				m.scrollRoster(1)
			}
			return nil
		}
		var cmd tea.Cmd
		switch mo.Button {
		case tea.MouseWheelUp:
			cmd = m.scrollUp(1)
		case tea.MouseWheelDown:
			m.moveScroll(1)
		}
		// The transcript moved under a still pointer.
		m.hover = m.hoverAt(mo.X, mo.Y)
		return cmd
	case tea.MouseClickMsg:
		return m.mouseDown(mo)
	case tea.MouseMotionMsg:
		return m.mouseMove(mo)
	case tea.MouseReleaseMsg:
		return m.mouseUp(mo)
	}
	return nil
}

// inRoster reports whether a cell lies on the participants panel's body
// rows as the last frame drew it.
func (m *Model) inRoster(x, y int) bool {
	l := m.layout
	return l.panelX1 > l.panelX0 && y >= l.bodyTop && y < l.bodyTop+l.bodyHeight && x >= l.panelX0 && x < l.panelX1
}

func (m *Model) inTranscript(x, y int) bool {
	l := m.layout
	return y >= l.bodyTop && y < l.bodyTop+l.bodyHeight && x >= 0 && x < l.transcriptWidth
}

// rowAt returns the transcript row drawn at screen row y.
func (m *Model) rowAt(y int) (transcriptRow, bool) {
	i := m.scroll + y - m.layout.bodyTop
	if y < m.layout.bodyTop || y >= m.layout.bodyTop+m.layout.bodyHeight || i < 0 || i >= len(m.lastTranscript) {
		return transcriptRow{}, false
	}
	return m.lastTranscript[i], true
}

func (m *Model) mouseDown(mo tea.Mouse) tea.Cmd {
	m.gesture++
	m.drag = dragState{}
	m.hover = hoverKey{}
	h, onTarget := m.hitAt(mo.X, mo.Y)
	if m.overlay != nil {
		if !onTarget || h.area != hitOverlay && h.area != hitOverlayRow && h.area != hitOverlayButton {
			if m.overlay.kind == overlayTakeOver {
				return nil // the choice is explicit: Take over or Cancel
			}
			if m.overlay.kind == overlayConfirm {
				m.say(levelInfo, "Attach canceled")
			}
			m.overlay = nil // a click outside the panel only closes it
			return nil
		}
		if mo.Button == tea.MouseLeft {
			m.drag = dragState{active: true, area: h.area, areaItem: h.item, id: h.id}
		}
		return nil
	}
	if mo.Button == tea.MouseRight {
		return m.details(mo, h, onTarget)
	}
	if mo.Button != tea.MouseLeft {
		return nil
	}
	if onTarget {
		m.drag = dragState{active: true, area: h.area, areaItem: h.item, id: h.id}
		return nil
	}
	if !m.inTranscript(mo.X, mo.Y) {
		return nil
	}
	row, _ := m.rowAt(mo.Y)
	p, ok := m.pointAt(mo.X, mo.Y)
	m.sel = nil
	m.drag = dragState{active: true, area: hitTranscript, x: mo.X, y: mo.Y, lastX: mo.X, lastY: mo.Y, press: p, pressOK: ok, act: actionAt(m.widthMethod, row.runs, mo.X), key: row.key}
	if ok && row.kind != rowEvent {
		m.drag.pick = p.key // a room notice row selects no message
	}
	return nil
}

// details opens the card for the participant or author under the pointer.
func (m *Model) details(mo tea.Mouse, h hit, onTarget bool) tea.Cmd {
	if onTarget && h.area == hitPanel && h.item < len(m.layout.panelMembers) {
		mem := m.layout.panelMembers[h.item]
		m.openCard(mem.Nick, mem.Account)
		return nil
	}
	if row, ok := m.rowAt(mo.Y); ok && m.inTranscript(mo.X, mo.Y) {
		switch act := actionAt(m.widthMethod, row.runs, mo.X); act.Kind {
		case actAuthor:
			m.openCard(act.Extra, act.Value)
		case actMention:
			m.mentionCard(act.Value)
		}
	}
	return nil
}

func (m *Model) mouseMove(mo tea.Mouse) tea.Cmd {
	d := &m.drag
	if !d.active {
		m.hover = m.hoverAt(mo.X, mo.Y)
		return nil
	}
	if d.area != hitTranscript || !d.pressOK {
		return nil
	}
	if !d.moved && mo.X == d.x && mo.Y == d.y {
		return nil
	}
	if !d.moved && d.pick != "" {
		// A selection that starts in a message selects that message, so
		// Enter quotes the highlight at once (Quote is first while the
		// highlight lies in it). pointAt snaps a press on its header, gutter
		// or separator to its text, so those start in it too.
		m.pick(d.pick)
	}
	d.moved = true
	d.lastX, d.lastY = mo.X, mo.Y
	if p, ok := m.pointAt(mo.X, mo.Y); ok {
		m.sel = &selection{anchor: d.press, focus: p}
	}
	d.edge = 0
	switch l := m.layout; {
	case mo.Y < l.bodyTop:
		d.edge = -1
	case mo.Y >= l.bodyTop+l.bodyHeight:
		d.edge = 1
	}
	if d.edge != 0 && !d.ticking {
		d.ticking = true
		return dragTick(m.gesture)
	}
	return nil
}

// dragStep scrolls one row while a drag is held past a transcript edge.
// It never requests older history; release stops it, and a tick from an
// earlier gesture neither moves nor schedules another.
func (m *Model) dragStep(tick dragTickMsg) tea.Cmd {
	d := &m.drag
	if tick.gesture != m.gesture {
		return nil
	}
	if !d.active || d.edge == 0 {
		d.ticking = false
		return nil
	}
	m.moveScroll(d.edge)
	if p, ok := m.pointAt(d.lastX, d.lastY); ok && m.sel != nil {
		m.sel.focus = p
	}
	return dragTick(m.gesture)
}

func (m *Model) mouseUp(mo tea.Mouse) tea.Cmd {
	d := m.drag
	m.drag = dragState{}
	if !d.active {
		return nil
	}
	if d.area == hitTranscript {
		if d.moved {
			if p, ok := m.pointAt(mo.X, mo.Y); ok && m.sel != nil && d.edge == 0 {
				m.sel.focus = p
			}
			// A new highlight returns Enter to the first action: Quote
			// when it lies in the picked message, whatever the copy does.
			m.actionIndex = 0
			return m.copySelection()
		}
		row, _ := m.rowAt(mo.Y)
		if act := actionAt(m.widthMethod, row.runs, mo.X); act.Kind != actNone && act == d.act {
			return m.activate(act)
		}
		if row.key != "" && row.key == d.key && row.kind != rowEvent {
			m.pick(row.key)
		}
		return nil
	}
	h, ok := m.hitAt(mo.X, mo.Y)
	if !ok || h.area != d.area || h.item != d.areaItem || h.id != d.id {
		return nil
	}
	return m.clickHit(h, mo)
}

func (m *Model) clickHit(h hit, mo tea.Mouse) tea.Cmd {
	switch h.area {
	case hitTop:
		switch h.id {
		case "channels":
			m.openOverlay(overlayState{kind: overlayChannels, index: max(slices.Index(m.order, m.name), 0)})
		case "live":
			m.jumpToLatest()
		}
	case hitFilter:
		switch h.id {
		case "clear":
			m.setFilter("", "")
		case "pick":
			m.openFilterPicker()
		default:
			m.toggleForMe()
		}
	case hitChannel:
		if h.item < len(m.order) {
			return m.selectChannel(m.order[h.item])
		}
	case hitPanel:
		if h.item < len(m.layout.panelMembers) {
			mem := m.layout.panelMembers[h.item]
			return m.requestAttach(mem.Nick, mem.Account)
		}
	case hitSuggest:
		m.acceptSuggestion(h.item, true)
	case hitFooter:
		if h.id == "reopen" {
			return m.reopen()
		}
		if h.id == "help" {
			m.openHelp()
			return nil
		}
		if h.id == "quote" {
			return m.quoteSelection()
		}
		for i, a := range m.messageActions() {
			if a.id == h.id {
				m.actionIndex = i
			}
		}
		return m.runAction(h.id)
	case hitReplyCancel:
		m.replyRevision++
		m.replyTo = ""
	case hitComposer:
		m.focus = focusComposer
		m.placeCursor(mo.X, mo.Y)
		m.refreshSuggest()
	case hitFold:
		// The chip's number names the fold, so a click resolves the same
		// paste even if the draft changed since the last frame. An image
		// never expands: the click puts the cursor after it.
		m.focus = focusComposer
		for i, f := range m.editor.folds {
			switch {
			case f.img != nil && imageHitID+strconv.Itoa(f.n) == h.id:
				m.editor.cursor, m.editor.column = f.end, nil
				m.say(levelInfo, imageLabel(f.n)+": "+imageDetails(f.img)+" · Backspace removes it")
			case f.img == nil && strconv.Itoa(f.n) == h.id && m.editor.expandFold(i):
				m.say(levelInfo, "Expanded the paste; Ctrl+Z collapses it again")
			}
		}
	case hitOverlayRow:
		if m.overlay != nil && m.overlay.kind == overlayFilter {
			m.overlay.index = h.item
			m.toggleFilterChoice()
			return nil
		}
		if m.overlay != nil && (m.overlay.kind == overlaySettings && m.overlay.nick == nil || m.overlay.kind == overlayChannels) {
			m.overlay.index = h.item
			return m.overlayActivate(-1)
		}
	case hitOverlayButton:
		return m.overlayActivate(h.item)
	}
	return nil
}

// activate runs a clicked target inside message text or a header.
func (m *Model) activate(act action) tea.Cmd {
	switch act.Kind {
	case markdownview.IntentToggleTableRow:
		return m.toggleTableRow(act)
	case actLink:
		return m.openLink(act.Value)
	case actAuthor:
		return m.requestAttach(act.Extra, act.Value)
	case actReply, actRef:
		return m.showMessage(act.Value)
	case actPreview:
		for i := range m.messages {
			if m.messages[i].ID == act.Value {
				return m.togglePreview(&m.messages[i])
			}
		}
		return nil
	case actMention:
		m.mentionCard(act.Value)
	case actFilter:
		m.setFilter("", "")
	case actCopyCode:
		return m.copyCode(act.Value)
	}
	return nil
}

// showMessage selects the message with this exact server ID among those
// loaded in this channel, as reply labels and references name it.
// Nothing is fetched or guessed: a missing target keeps the position. A
// loaded target a filter hides first brings back all messages.
func (m *Model) showMessage(id string) tea.Cmd {
	if m.selectLoaded(id) {
		return nil
	}
	// A message loaded in another open channel: select that channel the
	// way a person does (joined: at once; otherwise through the ordinary
	// typed join path, showing the message once the join switches to it).
	if name, ok := m.channelHolding(id); ok {
		cmd := m.selectChannel(name)
		switch {
		case m.name == name:
			m.selectLoaded(id)
		case m.joinSwitch.id != "" && strings.EqualFold(m.joinSwitch.channel, name):
			// The message is shown only by that join's own switch: a refused,
			// uncertain, superseded or reset join, or a move elsewhere before
			// it confirms, drops it with the join intent.
			m.joinSwitch.ref = id
		}
		return cmd
	}
	m.say(levelInfo, "That message is not loaded in any open channel; Page Up in its channel may load older history")
	return nil
}

// selectLoaded selects message id when it is loaded in the visible channel.
func (m *Model) selectLoaded(id string) bool {
	for i := range m.messages {
		if m.messages[i].ID == id {
			if m.filter != nil && !m.shown(m.bindings())[i] {
				m.setFilter("", "")
			}
			m.selected, m.picked, m.focus, m.actionIndex = i, true, focusMessages, 0
			m.follow = false
			m.anchor = &viewAnchor{key: messageKey(m.messages[i]), line: lineTop}
			m.clearInfo()
			m.touch()
			return true
		}
	}
	return false
}

// channelHolding names another open channel where message id is loaded.
// Only channels this client already shows can hold one, so nothing beyond
// existing membership and history becomes readable.
func (m *Model) channelHolding(id string) (string, bool) {
	for _, name := range m.order {
		if v := m.views[name]; v != nil && v != m.cur {
			for i := range v.messages {
				if v.messages[i].ID == id {
					return name, true
				}
			}
		}
	}
	return "", false
}

// mentionCard opens the card of the participant a mention names: the
// account its token binds (a saved nick, name or known alias, see
// nameBindings), else a connected session with exactly that nick. A token
// that names more than one account opens nothing. A mention in text never
// attaches directly.
func (m *Model) mentionCard(nick string) {
	account, state := m.bindings().resolve(nick)
	switch state {
	case nameConflict:
		m.say(levelInfo, "@"+plain(nick)+" names more than one participant; open a card from the participants panel")
		return
	case nameUnique:
		for _, mem := range m.members {
			if mem.Account == account {
				m.openCard(mem.Nick, account)
				return
			}
		}
		if _, trusted := m.opts.Identities[account]; trusted {
			m.openCard(nick, account) // not connected to this channel
			return
		}
	}
	for _, mem := range m.members {
		if strings.EqualFold(mem.Nick, nick) {
			m.openCard(mem.Nick, mem.Account)
			return
		}
	}
	m.say(levelInfo, "@"+plain(nick)+" is not connected")
}

// placeCursor moves the composer cursor to the clicked cell. The blank
// rows around the draft only focus the composer.
func (m *Model) placeCursor(x, y int) {
	l := m.layout
	n := l.firstDraftRow + y - l.inputTop
	if y < l.inputTop || y >= l.inputTop+l.inputRows || n < 0 || n >= len(l.draftRows) {
		return
	}
	e := &m.editor
	e.cursor = l.draftRows[n].stopAt(x - composerIndent)
	e.column = nil
}

package chatui

import (
	"hash/fnv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Anchor lines of the rows around one message's body, in display order.
// Separators belong to the message they precede; errors, presence notices
// and the spacers to the message they follow, offset by its line count.
// Notices are anchored there but are not part of the message: a spacer
// (lineEvents) sets them apart. lineTop anchors whatever row comes first for
// a message.
const (
	lineTop      = -9
	lineFilter   = -8 // and -7: a filtered transcript's scope rows, keyed ""
	lineBegin    = -5
	lineDay      = -4
	lineJoined   = -3
	lineHeader   = -2
	lineReply    = -1
	lineError    = 1
	lineEvents   = 2
	linePresence = 3
	linePreview  = 1 << 16 // and the preview's next rows
	lineSpacer   = 1 << 20
)

// bodyIndent puts message text under its author's name: a selection or
// mention marker, then the author's glyph hanging in a three-cell margin.
// One trailing column keeps text off the panel divider.
const bodyIndent = 4

// humanGlyph marks people: the bust-in-silhouette emoji the user chose. Unlike
// the agent_icons defaults it is an emoji, two cells wide (it fills the icon
// slot) and drawn in the font's own colors, so only the name carries the
// human accent.
const humanGlyph = "\U0001F464"

// who is how the view presents an author or participant. Nicknames, roles and
// attachability come only from the trusted identity map, keyed by the
// authenticated account; a nick alone is never trusted.
type who struct {
	name, nick, account string
	trusted, human, me  bool
	box                 bool
	agent               string
	color               spaceview.Color
	glyph               string
}

func (m *Model) identify(nick, account string) who {
	w := who{name: oneLine(nick), nick: oneLine(nick), account: account}
	if id, ok := m.opts.Identities[account]; ok && account != "" {
		w.trusted, w.human, w.box, w.agent = true, id.Human, id.Box, id.Agent
		if id.Nick != "" {
			w.name = oneLine(id.Nick)
		}
	}
	w.me = account != "" && account == m.opts.Account
	switch {
	case !w.trusted:
		// Nothing is known: a dim plain nick, with no glyph or mark.
		w.color = m.theme.Dim
	case w.human:
		w.color, w.glyph = m.theme.Brand, humanGlyph
	default:
		w.color, w.glyph = accent(m.theme, account), "●"
		if icon, ok := m.opts.Icons[w.agent]; ok && icon.Icon != "" && m.widthMethod.StringWidth(icon.Icon) <= 2 {
			w.glyph = oneLine(icon.Icon)
			if c, ok := m.theme.Agents[w.agent]; ok {
				w.color = c
			}
		}
	}
	return w
}

// accent gives each account a stable color from the theme accents.
func accent(t spaceview.Theme, account string) spaceview.Color {
	if len(t.Accents) == 0 {
		return t.Label
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(account))
	return t.Accents[int(h.Sum32()%uint32(len(t.Accents)))]
}

// author identifies a message author; a send not yet echoed is mine.
func (m *Model) author(msg *Message) who {
	if msg.Account == "" && msg.ClientID != "" {
		w := m.identify(m.opts.Nick, m.opts.Account)
		w.me = true
		return w
	}
	return m.identify(msg.Nick, msg.Account)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}

func dayLabel(t time.Time) string {
	now := time.Now()
	switch {
	case sameDay(t, now):
		return "Today"
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "Yesterday"
	case t.Local().Year() == now.Year():
		return t.Local().Format("Mon 2 Jan")
	}
	return t.Local().Format("Mon 2 Jan 2006")
}

// body returns the message's display lines and rows for width, cached.
func (m *Model) body(msg *Message, width int) cachedBody {
	key := messageKey(*msg)
	// A server artifact shows as a file chip; its announcement URL is never
	// a link. Images published with a message show as chips where their
	// markers are; a typed look-alike marker stays text. Source view and
	// Alt+Y keep the original text.
	text, tokens, chips := msg.Text, []rune(nil), []string(nil)
	if !m.source {
		text, tokens, chips = chipText(msg)
	}
	// Literal image placeholder characters in message text are display only
	// spaces: only this pane's own preview rows draw image cells. Copy,
	// Quote and the source copy keep the original text.
	text = chatimage.NeutralizeText(text)
	cacheText := text + "\x00" + strings.Join(chips, "\x00")
	body, ok := m.bodyCache[key]
	if !ok || body.stale || body.text != cacheText || body.source != m.source || body.me != m.self || len(body.fences) > 0 && body.drawWidth != width {
		next := cachedBody{text: cacheText, source: m.source, me: m.self, width: -1}
		// parse is every parse of this body: with drawings (and the fence
		// map for exactly the lines it returns) when chat draws diagrams.
		parse := func(text string) []displayLine {
			if m.opts.Mermaid != nil && !m.source {
				return m.parseDiagrams(key, text, width, &next) // drawings are for this width
			}
			return displayLines(m.widthMethod, text, m.source, m.theme, m.self)
		}
		base := parse(text)
		if len(tokens) > 0 {
			previewID := ""
			if previewArtifact(msg) != nil {
				previewID = msg.ID
			}
			placed, placedOK := placeChips(base, tokens, chips, previewID)
			switch {
			case placedOK && len(placed) == len(base):
				base = placed
			case placedOK:
				// Other lines than the parse: no fence outcomes apply.
				next.fences, next.outcomes, next.class, next.pending, next.transient, next.fenceAt = nil, nil, nil, nil, nil, nil
				base = placed
			default:
				// The literal re-parse keeps its line count (labels are swapped
				// within lines), so its own fence map stays exact.
				if literal, ok := literalChipLines(msg, tokens, chips, parse, m.widthMethod); ok {
					base = literal
				} else {
					// Not reached for well-formed text: the chips as plain text.
					plain := attachmentText(msg)
					if msg.Artifact != nil {
						plain = artifactLabel(msg.Artifact)
					}
					base = parse(chatimage.NeutralizeText(plain))
				}
			}
		}
		lines, keys := base, body.keys
		if len(keys) > 0 && len(keys) == len(m.previewKeys(-1, base, nil)) {
			lines = substitute(base, keys, m.theme) // the same previews, restyled
		} else {
			keys = nil
		}
		next.base, next.keys, next.lines = base, keys, lines
		if ok && m.anchor != nil && m.anchor.key == key && body.fenceAt != nil && next.fenceAt != nil {
			m.anchor.line, m.anchor.offset = carryLine(body, next, m.anchor.line, m.anchor.offset)
		}
		body = next
		if !m.source && len(m.tableRows[key]) > 0 {
			valid := map[markdownview.TableRowID]bool{}
			for _, record := range markdownview.TableRecords(markdownview.Document{Lines: lines}) {
				valid[record.ID] = true
			}
			for id := range m.tableRows[key] {
				if !valid[id] {
					delete(m.tableRows[key], id)
				}
			}
			if len(m.tableRows[key]) == 0 {
				delete(m.tableRows, key)
			}
		}
	}
	if body.width != width {
		body.width = width
		layout := markdownview.Layout(markdownview.Document{Lines: body.lines}, width, markdownview.LayoutOptions{Method: m.widthMethod, Theme: m.theme, CompactTables: true, OpenTableRows: m.tableRows[key]})
		body.rows = layout.Rows
		if len(layout.RefusedTableRows) > 0 {
			m.say(levelWarn, "Table row exceeds display limits; kept closed")
		}
		body.closed = map[int]bool{}
		for i := range body.rows {
			if body.rows[i].Closed {
				body.closed[body.rows[i].Line] = true
			}
			for j := range body.rows[i].Runs {
				if body.rows[i].Runs[j].Act.Kind == markdownview.IntentToggleTableRow {
					body.rows[i].Runs[j].Act.Extra = key
				}
			}
		}
	}
	m.bodyCache[key] = body
	return body
}

// transcript builds the visible view's rows. A filter leaves out the rows of
// the messages it hides, keeping their transport notices in place, and heads
// the rows with its scope.
func (m *Model) transcript(width int) []transcriptRow {
	t := m.theme
	var rows []transcriptRow
	textWidth := max(width-bodyIndent-1, 1)
	selected := m.selectedKey()
	m.self = m.selfNames()
	b := nameBindings{}
	if m.filter != nil {
		b = m.bindings()
	}
	shown, count := m.shown(b), 0
	for _, keep := range shown {
		if keep {
			count++
		}
	}
	if m.filter != nil {
		rows = append(rows, m.filterScopeRows(count, width)...)
	}
	rows = append(rows, m.presenceRows("", 0, []run{deco("    ", spaceview.Style{})}, width)...)
	if m.historyEnd && len(m.messages) > 0 {
		label := "beginning of retained history"
		if m.olderSkipped > 0 {
			label = "older history is not shown in this view"
		}
		rows = append(rows, separator(m.widthMethod, messageKey(m.messages[0]), lineBegin, label, width, spaceview.Style{FG: t.Faint}, t))
	}
	loaded := make(map[string]int, len(m.messages)) // a reference's target
	for i := range m.messages {
		if id := m.messages[i].ID; id != "" {
			if _, dup := loaded[id]; !dup {
				loaded[id] = i
			}
		}
	}
	elsewhere := m.loadedElsewhere(loaded)
	for i := range m.messages {
		m.refreshPreviews(i, textWidth, loaded, elsewhere)
	}
	var prev *Message
	for i := range m.messages {
		msg := &m.messages[i]
		key := messageKey(*msg)
		if !shown[i] {
			rows = append(rows, m.presenceRows(key, len(m.body(msg, textWidth).lines), []run{deco("    ", spaceview.Style{})}, width)...)
			continue
		}
		if prev != nil {
			prevKey := messageKey(*prev)
			rows = append(rows, transcriptRow{key: prevKey, line: len(m.bodyCache[prevKey].lines) + lineSpacer, kind: rowSpacer})
		}
		if (prev == nil || !sameDay(prev.Time, msg.Time)) && !msg.Time.IsZero() {
			rows = append(rows, separator(m.widthMethod, key, lineDay, dayLabel(msg.Time), width, spaceview.Style{FG: t.Dim}, t))
		}
		if prev != nil && prev.Replay && !msg.Replay && !m.joinedAt.IsZero() {
			rows = append(rows, separator(m.widthMethod, key, lineJoined, "joined "+m.joinedAt.Local().Format("15:04"), width, spaceview.Style{FG: t.Label}, t))
		}
		w := m.author(msg)
		mark := deco(" ", spaceview.Style{})
		switch {
		case key == selected:
			mark = deco("▌", spaceview.Style{FG: t.Brand})
		case !w.me && mentions(msg.Text, m.self):
			mark = deco("▌", spaceview.Style{FG: t.Capability})
		}
		rows = append(rows, m.header(msg, key, w, mark, width, key == selected))
		indent := []run{mark, deco("   ", spaceview.Style{})}
		if msg.ReplyTo != "" {
			label := m.replyLabel(msg.ReplyTo)
			rows = append(rows, transcriptRow{key: key, line: lineReply, kind: rowDecor, runs: append(slicesCloneRuns(indent),
				run{Text: truncate(m.widthMethod, label, textWidth, "…"), Style: spaceview.Style{FG: t.Dim}, Act: action{Kind: actReply, Value: msg.ReplyTo}})})
		}
		body := m.body(msg, textWidth)
		for _, br := range body.rows {
			row := transcriptRow{key: key, line: br.Line, offset: br.Start, end: br.End, kind: rowBody, frags: br.Frags}
			if br.Decor {
				row.kind = rowDecor
			}
			row.runs = append(slicesCloneRuns(indent), br.Runs...)
			if !m.canPreview(msg) && !m.previewShown(msg) {
				// The file chip toggles a preview only where one applies now.
				for i := range row.runs {
					if row.runs[i].Act.Kind == actPreview {
						row.runs[i].Act = action{}
					}
				}
			}
			if msg.Artifact != nil {
				// Even in source view an artifact's announcement URL is inert.
				for i := range row.runs {
					if row.runs[i].Act.Kind == actLink {
						row.runs[i].Act = action{}
					}
				}
			}
			rows = append(rows, row)
		}
		rows = append(rows, m.previewRows(msg, key, len(body.lines), indent)...)
		if msg.Error != "" {
			rows = append(rows, transcriptRow{key: key, line: len(body.lines) + lineError, kind: rowDecor,
				runs: append(slicesCloneRuns(indent), deco(prettylog.DefaultGlyphs.Error+" "+oneLine(msg.Error), spaceview.Style{FG: t.Bad}))})
		}
		// Room events after a message stand apart from it: no selection or
		// mention mark, and selecting it does not reach them.
		rows = append(rows, m.presenceRows(key, len(body.lines), []run{deco("    ", spaceview.Style{})}, width)...)
		prev = msg
	}
	return rows
}

func separator(wm ansi.Method, key string, line int, label string, width int, st spaceview.Style, t spaceview.Theme) transcriptRow {
	label = " " + label + " "
	side := max(width-wm.StringWidth(label), 0)
	left := side / 2
	return transcriptRow{key: key, line: line, kind: rowDecor, runs: []run{
		deco(strings.Repeat("─", left), spaceview.Style{FG: t.Faint}),
		deco(label, st),
		deco(strings.Repeat("─", side-left), spaceview.Style{FG: t.Faint}),
	}}
}

// header names a message: the author's glyph hangs in the margin, the
// name (a click target resolved by account) lines up with the text, and
// quiet labels follow: you or human, the local time, and any delivery
// state. An account outside the identity map has only its dim plain nick:
// nothing uncertain is labelled.
func (m *Model) header(msg *Message, key string, w who, mark run, width int, selected bool) transcriptRow {
	t := m.theme
	var bg spaceview.Color
	if selected {
		bg = t.Raised
	}
	name := w.name
	if m.source {
		name = w.nick
		if msg.Account != "" {
			name += "(" + msg.Account + ")"
		}
	}
	glyph := iconSlot(m.widthMethod, w.glyph)
	runs := []run{mark,
		deco(glyph, spaceview.Style{FG: w.color, BG: bg, Bold: true}),
		{Text: name, Style: spaceview.Style{FG: w.color, BG: bg, Bold: true}, Act: action{Kind: actAuthor, Value: msg.Account, Extra: msg.Nick}},
	}
	label := func(text string, st spaceview.Style) {
		st.BG = bg
		runs = append(runs, deco("  "+text, st))
	}
	switch {
	case !w.trusted:
	case w.me:
		label("you", spaceview.Style{FG: t.Dim})
	case w.human:
		label("human", spaceview.Style{FG: t.Dim})
	}
	label(msg.Time.Local().Format("15:04"), spaceview.Style{FG: t.Dim})
	switch msg.Delivery {
	case Pending:
		label("◌ sending", spaceview.Style{FG: t.Label})
	case Rejected:
		label(prettylog.DefaultGlyphs.Error+" not sent", spaceview.Style{FG: t.Bad, Bold: true})
	case Uncertain:
		label(prettylog.DefaultGlyphs.Unknown+" uncertain, check history", spaceview.Style{FG: t.Warn, Bold: true})
	}
	if m.source && msg.Replay {
		label("history", spaceview.Style{FG: t.Dim})
	}
	return transcriptRow{key: key, line: lineHeader, kind: rowHeader, runs: fitRuns(m.widthMethod, runs, width), fill: bg}
}

// fitRuns truncates runs to width cells.
func fitRuns(wm ansi.Method, runs []run, width int) []run {
	used := 0
	for i, r := range runs {
		w := wm.StringWidth(r.Text)
		if used+w > width {
			r.Text = truncate(wm, r.Text, max(width-used, 0), "…")
			return append(runs[:i:i], r)
		}
		used += w
	}
	return runs
}

// replyLabel shows the replied-to author and a short snippet when displayed.
func (m *Model) replyLabel(id string) string {
	if m.source {
		return "reply " + id
	}
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.ID == id {
			snippet := strings.Join(strings.Fields(plain(msg.Text)), " ")
			if len([]rune(snippet)) > 60 {
				snippet = string([]rune(snippet)[:60]) + "…"
			}
			return "↳ " + m.author(msg).name + ": " + snippet
		}
	}
	return "↳ earlier message"
}

// selectedKey is the message the view marks as selected: only an explicit
// choice (message focus or a click), never the implicit latest message.
func (m *Model) selectedKey() string {
	if !m.picked || m.selected < 0 || m.selected >= len(m.messages) {
		return ""
	}
	return messageKey(m.messages[m.selected])
}

func (m *Model) indexOf(key string) int {
	for i := range m.messages {
		if messageKey(m.messages[i]) == key {
			return i
		}
	}
	return -1
}

package chatui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Image previews: the host (the outer TUI) owns the terminal and draws
// images; chat only asks for one, then draws the placeholder cells the host
// placed the image under. One preview per pane at a time.

// PreviewCapabilityMsg is the host's current image capability.
type PreviewCapabilityMsg struct {
	Supported        bool
	Generation       uint64
	MaxCols, MaxRows int
}

// PreviewReadyMsg says the host transmitted the requested image under ID and
// placed it on Cols×Rows cells; Token releases it.
type PreviewReadyMsg struct {
	Request, Generation uint64
	Token               string
	ID                  uint32
	Cols, Rows          int
}

// PreviewErrorMsg says the host could not show the requested image.
type PreviewErrorMsg struct {
	Request uint64
	Code    string
}

// PreviewRequest asks the host for one message's artifact.
type PreviewRequest struct {
	Request, Generation uint64
	Channel, MessageID  string
	Artifact            ArtifactRef
	Chip                string
	// Cols×Rows is the area the pane can show the preview in, in cells:
	// the host fits the image inside it, keeping its aspect ratio, and
	// never enlarges it beyond its own size.
	Cols, Rows int
}

// imagePreview is the pane's one requested or shown preview: the image of
// one chip of one message, drawn under the line that holds the chip.
type imagePreview struct {
	key        string // the message's transcript key
	chip       int    // the chip's index among the message's previewable files
	channel    string
	request    uint64
	generation uint64
	want       int    // the width in cells it was requested at
	wantRows   int    // the height in cells it was requested at
	token      string // empty while pending
	id         uint32
	cols, rows int
}

// previewArtifacts are the files a message may preview, in chip order: a
// server artifact or each published attachment, of a message with a server
// message ID.
func previewArtifacts(msg *Message) []ArtifactRef {
	if msg == nil || !chatstream.ValidArtifactID(msg.ID) {
		return nil
	}
	if msg.Artifact != nil {
		return []ArtifactRef{*msg.Artifact}
	}
	arts := make([]ArtifactRef, 0, len(msg.Attachments))
	for i := range msg.Attachments {
		arts = append(arts, msg.Attachments[i].ArtifactRef)
	}
	return arts
}

// chipAction is the button of chip k of the message with server ID id.
func chipAction(id string, k int) action {
	return action{Kind: actPreview, Value: id, Extra: strconv.Itoa(k)}
}

// previewWidth is the widest a preview may be: the message text's width, at
// most the host's widest preview.
func (m *Model) previewWidth() int {
	return max(min(m.textWidth(), m.previewCap.MaxCols), 1)
}

// previewHeight is the tallest a preview may be: the transcript's steady
// height less one row, so the chip line it hangs under stays in view with
// the whole image, at most the host's tallest preview. A draft's extra lines
// or a notice do not shrink it, so typing never asks for the image again.
func (m *Model) previewHeight() int {
	if m.lastImageHeight < 1 {
		return max(m.previewCap.MaxRows, 1)
	}
	return max(min(m.lastImageHeight-1, m.previewCap.MaxRows), 1)
}

// fits reports whether shown preview p, placed on p.cols×p.rows cells for
// its request, is still what a request for a cols×rows area would place: it
// is when it fits the area and was not held smaller by an area bound that
// has since changed. An image bound only by its own size keeps its cells
// whatever the pane does.
func (p *imagePreview) fits(cols, rows int) bool {
	if p.cols > cols || p.rows > rows {
		return false
	}
	widthBound, heightBound := p.cols >= p.want, p.rows >= p.wantRows
	return (!widthBound || cols == p.want) && (!heightBound || rows == p.wantRows)
}

// canPreview reports whether a "Show image" action applies to msg now.
// The pane must also draw truecolor: a placeholder's colour is its image ID,
// and a reduced colour would leave the host filter nothing to show. It must be
// connected with sending access to the channel, so a preview the host has
// cached is never offered where a new read would be refused.
func (m *Model) canPreview(msg *Message) bool {
	return m.opts.PreviewImage != nil && m.previewCap.Supported && m.trueColor &&
		m.connected && m.canSend() && len(previewArtifacts(msg)) > 0
}

// previewShown reports whether one of msg's previews is requested or shown.
func (m *Model) previewShown(msg *Message) bool {
	return m.preview != nil && msg != nil && m.preview.key == messageKey(*msg)
}

// nextPreview is the chip the footer action shows next for msg: the first
// when none is shown, then each following one; -1 hides the last, or the
// shown one once no other can be shown.
func (m *Model) nextPreview(msg *Message) int {
	if !m.previewShown(msg) {
		return 0
	}
	if k := m.preview.chip + 1; k < len(previewArtifacts(msg)) && m.canPreview(msg) {
		return k
	}
	return -1
}

// imageActionLabel names what the footer action does for msg.
func (m *Model) imageActionLabel(msg *Message) string {
	k, n := m.nextPreview(msg), len(previewArtifacts(msg))
	switch {
	case k < 0:
		return "Hide image"
	case n > 1:
		return fmt.Sprintf("Show image %d of %d", k+1, n)
	}
	return "Show image"
}

// stepPreview runs the footer action: the next image of msg, or none.
func (m *Model) stepPreview(msg *Message) tea.Cmd {
	if k := m.nextPreview(msg); k >= 0 {
		return m.showPreview(msg, k)
	}
	return m.releasePreview()
}

// togglePreview shows the image of msg's chip k, or hides it when it is
// shown. Showing another image first releases the current one.
func (m *Model) togglePreview(msg *Message, k int) tea.Cmd {
	if m.previewShown(msg) && m.preview.chip == k {
		return m.releasePreview()
	}
	return m.showPreview(msg, k)
}

func (m *Model) showPreview(msg *Message, k int) tea.Cmd {
	arts := previewArtifacts(msg)
	if k < 0 || k >= len(arts) || !m.canPreview(msg) {
		m.say(levelInfo, "Image previews are not available in this terminal")
		return nil
	}
	release := m.releasePreview()
	return tea.Batch(release, m.requestPreview(msg, k))
}

// requestPreview asks the host for chip k of msg at the current width.
func (m *Model) requestPreview(msg *Message, k int) tea.Cmd {
	art := previewArtifacts(msg)[k]
	m.previewSeq++
	m.preview = &imagePreview{key: messageKey(*msg), chip: k, channel: msg.Channel, request: m.previewSeq,
		generation: m.previewCap.Generation, want: m.previewWidth(), wantRows: m.previewHeight()}
	m.touch()
	return m.opts.PreviewImage(PreviewRequest{
		Request: m.previewSeq, Generation: m.previewCap.Generation, Channel: msg.Channel,
		MessageID: msg.ID, Artifact: art, Chip: artifactLabel(&art), Cols: m.preview.want, Rows: m.preview.wantRows,
	})
}

// resizePreview asks again for a shown preview the pane's current area
// would place differently: it no longer fits, or an area bound that held it
// smaller has changed. A pending request is left to finish; its answer is
// checked here in turn.
func (m *Model) resizePreview() tea.Cmd {
	p := m.preview
	if p == nil || p.token == "" || p.fits(m.previewWidth(), m.previewHeight()) || m.opts.PreviewImage == nil {
		return nil
	}
	i := m.indexOf(p.key)
	if i < 0 || !m.canPreview(&m.messages[i]) || p.chip >= len(previewArtifacts(&m.messages[i])) {
		return nil
	}
	release := m.releasePreview()
	return tea.Batch(release, m.requestPreview(&m.messages[i], p.chip))
}

// releasePreview collapses the pane's preview and tells the host to free a
// shown image.
func (m *Model) releasePreview() tea.Cmd {
	p := m.preview
	if p == nil {
		return nil
	}
	m.preview = nil
	m.touch()
	if p.token == "" || m.opts.ReleasePreview == nil {
		return nil
	}
	return m.opts.ReleasePreview(p.token)
}

// previewUpdate handles the host's capability and replies. For a message the
// rest of the model also needs it returns handled false, with any command to
// run as well.
func (m *Model) previewUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	// Observed, not consumed: the rest of the model sees these too. A
	// reduced colour profile, a lost connection or lost membership of the
	// image's channel collapses the preview to its chip.
	case tea.ColorProfileMsg:
		m.trueColor = msg.Profile == colorprofile.TrueColor
		if !m.trueColor {
			return m.releasePreview(), false
		}
		return nil, false
	case ConnectionMsg:
		if !msg.Connected {
			return m.releasePreview(), false
		}
		return nil, false
	case MembershipMsg:
		if m.preview != nil && strings.EqualFold(msg.Channel, m.preview.channel) && msg.State != "joined" {
			return m.releasePreview(), false
		}
		return nil, false
	case PreviewCapabilityMsg:
		changed := msg.Generation != m.previewCap.Generation || !msg.Supported
		m.previewCap = msg
		if changed && m.preview != nil && m.preview.generation != msg.Generation {
			return m.releasePreview(), true
		}
		return nil, true
	case PreviewReadyMsg:
		p := m.preview
		if p == nil || p.request != msg.Request || p.token != "" || msg.Generation != p.generation {
			// A stale or unknown result: free it at once.
			if m.opts.ReleasePreview != nil && msg.Token != "" {
				return m.opts.ReleasePreview(msg.Token), true
			}
			return nil, true
		}
		p.token, p.id, p.cols, p.rows = msg.Token, msg.ID, msg.Cols, msg.Rows
		m.touch()
		return nil, true
	case PreviewErrorMsg:
		if p := m.preview; p != nil && p.request == msg.Request && p.token == "" {
			m.preview = nil
			m.touch()
			m.say(levelWarn, "Image preview unavailable: "+previewReason(msg.Code))
		}
		return nil, true
	}
	return nil, false
}

func previewReason(code string) string {
	switch code {
	case "unsupported":
		return "this terminal cannot show images"
	case "busy":
		return "another image is loading; try again"
	case "not_png":
		return "the file is not a PNG image"
	case "too_large":
		return "the image is too large"
	case "denied":
		return "you cannot read this file"
	case "stale":
		return "the display changed; try again"
	}
	return "the image could not be read"
}

// previewChip is where the chip of msg's shown preview ends: its display
// line and the byte offset after its label in that line. ok is false when no
// line holds its button (a literal chip in a table): the preview then
// follows the text.
func (m *Model) previewChip(msg *Message, lines []displayLine) (line, end int, ok bool) {
	p := m.preview
	if p == nil || p.key != messageKey(*msg) {
		return 0, 0, false
	}
	want := chipAction(msg.ID, p.chip)
	for n, dl := range lines {
		for _, piece := range dl.Pieces {
			if piece.Act == want {
				line, end, ok = n, piece.End, true
			}
		}
		if ok {
			return line, end, true
		}
	}
	return 0, 0, false
}

// previewRow is the index in rows of the displayed row the preview follows:
// the row holding the end of its chip (the chip's last row when it wraps),
// or the last row when no row holds it.
func (m *Model) previewRow(msg *Message, lines []displayLine, rows []bodyRow) int {
	if line, end, ok := m.previewChip(msg, lines); ok {
		for n, br := range rows {
			if br.Line == line && br.Start < end && end <= br.End {
				return n
			}
		}
	}
	return len(rows) - 1
}

// previewRows are the placeholder rows of msg's shown preview: each cell is
// U+10EEEE with its row and column marks, drawn in the image ID's colour, so
// the host's image appears exactly there.
func (m *Model) previewRows(msg *Message, key string, line int, indent []run) []transcriptRow {
	p := m.preview
	if p == nil || p.token == "" || p.key != key {
		return nil
	}
	style := spaceview.Style{FG: spaceview.RGB(uint8(p.id>>16), uint8(p.id>>8), uint8(p.id))}
	rows := make([]transcriptRow, 0, p.rows)
	for r := 0; r < p.rows; r++ {
		var cells strings.Builder
		for c := 0; c < p.cols; c++ {
			cells.WriteRune(kitty.Placeholder)
			cells.WriteRune(kitty.Diacritic(r))
			cells.WriteRune(kitty.Diacritic(c))
		}
		rows = append(rows, transcriptRow{key: key, line: line + linePreview + r, kind: rowDecor,
			runs: append(slicesCloneRuns(indent), run{Text: cells.String(), Style: style, Act: chipAction(msg.ID, p.chip)})})
	}
	return rows
}

// File chips are placed after Markdown parsing, so a file name shows
// literally and only the generated chip carries an action: chipText puts one
// token per chip into the text, each a private-use rune the message does not
// contain, and placeChips replaces each token with its literal label.

// chipText is the text the body parses: the message with each file chip as
// its token, and the chips' labels in token order. A message without chips
// keeps its text.
func chipText(msg *Message) (string, []rune, []string) {
	var labels []string
	switch {
	case msg.Artifact != nil:
		labels = []string{artifactLabel(msg.Artifact)}
	case len(msg.Attachments) > 0 && attachmentText(msg) != msg.Text:
		for i := range msg.Attachments {
			labels = append(labels, artifactLabel(&msg.Attachments[i].ArtifactRef))
		}
	default:
		return msg.Text, nil, nil
	}
	tokens := make([]rune, 0, len(labels))
	for r := rune(0x100000); len(tokens) < len(labels) && r <= 0x10fffd; r++ {
		if r != kitty.Placeholder && !strings.ContainsRune(msg.Text, r) {
			tokens = append(tokens, r)
		}
	}
	if msg.Artifact != nil {
		return string(tokens[0]), tokens, labels
	}
	return attachmentTextWith(msg, func(i int, _ *AttachmentRef) string { return string(tokens[i]) }), tokens, labels
}

// placeChips replaces each token with its chip label as literal text in the
// surrounding style; each chip toggles its own preview of the message with
// server ID previewID, when set. Copy yields the label. A token in a table, or one not seen exactly
// once, reports false: the caller then shows the chips as plain text.
func placeChips(lines []displayLine, tokens []rune, labels []string, previewID string) ([]displayLine, bool) {
	index := map[rune]int{}
	for k, r := range tokens {
		index[r] = k
	}
	seen := make([]int, len(tokens))
	replacements := make([][]markdownview.Replacement, len(lines))
	for n, dl := range lines {
		for at, r := range dl.Text {
			k, ok := index[r]
			if !ok {
				continue
			}
			if dl.Table != nil {
				return nil, false
			}
			seen[k]++
			// The label passes the same placeholder filter as message text.
			replacement := markdownview.Replacement{Start: at, End: at + utf8.RuneLen(r), Text: chatimage.NeutralizeText(labels[k])}
			for _, p := range dl.Pieces {
				if p.Start <= at && at < p.End {
					replacement.Style = p.Style
				}
			}
			if previewID != "" {
				replacement.Act = chipAction(previewID, k)
			}
			replacements[n] = append(replacements[n], replacement)
		}
	}
	for _, c := range seen {
		if c != 1 {
			return nil, false
		}
	}
	// Replacements take whole pieces: cut each piece at its tokens' edges.
	for n := range lines {
		for _, r := range replacements[n] {
			lines[n].Pieces = cutPieces(lines[n].Pieces, r.Start)
			lines[n].Pieces = cutPieces(lines[n].Pieces, r.End)
		}
	}
	out := markdownview.ApplyReplacements(lines, replacements)
	for n := range out {
		for j := range out[n].Subs {
			out[n].Subs[j].Copy = replacements[n][j].Text
		}
	}
	return out, true
}

// cutPieces splits the piece spanning at, if any, into two at at.
func cutPieces(pieces []piece, at int) []piece {
	for i, p := range pieces {
		if p.Start < at && at < p.End {
			left, right := p, p
			left.End, right.Start = at, at
			return append(pieces[:i:i], append([]piece{left, right}, pieces[i+1:]...)...)
		}
	}
	return pieces
}

// literalChipLines renders a message whose chips cannot be placed (a chip in
// a table, whose column widths are fixed when parsed). Each chip enters the
// parsed text as a run of its token as wide as its label, so the table is
// laid out for the label; the run is then swapped for the literal label in
// its line, shifting only later byte offsets. No Markdown sees the file name
// and nothing is renamed. Such chips carry no action; the footer action
// still applies. It reports false if a run is not seen exactly once.
func literalChipLines(msg *Message, tokens []rune, labels []string, parse func(string) []displayLine, wm ansi.Method) ([]displayLine, bool) {
	shown := make([]string, len(labels))
	runs := make([]string, len(labels))
	for k := range labels {
		shown[k] = chatimage.NeutralizeText(labels[k])
		runs[k] = strings.Repeat(string(tokens[k]), max(wm.StringWidth(shown[k]), 1))
	}
	text := runs[0]
	if msg.Artifact == nil {
		text = attachmentTextWith(msg, func(i int, _ *AttachmentRef) string { return runs[i] })
	}
	lines := parse(chatimage.NeutralizeText(text))
	seen := make([]int, len(labels))
	for n := range lines {
		for k := range runs {
			for {
				s := strings.Index(lines[n].Text, runs[k])
				if s < 0 {
					break
				}
				seen[k]++
				lines[n] = swapText(lines[n], s, s+len(runs[k]), shown[k])
			}
		}
	}
	for _, c := range seen {
		if c != 1 {
			return nil, false
		}
	}
	return lines, true
}

// swapText replaces dl.Text[s:e] with text, moving pieces and cells after it
// and stretching those around it.
func swapText(dl displayLine, s, e int, text string) displayLine {
	delta := len(text) - (e - s)
	start := func(p int) int {
		switch {
		case p < s:
			return p
		case p >= e:
			return p + delta
		}
		return s
	}
	end := func(p int) int {
		switch {
		case p <= s:
			return p
		case p >= e:
			return p + delta
		}
		return s + len(text)
	}
	dl.Text = dl.Text[:s] + text + dl.Text[e:]
	var pieces []piece
	for _, p := range dl.Pieces {
		p.Start, p.End = start(p.Start), end(p.End)
		if p.End > p.Start && (len(pieces) == 0 || pieces[len(pieces)-1].Start != p.Start || pieces[len(pieces)-1].End != p.End) {
			pieces = append(pieces, p)
		}
	}
	dl.Pieces = pieces
	cells := make([][2]int, len(dl.Cells))
	for c, r := range dl.Cells {
		cells[c] = [2]int{start(r[0]), end(r[1])}
	}
	if dl.Cells != nil {
		dl.Cells = cells
	}
	return dl
}

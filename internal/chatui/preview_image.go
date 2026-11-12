package chatui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/leogr/yoloctl/internal/chatstream"
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
}

// imagePreview is the pane's one requested or shown preview.
type imagePreview struct {
	key        string // the message's transcript key
	request    uint64
	generation uint64
	token      string // empty while pending
	id         uint32
	cols, rows int
}

// previewArtifact is the one artifact a message may preview, if any: a
// server artifact or a single published attachment, of a message with a
// server message ID.
func previewArtifact(msg *Message) *ArtifactRef {
	if msg == nil || !chatstream.ValidArtifactID(msg.ID) {
		return nil
	}
	switch {
	case msg.Artifact != nil:
		return msg.Artifact
	case len(msg.Attachments) == 1:
		return &msg.Attachments[0].ArtifactRef
	}
	return nil
}

// canPreview reports whether a "Show image" action applies to msg now.
func (m *Model) canPreview(msg *Message) bool {
	return m.opts.PreviewImage != nil && m.previewCap.Supported && previewArtifact(msg) != nil
}

// previewShown reports whether msg's preview is requested or shown.
func (m *Model) previewShown(msg *Message) bool {
	return m.preview != nil && msg != nil && m.preview.key == messageKey(*msg)
}

// togglePreview shows msg's image, or hides it when it is shown. Showing
// another message's image first releases the current one.
func (m *Model) togglePreview(msg *Message) tea.Cmd {
	if m.previewShown(msg) {
		return m.releasePreview()
	}
	art := previewArtifact(msg)
	if art == nil || !m.canPreview(msg) {
		m.say(levelInfo, "Image previews are not available in this terminal")
		return nil
	}
	release := m.releasePreview()
	m.previewSeq++
	m.preview = &imagePreview{key: messageKey(*msg), request: m.previewSeq, generation: m.previewCap.Generation}
	m.touch()
	return tea.Batch(release, m.opts.PreviewImage(PreviewRequest{
		Request: m.previewSeq, Generation: m.previewCap.Generation, Channel: msg.Channel,
		MessageID: msg.ID, Artifact: *art, Chip: artifactLabel(art),
	}))
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

// previewUpdate handles the host's capability and replies.
func (m *Model) previewUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
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
			runs: append(slicesCloneRuns(indent), run{Text: cells.String(), Style: style, Act: action{Kind: actPreview, Value: msg.ID}})})
	}
	return rows
}

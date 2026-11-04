package chatui

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Pasted image bounds: the managed room's artifact cap, and at most
// maxDraftImages images held across all drafts, counting those an undo could
// restore.
const (
	maxImageBytes  = 4 << 20
	maxDraftImages = 8
	// maxLateImages bounds the tokens of published images whose host
	// outcome had not arrived yet, so a late one can still be reported.
	maxLateImages = 8
)

// draftImage is one pasted image held by a placeholder in a channel's draft
// (see fold). Its bytes stay in this view; the host binds the token to the
// room and channel it was read for and consumes it on the upload attempt.
type draftImage struct {
	token, name   string
	data          []byte
	sha256        string
	width, height int
	// state is "reading" until the host answers, "" (ready to send),
	// "uploading" or "uncertain"; "sent", "refused" and "removed" mark an
	// image that left the draft for good.
	state string
	// requestID names the host operation of an uncertain upload, so its
	// recorded outcome can be inspected; problem is the host's explanation.
	requestID, problem string
	// awaiting is set while the host's outcome for the attempt is unknown,
	// including after a disconnect left it uncertain.
	awaiting bool
	// staging marks an attempt to stage the image unannounced for one
	// message (see staging.go); staged is the server's ID once confirmed.
	// State "expired" means it must be pasted again. frozen marks an image
	// of a send whose outcome was uncertain: its ID is never cleared.
	staging, frozen bool
	staged          string
}

// lateImage remembers an image that left its draft, published or removed,
// while its host outcome was still due.
type lateImage struct {
	token, channel    string
	published, staged bool
}

// imageRead is the one outstanding clipboard read. Its placeholder already
// sits where Ctrl+V was pressed, so text typed meanwhile keeps its order.
type imageRead struct {
	id, channel string
	img         *draftImage
}

// imageUpload is the one outstanding upload. revision and the reply capture
// the draft when it started: once the image is confirmed, the rest of the
// draft follows only if neither changed.
type imageUpload struct {
	channel, replyTo        string
	img                     *draftImage
	revision, replyRevision uint64
}

// imageHitID prefixes an image chip's hit id, keeping image and paste
// numbers apart.
const imageHitID = "img:"

// imageChipStyle shows a placeholder's state: italic while it is read or
// sent, yellow while its upload is uncertain.
func (m *Model) imageChipStyle(img *draftImage) spaceview.Style {
	t := m.theme
	st := spaceview.Style{FG: t.Bright, BG: t.Frame, Bold: true}
	switch img.state {
	case "reading", "uploading":
		st.Bold, st.Italic = false, true
	case "uncertain":
		st.FG = t.Capability
	case "expired":
		st.FG = t.Bad
	}
	return st
}

// imageDetails describes an image chip for its hover and click: verified
// pixels from the host decoder, size and state.
func imageDetails(img *draftImage) string {
	switch img.state {
	case "reading":
		return "reading the clipboard…"
	}
	what := "PNG"
	if img.width > 0 && img.height > 0 {
		what += fmt.Sprintf(" %d×%d", img.width, img.height)
	}
	what += " · " + sizeLabel(len(img.data))
	switch img.state {
	case "uploading":
		what += " · sending…"
	case "uncertain":
		what += " · upload uncertain"
	case "expired":
		what += " · paste it again"
	}
	return what
}

// imageLabel is an image placeholder's draft text and chip.
func imageLabel(n int) string { return "[Image #" + strconv.Itoa(n) + "]" }

// imageName labels img by its placeholder in the visible draft.
func (m *Model) imageName(img *draftImage) string {
	if f, ok := m.editor.imageFold(img); ok {
		return imageLabel(f.n)
	}
	return "the image"
}

// pasteImage starts the explicit Ctrl+V read and puts the image's
// placeholder at the cursor. Nothing reads the clipboard when the host gave
// no reader (seams.clipboard off or unsupported).
func (m *Model) pasteImage() tea.Cmd {
	switch {
	case m.opts.ReadImage == nil:
		m.say(levelInfo, "Image paste is unavailable here (seams.clipboard is off or images are unsupported)")
		return nil
	case m.imageRead != nil:
		m.say(levelInfo, "Still reading the clipboard")
		return nil
	case !m.roomForImage():
		m.say(levelWarn, fmt.Sprintf("At most %d images can wait in drafts; send or remove one first", maxDraftImages))
		return nil
	}
	img := &draftImage{state: "reading"}
	if err := m.editor.insertImage(img); err != nil {
		m.edit(err)
		return nil
	}
	m.imageRead = &imageRead{id: rand.Text(), channel: m.name, img: img}
	m.say(levelInfo, "Reading an image from the clipboard…")
	return m.opts.ReadImage(ImageRequest{ID: m.imageRead.id, Channel: m.name})
}

// roomForImage reports whether another image fits the bound. Images only an
// undo could restore give way first: that undo history is dropped.
func (m *Model) roomForImage() bool {
	count := func(list func(*editor) []*draftImage) int {
		n := 0
		for _, v := range m.views {
			n += len(list(&v.editor))
		}
		return n
	}
	if count((*editor).held) < maxDraftImages {
		return true
	}
	for _, v := range m.views {
		cur := v.editor.images()
		for _, img := range v.editor.held() {
			if !slices.Contains(cur, img) {
				v.editor.forget(img)
			}
		}
	}
	return count(func(e *editor) []*draftImage { return e.images() }) < maxDraftImages
}

// imagePrepared fills the placeholder of the read it answers, in the draft
// of the channel it was read for. A stale or duplicate answer is ignored; a
// failed read removes the placeholder and keeps the text around it.
func (m *Model) imagePrepared(msg ImagePreparedMsg) {
	r := m.imageRead
	if r == nil || msg.ID != r.id || msg.Channel != r.channel {
		return
	}
	m.imageRead = nil
	m.within(r.channel, func() {
		img := r.img
		f, present := m.editor.imageFold(img)
		level, problem := levelError, ""
		switch {
		case errors.Is(msg.Err, hostintegration.ErrNoImage):
			level, problem = levelInfo, "No PNG image on the clipboard; paste text with your terminal (for example Ctrl+Shift+V)"
		case msg.Err != nil:
			problem = "Image paste failed: " + oneLine(msg.Err.Error())
		case len(msg.Data) == 0 || len(msg.Data) > maxImageBytes:
			problem = "The image is " + sizeLabel(len(msg.Data)) + "; this room accepts files up to " + sizeLabel(maxImageBytes)
		case msg.Token == "" || msg.Name == "":
			problem = "The host did not prepare the image; nothing was attached"
		case !present:
			level, problem = levelWarn, "The image's placeholder was removed while reading; nothing was attached"
		}
		if problem != "" {
			img.state = "refused"
			m.dropImage(img)
			m.say(level, problem)
			return
		}
		sum := sha256.Sum256(msg.Data)
		img.token, img.name, img.data, img.sha256 = msg.Token, msg.Name, msg.Data, hex.EncodeToString(sum[:])
		img.width, img.height, img.state = msg.Width, msg.Height, ""
		m.say(levelOK, imageLabel(f.n)+" attached; Enter sends the draft in order, Backspace removes it")
	})
}

// dropImage takes img's placeholder out of the draft, keeping the text and
// cursor around it, and drops every undo state that could restore it.
func (m *Model) dropImage(img *draftImage) {
	e := &m.editor
	if f, ok := e.imageFold(img); ok {
		m.edit(e.remove(f.start, f.end))
	}
	e.forget(img)
}

// settleImages handles images that an edit or undo took out of the visible
// draft: a ready one stays restorable by undo, an uncertain upload's
// attempt stays on record, and anything else is forgotten.
func (m *Model) settleImages() {
	e := &m.editor
	cur := e.images()
	for _, img := range e.held() {
		if img.state == "" || slices.Contains(cur, img) {
			continue
		}
		e.forget(img)
		if img.state != "uncertain" {
			continue
		}
		img.state = "removed"
		// Removing clears the draft, not the host's record of an attempt.
		m.rememberLate(img, false)
		what := "upload"
		if img.staging {
			what = "staging"
		}
		if img.requestID != "" {
			note := m.inspect(img.requestID, "an image whose "+what+" is uncertain was removed")
			m.addPresence(presenceGuide, note)
			m.touch()
			m.say(levelWarn, note)
			continue
		}
		if img.staging {
			m.say(levelInfo, "Removed the image whose staging was uncertain; staging never announces it")
			continue
		}
		m.say(levelInfo, "Removed the image whose upload was uncertain; look for its announcement before pasting it again")
	}
}

// rememberLate keeps a bounded record of an image whose host outcome is
// still due when it leaves the draft, so that outcome can still be reported.
func (m *Model) rememberLate(img *draftImage, published bool) {
	if !img.awaiting {
		return
	}
	m.late = append(m.late, lateImage{token: img.token, channel: m.name, published: published, staged: img.staging})
	m.late = m.late[max(len(m.late)-maxLateImages, 0):]
}

// uploadImage publishes the image that leads the draft. The caller has
// checked that the view is connected, joined and has nothing unresolved.
func (m *Model) uploadImage(img *draftImage) tea.Cmd {
	if m.opts.UploadImage == nil {
		m.say(levelWarn, "Sending images is unavailable here; the image stays in the draft")
		return nil
	}
	img.state, img.awaiting = "uploading", true
	m.imageUpload = &imageUpload{channel: m.name, img: img, revision: m.editor.revision, replyTo: m.replyTo, replyRevision: m.replyRevision}
	m.say(levelInfo, "Sending "+m.imageName(img)+"…")
	return m.opts.UploadImage(ImageUpload{Channel: m.name, Token: img.token, Data: img.data})
}

// imageUploaded applies an upload outcome to the draft image whose token it
// names. Confirmed removes the placeholder and, if the draft and reply are
// unchanged, sends the rest of the draft in order. The host consumes a token
// on its attempt, so a Rejected image leaves the draft (paste it again to
// retry); Uncertain keeps it as a marker that blocks the channel until its
// announcement arrives or it is removed. Nothing is retried, and nothing
// continues after a refusal or uncertainty.
func (m *Model) imageUploaded(msg ImageUploadedMsg) tea.Cmd {
	var img *draftImage
	if v := m.views[strings.ToLower(msg.Channel)]; v != nil {
		for _, i := range v.editor.images() {
			if i.token == msg.Token && (i.state == "uploading" || i.state == "uncertain") {
				img = i
			}
		}
	}
	if img == nil {
		m.lateOutcome(msg) // perhaps for an image already shown published
		return nil
	}
	img.awaiting = false
	u := m.imageUpload
	ours := u != nil && u.img == img
	if ours {
		m.imageUpload = nil
	}
	var cmd tea.Cmd
	m.within(msg.Channel, func() {
		e, label := &m.editor, m.imageName(img)
		if img.staging {
			unchanged := ours && e.revision == u.revision && m.replyTo == u.replyTo && m.replyRevision == u.replyRevision
			cmd = m.imageStaged(msg, img, unchanged)
			return
		}
		// What is left of the draft is never run as a command.
		defer func() { e.remainder = e.text != "" }()
		switch msg.State {
		case Confirmed:
			img.state = "sent"
			unchanged := ours && e.revision == u.revision && m.replyTo == u.replyTo && m.replyRevision == u.replyRevision
			if f, ok := e.imageFold(img); ok && unchanged {
				// It led the draft: the blank space before the rest goes too.
				m.edit(e.remove(0, f.end+leadingBlank(e.text[f.end:])))
			} else {
				m.dropImage(img)
			}
			e.commit() // what was published never returns through undo
			m.say(levelOK, label+" sent")
			switch {
			case strings.TrimSpace(e.text) == "":
			case unchanged:
				cmd = m.sendDraft()
			default:
				m.say(levelOK, label+" sent; Enter sends the rest of the draft")
			}
		case Rejected:
			img.state = "refused"
			m.dropImage(img)
			reason := "refused"
			if msg.Err != nil {
				reason = oneLine(msg.Err.Error())
			}
			m.say(levelError, label+" was not sent ("+reason+"); nothing was published. Paste it again with Ctrl+V to retry")
		default:
			img.requestID = requestRef(msg.RequestID)
			if msg.Err != nil {
				// Bound retained error metadata independently of display geometry.
				img.problem = ansi.TruncateWc(oneLine(msg.Err.Error()), 240, "…")
			}
			m.uncertainImage(img)
		}
	})
	return cmd
}

// leadingBlank is the blank space that separated a sent placeholder from the
// rest of the draft: spaces on its row and whole blank lines, keeping the
// indentation of the next line of text.
func leadingBlank(s string) int {
	line := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			line = i + 1
		case ' ', '\t':
		default:
			if line == 0 {
				return i
			}
			return line
		}
	}
	return len(s)
}

// uncertainImage marks a draft image uncertain, unless its announcement
// already arrived while the upload was pending: that evidence stays in the
// transcript, so it is checked now instead of being missed. The rest of the
// draft stays unsent either way.
func (m *Model) uncertainImage(img *draftImage) {
	img.state = "uncertain"
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.published(m.messages[i]); img.state != "uncertain" {
			return
		}
	}
	m.sayEverywhere(levelWarn, m.imageUncertainText(img))
}

// imageUncertainText explains an uncertain upload: the host operation to
// inspect, when known, and the announcement to look for. Nothing is retried.
func (m *Model) imageUncertainText(img *draftImage) string {
	what := "upload"
	advice := "look for its announcement in the history before sending it again, or delete " + m.imageName(img) + " from the draft"
	if img.staging {
		what = "staging"
		advice = "staging never announces it; delete " + m.imageName(img) + " and paste it again"
	}
	switch {
	case img.requestID != "":
		return "Inspect yoloctl space operation " + m.opts.Space + " " + img.requestID + ": the image " + what + " is uncertain; " + advice
	case img.problem != "":
		return "The image " + what + " is uncertain (" + img.problem + "); " + advice
	}
	return "The image " + what + " is uncertain; " + advice
}

// imageUnresolved reports why sending must wait for an image outcome.
func (m *Model) imageUnresolved() string {
	if m.imageUpload != nil {
		return "An image is still being sent; wait for its result"
	}
	for _, img := range m.editor.images() {
		switch img.state {
		case "reading":
			return "Still reading " + m.imageName(img) + " from the clipboard; wait for it or delete it"
		case "uncertain":
			return m.imageUncertainText(img)
		case "expired":
			return m.imageName(img) + " has to be pasted again: press Ctrl+V for a fresh copy and delete the old placeholder"
		}
	}
	return ""
}

// lostImage marks an upload interrupted by a disconnect as uncertain. A late
// outcome for its token can still resolve it.
func (m *Model) lostImage() {
	u := m.imageUpload
	if u == nil {
		return
	}
	m.imageUpload = nil
	m.within(u.channel, func() {
		if u.img.state == "uploading" {
			m.uncertainImage(u.img) // a late result may still name its operation
		}
	})
}

// published resolves an uncertain draft image from its announcement: the
// reserved artifact metadata of a message from this account in the image's
// channel must match its generated name, size and digest. Text alone never
// does. The rest of the draft stays for an explicit Enter.
func (m *Model) published(msg Message) {
	a := msg.Artifact
	if a == nil || msg.Account == "" || msg.Account != m.opts.Account {
		return
	}
	for _, img := range m.editor.images() {
		if img.state != "uncertain" || img.staging || a.Name != img.name || a.Bytes != int64(len(img.data)) || !strings.EqualFold(a.SHA256, img.sha256) {
			continue
		}
		img.state = "sent"
		m.dropImage(img)
		m.editor.commit()
		m.editor.remainder = m.editor.text != ""
		m.rememberLate(img, true)
		if img.requestID == "" {
			m.say(levelOK, "The image's announcement was observed: it was published")
			return
		}
		// The draft that showed the operation is gone: keep it on record.
		text := m.inspect(img.requestID, "the image's announcement was observed, so it was published")
		m.addPresence(presenceGuide, text)
		m.touch()
		m.sayEverywhere(levelOK, text)
		return
	}
}

// inspect leads guidance about a host operation with the command that
// inspects it, so the ID stays visible where a row is cut short. Publication
// is evidence for the user; the view never acknowledges an operation or
// claims what its receipt says. Without an ID it is just the sentence.
func (m *Model) inspect(requestID, sentence string) string {
	if requestID == "" {
		return strings.ToUpper(sentence[:1]) + sentence[1:]
	}
	return "Inspect yoloctl space operation " + m.opts.Space + " " + requestID + ": " + sentence +
		"; acknowledge it only if the inspected outcome requires review"
}

// lateOutcome reports the host's outcome for an image that already left its
// draft, published or removed. It changes no draft, sends nothing and claims
// no publication beyond what was observed; the operation to inspect is
// recorded in that channel's transcript and shown wherever the user is.
func (m *Model) lateOutcome(msg ImageUploadedMsg) {
	i := slices.IndexFunc(m.late, func(l lateImage) bool {
		return l.token == msg.Token && strings.EqualFold(l.channel, msg.Channel)
	})
	if i < 0 {
		return // unknown, duplicate or evicted: nothing to correlate
	}
	l := m.late[i]
	m.late = slices.Delete(m.late, i, i+1)
	if u := m.imageUpload; u != nil && u.img.token == msg.Token {
		m.imageUpload = nil
	}
	what := "published"
	if !l.published {
		what = "removed"
	}
	id := requestRef(msg.RequestID)
	level, text := levelWarn, m.inspect(id, "the "+what+" image's upload outcome is uncertain")
	switch {
	case l.staged && msg.State == Confirmed:
		level, text = levelOK, "The host confirmed the "+what+" image's staging; it is never announced and expires"
	case l.staged:
		text = m.inspect(id, "the "+what+" image's staging outcome is uncertain; staging never announces it")
	case msg.State == Confirmed:
		level, text = levelOK, "The host confirmed the "+what+" image's upload: it was published"
	case msg.State == Rejected:
		text = m.inspect(id, "the host reported the "+what+" image's upload as refused")
	}
	m.imageNote(l.channel, level, text)
}

// imageNote records image guidance in its channel's transcript and shows it
// now, naming the channel when another is visible: notices from a hidden
// channel are otherwise dropped below error level.
func (m *Model) imageNote(channel string, level noticeLevel, text string) {
	m.within(channel, func() {
		m.addPresence(presenceGuide, text)
		m.touch()
		m.sayEverywhere(level, text)
	})
}

// requestRef is a host request ID safe to show in a command, or "".
func requestRef(id string) string {
	id = oneLine(id)
	if len(id) > 80 || strings.ContainsAny(id, " `") {
		return ""
	}
	return id
}

// sizeLabel is a compact byte size for file chips.
func sizeLabel(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return byteCount(n)
}

// artifactLabel shows a message's server artifact as a generic file: the
// metadata names it but does not prove its media type, and the announcement
// URL stays inert.
func artifactLabel(a *ArtifactRef) string {
	return "[file " + oneLine(a.Name) + " · " + sizeLabel(int(a.Bytes)) + "]"
}

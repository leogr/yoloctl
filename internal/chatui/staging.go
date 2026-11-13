package chatui

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Staged sends, when the room's AIRC has message-attachments: Enter stages
// each image of the draft, unannounced and undownloadable, one at a time in
// draft order, then sends the whole draft as one message whose [Image #k]
// markers place them. Nothing is visible before that message commits, so
// readers and agents get the text and its images together, never a part.

// stagedDraft is the draft's wire form: its text with each image as the
// marker [Image #k] at the byte offset attach lists, numbered in draft order.
type stagedDraft struct {
	text   string
	attach []AttachRef
	images []*draftImage
}

func (m *Model) stagedDraft() stagedDraft {
	e := &m.editor
	var b strings.Builder
	var d stagedDraft
	last := 0
	for _, f := range e.folds {
		if f.img == nil {
			continue
		}
		b.WriteString(e.text[last:f.start])
		d.images = append(d.images, f.img)
		d.attach = append(d.attach, AttachRef{ID: f.img.staged, At: b.Len()})
		b.WriteString(imageLabel(len(d.images)))
		last = f.end
	}
	b.WriteString(e.text[last:])
	d.text = strings.TrimRight(b.String(), " \t\n")
	return d
}

// hasImages reports whether the visible draft holds an image placeholder.
func (m *Model) hasImages() bool {
	return slices.ContainsFunc(m.editor.folds, func(f fold) bool { return f.img != nil })
}

// sendStaged stages the first image not yet staged or, once all are, sends
// the draft as one message. The caller checked the connection, membership
// and that nothing is unresolved.
func (m *Model) sendStaged() tea.Cmd {
	d := m.stagedDraft()
	if lines := wireLines(d.text); m.maxLines > 0 && lines > m.maxLines {
		m.say(levelWarn, fmt.Sprintf("Message needs about %d lines; the room accepts %d per message. Shorten it", lines, m.maxLines))
		return nil
	}
	for _, img := range d.images {
		if img.staged == "" {
			return m.stageImage(img)
		}
	}
	e := &m.editor
	out := Outgoing{ID: rand.Text(), Channel: m.name, Text: d.text, ReplyTo: m.replyTo, Attach: d.attach}
	m.inflight, m.inflightChannel, m.sentText, m.sentRevision = out.ID, m.name, out.Text, e.revision
	m.sentDraft, m.sentEnd, m.sentReplyRevision = e.text, len(e.text), m.replyRevision
	m.sentImages = d.images
	m.follow, m.anchor, m.picked = true, nil, false
	local := Message{ClientID: out.ID, Channel: m.name, Nick: m.opts.Nick, Text: out.Text, ReplyTo: out.ReplyTo, Time: time.Now(), Delivery: Pending}
	for i, img := range d.images {
		local.Attachments = append(local.Attachments, AttachmentRef{
			ArtifactRef: ArtifactRef{ID: img.staged, Name: img.name, SHA256: img.sha256, Bytes: int64(len(img.data))},
			At:          d.attach[i].At,
		})
	}
	m.append(local)
	m.clearNotice()
	return m.opts.Send(out)
}

// stageImage stores one image unannounced for the draft's message.
func (m *Model) stageImage(img *draftImage) tea.Cmd {
	if m.opts.UploadImage == nil {
		m.say(levelWarn, "Sending images is unavailable here; the image stays in the draft")
		return nil
	}
	img.state, img.awaiting, img.staging = "uploading", true, true
	m.imageUpload = &imageUpload{channel: m.name, img: img, revision: m.editor.revision, replyTo: m.replyTo, replyRevision: m.replyRevision}
	m.say(levelInfo, "Preparing "+m.imageName(img)+"…")
	return m.opts.UploadImage(ImageUpload{Channel: m.name, Token: img.token, Data: img.data, Stage: true})
}

// imageStaged applies a staging outcome. A confirmed stage binds its ID to
// the image only when it is a stage of exactly these bytes, then continues
// if the draft and reply did not change. The host spent the token, so a
// refused or mismatched image must be pasted again; an uncertain one blocks
// the channel like an uncertain upload. Nothing is retried.
func (m *Model) imageStaged(msg ImageUploadedMsg, img *draftImage, unchanged bool) tea.Cmd {
	label := m.imageName(img)
	switch msg.State {
	case Confirmed:
		if !msg.Staged || msg.MsgID != "" || len(msg.ArtifactID) != 36 || msg.Bytes != int64(len(img.data)) || !strings.EqualFold(msg.SHA256, img.sha256) {
			m.expire(img, "The host's answer for "+label+" did not match the image; nothing was sent")
			return nil
		}
		img.state, img.staged = "", strings.ToLower(msg.ArtifactID)
		if unchanged {
			return m.sendDraft()
		}
		m.say(levelOK, label+" is ready; Enter sends the draft")
	case Rejected:
		reason := "refused"
		if msg.Err != nil {
			reason = oneLine(msg.Err.Error())
		}
		m.expire(img, label+" was not prepared ("+reason+"); nothing was sent")
	default:
		img.requestID = requestRef(msg.RequestID)
		if msg.Err != nil {
			// Bound retained error metadata independently of display geometry.
			img.problem = ansi.TruncateWc(oneLine(msg.Err.Error()), 240, "…")
		}
		img.state = "uncertain"
		m.sayEverywhere(levelWarn, m.imageUncertainText(img))
	}
	return nil
}

// expire marks an image that has to be pasted again: its host token is spent
// and it has no usable staging. The placeholder stays, so the user sees which
// one, and so does the rest of the draft; Enter waits until it is replaced.
func (m *Model) expire(img *draftImage, why string) {
	img.state, img.staged = "expired", ""
	m.say(levelError, why+". "+m.pasteAgain(img))
}

func (m *Model) pasteAgain(img *draftImage) string {
	return "Paste the image again with Ctrl+V and delete " + m.imageName(img) + "; the rest of the draft is kept"
}

// stagedOutcome applies the delivery of submitted, a message with staged
// images. It runs before the generic delivery state is cleared.
func (m *Model) stagedOutcome(d DeliveryMsg, submitted Message) {
	sent := m.sentImages
	switch d.State {
	case Confirmed:
		m.sentImages = nil
		m.publishedImages(sent)
	case Rejected:
		m.sentImages = nil
		m.stagedRefusal(d.Refusal, sent, submitted)
	case Uncertain:
		// The message may have committed: its staged IDs are frozen, never
		// cleared or staged again, and a later echo can still confirm it.
		for _, img := range sent {
			img.frozen = true
		}
	}
}

// publishedImages: the message with these images is in the room. Any still
// in the draft (it changed meanwhile) leave it, and no undo brings them back.
func (m *Model) publishedImages(sent []*draftImage) {
	for _, img := range sent {
		img.state = "sent"
		m.dropImage(img)
	}
	m.editor.commit()
}

// stagedRefusal explains a definite refusal. The draft always stays. Only an
// unknown staging of a draft that never had an uncertain send is cleared, and
// then that image must be pasted again; a consumed one is evidence the draft
// may already be in the room, so nothing is cleared unless the view shows it.
func (m *Model) stagedRefusal(r *AttachmentRefusal, sent []*draftImage, submitted Message) {
	if r == nil {
		return // delivery showed the reason; the draft and its staged images stay
	}
	frozen := slices.ContainsFunc(sent, func(img *draftImage) bool { return img.frozen })
	switch r.Reason {
	case "unknown":
		if r.Index < 1 || r.Index > len(sent) {
			return
		}
		img := sent[r.Index-1]
		if _, ok := m.editor.imageFold(img); !ok {
			return
		}
		if frozen {
			m.say(levelError, m.imageName(img)+" is no longer staged after an uncertain send; inspect the room history before pasting it again")
			return
		}
		m.expire(img, m.imageName(img)+" expired before the message was sent; nothing was sent")
	case "consumed":
		if r.Index < 1 || r.Index > len(sent) {
			return
		}
		if m.publishedAs(r.MessageID, submitted) {
			// As for a confirmed send, only a draft and reply left unchanged
			// are cleared; later edits stay, without the published images.
			if m.editor.text == m.sentDraft && m.editor.revision == m.sentRevision && m.replyRevision == m.sentReplyRevision {
				m.edit(m.editor.replace(0, len(m.editor.text), ""))
				m.replyTo = ""
				m.suggest = nil
			}
			m.publishedImages(sent)
			m.say(levelOK, "This draft was already sent; its message is in the room")
			return
		}
		for _, img := range sent {
			img.frozen = true
		}
		m.say(levelError, "This draft may already have been sent: its images were used by message "+oneLine(r.MessageID)+". Inspect the room history before sending again; nothing was cleared")
	case "unavailable":
		m.say(levelError, "Images cannot be sent in "+m.name+" now (files are unavailable here or read access changed); the draft is kept")
	case "store":
		// A definite refusal of this attempt only: an earlier uncertain one
		// may still have landed, so its frozen images stay frozen. The staged
		// references keep their upload expiry; a later Enter that finds one
		// gone is refused as unknown.
		why := "The room's server refused to store this message with its images; the draft, reply and image references are kept here. "
		if frozen {
			m.say(levelError, why+"An earlier send of this draft is still uncertain: inspect the room history before sending again")
			return
		}
		m.say(levelError, why+"Enter tries again with them while they are still available; if it keeps failing, check the server with yoloctl chat status")
	case "too-long":
		m.say(levelError, "The room refused this message: its images do not fit in one message. Send fewer images in it; the draft is kept")
	case "invalid":
		m.say(levelError, "The room refused where this message places its images; the draft is kept")
	case "channel-only":
		m.say(levelError, "Images can only be sent to a channel; the draft is kept")
	}
}

// publishedAs reports whether message id, from this account and in view, is
// exactly the submitted message: the same text and reply, and the same staged
// images at the same offsets. Only then did an earlier send of it land.
func (m *Model) publishedAs(id string, submitted Message) bool {
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.ID != id || id == "" {
			continue
		}
		if msg.Account == "" || msg.Account != m.opts.Account || msg.Text != submitted.Text || msg.ReplyTo != submitted.ReplyTo || len(msg.Attachments) != len(submitted.Attachments) {
			return false
		}
		for k, a := range msg.Attachments {
			if a.ID != submitted.Attachments[k].ID || a.At != submitted.Attachments[k].At {
				return false
			}
		}
		return true
	}
	return false
}

// attachmentText shows each verified attachment span as a file chip. Only
// spans whose text is exactly their marker are attachments; anything else
// keeps the original text.
func attachmentText(msg *Message) string {
	return attachmentTextWith(msg, func(_ int, a *AttachmentRef) string { return artifactLabel(&a.ArtifactRef) })
}

// attachmentTextWith is attachmentText with chip(i, a) written for each
// verified span.
func attachmentTextWith(msg *Message, chip func(int, *AttachmentRef) string) string {
	var b strings.Builder
	last := 0
	for i := range msg.Attachments {
		a := &msg.Attachments[i]
		marker := imageLabel(i + 1)
		if a.At < last || a.At > len(msg.Text) || !strings.HasPrefix(msg.Text[a.At:], marker) {
			return msg.Text
		}
		b.WriteString(msg.Text[last:a.At])
		b.WriteString(chip(i, a))
		last = a.At + len(marker)
	}
	b.WriteString(msg.Text[last:])
	return b.String()
}

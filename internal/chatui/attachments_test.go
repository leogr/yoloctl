package chatui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/hostintegration"
)

type imageHost struct {
	reads   []ImageRequest
	uploads []ImageUpload
}

func imageModel(t *testing.T) (*Model, *[]Outgoing, *imageHost) {
	t.Helper()
	m, sends, _ := channelModel(t)
	m.opts.Account = "mhuman"
	h := &imageHost{}
	m.opts.ReadImage = func(r ImageRequest) tea.Cmd { h.reads = append(h.reads, r); return nil }
	m.opts.UploadImage = func(u ImageUpload) tea.Cmd { h.uploads = append(h.uploads, u); return nil }
	return m, sends, h
}

func prepared(r ImageRequest, data string) ImagePreparedMsg {
	return ImagePreparedMsg{ID: r.ID, Channel: r.Channel, Token: "tok" + r.ID[:4], Name: "paste-x-" + r.ID[:4] + ".png", Data: []byte(data), Width: 2, Height: 3}
}

func announcement(account, channel, name, data string) Message {
	sum := sha256.Sum256([]byte(data))
	return Message{ID: "ann-" + name, Account: account, Nick: "human", Channel: channel,
		Text:     fmt.Sprintf("Artifact %s (%d bytes): http://airc.local/v1/artifacts/00000000-0000-4000-8000-000000000000", name, len(data)),
		Artifact: &ArtifactRef{ID: "00000000-0000-4000-8000-000000000000", Name: name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}}
}

// img0 is the first image in the visible draft, or nil.
func img0(m *Model) *draftImage {
	if imgs := m.editor.images(); len(imgs) > 0 {
		return imgs[0]
	}
	return nil
}

// pasteImg presses Ctrl+V and answers the read with data.
func pasteImg(t *testing.T, m *Model, h *imageHost, data string) *draftImage {
	t.Helper()
	n := len(h.reads)
	press(m, 'v', tea.ModCtrl)
	if len(h.reads) != n+1 {
		t.Fatalf("Ctrl+V did not read: %q", m.notice)
	}
	m.Update(prepared(h.reads[n], data))
	imgs := m.editor.images()
	img := imgs[len(imgs)-1]
	for _, i := range imgs {
		if i.token == "tok"+h.reads[n].ID[:4] {
			img = i
		}
	}
	if img.state != "" || string(img.data) != data {
		t.Fatalf("the image was not attached: %q", m.notice)
	}
	return img
}

// deleteImg places the cursor after img's placeholder and presses Backspace.
func deleteImg(t *testing.T, m *Model, img *draftImage) {
	t.Helper()
	f, ok := m.editor.imageFold(img)
	if !ok {
		t.Fatal("the image is not in the draft")
	}
	m.editor.cursor = f.end
	press(m, tea.KeyBackspace, 0)
}

func TestCtrlVReadsOneImageIntoTheCapturedDraft(t *testing.T) {
	m, _, _ := imageModel(t)
	m.opts.ReadImage = nil
	press(m, 'v', tea.ModCtrl)
	if !strings.Contains(m.notice, "unavailable") || m.Draft() != "" {
		t.Fatal("without a host reader nothing is read or inserted")
	}
	m, _, h := imageModel(t)
	press(m, 'v', tea.ModCtrl)
	press(m, 'v', tea.ModCtrl)
	if len(h.reads) != 1 || h.reads[0].Channel != "#space" || !strings.Contains(m.notice, "Still reading") {
		t.Fatalf("one read at a time, for the visible channel: %+v", h.reads)
	}
	if m.Draft() != "[Image #1]" || img0(m).state != "reading" {
		t.Fatalf("the placeholder holds its place while reading: %q", m.Draft())
	}
	r := h.reads[0]
	alt(m, '2') // the user moves on while the host reads
	m.Update(ImagePreparedMsg{ID: "other", Channel: r.Channel, Token: "t", Name: "n", Data: []byte("x")})
	if m.views["#space"].editor.images()[0].state != "reading" || m.imageRead == nil {
		t.Fatal("an answer for another request is ignored")
	}
	m.Update(prepared(r, "png-bytes"))
	if imgs := m.views["#space"].editor.images(); len(imgs) != 1 || imgs[0].state != "" || len(m.views["#build"].editor.images()) != 0 || m.imageRead != nil {
		t.Fatal("the image belongs to the draft where Ctrl+V was pressed")
	}
	m.Update(prepared(r, "again"))
	if string(m.views["#space"].editor.images()[0].data) != "png-bytes" {
		t.Fatal("a duplicate answer does not replace the attached image")
	}
	alt(m, '1')
	if _, line := lineWith(t, m, "❯ [Image #1]"); strings.Contains(view(m), "[image") || strings.Contains(line, "✕") {
		t.Fatalf("the image is inline in the prompt, not in a strip:\n%s", view(m))
	}
	pasteImg(t, m, h, "second")
	if m.Draft() != "[Image #1][Image #2]" || len(m.editor.images()) != 2 {
		t.Fatalf("a draft holds several images: %q", m.Draft())
	}
}

func TestImageReadFailuresAreBoundedAndExplained(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ImagePreparedMsg)
		want string
	}{
		{"no image", func(p *ImagePreparedMsg) { p.Err = fmt.Errorf("read: %w", hostintegration.ErrNoImage) }, "No PNG image"},
		{"failure", func(p *ImagePreparedMsg) { p.Err = errors.New("xclip failed") }, "xclip failed"},
		{"oversized", func(p *ImagePreparedMsg) { p.Data = make([]byte, maxImageBytes+1) }, "up to 4.0 MiB"},
		{"unprepared", func(p *ImagePreparedMsg) { p.Token = "" }, "did not prepare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, h := imageModel(t)
			paste(m, "a ")
			press(m, 'v', tea.ModCtrl)
			paste(m, " b")
			p := prepared(h.reads[0], "png")
			tc.edit(&p)
			m.Update(p)
			if len(m.editor.held()) != 0 || m.Draft() != "a  b" || !strings.Contains(m.notice, tc.want) {
				t.Fatalf("nothing is attached, the text stays and the reason is shown: %q %q", m.Draft(), m.notice)
			}
			press(m, 'z', tea.ModCtrl)
			if len(m.editor.images()) != 0 {
				t.Fatal("undo cannot bring back a failed placeholder")
			}
		})
	}
	m, _, h := imageModel(t)
	for range maxDraftImages - 1 {
		pasteImg(t, m, h, "png")
	}
	alt(m, '2')
	last := pasteImg(t, m, h, "png")
	alt(m, '1')
	press(m, 'v', tea.ModCtrl)
	if len(h.reads) != maxDraftImages || !strings.Contains(m.notice, "At most 8") {
		t.Fatalf("draft images are bounded across drafts: %q", m.notice)
	}
	alt(m, '2')
	deleteImg(t, m, last) // undo could restore it, so it is still held
	alt(m, '1')
	press(m, 'v', tea.ModCtrl)
	if len(h.reads) != maxDraftImages+1 || len(m.views["#build"].editor.held()) != 0 {
		t.Fatal("an image only undo could restore gives way to a new paste")
	}
}

// One draft sends its text and images in draft order, each part after the
// previous one is confirmed; the sent parts leave the draft.
func TestDraftPartsSendInDraftOrder(t *testing.T) {
	m, sends, h := imageModel(t)
	var log []string
	send := m.opts.Send
	m.opts.Send = func(out Outgoing) tea.Cmd { log = append(log, "text:"+out.Text); return send(out) }
	upload := m.opts.UploadImage
	m.opts.UploadImage = func(u ImageUpload) tea.Cmd { log = append(log, "image:"+string(u.Data)); return upload(u) }
	paste(m, "look ")
	pasteImg(t, m, h, "png1")
	paste(m, " and\n")
	pasteImg(t, m, h, "png2")
	paste(m, "\n\n  done")
	if m.Draft() != "look [Image #1] and\n[Image #2]\n\n  done" {
		t.Fatalf("draft: %q", m.Draft())
	}
	confirm := func() {
		t.Helper()
		if len(h.uploads) > 0 && m.imageUpload != nil {
			m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[len(h.uploads)-1].Token, State: Confirmed})
			return
		}
		m.Update(DeliveryMsg{ID: (*sends)[len(*sends)-1].ID, Channel: "#space", MessageID: fmt.Sprint("m", len(*sends)), State: Confirmed})
	}
	press(m, tea.KeyEnter, 0)
	if strings.Join(log, "|") != "text:look" {
		t.Fatalf("the leading text goes first: %v", log)
	}
	confirm()
	if m.Draft() != "[Image #1] and\n[Image #2]\n\n  done" || strings.Join(log, "|") != "text:look|image:png1" {
		t.Fatalf("the confirmed text leaves the draft and the image follows: %q %v", m.Draft(), log)
	}
	img := img0(m)
	press(m, tea.KeyEnter, 0)
	deleteImg(t, m, img)
	if len(h.uploads) != 1 || img0(m) != img || !strings.Contains(m.notice, "being sent") {
		t.Fatalf("an image in flight is neither resent nor removable: %q", m.notice)
	}
	confirm()
	if m.Draft() != "and\n[Image #2]\n\n  done" || strings.Join(log, "|") != "text:look|image:png1|text:and" {
		t.Fatalf("the sent image leaves with the space after it: %q %v", m.Draft(), log)
	}
	confirm()
	confirm()
	if m.Draft() != "  done" || strings.Join(log, "|") != "text:look|image:png1|text:and|image:png2|text:  done" {
		t.Fatalf("blank lines after an image go, indentation stays: %q %v", m.Draft(), log)
	}
	confirm()
	if m.Draft() != "" || m.inflight != "" || len(m.editor.held()) != 0 {
		t.Fatalf("the whole draft was sent: %q", m.Draft())
	}
	press(m, 'z', tea.ModCtrl)
	press(m, 'z', tea.ModCtrl)
	if len(m.editor.images()) != 0 {
		t.Fatal("undo never brings back a sent image")
	}
}

// No edit, cut or undo takes an image out of the draft while it is sent.
func TestUndoNeverRemovesAnImageInFlight(t *testing.T) {
	m, sends, h := imageModel(t)
	img := pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	for _, key := range []rune{'z', 'u', 'w'} {
		press(m, key, tea.ModCtrl)
		if img0(m) != img || !strings.Contains(m.notice, "being sent") {
			t.Fatalf("Ctrl+%c removed the image in flight: %q", key, m.notice)
		}
	}
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed})
	if m.Draft() != "" || len(*sends) != 0 || len(h.uploads) != 1 {
		t.Fatalf("the image was sent once and nothing else: %q", m.Draft())
	}
}

// An edit while a part is in flight stops the sequence: the confirmed part
// leaves, the rest waits for Enter, and nothing is sent twice.
func TestEditedDraftStopsAfterTheConfirmedPart(t *testing.T) {
	m, sends, h := imageModel(t)
	pasteImg(t, m, h, "png")
	paste(m, "caption @sol")
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 1 || len(*sends) != 0 {
		t.Fatalf("Enter uploads the leading image: %+v / %+v", h.uploads, *sends)
	}
	tok := h.uploads[0].Token
	press(m, tea.KeyEnter, 0)
	press(m, tea.KeyBackspace, 0)
	paste(m, "l") // the user briefly edits the caption back
	m.Update(ImageUploadedMsg{Channel: "#space", Token: tok, MsgID: "a1", State: Confirmed})
	if len(m.editor.images()) != 0 || len(*sends) != 0 || m.Draft() != "caption @sol" || !strings.Contains(m.notice, "Enter sends the rest") {
		t.Fatalf("edited text is kept, not sent: %+v %q %q", *sends, m.Draft(), m.notice)
	}
	m.Update(ImageUploadedMsg{Channel: "#space", Token: tok, State: Confirmed})
	if len(*sends) != 0 || m.Draft() != "caption @sol" {
		t.Fatal("a duplicate outcome changes nothing")
	}

	// A text part that is refused keeps the whole draft and stops.
	if err := m.SetDraft(""); err != nil {
		t.Fatal(err)
	}
	paste(m, "first ")
	img := pasteImg(t, m, h, "png2")
	press(m, tea.KeyEnter, 0)
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", State: Rejected, Err: errors.New("refused")})
	if m.Draft() != "first [Image #1]" || len(h.uploads) != 1 || img.state != "" {
		t.Fatalf("a refused part keeps the draft and sends nothing more: %q", m.Draft())
	}
}

func TestImageRejectionAndUncertainty(t *testing.T) {
	m, sends, h := imageModel(t)
	pasteImg(t, m, h, "png")
	paste(m, " /help")
	press(m, tea.KeyEnter, 0)
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Rejected, Err: errors.New("413 too large")})
	if len(m.editor.images()) != 0 || m.Draft() != " /help" || len(*sends) != 0 ||
		!strings.Contains(m.notice, "413 too large") || !strings.Contains(m.notice, "Paste it again") {
		t.Fatalf("a rejected image's token is spent: it leaves the draft with the reason, the rest stays: %q %q", m.Draft(), m.notice)
	}
	press(m, 'z', tea.ModCtrl)
	if len(m.editor.images()) != 0 {
		t.Fatal("undo cannot bring back a spent image")
	}
	press(m, tea.KeyEnter, 0)
	if m.overlay != nil || len(*sends) != 1 || (*sends)[0].Text != " /help" {
		t.Fatalf("what remains of a mixed draft is a message, never a command: %+v", *sends)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", MessageID: "h", State: Confirmed})
	*sends = (*sends)[:0]

	// A lost result is uncertain: nothing retries, sending waits.
	if err := m.SetDraft(""); err != nil {
		t.Fatal(err)
	}
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[1].Token, State: Uncertain})
	paste(m, "text")
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 2 || len(*sends) != 0 || !strings.Contains(m.notice, "uncertain") || !strings.Contains(m.notice, "delete [Image #1]") {
		t.Fatalf("an uncertain image blocks its channel: %q", m.notice)
	}
	alt(m, '2')
	paste(m, "elsewhere")
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 {
		t.Fatal("other channels can still send")
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#build", MessageID: "b", State: Confirmed})
	alt(m, '1')

	// Only matching reserved metadata from this account resolves it.
	img := img0(m)
	for _, msg := range []Message{
		announcement("mother", "#space", img.name, "png"),
		announcement("mhuman", "#space", img.name, "PNG"),
		{ID: "text", Account: "mhuman", Channel: "#space", Text: "Artifact " + img.name + " (3 bytes): http://airc.local/v1/artifacts/x"},
	} {
		m.Update(IncomingMsg{Message: msg})
		if img0(m) == nil {
			t.Fatalf("%s must not resolve the uncertain image", msg.ID)
		}
	}
	m.Update(IncomingMsg{Message: announcement("mhuman", "#space", img.name, "png")})
	if img0(m) != nil || m.Draft() != "text" || !strings.Contains(m.notice, "published") {
		t.Fatal("the matching announcement resolves the image and keeps the rest")
	}
	if len(*sends) != 1 {
		t.Fatal("nothing continues automatically after uncertainty")
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 2 || (*sends)[1].Text != "text" {
		t.Fatal("sending resumes once the image is resolved")
	}
}

func TestDisconnectedImageUploadBecomesUncertain(t *testing.T) {
	m, _, h := imageModel(t)
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(ConnectionMsg{})
	if img0(m).state != "uncertain" || m.imageUpload != nil {
		t.Fatal("a disconnect leaves the upload uncertain")
	}
	m.Update(ConnectionMsg{Connected: true})
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed})
	if img0(m) != nil {
		t.Fatal("a late result for the token still resolves it")
	}
	pasteImg(t, m, h, "png")
	paste(m, " after")
	m.View()
	x, y := hitFor(t, m, hitFold, 0)
	m.Update(tea.MouseClickMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	if m.editor.cursor != len("[Image #1]") || !strings.Contains(m.notice, "[Image #1]: PNG 2×3 · 3 B") {
		t.Fatalf("a click puts the cursor after the image and describes it: %d %q", m.editor.cursor, m.notice)
	}
	press(m, tea.KeyBackspace, 0)
	if img0(m) != nil || m.Draft() != " after" {
		t.Fatal("Backspace removes the image whole")
	}
}

func TestArtifactMessagesShowAsInertFiles(t *testing.T) {
	m, _, _ := imageModel(t)
	var copied []string
	m.opts.Copy = func(s string) tea.Cmd { copied = append(copied, s); return nil }
	m.opts.Clipboard = "on"
	m.opts.OpenLink = func(string) tea.Cmd { t.Fatal("an artifact URL must not open"); return nil }
	m.Update(IncomingMsg{Message: announcement("mother", "#space", "paste-a.png", strings.Repeat("x", 2048))})
	out := view(m)
	if !strings.Contains(out, "[file paste-a.png · 2048 B]") || strings.Contains(out, "airc.local") {
		t.Fatalf("an artifact shows as a generic file chip:\n%s", out)
	}
	press(m, tea.KeyUp, tea.ModAlt)
	press(m, 'o', tea.ModCtrl)
	if !strings.Contains(m.notice, "no web link") {
		t.Fatalf("Ctrl+O does not open the announcement URL: %q", m.notice)
	}
	if cmd := m.runAction("artifact"); cmd != nil {
		cmd()
	}
	if len(copied) != 1 || copied[0] != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("the file id is copied: %v", copied)
	}
	press(m, 's', tea.ModCtrl)
	if !strings.Contains(view(m), "airc.local") {
		t.Fatal("source view keeps the original announcement")
	}
}

// An announcement that arrives while the upload is pending is evidence the
// later Uncertain outcome must not ignore (root phase-2 review 1).
func TestEarlyAnnouncementResolvesALaterUncertainOutcome(t *testing.T) {
	for _, lose := range []string{"uncertain", "disconnect"} {
		t.Run(lose, func(t *testing.T) {
			m, sends, h := imageModel(t)
			img := pasteImg(t, m, h, "png")
			paste(m, "caption")
			press(m, tea.KeyEnter, 0)
			m.Update(IncomingMsg{Message: announcement("mhuman", "#space", img.name, "png")})
			if img0(m) != img || img.state != "uploading" {
				t.Fatal("the result is still awaited")
			}
			if lose == "uncertain" {
				m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Uncertain})
			} else {
				m.Update(ConnectionMsg{})
				m.Update(ConnectionMsg{Connected: true})
			}
			// A disconnect then reports itself; the outcome notice is for Uncertain.
			if img0(m) != nil || m.imageUnresolved() != "" || lose == "uncertain" && !strings.Contains(m.notice, "published") {
				t.Fatalf("the seen announcement resolves the image: %q", m.notice)
			}
			if len(*sends) != 0 || m.Draft() != "caption" || len(h.uploads) != 1 {
				t.Fatal("the caption stays an unsent draft and nothing is retried")
			}
		})
	}
}

// The caption follows its image only with the reply intent it was sent with
// (root phase-2 review 2).
func TestChangedReplyKeepsTheCaptionUnsent(t *testing.T) {
	m, sends, h := imageModel(t)
	m.Update(IncomingMsg{Message: Message{ID: "r1", Account: "mother", Nick: "other", Text: "first"}})
	m.Update(IncomingMsg{Message: Message{ID: "r2", Account: "mother", Nick: "other", Text: "second"}})
	pasteImg(t, m, h, "png")
	paste(m, "caption")
	m.replyTo, m.replyRevision = "r1", m.replyRevision+1
	press(m, tea.KeyEnter, 0)
	m.replyTo, m.replyRevision = "r2", m.replyRevision+1
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed})
	if len(*sends) != 0 || m.Draft() != "caption" || m.replyTo != "r2" {
		t.Fatalf("a changed reply target keeps the caption unsent: %+v", *sends)
	}
}

// An uncertain outcome keeps the host's operation ID so its recorded outcome
// can be inspected; publication evidence never acknowledges it (QA phase-2).
func TestUncertainImageNamesItsOperation(t *testing.T) {
	m, _, h := imageModel(t)
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: "REQ7", State: Uncertain, Err: errors.New("image publication is uncertain; inspect request REQ7")})
	if !strings.HasPrefix(m.notice, "Inspect yoloctl space operation trial REQ7: ") {
		t.Fatalf("the operation to inspect leads the notice: %q", m.notice)
	}
	press(m, tea.KeyEnter, 0)
	if !strings.Contains(m.notice, "REQ7") {
		t.Fatal("a refused send repeats the operation to inspect")
	}
	m.Update(IncomingMsg{Message: announcement("mhuman", "#space", img0(m).name, "png")})
	if img0(m) != nil || !strings.HasPrefix(m.notice, "Inspect yoloctl space operation trial REQ7: the image's announcement was observed") ||
		!strings.Contains(m.notice, "acknowledge it only if") || strings.Contains(m.notice, "stays uncertain") {
		t.Fatalf("publication resolves the draft but leaves the review to the user: %q", m.notice)
	}
	if len(m.late) != 0 {
		t.Fatal("an outcome that already arrived is not awaited again")
	}
}

// Source view keeps an artifact announcement literal: its URL is never a
// click target (QA phase-2).
func TestArtifactSourceURLIsNotClickable(t *testing.T) {
	m, _, _ := imageModel(t)
	m.opts.OpenLink = func(string) tea.Cmd { t.Fatal("an artifact URL must not open"); return nil }
	m.Update(IncomingMsg{Message: announcement("mother", "#space", "paste-a.png", "png")})
	press(m, 's', tea.ModCtrl)
	y, line := lineWith(t, m, "airc.local")
	x := strings.Index(line, "http")
	x = len([]rune(line[:x])) + 3
	if act := actionAt(ansi.WcWidth, m.lastTranscript[m.scroll+y-m.layout.bodyTop].runs, x); act.Kind != actNone {
		t.Fatalf("the source URL is a %v target", act.Kind)
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.linkPrompt != nil {
		t.Fatal("clicking the source URL must not open or ask")
	}
}

// A host outcome that arrives after an announcement already resolved the
// draft still names its operation (root follow-up review).
func TestLateOutcomeAfterPublicationStillNamesItsOperation(t *testing.T) {
	m, sends, h := imageModel(t)
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(IncomingMsg{Message: announcement("mhuman", "#space", img0(m).name, "png")})
	m.Update(ConnectionMsg{})
	m.Update(ConnectionMsg{Connected: true})
	if img0(m) != nil || len(m.late) != 1 {
		t.Fatal("the announcement resolved the draft; the outcome is still due")
	}
	alt(m, '2') // the outcome arrives while another channel is shown
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: "REQ9", State: Uncertain})
	if !strings.Contains(m.notice, "#space: Inspect yoloctl space operation trial REQ9: the published image's upload outcome is uncertain") ||
		len(m.late) != 0 || len(m.views["#space"].editor.images()) != 0 || len(*sends) != 0 || m.name != "#build" {
		t.Fatalf("the late outcome names its operation and changes nothing else: %q", m.notice)
	}
	if !presenceSays(m.views["#space"], "REQ9") {
		t.Fatal("the guidance is kept in the image's channel")
	}
	alt(m, '1')
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: "REQ9", State: Uncertain})
	if len(m.late) != 0 {
		t.Fatal("a duplicate late outcome is ignored")
	}
	for i := range maxLateImages + 3 {
		sum := sha256.Sum256([]byte("png"))
		name := fmt.Sprintf("paste-%d.png", i)
		img := &draftImage{token: fmt.Sprint(i), name: name, data: []byte("png"), sha256: hex.EncodeToString(sum[:]), state: "uncertain", awaiting: true}
		if err := m.editor.insertImage(img); err != nil {
			t.Fatal(err)
		}
		m.published(announcement("mhuman", "#space", name, "png"))
	}
	if len(m.late) != maxLateImages || m.late[0].token != "3" {
		t.Fatalf("late outcomes are bounded, oldest dropped: %+v", m.late)
	}
}

func presenceSays(v *channelView, text string) bool {
	for _, ev := range v.presence {
		if strings.Contains(strings.Join(ev.names, " "), text) {
			return true
		}
	}
	return false
}

// Removing an image whose host outcome is still due clears the draft but
// keeps the attempt correlated, so its operation is still named (QA).
func TestRemovedImageStillReportsItsLateOutcome(t *testing.T) {
	m, sends, h := imageModel(t)
	img := pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(ConnectionMsg{})
	m.Update(ConnectionMsg{Connected: true})
	deleteImg(t, m, img)
	if img0(m) != nil || len(m.late) != 1 {
		t.Fatal("removal clears the draft and keeps the attempt")
	}
	press(m, 'z', tea.ModCtrl)
	if img0(m) != nil {
		t.Fatal("undo cannot bring back a removed uncertain upload")
	}
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: "REQ5", State: Uncertain})
	if !strings.Contains(m.notice, "Inspect yoloctl space operation trial REQ5: the removed image's upload outcome is uncertain") ||
		img0(m) != nil || len(m.late) != 0 || len(*sends) != 0 || !presenceSays(m.views["#space"], "REQ5") {
		t.Fatalf("the removed image's outcome is reported without recreating it: %q", m.notice)
	}

	// An image removed after its Uncertain outcome named an operation keeps
	// that operation in the channel's transcript.
	img = pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[1].Token, RequestID: "REQ6", State: Uncertain})
	deleteImg(t, m, img)
	if img0(m) != nil || len(m.late) != 0 || !presenceSays(m.views["#space"], "REQ6") {
		t.Fatal("removing an uncertain image records the operation to inspect")
	}
}

// Guidance about an uncertain upload reaches the user from a hidden channel,
// whichever of the outcome and the announcement comes first (QA).
func TestHiddenChannelImageGuidanceIsNotDropped(t *testing.T) {
	m, sends, h := imageModel(t)
	name := pasteImg(t, m, h, "png").name
	press(m, tea.KeyEnter, 0)
	alt(m, '2')
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: "REQ3", State: Uncertain})
	if !strings.Contains(m.notice, "#space: Inspect yoloctl space operation trial REQ3: the image upload is uncertain") || m.name != "#build" {
		t.Fatalf("a hidden channel's uncertain upload is shown, naming it: %q", m.notice)
	}
	m.Update(IncomingMsg{Message: announcement("mhuman", "#space", name, "png")})
	if len(m.views["#space"].editor.images()) != 0 || !strings.Contains(m.notice, "#space: Inspect yoloctl space operation trial REQ3: the image's announcement was observed") {
		t.Fatalf("publication in a hidden channel keeps the operation to inspect: %q", m.notice)
	}
	if !presenceSays(m.views["#space"], "REQ3") || len(*sends) != 0 || m.name != "#build" {
		t.Fatal("the operation stays on record in its channel; nothing is sent or switched")
	}
	m.say(levelInfo, "")
	m.Update(MembersMsg{Channel: "#space", Members: []Member{{Nick: "x"}}})
	if m.notice != "" {
		t.Fatal("ordinary hidden-channel notices stay quiet")
	}
}

// A controller-length operation ID and its command stay readable on screen at
// 80 columns with the participants panel: the guidance note wraps in the
// transcript and the command leads the one-row footer (QA).
func TestOperationGuidanceIsReadableAtNarrowWidth(t *testing.T) {
	m, _, h := imageModel(t)
	m.Update(MembersMsg{Channel: "#space", Members: []Member{{Nick: "human", Account: "mhuman"}}})
	id := "A7QK3ZP2M5XWJ4R6TB8NC9DHFE" // rand.Text length, as the host's request IDs
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(IncomingMsg{Message: announcement("mhuman", "#space", img0(m).name, "png")})
	m.Update(ConnectionMsg{})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: id, State: Uncertain})
	if m.width != 80 || panelWidth(m.width) == 0 || !m.rosterOpen {
		t.Fatal("the check runs at 80 columns with the side panel")
	}
	lines := strings.Split(view(m), "\n")
	command := "yoloctl space operation trial " + id
	if footer := lines[len(lines)-1]; !strings.Contains(footer, command) {
		t.Fatalf("the footer shows the whole command: %q", footer)
	}
	m.say(levelInfo, "") // the footer moves on; the transcript keeps the note
	var note []string
	for _, line := range strings.Split(view(m), "\n") {
		if text, _, ok := strings.Cut(line, "│"); ok && strings.TrimSpace(text) != "" {
			note = append(note, strings.TrimSpace(text))
		}
	}
	joined := strings.Join(note, " ")
	if !strings.Contains(joined, command) || !strings.Contains(joined, "acknowledge it only if the inspected outcome requires review") {
		t.Fatalf("the transcript note wraps to show the whole command and guidance:\n%s", view(m))
	}
	found := false
	for _, line := range strings.Split(view(m), "\n") {
		text, _, _ := strings.Cut(line, "│")
		found = found || strings.Contains(line, id)
		if strings.Contains(text, "outcome") && !strings.HasSuffix(text, " ") {
			t.Fatalf("a wrapped note keeps a column before the panel divider: %q", line)
		}
	}
	if !found {
		t.Fatal("the ID itself is never split across rows")
	}
}

package chatui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

const (
	stagedA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	stagedB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	stagedC = "16fd2706-8baf-433b-82eb-8c7fada847da"
)

// stagedModel is an image model whose connection sends text and images as
// one message.
func stagedModel(t *testing.T) (*Model, *[]Outgoing, *imageHost) {
	t.Helper()
	m, sends, h := imageModel(t)
	m.Update(ConnectionMsg{Connected: true, Attachments: true})
	return m, sends, h
}

// stagedOK confirms the outstanding stage as artifact id.
func stagedOK(m *Model, h *imageHost, id string) {
	u := h.uploads[len(h.uploads)-1]
	sum := sha256.Sum256(u.Data)
	m.Update(ImageUploadedMsg{Channel: u.Channel, Token: u.Token, State: Confirmed, Staged: true, ArtifactID: id, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(u.Data))})
}

func TestStagedDraftSendsTextAndImagesAsOneMessage(t *testing.T) {
	m, sends, h := stagedModel(t)
	paste(m, "look ")
	pasteImg(t, m, h, "png1")
	paste(m, " and\n")
	pasteImg(t, m, h, "png2")
	paste(m, "\n  done  ")
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 1 || !h.uploads[0].Stage || string(h.uploads[0].Data) != "png1" || len(*sends) != 0 {
		t.Fatalf("Enter stages the first image and sends nothing yet: %+v %v", h.uploads, *sends)
	}
	stagedOK(m, h, stagedA)
	if len(h.uploads) != 2 || !h.uploads[1].Stage || string(h.uploads[1].Data) != "png2" || len(*sends) != 0 {
		t.Fatalf("the next image is staged in draft order: %+v", h.uploads)
	}
	stagedOK(m, h, stagedB)
	if len(*sends) != 1 {
		t.Fatalf("one message follows the last stage: %v", *sends)
	}
	out := (*sends)[0]
	want := "look [Image #1] and\n[Image #2]\n  done"
	if out.Text != want || len(out.Attach) != 2 || out.Attach[0] != (AttachRef{ID: stagedA, At: 5}) || out.Attach[1] != (AttachRef{ID: stagedB, At: strings.Index(want, "[Image #2]")}) {
		t.Fatalf("the draft is one message with ordered markers: %q %+v", out.Text, out.Attach)
	}
	if !strings.Contains(view(m), "look [file paste-x-") || !strings.Contains(view(m), "◌ sending") {
		t.Fatalf("the pending message shows its images inline:\n%s", view(m))
	}
	m.Update(DeliveryMsg{ID: out.ID, Channel: "#space", MessageID: "m1", State: Confirmed})
	if m.Draft() != "" || m.inflight != "" || len(m.editor.held()) != 0 {
		t.Fatalf("the confirmed message clears the draft: %q", m.Draft())
	}
	press(m, 'z', tea.ModCtrl)
	if len(m.editor.images()) != 0 {
		t.Fatal("undo never brings back a published image")
	}
	if n := strings.Count(view(m), "human"); n != 1 {
		t.Fatalf("one transcript entry with one header, got %d:\n%s", n, view(m))
	}
}

func TestStagedMarkersAreRenumberedInDraftOrder(t *testing.T) {
	m, sends, h := stagedModel(t)
	first := pasteImg(t, m, h, "one")
	pasteImg(t, m, h, "two")
	paste(m, " x ")
	pasteImg(t, m, h, "three")
	deleteImg(t, m, first)
	if m.Draft() != "[Image #2] x [Image #3]" {
		t.Fatalf("labels keep their paste numbers in the draft: %q", m.Draft())
	}
	press(m, tea.KeyEnter, 0)
	stagedOK(m, h, stagedA)
	stagedOK(m, h, stagedB)
	out := (*sends)[0]
	if out.Text != "[Image #1] x [Image #2]" || out.Attach[0].At != 0 || out.Attach[1].At != 13 {
		t.Fatalf("wire markers count 1..N in draft order: %q %+v", out.Text, out.Attach)
	}
}

func TestEditingWhileStagingWaitsForEnter(t *testing.T) {
	m, sends, h := stagedModel(t)
	paste(m, "caption ")
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	paste(m, " more")
	stagedOK(m, h, stagedA)
	if len(*sends) != 0 || !strings.Contains(m.notice, "ready; Enter sends") {
		t.Fatalf("a changed draft is not sent automatically: %q %v", m.notice, *sends)
	}
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 1 || len(*sends) != 1 || (*sends)[0].Text != "caption [Image #1] more" {
		t.Fatalf("Enter sends the current draft with the staged image, not staging again: %v", *sends)
	}
}

func TestRefusedOrMismatchedStagingAsksForAFreshPaste(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(*Model, *imageHost)
	}{
		{"refused", func(m *Model, h *imageHost) {
			m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Rejected, Err: errors.New("quota")})
		}},
		{"mismatched", func(m *Model, h *imageHost) {
			m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed, Staged: true, ArtifactID: stagedA, SHA256: strings.Repeat("a", 64), Bytes: 3})
		}},
		{"announced", func(m *Model, h *imageHost) {
			sum := sha256.Sum256([]byte("png"))
			m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, State: Confirmed, ArtifactID: stagedA, MsgID: stagedB, SHA256: hex.EncodeToString(sum[:]), Bytes: 3})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, sends, h := stagedModel(t)
			paste(m, "keep this ")
			img := pasteImg(t, m, h, "png")
			press(m, tea.KeyEnter, 0)
			tc.answer(m, h)
			if img.state != "expired" || img.staged != "" || m.Draft() != "keep this [Image #1]" {
				t.Fatalf("the placeholder and the rest of the draft stay: %q %q", img.state, m.Draft())
			}
			if !strings.Contains(m.notice, "Paste the image again with Ctrl+V") {
				t.Fatalf("the fresh-paste requirement is explicit: %q", m.notice)
			}
			press(m, tea.KeyEnter, 0)
			if len(h.uploads) != 1 || len(*sends) != 0 || !strings.Contains(m.notice, "pasted again") {
				t.Fatalf("Enter waits for a fresh copy: %q", m.notice)
			}
			deleteImg(t, m, img)
			pasteImg(t, m, h, "png")
			press(m, tea.KeyEnter, 0)
			if len(h.uploads) != 2 || !h.uploads[1].Stage {
				t.Fatal("a fresh paste is staged with its own token")
			}
		})
	}
}

func TestUncertainStagingBlocksWithStagingGuidance(t *testing.T) {
	m, sends, h := stagedModel(t)
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	m.Update(ImageUploadedMsg{Channel: "#space", Token: h.uploads[0].Token, RequestID: "REQ1", State: Uncertain})
	if !strings.Contains(m.notice, "REQ1") || !strings.Contains(m.notice, "staging is uncertain") || !strings.Contains(m.notice, "never announces") {
		t.Fatalf("staging uncertainty names its operation, not an announcement: %q", m.notice)
	}
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 1 || len(*sends) != 0 {
		t.Fatal("nothing is retried or sent while staging is uncertain")
	}
}

// sendStaged runs a two-image draft to its one outgoing message.
func sendStaged(t *testing.T) (*Model, *[]Outgoing, []*draftImage) {
	t.Helper()
	m, sends, h := stagedModel(t)
	paste(m, "a ")
	one := pasteImg(t, m, h, "one")
	paste(m, " b ")
	two := pasteImg(t, m, h, "two")
	press(m, tea.KeyEnter, 0)
	stagedOK(m, h, stagedA)
	stagedOK(m, h, stagedB)
	if len(*sends) != 1 {
		t.Fatal("the draft was not sent")
	}
	return m, sends, []*draftImage{one, two}
}

// sentAs is the room's message id carrying exactly the draft sendStaged sends.
func sentAs(id string) Message {
	return Message{ID: id, Channel: "#space", Account: "mhuman", Nick: "human", Text: "a [Image #1] b [Image #2]",
		Attachments: []AttachmentRef{{ArtifactRef: ArtifactRef{ID: stagedA, Name: "a.png", Bytes: 3}, At: 2}, {ArtifactRef: ArtifactRef{ID: stagedB, Name: "b.png", Bytes: 3}, At: 15}}}
}

func refuse(m *Model, out Outgoing, r AttachmentRefusal) {
	m.Update(DeliveryMsg{ID: out.ID, Channel: "#space", State: Rejected, Err: errors.New("FAIL PRIVMSG ATTACHMENTS #space attachments: " + r.Reason), Refusal: &r})
}

func TestFinalSendRefusalsKeepTheDraftAndFrozenImages(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "unknown", Index: 2})
		if imgs[0].staged != stagedA || imgs[1].state != "expired" || imgs[1].staged != "" || m.Draft() != "a [Image #1] b [Image #2]" {
			t.Fatalf("only the named image expires; the draft stays: %+v %q", imgs[1], m.Draft())
		}
		if !strings.Contains(m.notice, "expired before the message was sent") || !strings.Contains(m.notice, "Ctrl+V") {
			t.Fatalf("the refusal asks for a fresh paste: %q", m.notice)
		}
	})
	t.Run("unknown after an uncertain send", func(t *testing.T) {
		// chatstream cannot deliver a later rejection for a send it reported
		// uncertain (only a late matching echo can still confirm it); this
		// order is synthetic defence in depth, not a live route.
		m, sends, imgs := sendStaged(t)
		m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", State: Uncertain})
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "unknown", Index: 1})
		if !imgs[0].frozen || imgs[0].staged != stagedA || imgs[0].state == "expired" {
			t.Fatalf("a frozen ID is never cleared: %+v", imgs[0])
		}
		if !strings.Contains(m.notice, "after an uncertain send") {
			t.Fatalf("history comes first: %q", m.notice)
		}
	})
	t.Run("consumed and shown", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		m.Update(IncomingMsg{Message: sentAs("m7")})
		m.suggest = &suggestState{word: "@so", items: []suggestion{{insert: "@sol ", label: "@sol"}}}
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "consumed", Index: 1, MessageID: "m7"})
		if m.Draft() != "" || imgs[0].state != "sent" || imgs[1].state != "sent" {
			t.Fatalf("the shown message proves the draft was sent: %q", m.Draft())
		}
		if m.suggest != nil {
			t.Fatalf("clearing the draft must also close its suggestions, as for Confirmed: %+v", m.suggest)
		}
	})
	t.Run("consumed by a different message", func(t *testing.T) {
		for name, change := range map[string]func(*Message){
			"text":    func(msg *Message) { msg.Text = "a [Image #1] c [Image #2]" },
			"reply":   func(msg *Message) { msg.ReplyTo = "r9" },
			"offset":  func(msg *Message) { msg.Attachments[1].At = 16 },
			"account": func(msg *Message) { msg.Account = "mother" },
		} {
			m, sends, imgs := sendStaged(t)
			shown := sentAs("m7")
			change(&shown)
			m.Update(IncomingMsg{Message: shown})
			refuse(m, (*sends)[0], AttachmentRefusal{Reason: "consumed", Index: 1, MessageID: "m7"})
			if m.Draft() != "a [Image #1] b [Image #2]" || !imgs[0].frozen || imgs[0].staged != stagedA || imgs[1].staged != stagedB {
				t.Errorf("%s: the same images alone never prove this draft was sent: %q %+v", name, m.Draft(), imgs[0])
			}
			if !strings.Contains(m.notice, "may already have been sent") {
				t.Errorf("%s: the user inspects history: %q", name, m.notice)
			}
		}
	})
	t.Run("consumed after a reply change in flight", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		m.Update(IncomingMsg{Message: sentAs("m7")})
		m.replyTo, m.replyRevision = "r2", m.replyRevision+1
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "consumed", Index: 1, MessageID: "m7"})
		if m.replyTo != "r2" || m.Draft() == "" || imgs[0].state != "sent" || len(m.editor.images()) != 0 {
			t.Fatalf("the edited reply and text stay without the published images: %q %q", m.replyTo, m.Draft())
		}
	})
	t.Run("no second send after an uncertain one", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", State: Uncertain})
		paste(m, " edited")
		press(m, tea.KeyEnter, 0)
		if len(*sends) != 1 || !imgs[0].frozen || !strings.Contains(m.notice, "unresolved") {
			t.Fatalf("Enter waits for the uncertain send's outcome: %d %q", len(*sends), m.notice)
		}
	})
	t.Run("consumed elsewhere", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "consumed", Index: 1, MessageID: "m8"})
		if m.Draft() == "" || !imgs[0].frozen || imgs[0].staged != stagedA || imgs[1].staged != stagedB {
			t.Fatalf("an unseen message clears nothing and freezes the IDs: %q %+v", m.Draft(), imgs[0])
		}
		if !strings.Contains(m.notice, "may already have been sent") {
			t.Fatalf("the user inspects history: %q", m.notice)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "unavailable"})
		if m.Draft() == "" || imgs[0].staged != stagedA || imgs[0].state == "expired" || !strings.Contains(m.notice, "cannot be sent") {
			t.Fatalf("unavailable is no expiry: %q %+v", m.notice, imgs[0])
		}
	})
	t.Run("store", func(t *testing.T) {
		m, sends, imgs := sendStaged(t)
		out := (*sends)[0]
		refuse(m, out, AttachmentRefusal{Reason: "store"})
		if m.Draft() != "a [Image #1] b [Image #2]" || imgs[0].staged != stagedA || imgs[1].staged != stagedB || imgs[0].frozen || imgs[1].state == "expired" {
			t.Fatalf("a storage refusal keeps the draft and its staged images: %q %+v %+v", m.Draft(), imgs[0], imgs[1])
		}
		if !strings.Contains(m.notice, "refused to store") || !strings.Contains(m.notice, "Enter tries again") || strings.Contains(m.notice, "Ctrl+V") || strings.Contains(m.notice, "nothing was sent") {
			t.Fatalf("the refusal is explained with an explicit retry: %q", m.notice)
		}
		local := m.messages[len(m.messages)-1]
		if local.ClientID != out.ID || local.Delivery != Rejected || !strings.Contains(local.Error, "attachments: store") {
			t.Fatalf("the refused message keeps the server's reason: %+v", local)
		}
		if len(*sends) != 1 {
			t.Fatal("nothing is retried without Enter")
		}
		press(m, tea.KeyEnter, 0)
		if len(*sends) != 2 || (*sends)[1].Text != out.Text || len((*sends)[1].Attach) != 2 || (*sends)[1].Attach[0] != out.Attach[0] || (*sends)[1].Attach[1] != out.Attach[1] {
			t.Fatalf("Enter sends the same draft with the same staged images, without staging again: %+v", *sends)
		}
		// A reference that expired meanwhile keeps its existing handling.
		refuse(m, (*sends)[1], AttachmentRefusal{Reason: "unknown", Index: 1})
		if imgs[0].state != "expired" || imgs[1].staged != stagedB || !strings.Contains(m.notice, "expired before the message was sent") {
			t.Fatalf("an expired stage asks for a fresh paste: %q %+v", m.notice, imgs[0])
		}
	})
	t.Run("store after an uncertain send", func(t *testing.T) {
		// As above, a rejection after an uncertain report is synthetic.
		m, sends, imgs := sendStaged(t)
		m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", State: Uncertain})
		refuse(m, (*sends)[0], AttachmentRefusal{Reason: "store"})
		if !imgs[0].frozen || !imgs[1].frozen || imgs[0].staged != stagedA || m.Draft() == "" {
			t.Fatalf("frozen images stay frozen and staged: %+v", imgs[0])
		}
		if !strings.Contains(m.notice, "still uncertain") || strings.Contains(m.notice, "Enter tries again") {
			t.Fatalf("history comes before any retry: %q", m.notice)
		}
	})
	for reason, want := range map[string]string{
		"too-long":     "Send fewer images",
		"invalid":      "places its images",
		"channel-only": "only be sent to a channel",
	} {
		t.Run(reason, func(t *testing.T) {
			m, sends, imgs := sendStaged(t)
			refuse(m, (*sends)[0], AttachmentRefusal{Reason: reason})
			if m.Draft() != "a [Image #1] b [Image #2]" || imgs[0].staged != stagedA || imgs[1].staged != stagedB || imgs[1].state == "expired" {
				t.Fatalf("the draft and its staged images stay: %q %+v", m.Draft(), imgs[1])
			}
			if !strings.Contains(m.notice, want) || !strings.Contains(m.notice, "draft is kept") {
				t.Fatalf("the refusal has its own explanation: %q", m.notice)
			}
		})
	}
}

func TestPublishedAttachmentsRenderInlineAndOnlyAtTheirSpans(t *testing.T) {
	m, _, _ := imageModel(t)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	var copied []string
	m.opts.Copy = func(s string) tea.Cmd { copied = append(copied, s); return nil }
	m.opts.Clipboard = "on"
	text := "see [Image #1] then [Image #2], and a typed [Image #3]"
	m.Update(IncomingMsg{Message: Message{ID: "p1", Channel: "#space", Account: "mother", Nick: "other", Text: text, Attachments: []AttachmentRef{
		{ArtifactRef: ArtifactRef{ID: stagedA, Name: "paste-a.png", Bytes: 2048}, At: 4},
		{ArtifactRef: ArtifactRef{ID: stagedC, Name: "paste-c.png", Bytes: 10}, At: 20},
	}}})
	out := view(m)
	if !strings.Contains(out, "see [file paste-a.png · 2048 B] then [file paste-c.png · 10 B], and a typed [Image #3]") {
		t.Fatalf("images sit inline at their markers; a typed look-alike stays text:\n%s", out)
	}
	press(m, tea.KeyUp, tea.ModAlt)
	if cmd := m.runAction("artifact"); cmd != nil {
		cmd()
	}
	if len(copied) != 1 || copied[0] != stagedA+"\n"+stagedC {
		t.Fatalf("every file id is copied, one per line: %v", copied)
	}
	press(m, 's', tea.ModCtrl)
	if !strings.Contains(view(m), text) {
		t.Fatal("source view keeps the original text")
	}
	press(m, 's', tea.ModCtrl)
	m.Update(IncomingMsg{Message: Message{ID: "p2", Channel: "#space", Account: "mother", Nick: "other", Text: "moved [Image #1]",
		Attachments: []AttachmentRef{{ArtifactRef: ArtifactRef{ID: stagedA, Name: "x.png", Bytes: 1}, At: 0}}}})
	if !strings.Contains(view(m), "moved [Image #1]") {
		t.Fatal("a span that does not hold its marker renders the original text")
	}
}

func TestWithoutTheFeatureImagesStillSendAsParts(t *testing.T) {
	m, sends, h := imageModel(t)
	paste(m, "caption ")
	pasteImg(t, m, h, "png")
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Text != "caption" || (*sends)[0].Attach != nil || len(h.uploads) != 0 {
		t.Fatalf("legacy servers keep ordered parts: %v %+v", *sends, h.uploads)
	}
}

func TestIdenticalPastesStageSeparatelyAndRemovedImagesNeverAttach(t *testing.T) {
	m, sends, h := stagedModel(t)
	first := pasteImg(t, m, h, "same")
	pasteImg(t, m, h, "same")
	press(m, tea.KeyEnter, 0)
	paste(m, " edit") // the draft changes while the first image is staged
	stagedOK(m, h, stagedA)
	if first.staged != stagedA || len(*sends) != 0 {
		t.Fatal("the stage binds to its own image; nothing is sent after an edit")
	}
	deleteImg(t, m, first)
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 2 || h.uploads[1].Token == h.uploads[0].Token {
		t.Fatal("an identical paste is staged on its own, never reusing another image's stage")
	}
	stagedOK(m, h, stagedB)
	out := (*sends)[0]
	if len(out.Attach) != 1 || out.Attach[0].ID != stagedB || out.Text != "[Image #1] edit" {
		t.Fatalf("a removed image's stage is never attached: %q %+v", out.Text, out.Attach)
	}
}

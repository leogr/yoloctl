package chatui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A send the stream refused before writing it arrives as Rejected without an
// AIRC refusal: it is definitely not in the room. The draft, its reply and the
// staged images stay usable, nothing is staged or sent on its own, and an
// explicit Enter sends the same staged IDs again without restaging.
func TestPreWriteRejectionKeepsTheStagedDraftForAnExplicitResend(t *testing.T) {
	m, sends, h := stagedModel(t)
	m.replyTo, m.replyRevision = "parent", m.replyRevision+1
	paste(m, "a ")
	one := pasteImg(t, m, h, "one")
	paste(m, " b ")
	two := pasteImg(t, m, h, "two")
	press(m, tea.KeyEnter, 0)
	stagedOK(m, h, stagedA)
	stagedOK(m, h, stagedB)
	if len(*sends) != 1 {
		t.Fatalf("the staged draft was not sent: %v", *sends)
	}
	first, draft := (*sends)[0], m.Draft()
	m.Update(DeliveryMsg{ID: first.ID, Channel: "#space", State: Rejected, Err: errors.New("message was not sent: this view refused the request before writing it (invalid or unsupported managed chat stream request)")})
	if m.Draft() != draft || m.replyTo != "parent" {
		t.Fatalf("the draft and its reply stay: %q %q", m.Draft(), m.replyTo)
	}
	for i, img := range []*draftImage{one, two} {
		if img.staged != []string{stagedA, stagedB}[i] || img.frozen || img.state != "" || string(img.data) != []string{"one", "two"}[i] {
			t.Fatalf("image %d keeps its staged ID and bytes, unfrozen: %+v", i+1, img)
		}
	}
	if m.inflight != "" || m.sentImages != nil || !m.connected || !strings.Contains(m.notice, "message was not sent") {
		t.Fatalf("the send resolves on a usable connection: inflight=%q connected=%v notice=%q", m.inflight, m.connected, m.notice)
	}
	if len(h.uploads) != 2 || len(*sends) != 1 {
		t.Fatalf("nothing is staged or sent on its own: uploads=%d sends=%d", len(h.uploads), len(*sends))
	}
	press(m, tea.KeyEnter, 0)
	if len(h.uploads) != 2 || len(*sends) != 2 {
		t.Fatalf("Enter sends once more without restaging: uploads=%d sends=%d", len(h.uploads), len(*sends))
	}
	again := (*sends)[1]
	if again.ID == first.ID || again.Text != first.Text || again.ReplyTo != "parent" || !slices.Equal(again.Attach, first.Attach) {
		t.Fatalf("the explicit resend carries the same draft and staged IDs under a new ID: %+v vs %+v", again, first)
	}
	m.Update(DeliveryMsg{ID: again.ID, Channel: "#space", MessageID: "m1", State: Confirmed})
	if m.Draft() != "" || one.state != "sent" || two.state != "sent" {
		t.Fatalf("its confirmation publishes the images and clears the draft: %q", m.Draft())
	}
}

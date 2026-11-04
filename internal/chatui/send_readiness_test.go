package chatui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestInitialOfflineStateRendersTruthfulAffordances verifies that a freshly created
// chat model before receiving a connection message shows offline hints rather than
// false "Message #channel" or "Enter send" affordances, that pressing Enter
// preserves any typed draft without sending and sets a disconnected notice,
// and that connecting alone emits no send until an explicit Enter is pressed.
func TestInitialOfflineStateRendersTruthfulAffordances(t *testing.T) {
	var sends []Outgoing
	m := New(Options{Space: "trial", Channel: "#space", Nick: "human", Send: func(out Outgoing) tea.Cmd {
		sends = append(sends, out)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// 1. Initial empty draft when offline: truthful placeholder and footer
	out := view(m)
	if !strings.Contains(out, "Offline · drafts are preserved") {
		t.Fatalf("empty placeholder must indicate offline state:\n%s", out)
	}
	if strings.Contains(out, "Message #space") {
		t.Fatalf("empty placeholder must not invite sending when offline:\n%s", out)
	}
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send when offline:\n%s", out)
	}
	if !strings.Contains(out, "offline draft kept") {
		t.Fatalf("footer must truthfully indicate offline draft kept:\n%s", out)
	}

	// 2. Typing while offline preserves draft; footer remains truthful
	paste(m, "work in progress")
	out = view(m)
	if m.Draft() != "work in progress" {
		t.Fatalf("draft must be preserved: %q", m.Draft())
	}
	if !strings.Contains(out, "work in progress") {
		t.Fatalf("draft must render in composer:\n%s", out)
	}
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer with nonempty draft must not advertise Enter send when offline:\n%s", out)
	}
	if !strings.Contains(out, "offline draft kept") {
		t.Fatalf("footer must keep offline draft kept hint:\n%s", out)
	}

	// 3. Pressing Enter while offline rejects send, keeps draft, sets notice
	press(m, tea.KeyEnter, 0)
	if len(sends) != 0 {
		t.Fatalf("send must not be emitted when offline: %v", sends)
	}
	if m.Draft() != "work in progress" {
		t.Fatalf("draft must remain intact after offline Enter: %q", m.Draft())
	}
	if !strings.Contains(m.notice, "Chat is disconnected; draft retained") {
		t.Fatalf("expected disconnected warning notice, got: %q", m.notice)
	}
	// Notice takes footer precedence
	out = view(m)
	if !strings.Contains(out, "Chat is disconnected; draft retained") {
		t.Fatalf("footer must show notice after offline Enter:\n%s", out)
	}

	// 4. Connecting alone emits no send; draft remains intact
	m.Update(ConnectionMsg{Connected: true})
	if len(sends) != 0 {
		t.Fatalf("connecting alone must emit no send: %v", sends)
	}
	if m.Draft() != "work in progress" {
		t.Fatalf("draft must remain intact after connecting: %q", m.Draft())
	}

	// 5. Notice still takes precedence until cleared
	out = view(m)
	if !strings.Contains(out, "Chat is disconnected; draft retained") {
		t.Fatalf("notice must persist until cleared:\n%s", out)
	}

	// Clearing notice exposes the default Enter send hint once connected and joined
	m.notice = ""
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("footer must advertise Enter send once connected, joined, and notice cleared:\n%s", out)
	}

	// 6. Explicit Enter now sends
	press(m, tea.KeyEnter, 0)
	if len(sends) != 1 || sends[0].Text != "work in progress" {
		t.Fatalf("explicit Enter after connect must send once: %v", sends)
	}
}

// TestReconnectingStateRendersTruthfulAffordances verifies that explicit reconnect
// shows connecting hints and preserves drafts, while slash commands execute.
func TestReconnectingStateRendersTruthfulAffordances(t *testing.T) {
	m, sends := readyModel()
	if err := m.SetDraft("reconnect draft"); err != nil {
		t.Fatalf("SetDraft failed: %v", err)
	}

	// In an explicit reconnect, reconnecting is set and connection is down
	m.connected = false
	m.reconnecting = true
	m.notice = "" // clear any prior notice to observe default footer hints

	out := view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send while reconnecting:\n%s", out)
	}
	if !strings.Contains(out, "connecting… draft kept") {
		t.Fatalf("footer must indicate connecting… draft kept:\n%s", out)
	}

	// Empty draft placeholder during reconnecting
	if err := m.SetDraft(""); err != nil {
		t.Fatalf("SetDraft failed: %v", err)
	}
	out = view(m)
	if !strings.Contains(out, "Connecting to #space… drafts are preserved") {
		t.Fatalf("placeholder must indicate connecting state:\n%s", out)
	}

	// Submitting slash commands like /help still works during reconnect
	paste(m, "/help")
	press(m, tea.KeyEnter, 0)
	if m.overlay == nil || m.overlay.kind != overlayHelp {
		t.Fatal("/help should still open overlay while reconnecting")
	}
	press(m, tea.KeyEsc, 0)
	if m.overlay != nil {
		t.Fatal("Esc should close overlay")
	}

	if len(*sends) != 0 {
		t.Fatalf("no sends should have been dispatched: %v", *sends)
	}
}

// TestChannelMembershipReadinessAffordances verifies non-sendable membership states
// (joining, not joined, deleted) present accurate guidance, do not auto-send on join,
// and distinguish nonempty drafts from empty placeholders.
func TestChannelMembershipReadinessAffordances(t *testing.T) {
	m, sends := readyModel()

	// 1. Deleted channel with empty draft
	m.deleted = true
	out := view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send for deleted view:\n%s", out)
	}
	if !strings.Contains(out, "deleted read-only") {
		t.Fatalf("footer must indicate deleted read-only:\n%s", out)
	}
	if !strings.Contains(out, "#space was deleted") {
		t.Fatalf("placeholder must indicate deleted status:\n%s", out)
	}
	m.deleted = false

	// 2. Joining channel with empty draft
	m.membership = "joining"
	out = view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send while joining:\n%s", out)
	}
	if !strings.Contains(out, "joining… draft kept") {
		t.Fatalf("footer must indicate joining… draft kept:\n%s", out)
	}
	if !strings.Contains(out, "Joining #space… drafts are preserved") {
		t.Fatalf("placeholder must indicate joining status:\n%s", out)
	}

	// 3. Membership transition to joined with held draft: no auto-send
	paste(m, "held draft")
	m.Update(MembershipMsg{Channel: "#space", State: "joined"})
	if len(*sends) != 0 {
		t.Fatalf("transition to joined must not auto-send draft: %v", *sends)
	}
	if m.Draft() != "held draft" {
		t.Fatalf("draft must remain intact after joining transition: %q", m.Draft())
	}
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("footer must show Enter send once joined:\n%s", out)
	}
	if !strings.Contains(out, "held draft") {
		t.Fatalf("nonempty draft must render in composer:\n%s", out)
	}

	// 4. Non-default unjoined channel with nonempty draft: draft renders, footer shows /join
	m.name = "#other"
	m.membership = ""
	out = view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send when not in channel:\n%s", out)
	}
	if !strings.Contains(out, "/join #other to send") {
		t.Fatalf("footer must show actionable /join hint:\n%s", out)
	}
	if !strings.Contains(out, "held draft") {
		t.Fatalf("nonempty draft must render instead of placeholder in unjoined channel:\n%s", out)
	}

	// 5. Separate empty draft in unjoined channel asserts placeholder
	if err := m.SetDraft(""); err != nil {
		t.Fatalf("SetDraft failed: %v", err)
	}
	out = view(m)
	if !strings.Contains(out, "You are not in #other") {
		t.Fatalf("empty placeholder must show not-in-channel hint:\n%s", out)
	}
	if !strings.Contains(out, "/join #other to send") {
		t.Fatalf("empty draft footer must still show /join hint:\n%s", out)
	}
}

// TestUnresolvedWorkAndUncertaintyBlocksSendAffordance verifies that inflight sends,
// uncertain deliveries, pending channel actions, reading images, and uncertain image
// states truthfully block send affordances.
func TestUnresolvedWorkAndUncertaintyBlocksSendAffordance(t *testing.T) {
	m, _, h := imageModel(t)

	// 1. Reading image from clipboard
	paste(m, "text ")
	press(m, 'v', tea.ModCtrl)
	// Ctrl+V sets "Reading an image from the clipboard…", which takes footer precedence
	out := view(m)
	if !strings.Contains(out, "Reading an image from the clipboard…") {
		t.Fatalf("reading notice must take footer precedence:\n%s", out)
	}
	// Clearing notice exposes the default image needs attention hint
	m.notice = ""
	out = view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send while image is reading:\n%s", out)
	}
	if !strings.Contains(out, "image needs attention") {
		t.Fatalf("footer must indicate image needs attention:\n%s", out)
	}

	// Complete reading
	m.Update(prepared(h.reads[0], "payload"))
	m.notice = "" // clear attachment notice to inspect default hints
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("footer must restore Enter send after image is ready:\n%s", out)
	}

	// 2. Uncertain image state
	imgs := m.editor.images()
	if len(imgs) == 0 {
		t.Fatal("image must exist after being prepared")
	}
	imgs[0].state = "uncertain"
	out = view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send when image is uncertain:\n%s", out)
	}
	if !strings.Contains(out, "image needs attention") {
		t.Fatalf("footer must indicate image needs attention on uncertain image:\n%s", out)
	}
	// Restore real ready state (empty string)
	imgs[0].state = ""
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("footer must restore Enter send when image ready state restored:\n%s", out)
	}

	// 3. Inflight send, real uncertain delivery, and refusal to resend (separate plain readyModel)
	m2, sends2 := readyModel()
	paste(m2, "pending message")
	press(m2, tea.KeyEnter, 0)
	if len(*sends2) != 1 {
		t.Fatalf("first Enter must dispatch one send: %v", *sends2)
	}
	id := (*sends2)[0].ID

	// While inflight: footer shows unresolved draft kept
	out = view(m2)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send during inflight send:\n%s", out)
	}
	if !strings.Contains(out, "unresolved draft kept") {
		t.Fatalf("footer must indicate unresolved draft kept during inflight:\n%s", out)
	}

	// Update delivery state to Uncertain; inflight is retained by model until resolved
	m2.Update(DeliveryMsg{ID: id, MessageID: "srv-1", State: Uncertain, Err: errors.New("delivery uncertain")})
	if m2.inflight != id {
		t.Fatalf("inflight must retain id: %q != %q", m2.inflight, id)
	}
	// Uncertain delivery sets notice; clearing notice exposes default footer
	m2.notice = ""
	out = view(m2)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send while delivery is uncertain:\n%s", out)
	}
	if !strings.Contains(out, "unresolved draft kept") {
		t.Fatalf("footer must keep unresolved draft kept hint while delivery uncertain:\n%s", out)
	}

	// Second Enter while uncertain must not dispatch another send; draft stays intact
	press(m2, tea.KeyEnter, 0)
	if len(*sends2) != 1 {
		t.Fatalf("second Enter while uncertain must not retry send: %v", *sends2)
	}
	if m2.Draft() != "pending message" {
		t.Fatalf("draft must remain intact while delivery uncertain: %q", m2.Draft())
	}

	// In a separate empty model, unresolved inflight shows truthful empty placeholder
	mEmpty, _ := readyModel()
	mEmpty.inflight = "unresolved-msg"
	out = view(mEmpty)
	if !strings.Contains(out, "A send or channel action is unresolved · draft kept") {
		t.Fatalf("empty placeholder must show unresolved status: %s", out)
	}

	// 4. Pending channel action
	mPending, _ := readyModel()
	mPending.pending = &pendingAction{Action: Action{Kind: "join", Channel: "#other"}, uncertain: true}
	out = view(mPending)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send during pending channel action:\n%s", out)
	}
	if !strings.Contains(out, "unresolved draft kept") {
		t.Fatalf("footer must indicate unresolved draft kept during pending action:\n%s", out)
	}
	mPending.pending = nil
	out = view(mPending)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("footer must restore Enter send after pending action settles:\n%s", out)
	}
}

// TestMissingSendCallbackShowsSendingUnavailable verifies that if opts.Send is nil,
// send is not advertised even when connected.
func TestMissingSendCallbackShowsSendingUnavailable(t *testing.T) {
	m := New(Options{Space: "trial", Channel: "#space", Nick: "human", Send: nil})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	out := view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer must not advertise Enter send when Send callback is nil:\n%s", out)
	}
	if !strings.Contains(out, "sending unavailable draft kept") {
		t.Fatalf("footer must show sending unavailable:\n%s", out)
	}
	if !strings.Contains(out, "Sending unavailable · view is read-only") {
		t.Fatalf("placeholder must show sending unavailable:\n%s", out)
	}

	// Nonempty draft also retains sending unavailable hint
	paste(m, "retained notes")
	out = view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("footer with nonempty draft must not advertise Enter send when Send is nil:\n%s", out)
	}
	if !strings.Contains(out, "sending unavailable draft kept") {
		t.Fatalf("footer must retain sending unavailable hint with draft:\n%s", out)
	}
}

// TestNarrowFooterRetainsReadinessHintAndExcludesEnterSend verifies that at narrow
// viewport widths, the default footer reduction loop preserves the readiness hint,
// drops ancillary /help before the readiness reason, and never advertises Enter send when unready.
func TestNarrowFooterRetainsReadinessHintAndExcludesEnterSend(t *testing.T) {
	// Case 1: Astra's concrete source-derived regression:
	// width 35, connected, Send=nil, nonempty draft, no notice/selection/hover.
	m := New(Options{Space: "trial", Channel: "#space", Nick: "human", Send: nil})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 35, Height: 24})
	paste(m, "nonempty draft")

	out := view(m)
	if strings.Contains(out, "Enter send") {
		t.Fatalf("narrow footer must not contain Enter send when Send=nil:\n%s", out)
	}
	if !strings.Contains(out, "sending unavailable draft kept") {
		t.Fatalf("narrow footer must preserve availability reason over ancillary /help:\n%s", out)
	}
	if strings.Contains(out, "/help") {
		t.Fatalf("narrow footer at width 35 must drop /help when both cannot fit:\n%s", out)
	}

	// Case 2: Normal ready case at width 35
	m.opts.Send = func(out Outgoing) tea.Cmd { return nil }
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("narrow footer must retain Enter send when connected and ready:\n%s", out)
	}

	// Case 3: Very narrow viewport (width 20) with ready model
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("width 20 footer must retain Enter send:\n%s", out)
	}
	if strings.Contains(out, "/help") {
		t.Fatalf("width 20 footer must drop /help when both cannot fit:\n%s", out)
	}
}

// TestKeyboardBehaviorsAndNoticePrecedencePreserved verifies that Alt+Enter newline,
// notice precedence, suggestions, and message selection footer precedence remain intact.
func TestKeyboardBehaviorsAndNoticePrecedencePreserved(t *testing.T) {
	m, _ := readyModel()

	// 1. Alt+Enter inserts newline in draft
	paste(m, "line1")
	press(m, tea.KeyEnter, tea.ModAlt)
	paste(m, "line2")
	if m.Draft() != "line1\nline2" {
		t.Fatalf("Alt+Enter must insert newline: %q", m.Draft())
	}

	// 2. Notices take precedence over default footer hints
	m.say(levelWarn, "A high-priority warning")
	out := view(m)
	if !strings.Contains(out, "A high-priority warning") {
		t.Fatalf("notice must render in footer:\n%s", out)
	}
	if strings.Contains(out, "Enter send") {
		t.Fatalf("notice must take precedence over default Enter send hint:\n%s", out)
	}
	m.notice = "" // clear notice

	// 3. Suggestions take precedence over default footer hints
	m.suggest = &suggestState{
		word:  "@",
		items: []suggestion{{label: "alice", insert: "@alice"}},
	}
	out = view(m)
	if !strings.Contains(out, "choose") || !strings.Contains(out, "insert") {
		t.Fatalf("suggestion footer hints must take precedence:\n%s", out)
	}
	m.suggest = nil

	// 4. Selected message actions take precedence over default footer hints
	m.append(Message{ID: "m1", ClientID: "m1", Channel: "#space", Nick: "alice", Text: "hello"})
	m.picked = true
	m.selected = 0
	out = view(m)
	if !strings.Contains(out, "Copy") {
		t.Fatalf("selected message actions must take precedence in footer:\n%s", out)
	}
	if strings.Contains(out, "Enter send") {
		t.Fatalf("message actions must override default Enter send hint:\n%s", out)
	}
	m.picked = false
	out = view(m)
	if !strings.Contains(out, "Enter send") {
		t.Fatalf("footer must restore Enter send once message selection cleared:\n%s", out)
	}
}

package chatui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

// jumpRig is a chat view joined to channels whose history requests are
// recorded, not sent: tests answer them with the messages the stream would.
type jumpRig struct {
	t     *testing.T
	m     *Model
	calls []HistoryRequest
}

func newJumpRig(t *testing.T, channels ...string) *jumpRig {
	t.Helper()
	m, _ := readyModel()
	r := &jumpRig{t: t, m: m}
	var infos []ChannelInfo
	for _, c := range channels {
		infos = append(infos, ChannelInfo{Name: c})
	}
	m.Update(CatalogMsg{Generation: 2, Default: channels[0], Applied: true, Channels: infos})
	for _, c := range channels {
		m.Update(MembershipMsg{Channel: c, State: "joined"})
	}
	record := func(h HistoryRequest) tea.Cmd {
		r.calls = append(r.calls, h)
		return nil
	}
	m.opts.LookupReference, m.opts.LoadWindow, m.opts.LoadOlder = record, record, record
	m.switchTo(channels[0])
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.View()
	return r
}

func mid(n int) string { return fmt.Sprintf("msg-%03d", n) }

func msgs(channel string, from, to int) []Message {
	var out []Message
	for n := from; n <= to; n++ {
		out = append(out, Message{ID: mid(n), Channel: channel, Nick: "peer", Account: "peer", Text: fmt.Sprintf("text %d", n)})
	}
	return out
}

// history loads messages as retained history: no unread.
func (r *jumpRig) history(channel string, from, to int) {
	for _, msg := range msgs(channel, from, to) {
		msg.Replay = true
		r.m.Update(IncomingMsg{Message: msg})
	}
	r.m.View()
}

// arrive delivers live messages.
func (r *jumpRig) arrive(channel string, from, to int) {
	for _, msg := range msgs(channel, from, to) {
		r.m.Update(IncomingMsg{Message: msg})
	}
}

func (r *jumpRig) last() HistoryRequest {
	r.t.Helper()
	if len(r.calls) == 0 {
		r.t.Fatal("no history request")
	}
	return r.calls[len(r.calls)-1]
}

// find answers the exact search for id: a miss in every channel before
// channel, the hit there. It returns the page request that follows.
func (r *jumpRig) find(id, channel string) HistoryRequest {
	r.t.Helper()
	for range 8 {
		q := r.last()
		if q.Window {
			return q
		}
		if q.Lookup != id {
			r.t.Fatalf("expected the search for %s, got %+v", id, q)
		}
		if q.Channel != channel {
			r.m.Update(ReferenceLookupMsg{ID: q.ID, Channel: q.Channel})
			continue
		}
		r.m.Update(ReferenceLookupMsg{ID: q.ID, Channel: q.Channel, Message: Message{ID: id, Channel: q.Channel}})
	}
	q := r.last()
	if !q.Window || q.Around != id || q.Channel != channel {
		r.t.Fatalf("expected the page around %s in %s, got %+v", id, channel, q)
	}
	return q
}

// jump clicks a link to an unloaded message and completes it with page.
func (r *jumpRig) jump(id, channel string, page []Message) {
	r.t.Helper()
	r.m.showMessage(id)
	q := r.find(id, channel)
	r.m.Update(WindowPageMsg{ID: q.ID, Channel: channel, Messages: page})
	r.m.View()
}

func (r *jumpRig) selectedID() string {
	if !r.m.picked || r.m.selected < 0 || r.m.selected >= len(r.m.messages) {
		return ""
	}
	return r.m.messages[r.m.selected].ID
}

func (r *jumpRig) topKey() string {
	r.m.View()
	return r.m.lastTranscript[r.m.scroll].key
}

func ids(list []Message) []string {
	var out []string
	for _, msg := range list {
		out = append(out, msg.ID)
	}
	return out
}

// scrollTo scrolls down until the first row of id is the top row.
func (r *jumpRig) scrollTo(id string) {
	r.t.Helper()
	r.m.View()
	for i, row := range r.m.lastTranscript {
		if row.key == "server:"+id {
			r.m.scrollDown(i - r.m.scroll)
			return
		}
	}
	r.t.Fatalf("%s is not in the transcript", id)
}

// Plan test 1: a jump to a loaded message, from the tail, from a scrolled
// position and from a filtered view; Esc returns to exactly that view.
func TestJumpToLoadedMessageAndBack(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 1, 30)
	m.showMessage(mid(5))
	if r.selectedID() != mid(5) || m.follow || m.focus != focusMessages || len(m.back) != 1 {
		t.Fatalf("jump: selected %q follow %v back %d", r.selectedID(), m.follow, len(m.back))
	}
	m.View()
	press(m, tea.KeyEscape, 0)
	if !m.follow || m.picked || m.focus != focusComposer || len(m.back) != 0 {
		t.Fatalf("Esc from a jump made at the tail: follow %v picked %v back %d", m.follow, m.picked, len(m.back))
	}

	m.View()
	m.scrollUp(6)
	before := r.topKey()
	m.showMessage(mid(2))
	m.View()
	press(m, tea.KeyEscape, 0)
	if got := r.topKey(); got != before || m.follow {
		t.Fatalf("Esc from a scrolled position: top %q, want %q", got, before)
	}

	for i := range m.messages {
		if i%2 == 1 {
			m.messages[i].Account, m.messages[i].Nick = "bob", "bob"
		}
	}
	m.touch()
	m.setFilter("peer", "peer")
	f := m.filter
	if f == nil {
		t.Fatal("no filter")
	}
	m.showMessage(mid(4)) // bob's: the filter hides it
	if m.filter != nil || r.selectedID() != mid(4) {
		t.Fatal("the jump did not show the hidden target")
	}
	m.View()
	press(m, tea.KeyEscape, 0)
	if m.filter != f {
		t.Fatal("Esc did not restore the filter the jump cleared")
	}
}

// Plan test 2: across channels, and a same-channel chain of two windows.
func TestJumpChainsGoBackOneStepEach(t *testing.T) {
	r := newJumpRig(t, "#space", "#other")
	m := r.m
	r.history("#space", 200, 230)
	r.history("#other", 1, 10)
	m.showMessage(mid(3))
	if m.name != "#other" || r.selectedID() != mid(3) {
		t.Fatalf("cross-channel jump: %s %q", m.name, r.selectedID())
	}
	m.View()
	press(m, tea.KeyEscape, 0)
	if m.name != "#space" || !m.follow {
		t.Fatalf("Esc did not return to #space at its tail: %s", m.name)
	}

	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	if m.win == nil || r.selectedID() != mid(50) {
		t.Fatal("page B is not shown at its target")
	}
	r.jump(mid(120), "#space", msgs("#space", 95, 145))
	if m.win == nil || r.selectedID() != mid(120) || len(m.back) != 2 {
		t.Fatalf("page C: selected %q back %d", r.selectedID(), len(m.back))
	}
	r.arrive("#space", 231, 231)
	calls := len(r.calls)
	press(m, tea.KeyEscape, 0)
	q := r.last()
	if len(r.calls) != calls+1 || !q.Window || q.Channel != "#space" || !strings.HasPrefix(q.Around, "msg-0") || m.notice != "Going back…" {
		t.Fatalf("going back to B should fetch the page around its anchor: %+v %q", q, m.notice)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 25, 75)})
	m.View()
	if m.win == nil || r.selectedID() != mid(50) || m.notice == "Going back…" {
		t.Fatalf("B is not back: selected %q", r.selectedID())
	}
	press(m, tea.KeyEscape, 0)
	if m.win != nil || !m.follow || m.messages[len(m.messages)-1].ID != mid(231) || m.unread != 0 || len(m.back) != 0 {
		t.Fatalf("the second Esc should return to the tail with the arrival: win %v follow %v", m.win != nil, m.follow)
	}
}

// Plan test 3, the screenshot case: an old message opens in context, no
// panel; arrivals wait as unread behind the window.
func TestJumpToUnloadedMessageShowsItInContext(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	if m.overlay != nil || m.win == nil || r.selectedID() != mid(50) || m.follow {
		t.Fatal("the target is not shown selected in a window")
	}
	if v := view(m); !strings.Contains(v, "text 50") || !strings.Contains(v, "text 51") {
		t.Fatalf("the target or its neighbours are not on screen:\n%s", v)
	}
	r.arrive("#space", 231, 231)
	if m.unread != 1 || slices.Contains(ids(m.messages), mid(231)) || !m.behindLatest() {
		t.Fatalf("an arrival behind the window: unread %d", m.unread)
	}
	press(m, tea.KeyEnd, tea.ModAlt)
	if m.win != nil || !m.follow || m.messages[len(m.messages)-1].ID != mid(231) {
		t.Fatal("Jump to latest did not return to the live list with the arrival")
	}
}

// Plan test 4 (F1): paging state belongs to each list.
func TestWindowPagingStateIsItsOwn(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	m.scrollUp(1000)
	m.View()
	m.scrollUp(1)
	q := r.last()
	if !q.Window || q.Before != mid(25) {
		t.Fatalf("the window's top should ask for the page before it: %+v", q)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space"})
	if !m.win.start || m.notice != "Beginning of retained history" {
		t.Fatal("an empty page before the window is the start of its history")
	}
	press(m, tea.KeyEscape, 0)
	if m.win != nil || m.historyEnd || m.loadingOlder {
		t.Fatal("the live list came back with the window's paging state")
	}
	m.View()
	m.scrollUp(1000)
	m.View()
	m.scrollUp(1)
	if q := r.last(); q.Window || q.Before != mid(200) {
		t.Fatalf("Page Up in the live list should still load older messages: %+v", q)
	}
}

// Plan test 5 (F2): a target the chat holds is never fetched again.
func TestJumpFindsHeldTargetsBehindWindows(t *testing.T) {
	r := newJumpRig(t, "#space", "#other")
	m := r.m
	r.history("#space", 200, 230)
	r.history("#other", 300, 320)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	calls := len(r.calls)
	m.showMessage(mid(210))
	if len(r.calls) != calls || m.win != nil || r.selectedID() != mid(210) {
		t.Fatal("a target in the live list behind the window was not selected there")
	}

	m.chooseChannel("#other")
	r.jump(mid(150), "#other", msgs("#other", 125, 175))
	m.chooseChannel("#space")
	calls = len(r.calls)
	m.showMessage(mid(310))
	if len(r.calls) != calls || m.name != "#other" || m.win != nil || r.selectedID() != mid(310) {
		t.Fatalf("a target behind another channel's window: %s %q", m.name, r.selectedID())
	}
}

// Plan test 6 (F3): back at the latest by hand, Esc no longer jumps.
func TestBackListDroppedAtTheLatest(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 1, 30)
	m.showMessage(mid(5))
	m.View()
	for range 10 {
		press(m, tea.KeyPgDown, 0)
		m.View()
	}
	if !m.follow || len(m.back) != 0 {
		t.Fatalf("at the latest again: follow %v back %d", m.follow, len(m.back))
	}
	r.arrive("#space", 31, 31)
	m.focus = focusMessages
	press(m, tea.KeyEscape, 0)
	if !m.follow || m.focus != focusComposer {
		t.Fatal("Esc jumped away from the latest messages")
	}
}

// Plan test 7 (F4): one neutral line while a fetch runs; Esc cancels it.
func TestJumpFetchLineAndCancel(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	m.showMessage(mid(50))
	if m.notice != noticeOpening {
		t.Fatalf("while fetching: %q", m.notice)
	}
	q := r.find(mid(50), "#space")
	if m.notice != noticeOpening {
		t.Fatalf("while fetching the page: %q", m.notice)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 25, 75)})
	if m.notice == noticeOpening {
		t.Fatal("the line stayed after the jump")
	}

	m.showMessage(mid(120))
	q = r.find(mid(120), "#space")
	press(m, tea.KeyEscape, 0)
	if m.reference != nil || m.notice == noticeOpening {
		t.Fatal("Esc did not cancel the fetch")
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 95, 145)})
	if r.selectedID() != mid(50) || !slices.Contains(ids(m.messages), mid(50)) {
		t.Fatal("a cancelled page was applied")
	}
}

// Plan test 8 (F5): a reconnect or leaving keeps the window like any
// reading position; its pending edge page is dropped.
func TestWindowSurvivesChannelEvents(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	m.scrollDown(1000)
	q := r.last()
	if !q.Window || q.After != mid(75) {
		t.Fatalf("the window's bottom should ask for the page after it: %+v", q)
	}
	m.Update(ConnectionMsg{})
	if m.win == nil || m.win.edge != nil {
		t.Fatal("a reconnect should keep the window and drop its pending page")
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 76, 80)})
	if slices.Contains(ids(m.messages), mid(76)) {
		t.Fatal("a page lost with the stream was applied")
	}
	m.Update(ConnectionMsg{Connected: true})
	m.Update(MembershipMsg{Channel: "#space", State: "left"})
	calls := len(r.calls)
	m.View()
	m.scrollDown(1000)
	if m.win == nil || len(r.calls) != calls {
		t.Fatal("a left channel should keep its window read-only, without paging")
	}
}

// Plan test 9: a full transcript keeps the clicked target in a window; a
// page that fits joins the live list.
func TestFullTranscriptKeepsTheTarget(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 100, 355)
	if len(m.messages) != maxMessages {
		t.Fatalf("fixture holds %d messages", len(m.messages))
	}
	r.jump(mid(80), "#space", msgs("#space", 55, 124))
	if m.win == nil || r.selectedID() != mid(80) {
		t.Fatal("the target of a page that cannot join a full list was lost")
	}

	r = newJumpRig(t, "#space")
	m = r.m
	r.history("#space", 100, 130)
	r.jump(mid(80), "#space", msgs("#space", 55, 124))
	if m.win != nil || r.selectedID() != mid(80) || m.messages[0].ID != mid(55) || len(m.messages) != 76 {
		t.Fatalf("a page that fits should join the live list: %v", ids(m.messages)[:3])
	}
}

// Plan test 10 (A2): reading down a window to a full live list shows every
// row once, switching at the first shared row on top; with room it merges.
func TestWindowSwitchesAtTheSharedRow(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 70, 325)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	m.scrollDown(1000)
	q := r.last()
	if q.After != mid(50) {
		t.Fatalf("expected the page after the window: %+v", q)
	}
	r.arrive("#space", 326, 326) // the full live list gives way: it starts at 71 now
	if m.unread != 1 || m.win.live.messages[0].ID != mid(71) {
		t.Fatalf("fixture: unread %d, live starts %s", m.unread, m.win.live.messages[0].ID)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 51, 100)})
	if m.win == nil || !slices.Equal(ids(m.messages), ids(msgs("", 1, 100))) {
		t.Fatalf("the window should hold 1–100 without a gap: %v", ids(m.messages))
	}
	r.scrollTo(mid(70))
	if m.win == nil {
		t.Fatal("switched before the first shared row reached the top")
	}
	r.scrollTo(mid(71))
	if m.win != nil || m.follow || m.unread != 1 {
		t.Fatalf("at the shared row: window %v follow %v unread %d", m.win != nil, m.follow, m.unread)
	}
	if top := r.topKey(); top != "server:"+mid(71) {
		t.Fatalf("the switch moved the view: top %q", top)
	}

	r = newJumpRig(t, "#space")
	m = r.m
	r.history("#space", 70, 110)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	m.scrollDown(1000)
	q = r.last()
	before := r.topKey()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 51, 100)})
	if m.win != nil || !slices.Equal(ids(m.messages), ids(msgs("", 1, 110))) {
		t.Fatalf("with room the window should merge: window %v, %d messages", m.win != nil, len(m.messages))
	}
	if r.topKey() != before {
		t.Fatal("the merge moved the view")
	}
}

// Plan test 11 (F6, archyAstra's example): live 40–50 and 70–90 after a
// gap; the page 30–79 around 55 cannot merge, so 51–69 stay readable.
func TestWindowWithHoleInTheLiveList(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 40, 50)
	r.history("#space", 70, 90)
	r.jump(mid(55), "#space", msgs("#space", 30, 79))
	if m.win == nil || r.selectedID() != mid(55) || !slices.Contains(ids(m.messages), mid(60)) {
		t.Fatal("the target in the hole was lost")
	}
	r.scrollTo(mid(69))
	if m.win == nil {
		t.Fatal("switched inside the hole")
	}
	r.scrollTo(mid(70))
	if m.win != nil || r.topKey() != "server:"+mid(70) {
		t.Fatal("the window should give way at 70, in place")
	}
}

// Plan test 12 (A1): edge pages exclude their anchor; an empty page after
// the window ends that edge until a live arrival; a stale reply is ignored.
func TestWindowEdgePages(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	m.scrollDown(1000)
	q := r.last()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 51, 60)})
	if !slices.Contains(ids(m.messages), mid(60)) {
		t.Fatal("an exclusive edge page was not applied")
	}
	m.View()
	m.scrollDown(1000)
	q = r.last()
	if q.After != mid(60) {
		t.Fatalf("next page: %+v", q)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space"})
	calls := len(r.calls)
	m.View()
	m.scrollDown(1000)
	if !m.win.end || len(r.calls) != calls || !m.behindLatest() {
		t.Fatal("an empty page after the window should end that edge, Jump to latest still offered")
	}
	r.arrive("#space", 231, 231)
	m.View()
	m.scrollDown(1000)
	if len(r.calls) != calls+1 || r.last().After != mid(60) {
		t.Fatal("a live arrival should re-arm the edge")
	}
	stale := r.last()
	r.jump(mid(120), "#space", msgs("#space", 95, 145))
	m.Update(WindowPageMsg{ID: stale.ID, Channel: "#space", Messages: msgs("#space", 61, 62)})
	if slices.Contains(ids(m.messages), mid(61)) {
		t.Fatal("an edge reply for a replaced window was applied")
	}
}

// Plan test 13: a jump completes only with a page holding its exact target;
// anything else leaves the source view and the back list as they were.
func TestJumpCompletesOnlyWithTheTarget(t *testing.T) {
	for _, test := range []struct {
		name string
		end  func(*jumpRig, HistoryRequest)
	}{
		{"failed page", func(r *jumpRig, q HistoryRequest) {
			r.m.Update(WindowPageMsg{ID: q.ID, Channel: q.Channel, Err: errors.New("connection ended")})
		}},
		{"target gone", func(r *jumpRig, q HistoryRequest) {
			r.m.Update(WindowPageMsg{ID: q.ID, Channel: q.Channel, Messages: msgs(q.Channel, 25, 49)})
		}},
		{"channel left", func(r *jumpRig, q HistoryRequest) {
			r.m.Update(MembershipMsg{Channel: q.Channel, State: "left"})
			r.m.Update(WindowPageMsg{ID: q.ID, Channel: q.Channel, Messages: msgs(q.Channel, 25, 75)})
		}},
		{"cancelled", func(r *jumpRig, q HistoryRequest) {
			press(r.m, tea.KeyEscape, 0)
			r.m.Update(WindowPageMsg{ID: q.ID, Channel: q.Channel, Messages: msgs(q.Channel, 25, 75)})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newJumpRig(t, "#space", "#other")
			m := r.m
			r.history("#space", 200, 230)
			m.showMessage(mid(205)) // a loaded jump first: one back entry
			m.View()
			m.showMessage(mid(50))
			q := r.find(mid(50), "#other")
			test.end(r, q)
			if m.name != "#space" || m.win != nil || r.selectedID() != mid(205) || len(m.back) != 1 || m.views["#other"].win != nil {
				t.Fatalf("the source view changed: %s selected %q back %d", m.name, r.selectedID(), len(m.back))
			}
		})
	}

	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	m.showMessage(mid(50))
	q := r.find(mid(50), "#space")
	r.arrive("#space", 231, 231) // a live row while the page is fetched
	page := msgs("#space", 25, 75)
	page[25].ReplyTo = mid(49)
	page[25].Attachments = []AttachmentRef{{ArtifactRef: ArtifactRef{ID: "a1", Name: "shot.png", Bytes: 3}}}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: page})
	if m.win == nil || !slices.Contains(ids(m.win.live.messages), mid(231)) {
		t.Fatal("the live row did not take its ordinary path")
	}
	if got := m.messages[m.selected]; got.ID != mid(50) || got.ReplyTo != mid(49) || len(got.Attachments) != 1 {
		t.Fatalf("page rows lost their fields: %+v", got)
	}
}

// Plan test 14: a send confirmed while a window is shown is matched in the
// live list.
func TestReceiptMatchedBehindWindow(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	var sent []Outgoing
	m.opts.Send = func(out Outgoing) tea.Cmd {
		sent = append(sent, out)
		return nil
	}
	r.history("#space", 200, 230)
	paste(m, "hello")
	press(m, tea.KeyEnter, 0)
	if len(sent) != 1 {
		t.Fatal("fixture: nothing sent")
	}
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	m.Update(DeliveryMsg{ID: sent[0].ID, MessageID: "sent-1", Channel: "#space", State: Confirmed})
	if m.inflight != "" || m.win == nil {
		t.Fatal("the receipt was not matched while the window is shown")
	}
	press(m, tea.KeyEnd, tea.ModAlt)
	last := m.messages[len(m.messages)-1]
	if last.ClientID != sent[0].ID || last.ID != "sent-1" || last.Delivery != Confirmed {
		t.Fatalf("the live list's send: %+v", last)
	}
}

// Review r1, 1: a return that must fetch its page keeps its entry until the
// page applies; a failed or cancelled return can be tried again.
func TestBackEntryKeptUntilTheReturnApplies(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	r.jump(mid(120), "#space", msgs("#space", 95, 145))
	press(m, tea.KeyEscape, 0)
	q := r.last()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Err: errors.New("refused")})
	if r.selectedID() != mid(120) || len(m.back) != 2 {
		t.Fatalf("a failed return consumed its entry: selected %q back %d", r.selectedID(), len(m.back))
	}
	press(m, tea.KeyEscape, 0)
	q = r.last()
	if !q.Window || q.Around == "" {
		t.Fatalf("Esc should ask for B again: %+v", q)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 95, 145)})
	if r.selectedID() != mid(120) || len(m.back) != 2 {
		t.Fatal("a page without B consumed its entry")
	}
	press(m, tea.KeyEscape, 0)
	q = r.last()
	press(m, tea.KeyEscape, 0) // cancels the return
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 25, 75)})
	if r.selectedID() != mid(120) || len(m.back) != 2 || m.reference != nil {
		t.Fatal("a cancelled return was applied or consumed its entry")
	}
	press(m, tea.KeyEscape, 0)
	q = r.last()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 25, 75)})
	if r.selectedID() != mid(50) || len(m.back) != 1 {
		t.Fatalf("B is not back: selected %q back %d", r.selectedID(), len(m.back))
	}
	press(m, tea.KeyEscape, 0)
	if m.win != nil || !m.follow || len(m.back) != 0 {
		t.Fatal("the last Esc should return to the tail")
	}
}

// Review r1, 2: a window slides within the display bounds in both
// directions, keeping the rows on screen and releasing an offscreen pick;
// an evicted edge can be paged back.
func TestWindowSlidesWithinTheBounds(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 1000, 1020)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	next := 51
	for range 12 { // 600 rows forward
		m.scrollDown(100000)
		q := r.last()
		if !q.Window || q.After != m.messages[len(m.messages)-1].ID {
			t.Fatalf("expected the page after the window: %+v", q)
		}
		top := r.topKey()
		m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", next, next+49)})
		next += 50
		if len(m.messages) > maxMessages {
			t.Fatalf("the window grew to %d rows", len(m.messages))
		}
		if r.topKey() != top {
			t.Fatal("sliding moved the rows on screen")
		}
	}
	if m.picked || m.messages[0].ID == mid(1) || m.win.start {
		t.Fatalf("the offscreen pick or the evicted top stayed: picked %v first %s", m.picked, m.messages[0].ID)
	}
	first := m.messages[0].ID
	m.scrollUp(100000)
	m.View()
	m.scrollUp(1)
	if q := r.last(); q.Before != first {
		t.Fatalf("the evicted top should be paged back: %+v", q)
	}
	q := r.last()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 345, 394)})
	if len(m.messages) > maxMessages || m.win.end || m.messages[0].ID != mid(345) {
		t.Fatalf("paging back: %d rows, end %v", len(m.messages), m.win.end)
	}
}

// Review r1, 2: windows count toward the whole-view bounds, and a live
// arrival at the bound trims hidden live lists, never panics or drops the
// window's rows on screen or an unresolved send.
func TestWindowsCountInTheWholeViewBounds(t *testing.T) {
	channels := []string{"#space", "#b", "#c", "#d"}
	r := newJumpRig(t, channels...)
	m := r.m
	for i, c := range channels {
		r.history(c, 2000+i*1000, 2000+i*1000+255)
	}
	m.switchTo("#space")
	paste(m, "pending")
	press(m, tea.KeyEnter, 0)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	total := func() int {
		n := 0
		for _, v := range m.views {
			n += len(v.messages)
			if v.win != nil {
				n += len(v.win.live.messages)
			}
		}
		return n
	}
	if total() > maxTotalMessages || m.win == nil || r.selectedID() != mid(50) {
		t.Fatalf("opening the window: %d messages", total())
	}
	r.arrive("#space", 2256, 2260)
	r.arrive("#b", 3256, 3260)
	if total() > maxTotalMessages || m.win == nil || r.selectedID() != mid(50) {
		t.Fatalf("live arrivals at the bound: %d messages", total())
	}
	if !slices.ContainsFunc(m.win.live.messages, func(msg Message) bool { return msg.Text == "pending" }) {
		t.Fatal("the unresolved send left its live list")
	}
}

// Review r1, 3: a disconnect's uncertain outcome reaches the send in the
// live list behind a window.
func TestDisconnectOutcomeReachesTheLiveSend(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	var sent []Outgoing
	m.opts.Send = func(out Outgoing) tea.Cmd {
		sent = append(sent, out)
		return nil
	}
	r.history("#space", 200, 230)
	paste(m, "hello")
	press(m, tea.KeyEnter, 0)
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	m.Update(ConnectionMsg{})
	press(m, tea.KeyEnd, tea.ModAlt)
	last := m.messages[len(m.messages)-1]
	if len(sent) != 1 || last.ClientID != sent[0].ID || last.Text != "hello" || last.Delivery != Uncertain || m.Draft() != "hello" {
		t.Fatalf("the send behind the window: %+v, draft %q", last, m.Draft())
	}
}

// Review r1, 4: live notices stay with the live list; a window draws none.
func TestLiveNoticesStayWithTheLiveList(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	notices := func() string {
		m.View()
		var b strings.Builder
		for _, row := range m.lastTranscript {
			if row.kind == rowEvent {
				for _, run := range row.runs {
					b.WriteString(run.Text)
				}
			}
		}
		return b.String()
	}
	m.Update(GapMsg{Channel: "#space", Text: "first notice"})
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	m.Update(GapMsg{Channel: "#space", Text: "second notice"})
	if got := notices(); got != "" {
		t.Fatalf("a live notice is drawn beside older history: %q", got)
	}
	press(m, tea.KeyEnd, tea.ModAlt)
	if got := notices(); !strings.Contains(got, "first notice") || !strings.Contains(got, "second notice") {
		t.Fatalf("live notices are missing after the return: %q", got)
	}
}

// Review r1, 5: a lost edge cursor is a failed read, not an exhausted edge.
func TestEdgeFailureKeepsTheEdge(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 200, 230)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	m.scrollDown(100000)
	q := r.last()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Err: errors.New("the message this page continues from is no longer retained")})
	calls := len(r.calls)
	m.View()
	m.scrollDown(100000)
	if m.win.end || len(r.calls) != calls+1 || !strings.Contains(m.notice, "unavailable") && m.notice != "" {
		t.Fatalf("a failed edge was taken as the end: end %v", m.win.end)
	}
}

// Review r1, 6: a return to the latest, by Alt+End or by sending, cancels a
// jump still being fetched.
func TestReturnToLatestCancelsAPendingJump(t *testing.T) {
	for _, test := range []struct {
		name string
		act  func(*Model)
	}{
		{"alt+end", func(m *Model) { press(m, tea.KeyEnd, tea.ModAlt) }},
		{"send", func(m *Model) {
			m.focus = focusComposer
			paste(m, "hi")
			press(m, tea.KeyEnter, 0)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newJumpRig(t, "#space")
			m := r.m
			r.history("#space", 200, 230)
			r.jump(mid(50), "#space", msgs("#space", 25, 75))
			m.showMessage(mid(120))
			q := r.find(mid(120), "#space")
			test.act(m)
			m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 95, 145)})
			if m.win != nil || !m.follow || len(m.back) != 0 || m.reference != nil {
				t.Fatal("a late reply took the reader away from the latest")
			}
		})
	}
}

// Review r1, 7: in message focus a text highlight gets its own Esc before
// going back.
func TestHighlightEscBeforeBack(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 1, 30)
	m.showMessage(mid(5))
	m.View()
	dragOver(t, m, mid(5), 0, "text")
	m.focus = focusMessages
	if m.sel == nil || len(m.back) != 1 {
		t.Fatal("fixture: no highlight or no back entry")
	}
	press(m, tea.KeyEscape, 0)
	if m.sel != nil || len(m.back) != 1 || m.follow {
		t.Fatal("the first Esc should only clear the highlight")
	}
	press(m, tea.KeyEscape, 0)
	if len(m.back) != 0 || !m.follow {
		t.Fatal("the second Esc should go back")
	}
}

// Review r2, 1: evicting a pick from the live list behind a window keeps the
// keyboard on the target: Enter replies, the draft stays, nothing is sent.
func TestHiddenEvictionKeepsTheFocus(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	var sent []Outgoing
	m.opts.Send = func(out Outgoing) tea.Cmd {
		sent = append(sent, out)
		return nil
	}
	r.history("#space", 100, 355)
	m.pick("server:" + mid(100))
	m.focus = focusComposer
	paste(m, "my draft")
	r.jump(mid(50), "#space", msgs("#space", 25, 75))
	r.arrive("#space", 356, 356) // evicts the hidden pick, 100
	if m.focus != focusMessages || r.selectedID() != mid(50) {
		t.Fatalf("a hidden eviction moved the focus: %v", m.focus)
	}
	press(m, tea.KeyEnter, 0)
	if len(sent) != 0 || m.Draft() != "my draft" || m.replyTo != mid(50) {
		t.Fatalf("Enter: sent %d, draft %q, reply %q", len(sent), m.Draft(), m.replyTo)
	}
}

// Review r2, 2: an edge read whose boundary was evicted meanwhile is not
// applied; the next read continues from the surviving edge.
func TestStaleEdgeReadAfterEviction(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 900, 920)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	m.scrollDown(100000)
	q := r.last()
	if q.After != mid(50) {
		t.Fatalf("fixture: %+v", q)
	}
	m.scrollUp(100000)
	m.View()
	if !m.windowEvictOne(nil) || m.edgeID(true) != mid(49) {
		t.Fatalf("fixture: the bottom edge is %s", m.edgeID(true))
	}
	m.View()
	m.scrollDown(100000)
	if next := r.last(); next.ID == q.ID || next.After != mid(49) {
		t.Fatalf("the next read should continue from 49 at once: %+v", next)
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 51, 100)})
	if slices.Contains(ids(m.messages), mid(51)) {
		t.Fatal("a page continuing from an evicted edge left a hole")
	}

	r = newJumpRig(t, "#space")
	m = r.m
	r.history("#space", 900, 920)
	r.jump(mid(75), "#space", msgs("#space", 51, 100))
	m.scrollUp(100000)
	m.View()
	m.scrollUp(1)
	q = r.last()
	if q.Before != mid(51) {
		t.Fatalf("fixture: %+v", q)
	}
	m.scrollDown(100000)
	m.View()
	if !m.windowEvictOne(nil) || m.edgeID(false) != mid(52) {
		t.Fatalf("fixture: the top edge is %s", m.edgeID(false))
	}
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 1, 50)})
	if slices.Contains(ids(m.messages), mid(50)) {
		t.Fatal("a page before an evicted edge left a hole")
	}
}

// Review r2, 3: replaced and closed windows release their rendered bodies;
// rows the live list holds keep theirs.
func TestDiscardedWindowsReleaseTheirBodies(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 900, 910)
	for k := 1; k <= 6; k++ {
		r.jump(mid(k*100+25), "#space", msgs("#space", k*100, k*100+49))
		m.View()
	}
	press(m, tea.KeyEnd, tea.ModAlt)
	m.View()
	for key := range m.bodyCache {
		if m.indexOf(key) < 0 {
			t.Fatalf("a discarded window's body stayed: %s (%d cached)", key, len(m.bodyCache))
		}
	}
	if _, ok := m.bodyCache["server:"+mid(905)]; !ok {
		t.Fatal("a live row lost its body")
	}
}

// Review r2, small: the highlight's Esc also resets the action choice.
func TestHighlightEscResetsTheAction(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 1, 30)
	m.showMessage(mid(5))
	m.View()
	dragOver(t, m, mid(5), 0, "text")
	m.focus = focusMessages
	press(m, tea.KeyRight, 0)
	if m.actionIndex == 0 {
		t.Fatal("fixture: Right did not move the action")
	}
	press(m, tea.KeyEscape, 0)
	if m.sel != nil || m.actionIndex != 0 {
		t.Fatalf("Esc: sel %v index %d", m.sel != nil, m.actionIndex)
	}
}

// Review r3: an expanded table row survives while its message is shown:
// through a merge, and when the hidden live copy of a shown row is evicted.
func TestTableRowStateKeptWhileShown(t *testing.T) {
	expand := func(m *Model, id string) {
		if m.tableRows == nil {
			m.tableRows = map[string]map[markdownview.TableRowID]bool{}
		}
		m.tableRows["server:"+id] = map[markdownview.TableRowID]bool{{Table: 0, Row: 1}: true}
	}
	r := newJumpRig(t, "#space")
	m := r.m
	r.history("#space", 70, 110)
	r.jump(mid(25), "#space", msgs("#space", 1, 50))
	expand(m, mid(49))
	m.scrollDown(100000)
	q := r.last()
	m.Update(WindowPageMsg{ID: q.ID, Channel: "#space", Messages: msgs("#space", 51, 100)})
	if m.win != nil || len(m.tableRows["server:"+mid(49)]) == 0 {
		t.Fatalf("the merge folded a shown row: window %v", m.win != nil)
	}

	r = newJumpRig(t, "#space")
	m = r.m
	r.history("#space", 70, 325)
	r.jump(mid(60), "#space", msgs("#space", 55, 104))
	if m.win == nil {
		t.Fatal("fixture: the page should be a window")
	}
	expand(m, mid(70))
	r.arrive("#space", 326, 326) // evicts the hidden live copy of 70
	if slices.Contains(ids(m.win.live.messages), mid(70)) || len(m.tableRows["server:"+mid(70)]) == 0 {
		t.Fatal("evicting the hidden copy folded the shown row")
	}
}

// Review r4: a compact table the reader posted stays open across its two
// copies: the live echo (client key) and the history copy (server key).
func TestOwnTableRowStaysOpenAcrossCopies(t *testing.T) {
	r := newJumpRig(t, "#space")
	m := r.m
	table := "|Name|Detail|\n|---|---|\n|alpha|" + strings.Repeat("first detail ", 25) + strings.Repeat("x", 200) + "|\n|beta|" + strings.Repeat("second detail ", 25) + strings.Repeat("x", 200) + "|"
	for _, msg := range msgs("#space", 70, 325) {
		msg.Replay = true
		if msg.ID == mid(70) {
			msg.ClientID, msg.Nick, msg.Account, msg.Text = "send-70", "human", "", table
		}
		m.Update(IncomingMsg{Message: msg})
	}
	page := msgs("#space", 55, 104)
	page[15].Nick, page[15].Account, page[15].Text = "human", "", table // 70, from history: no client ID
	r.jump(mid(60), "#space", page)
	if m.win == nil {
		t.Fatal("fixture: the page should be a window")
	}
	open := func(key string) bool {
		t.Helper()
		m.View()
		i := m.indexOf(key)
		if i < 0 {
			t.Fatalf("%s is not shown", key)
		}
		body := m.body(&m.messages[i], m.lastTextWidth)
		return len(body.closed) == 1
	}
	m.toggleTableRow(action{Kind: markdownview.IntentToggleTableRow, Value: "0:0", Extra: "server:" + mid(70)})
	if !open("server:" + mid(70)) {
		t.Fatal("fixture: the toggle did not open the window copy's row")
	}
	r.scrollTo(mid(70))
	if m.win != nil {
		t.Fatal("fixture: no switch at 70")
	}
	if !open("client:send-70") {
		t.Fatal("the opened row folded on the live copy of the same message")
	}
}

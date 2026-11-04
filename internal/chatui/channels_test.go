package chatui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func channelModel(t *testing.T) (*Model, *[]Outgoing, *[]Action) {
	t.Helper()
	m, sends := readyModel()
	var actions []Action
	m.opts.Control = func(a Action) tea.Cmd { actions = append(actions, a); return nil }
	m.Update(CatalogMsg{Generation: 1, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}}, Applied: true})
	m.Update(MembershipMsg{Channel: "#space", State: "joined"})
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	return m, sends, &actions
}

func alt(m *Model, r rune) { press(m, r, tea.ModAlt) }

// Each channel keeps its own draft, unread count and reading position;
// switching never sends and an arrival elsewhere does not move the view.
func TestChannelsKeepSeparateDraftsAndUnread(t *testing.T) {
	m, sends, _ := channelModel(t)
	paste(m, "for space")
	alt(m, '2')
	if m.name != "#build" || m.Draft() != "" {
		t.Fatalf("switch shows #build with its own empty draft: %s %q", m.name, m.Draft())
	}
	paste(m, "for build")
	m.Update(IncomingMsg{Message: Message{ID: "s1", Channel: "#space", Nick: "x", Text: "hello space"}})
	m.Update(IncomingMsg{Message: Message{ID: "s2", Channel: "#space", Nick: "x", Text: "@human look", Account: "mx"}})
	if v := m.views["#space"]; v.unread != 2 || len(m.messages) != 0 {
		t.Fatalf("hidden arrivals count as unread there only: %d, visible %d", v.unread, len(m.messages))
	}
	if !strings.Contains(view(m), "2 elsewhere") {
		t.Fatalf("the top band reports unread elsewhere:\n%s", view(m))
	}
	alt(m, '1')
	if m.name != "#space" || m.Draft() != "for space" || m.unread != 0 || len(m.messages) != 2 || len(*sends) != 0 {
		t.Fatalf("switching back restores #space: %s %q unread=%d", m.name, m.Draft(), m.unread)
	}
	alt(m, '2')
	if m.Draft() != "for build" {
		t.Fatalf("#build draft lost: %q", m.Draft())
	}
}

// A send carries its channel; confirming it clears only that channel's
// unchanged draft, even after switching away.
func TestSendTargetsItsChannelAndConfirmationClearsOnlyThatDraft(t *testing.T) {
	m, sends, _ := channelModel(t)
	alt(m, '2')
	paste(m, "deploy done")
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Channel != "#build" {
		t.Fatalf("send targets the visible channel: %+v", *sends)
	}
	alt(m, '1')
	paste(m, "unrelated")
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#build", MessageID: "b1", State: Confirmed})
	if m.Draft() != "unrelated" || m.views["#build"].editor.text != "" || m.inflight != "" {
		t.Fatalf("confirmation for #build touched #space: %q / %q", m.Draft(), m.views["#build"].editor.text)
	}
	if got := m.views["#build"].messages; len(got) != 1 || got[0].ID != "b1" || got[0].Delivery != Confirmed {
		t.Fatalf("#build keeps its confirmed message: %+v", got)
	}
}

func TestLeftKickedOrUnknownChannelsCannotSend(t *testing.T) {
	for _, state := range []string{"left", "kicked", "unknown", "joining"} {
		m, sends, actions := channelModel(t)
		alt(m, '2')
		m.Update(MembershipMsg{Channel: "#build", State: state})
		paste(m, "text")
		press(m, tea.KeyEnter, 0)
		if len(*sends) != 0 || m.Draft() != "text" || !strings.Contains(m.notice, "/join #build") {
			t.Fatalf("%s: sending must be refused with the draft kept: %q", state, m.notice)
		}
		if len(*actions) != 0 {
			t.Fatalf("%s: nothing is rejoined automatically", state)
		}
	}
}

// Channel commands become typed actions; only a whole single-line draft is
// a command, one action is outstanding, and an uncertain one blocks more.
func TestChannelCommandsAreTypedActions(t *testing.T) {
	m, sends, actions := channelModel(t)
	paste(m, "/join Ops")
	press(m, tea.KeyEnter, 0)
	// The request alone neither shows the channel nor claims membership.
	if len(*actions) != 1 || (*actions)[0].Kind != "join" || (*actions)[0].Channel != "#ops" || m.name != "#space" || m.views["#ops"] != nil || !strings.Contains(m.notice, "Joining #ops") {
		t.Fatalf("join: %+v in %s (%q)", *actions, m.name, m.notice)
	}
	paste(m, "/part")
	press(m, tea.KeyEnter, 0)
	if len(*actions) != 1 || m.Draft() != "/part" {
		t.Fatal("a second action must wait for the first")
	}
	_ = m.SetDraft("")
	m.Update(MembershipMsg{Channel: "#ops", State: "joining", ID: (*actions)[0].ID})
	if m.name != "#space" {
		t.Fatal("a stream-reported joining state must not switch the view")
	}
	// Confirmation may arrive before the authenticated self JOIN.
	m.Update(ControlMsg{ID: (*actions)[0].ID, Channel: "#ops", Kind: "join", State: "confirmed"})
	if m.name != "#space" {
		t.Fatal("the view switched before the server reported membership")
	}
	m.Update(MembershipMsg{Channel: "#ops", State: "joined", ID: (*actions)[0].ID})
	if m.name != "#ops" || m.membership != "joined" {
		t.Fatalf("a confirmed join shows its channel: %s (%s)", m.name, m.membership)
	}
	for _, tc := range []struct {
		text, kind string
		topic      *string
	}{
		{"/topic set Ship it  today", "topic-set", ptr("Ship it  today")},
		{"/topic set clear", "topic-set", ptr("clear")},
		{"/topic clear", "topic-clear", nil},
		{"/part", "part", nil},
	} {
		paste(m, tc.text)
		press(m, tea.KeyEnter, 0)
		last := (*actions)[len(*actions)-1]
		if last.Kind != tc.kind || last.Channel != "#ops" || (tc.topic == nil) != (last.Topic == nil) || tc.topic != nil && *last.Topic != *tc.topic {
			t.Fatalf("%q: %+v topic=%q draft=%q notice=%q", tc.text, last, deref(last.Topic), m.Draft(), m.notice)
		}
		m.Update(ControlMsg{ID: last.ID, Channel: "#ops", Kind: last.Kind, State: "confirmed"})
	}
	paste(m, "/join x\nmore text")
	m.Update(MembershipMsg{Channel: "#ops", State: "joined"})
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 1 || (*sends)[0].Text != "/join x\nmore text" {
		t.Fatalf("a multiline draft is a message, not a command: %+v", *sends)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#ops", MessageID: "o1", State: Confirmed})
	paste(m, "/topic set x")
	press(m, tea.KeyEnter, 0)
	m.Update(ControlMsg{ID: (*actions)[len(*actions)-1].ID, State: "uncertain"})
	_ = m.SetDraft("/part")
	press(m, tea.KeyEnter, 0)
	if !strings.Contains(m.notice, "uncertain") || m.Draft() != "/part" {
		t.Fatalf("an uncertain action blocks the next: %q", m.notice)
	}
	m.Update(ConnectionMsg{})
	if m.pending != nil {
		t.Fatal("a new stream starts without the old pending action")
	}
}

func ptr(s string) *string { return &s }

func TestCatalogGenerationAndKickAreRespected(t *testing.T) {
	m, _, actions := channelModel(t)
	m.Update(CatalogMsg{Generation: 3, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}, {Name: "#ops"}}})
	m.Update(CatalogMsg{Generation: 2, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}}})
	if strings.Join(m.order, " ") != "#space #build #ops" || !strings.Contains(m.notice, "older channel catalog") {
		t.Fatalf("an older catalog must be ignored: %v %q", m.order, m.notice)
	}
	m.Update(MembershipMsg{Channel: "#build", State: "kicked"})
	if m.views["#build"].membership != "kicked" || len(*actions) != 0 {
		t.Fatal("a kick is recorded and never rejoined")
	}
}

// A member enrolled while the view is open becomes trusted from the host's
// catalog, not from its nick; an older catalog cannot grant trust and a
// newer one without the member removes it.
func TestTrustedIdentitiesFollowTheCatalog(t *testing.T) {
	m, _, _ := channelModel(t)
	refreshes := 0
	m.opts.RefreshChannels = func() tea.Cmd { refreshes++; return nil }
	m.opts.Identities = map[string]Identity{"mhuman": {Name: "human", Human: true}}
	m.Update(MembersMsg{Channel: "#space", Members: []Member{{Nick: "human", Account: "mhuman"}, {Nick: "opus2", Account: "mopus2"}}})
	m.Update(IncomingMsg{Message: Message{ID: "n1", Channel: "#space", Nick: "opus2", Account: "mopus2", Text: "hello"}})
	if refreshes != 1 || m.identify("opus2", "mopus2").trusted {
		t.Fatalf("an unknown account asks for one catalog refresh and stays unverified: %d", refreshes)
	}
	trusted := map[string]Identity{"mhuman": {Name: "human", Human: true}, "mopus2": {Name: "opus2", Box: true}}
	m.Update(CatalogMsg{Generation: 5, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}}, Identities: trusted})
	if w := m.identify("opus2", "mopus2"); !w.trusted || !w.box || !strings.Contains(view(m), "opus2  ") || strings.Contains(view(m), "unverified") {
		t.Fatalf("the refreshed catalog must trust the new member:\n%s", view(m))
	}
	m.Update(CatalogMsg{Generation: 4, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}}, Identities: map[string]Identity{"mevil": {Name: "human", Human: true}}})
	if _, ok := m.opts.Identities["mevil"]; ok || !m.identify("opus2", "mopus2").trusted {
		t.Fatal("an older catalog must not change trust")
	}
	m.Update(CatalogMsg{Generation: 6, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}}, Identities: map[string]Identity{"mhuman": {Name: "human", Human: true}}})
	if m.identify("opus2", "mopus2").trusted {
		t.Fatal("a revoked member must lose trust")
	}
}

func TestConnectionGapMarksEveryChannel(t *testing.T) {
	m, _, _ := channelModel(t)
	m.Update(GapMsg{Text: "read buffer overflowed"})
	for _, name := range m.order {
		v := m.views[name]
		if len(v.presence) == 0 || v.presence[len(v.presence)-1].kind != presenceGap {
			t.Fatalf("%s did not record the gap", name)
		}
	}
}

// Past the whole-view bounds hidden channels give up their oldest messages
// first; the visible channel and drafts are kept.
func TestAggregateBoundsTrimHiddenChannelsFirst(t *testing.T) {
	m, _, _ := channelModel(t)
	paste(m, "kept draft")
	// The visible channel is the largest, so only the hidden-first rule
	// (not "largest first") keeps it whole.
	for i := range 250 {
		m.Update(IncomingMsg{Message: Message{ID: fmt.Sprint("v", i), Nick: "x", Text: "x"}})
	}
	for c := range 4 {
		name := fmt.Sprintf("#c%d", c)
		for i := range 200 {
			m.Update(IncomingMsg{Message: Message{ID: fmt.Sprintf("%s-%d", name, i), Channel: name, Nick: "x", Text: "x"}})
		}
	}
	total := 0
	trimmed := false
	for _, v := range m.views {
		total += len(v.messages)
		if v != m.cur && v.historyEnd && v.olderSkipped > 0 {
			trimmed = true
		}
	}
	if total > maxTotalMessages || len(m.messages) != 250 || !trimmed || m.Draft() != "kept draft" {
		t.Fatalf("aggregate trim: total=%d visible=%d trimmed=%v", total, len(m.messages), trimmed)
	}
}

func TestOlderPagesBelongToTheirChannel(t *testing.T) {
	m, _, _ := channelModel(t)
	var requests []HistoryRequest
	m.opts.LoadOlder = func(r HistoryRequest) tea.Cmd { requests = append(requests, r); return nil }
	alt(m, '2')
	m.Update(IncomingMsg{Message: Message{ID: "b1", Channel: "#build", Nick: "x", Text: "first", Replay: true}})
	press(m, tea.KeyPgUp, 0)
	if len(requests) != 1 || requests[0].Channel != "#build" || requests[0].Before != "b1" || requests[0].ID == "" {
		t.Fatalf("history request: %+v", requests)
	}
	m.Update(HistoryPageMsg{Channel: "#build", ID: "someone-else", Count: 0})
	if !m.loadingOlder {
		t.Fatal("a page for another request must not finish this one")
	}
	m.Update(HistoryPageMsg{Channel: "#build", ID: requests[0].ID, Count: 0})
	if m.loadingOlder || !m.historyEnd {
		t.Fatal("the matching page ends paging for #build")
	}
	if m.views["#space"].historyEnd {
		t.Fatal("#space paging is independent")
	}
}

// Notices about hidden channels stay out of the footer unless they are
// errors, such as a failed send there.
func TestHiddenChannelNoticesStayQuiet(t *testing.T) {
	m, sends, _ := channelModel(t)
	alt(m, '2')
	paste(m, "to build")
	press(m, tea.KeyEnter, 0)
	alt(m, '1')
	m.notice = ""
	m.Update(IncomingMsg{Message: Message{ID: "dup", Channel: "#build", Nick: "x", Text: "a"}})
	m.Update(IncomingMsg{Message: Message{ID: "dup", Channel: "#build", Nick: "x", Text: "b"}})
	if m.notice != "" {
		t.Fatalf("a hidden channel's warning leaked: %q", m.notice)
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#build", State: Rejected})
	if !strings.Contains(m.notice, "rejected") {
		t.Fatalf("a failed send in another channel must be visible: %q", m.notice)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// A send the stream never read is rejected after the disconnect marked it
// uncertain: the draft stays and sending is possible again after reconnect.
func TestUnsentAfterDisconnectIsRejectedNotStuck(t *testing.T) {
	m, sends, _ := channelModel(t)
	paste(m, "queued only")
	press(m, tea.KeyEnter, 0)
	m.Update(ConnectionMsg{})
	if m.Messages()[0].Delivery != Uncertain {
		t.Fatal("fixture: disconnect marks the send uncertain")
	}
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, Channel: "#space", State: Rejected})
	m.Update(ConnectionMsg{Connected: true})
	if m.inflight != "" || m.Draft() != "queued only" || m.Messages()[0].Delivery != Rejected {
		t.Fatalf("rejection must resolve the send and keep the draft: inflight=%q draft=%q", m.inflight, m.Draft())
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 2 {
		t.Fatal("sending must be possible again")
	}
}

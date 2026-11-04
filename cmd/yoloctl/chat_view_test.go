package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/termio"
)

const (
	viewRoomID   = "00d80cf1b45f441789b2a12499119956"
	viewMemberID = "c038734ffc305c288f62b94250797484"
	viewBoxID    = "b4a77641e96deeb994b8363278a80d88"
)

func viewHeader() chatstream.Header {
	return chatstream.Header{Bootstrap: managedchat.Bootstrap{Identities: []managedchat.BootstrapIdentity{
		{ID: "h", Account: "mhuman", Name: "lee", Nick: "leogr", Role: "human"},
		{ID: viewMemberID, Account: "mcoder", Name: "coder", BoxID: viewBoxID, Role: "member"},
		{ID: "gone", Account: "mgone", Name: "old", BoxID: "x", Role: "member", Revoked: true},
	}}}
}

// Only the trusted identity map names people; only an enrolled member's
// immutable ID can be resolved for attachment, and revoked ones are absent.
// The saved nick travels with the identity: each view's live alias differs.
func TestChatIdentitiesComeFromTheTrustedMap(t *testing.T) {
	ids, members := chatIdentities(viewHeader())
	if !ids["mhuman"].Human || ids["mhuman"].Box || ids["mhuman"].Nick != "leogr" || !ids["mcoder"].Box || ids["mcoder"].Name != "coder" {
		t.Fatalf("identities: %+v", ids)
	}
	if _, ok := ids["mgone"]; ok || len(members) != 1 || members["mcoder"] != viewMemberID {
		t.Fatalf("members: %+v %+v", ids, members)
	}
	s := &chatSession{members: members}
	if msg := s.resolveAttach(nil, "mhuman").(chatui.AttachTargetMsg); msg.Err == nil {
		t.Fatal("a human account must not resolve to a box")
	}
}

// Agent glyphs follow the declaration only while its ID matches the member.
func TestChatAgentsMatchTheMemberDeclarationID(t *testing.T) {
	view := []controllerBoxView{
		{Config: &agentbox.Config{Name: "coder", ID: viewBoxID, Agent: "claude"}},
		{Config: &agentbox.Config{Name: "old", ID: "x", Agent: "codex"}},
		{Config: &agentbox.Config{Name: "lee", ID: "other", Agent: "codex"}},
	}
	agents := chatAgents(viewHeader(), view)
	if len(agents) != 1 || agents["mcoder"] != "claude" {
		t.Fatalf("agents: %v", agents)
	}
	view[0].Config.ID = "replaced"
	if agents := chatAgents(viewHeader(), view); len(agents) != 0 {
		t.Fatalf("a replaced declaration kept its glyph: %v", agents)
	}
}

// A matching declaration without a running container asks before starting;
// only a deleted/replaced or revoked member is refused.
func TestAttachTargetViewOffersStartForAMatchingBox(t *testing.T) {
	base := managedchat.AttachTarget{RoomID: viewRoomID, MemberID: viewMemberID, Box: "coder", BoxID: viewBoxID}
	for _, tc := range []struct {
		state, box, want string
	}{
		{"running", "coder", "running"},
		{"stopped", "coder", "stopped"},
		{"missing", "coder", "stopped"},
		{"missing", "", "missing"},
		{"revoked", "", "revoked"},
	} {
		in := base
		in.State, in.Box = tc.state, tc.box
		got := attachTargetView("mcoder", in)
		if got.State != tc.want || (tc.want == "missing" || tc.want == "revoked") && (got.Box != "" || got.Reason == "") {
			t.Fatalf("%s/%q: %+v", tc.state, tc.box, got)
		}
		if tc.want == "stopped" {
			req := termio.TabRequest{Op: "attach", Space: "dev", Box: got.Box, ChatRoomID: got.RoomID, ChatMemberID: got.MemberID, ChatBoxID: got.BoxID, ChatAllowStart: got.State == "stopped"}
			if req.ValidateChat() != nil || !req.ChatAllowStart {
				t.Fatalf("%s: a confirmed start must carry --chat-allow-start: %+v", tc.state, req)
			}
		}
	}
}

// Inside the TUI a resolved participant becomes one typed, guarded request;
// an incomplete resolution never becomes a request.
func TestChatAttachSendsAGuardedTabRequest(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	attach := chatAttacher("dev", w)
	target := chatui.AttachTarget{Account: "mcoder", Box: "coder", BoxID: viewBoxID, State: "stopped", RoomID: viewRoomID, MemberID: viewMemberID}
	if msg := attach(target)().(chatui.ActionResultMsg); msg.Err != nil {
		t.Fatal(msg.Err)
	}
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var req termio.TabRequest
	if err := json.Unmarshal(line, &req); err != nil {
		t.Fatal(err)
	}
	want := termio.TabRequest{Op: "attach", Space: "dev", Box: "coder", ChatRoomID: viewRoomID, ChatMemberID: viewMemberID, ChatBoxID: viewBoxID, ChatAllowStart: true}
	if req != want || req.ValidateChat() != nil {
		t.Fatalf("request: %+v", req)
	}
	if args := strings.Join(req.ChatAttachArgs(), " "); !strings.Contains(args, "--chat-allow-start") || !strings.HasPrefix(args, "box attach coder --space dev ") {
		t.Fatalf("argv: %s", args)
	}
	target.MemberID = ""
	if msg := attach(target)().(chatui.ActionResultMsg); msg.Err == nil {
		t.Fatal("a partial identity must not become a request")
	}
}

// A driver-validated catalog replaces the view's trusted identity map and
// the host's attach member map: new members become trusted and attachable,
// revoked ones lose both. Nothing comes from nicks.
func TestCatalogReplacesTrustedIdentitiesAndMembers(t *testing.T) {
	header := viewHeader()
	header.Generation, header.DefaultChannel = 7, "#space"
	header.Channels = []managedchat.Channel{{ID: "c1", Name: "#space", Members: []string{viewMemberID}}, {ID: "c2", Name: "#build"}}
	header.Identities = append(header.Identities, managedchat.BootstrapIdentity{ID: "new", Account: "mnew", Name: "opus2", BoxID: "b2", Role: "member"})
	msg := chatCatalog(chatstream.Catalog{Header: header, RuntimeApplied: true})
	if msg.Generation != 7 || msg.Default != "#space" || len(msg.Channels) != 2 || msg.Channels[0].Members != 1 || !msg.Applied {
		t.Fatalf("catalog: %+v", msg)
	}
	if !msg.Identities["mnew"].Box || msg.Identities["mnew"].Name != "opus2" {
		t.Fatalf("a newly enrolled member must be trusted: %+v", msg.Identities)
	}
	if _, ok := msg.Identities["mgone"]; ok {
		t.Fatal("a revoked identity must not be trusted")
	}
	s := &chatSession{members: map[string]string{"mcoder": viewMemberID, "mstale": "old"}}
	s.adoptCatalogFor(nil, header)
	if s.members["mnew"] != "new" || s.members["mcoder"] != viewMemberID {
		t.Fatalf("attach members follow the catalog: %v", s.members)
	}
	if _, ok := s.members["mstale"]; ok {
		t.Fatal("a member absent from the catalog must not stay attachable")
	}
	legacy := chatCatalog(chatstream.Catalog{Header: chatstream.Header{Channel: "#space"}})
	if len(legacy.Channels) != 1 || legacy.Channels[0].Name != "#space" {
		t.Fatalf("a legacy header still names its channel: %+v", legacy)
	}
}

func TestChannelEventsKeepTheirChannel(t *testing.T) {
	for _, tc := range []struct {
		event chatstream.Event
		check func(tea.Msg) bool
	}{
		{chatstream.Event{Kind: "membership", Channel: "#build", State: "kicked", ID: "a1"}, func(m tea.Msg) bool {
			v, ok := m.(chatui.MembershipMsg)
			return ok && v.Channel == "#build" && v.State == "kicked" && v.ID == "a1"
		}},
		{chatstream.Event{Kind: "control", Channel: "#build", ID: "a2", Action: "topic-set", State: "uncertain"}, func(m tea.Msg) bool {
			v, ok := m.(chatui.ControlMsg)
			return ok && v.Kind == "topic-set" && v.State == "uncertain" && v.ID == "a2"
		}},
		{chatstream.Event{Kind: "topic", Channel: "#build", State: "unknown"}, func(m tea.Msg) bool {
			v, ok := m.(chatui.TopicMsg)
			return ok && v.Unknown && v.Channel == "#build"
		}},
		{chatstream.Event{Kind: "history-page", Channel: "#build", ID: "h1", Count: 3}, func(m tea.Msg) bool {
			v, ok := m.(chatui.HistoryPageMsg)
			return ok && v.ID == "h1" && v.Channel == "#build" && v.Count == 3
		}},
		{chatstream.Event{Kind: "message", Channel: "#build", Message: chatstream.Message{ID: "m1", Text: "x"}}, func(m tea.Msg) bool {
			v, ok := m.(chatui.IncomingMsg)
			return ok && v.Message.Channel == "#build"
		}},
		{chatstream.Event{Kind: "catalog", Err: errors.New("stale generation")}, func(m tea.Msg) bool {
			v, ok := m.(chatui.ActionResultMsg)
			return ok && v.Err != nil && strings.Contains(v.Err.Error(), "stale generation")
		}},
	} {
		if got := chatViewEvent(tc.event); !tc.check(got) {
			t.Fatalf("%s: %#v", tc.event.Kind, got)
		}
	}
}

// Requests the ended stream never read come back as not sent, so a queued
// message is rejected (draft kept) instead of staying uncertain forever.
func TestEndedStreamReportsUnreadRequestsAsNotSent(t *testing.T) {
	l := &chatLink{outgoing: make(chan chatstream.Send, 1), older: make(chan chatstream.HistoryRequest, 1), actions: make(chan chatstream.Action, 1)}
	l.outgoing <- chatstream.Send{ID: "s1", Channel: "#build", Text: "x"}
	l.actions <- chatstream.Action{ID: "a1", Kind: "join", Channel: "#ops"}
	l.older <- chatstream.HistoryRequest{ID: "h1", Channel: "#build", Before: "m1"}
	got := l.close()
	if len(got) != 3 || len(l.unread()) != 0 {
		t.Fatalf("unread requests: %#v", got)
	}
	if queued, closed := admit(l, l.outgoing, chatstream.Send{ID: "late"}); queued || !closed {
		t.Fatal("a closed link must refuse new requests instead of losing them")
	}
	for _, msg := range got {
		switch v := msg.(type) {
		case chatui.DeliveryMsg:
			if v.ID != "s1" || v.Channel != "#build" || v.State != chatui.Rejected || v.Err == nil {
				t.Fatalf("send: %+v", v)
			}
		case chatui.ControlMsg:
			if v.ID != "a1" || v.State != "refused" {
				t.Fatalf("action: %+v", v)
			}
		case chatui.HistoryPageMsg:
			if v.ID != "h1" || v.Count != -1 {
				t.Fatalf("history: %+v", v)
			}
		default:
			t.Fatalf("unexpected %#v", msg)
		}
	}
}

// Queued catalog snapshots coalesce only within one room identity: the
// higher generation wins, an equal generation may only change its runtime
// observation, and identity or equal-generation conflicts are reported while
// the queued snapshot stays for the driver to judge.
func TestCatalogQueueCoalescesOnlyCompatibleSnapshots(t *testing.T) {
	snap := func(room string, generation uint64, nick string, applied bool) chatstream.Catalog {
		return chatstream.Catalog{RuntimeApplied: applied, Header: chatstream.Header{Nick: nick, Bootstrap: managedchat.Bootstrap{RoomID: room, DiskID: "d", SelfAccount: "me", Generation: generation}}}
	}
	l := &chatLink{catalogs: make(chan chatstream.Catalog, 1)}
	for _, generation := range []uint64{3, 5, 4} {
		if err := l.offerCatalog(snap("r", generation, "n", false)); err != nil {
			t.Fatal(err)
		}
	}
	if got := <-l.catalogs; got.Header.Generation != 5 || len(l.catalogs) != 0 {
		t.Fatalf("out-of-order refreshes left generation %d", got.Header.Generation)
	}
	_ = l.offerCatalog(snap("r", 6, "n", false))
	if err := l.offerCatalog(snap("r", 6, "n", true)); err != nil {
		t.Fatal("a newer runtime observation of the same header must coalesce", err)
	}
	if got := <-l.catalogs; !got.RuntimeApplied {
		t.Fatal("the newer observation must win at an equal header")
	}
	_ = l.offerCatalog(snap("r", 7, "n", false))
	if err := l.offerCatalog(snap("r", 7, "other", false)); err == nil {
		t.Fatal("an equal generation with a different header must be reported")
	}
	if err := l.offerCatalog(snap("foreign", 9, "n", false)); err == nil {
		t.Fatal("a different room identity must be reported, not coalesced")
	}
	if got := <-l.catalogs; got.Header.RoomID != "r" || got.Header.Generation != 7 || got.Header.Nick != "n" {
		t.Fatalf("a conflict replaced the queued snapshot: %+v", got.Header)
	}
	l.close()
	if err := l.offerCatalog(snap("r", 9, "n", false)); err != nil || len(l.catalogs) != 0 {
		t.Fatal("a closed link must not queue a catalog")
	}
}

package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestOnlySpaceViewAndChatTabsGetRequestChannel(t *testing.T) {
	m := &model{}
	for _, args := range [][]string{{"space", "view"}, {"sp", "view", "dev"}, {"chat"}, {"chat", "--space", "dev"}} {
		if m.tabRequestHandler(args, new(atomic.Pointer[term.Session])) == nil {
			t.Errorf("%v has no request channel", args)
		}
	}
	for _, args := range [][]string{{"box", "attach", "x"}, {"space", "ssh"}, {"space"}, {"box", "view"}, {"chat", "status"}, {"chat", "--help"}} {
		if m.tabRequestHandler(args, new(atomic.Pointer[term.Session])) != nil {
			t.Errorf("%v was offered a request channel", args)
		}
	}
}

// A request can only ever become `box attach NAME --space SPACE` with
// validated names; anything else is refused without opening a tab.
func TestTabRequestValidation(t *testing.T) {
	for _, r := range []termio.TabRequest{
		{Op: "exec", Space: "dev", Box: "api"},
		{Op: "attach", Space: "dev", Box: "--help"},
		{Op: "attach", Space: "dev", Box: "a b"},
		{Op: "attach", Space: "../dev", Box: "api"},
		{Op: "attach", Space: "dev", Box: ""},
	} {
		m := &model{}
		m.handleTabRequest(r, false, nil)
		if len(m.tabs.Tabs) != 0 || m.status == "" {
			t.Errorf("request %+v: tabs=%d status=%q", r, len(m.tabs.Tabs), m.status)
		}
	}
}

func TestTabRequestReusesExistingAttachTab(t *testing.T) {
	m := &model{focusPalette: true}
	m.tabs.Add(testTab(nil, "yoloctl space view", false))
	m.tabs.Add(testTab(nil, "yoloctl box attach api --space dev", false))
	m.tabs.Select(0)
	m.handleTabRequest(termio.TabRequest{Op: "attach", Space: "dev", Box: "api"}, false, nil)
	if m.tabs.Active != 1 || m.focusPalette || len(m.tabs.Tabs) != 2 {
		t.Fatalf("active=%d palette=%v tabs=%d", m.tabs.Active, m.focusPalette, len(m.tabs.Tabs))
	}
}

const (
	testRoomID   = "00d80cf1b45f441789b2a12499119956"
	testMemberID = "c038734ffc305c288f62b94250797484"
	testBoxID    = "b4a77641e96deeb994b8363278a80d88"
)

// Chat tabs may only ask for a complete guarded participant attach, and
// space views only for a plain one; nothing partial is downgraded.
func TestChatTabRequestsMustCarryEveryIdentity(t *testing.T) {
	guarded := termio.TabRequest{Op: "attach", Space: "dev", Box: "api", ChatRoomID: testRoomID, ChatMemberID: testMemberID, ChatBoxID: testBoxID}
	for _, tc := range []struct {
		r    termio.TabRequest
		chat bool
	}{
		{termio.TabRequest{Op: "attach", Space: "dev", Box: "api"}, true},
		{termio.TabRequest{Op: "attach", Space: "dev", Box: "api", ChatRoomID: testRoomID, ChatMemberID: testMemberID}, true},
		{termio.TabRequest{Op: "attach", Space: "dev", Box: "api", ChatRoomID: testRoomID, ChatMemberID: testMemberID, ChatBoxID: "B4A77641E96DEEB994B8363278A80D88"}, true},
		{termio.TabRequest{Op: "attach", Space: "dev", Box: "--help", ChatRoomID: testRoomID, ChatMemberID: testMemberID, ChatBoxID: testBoxID}, true},
		{termio.TabRequest{Op: "exec", Space: "dev", Box: "api", ChatRoomID: testRoomID, ChatMemberID: testMemberID, ChatBoxID: testBoxID}, true},
		{guarded, false},
		{termio.TabRequest{Op: "attach", Space: "dev", Box: "api", ChatAllowStart: true}, false},
	} {
		m := &model{}
		m.handleTabRequest(tc.r, tc.chat, nil)
		if len(m.tabs.Tabs) != 0 || m.status == "" {
			t.Errorf("request %+v from chat=%v: tabs=%d status=%q", tc.r, tc.chat, len(m.tabs.Tabs), m.status)
		}
	}
	want := "box attach api --space dev --chat-room-id " + testRoomID + " --chat-member-id " + testMemberID + " --chat-box-id " + testBoxID
	if got := strings.Join(guarded.ChatAttachArgs(), " "); got != want {
		t.Fatalf("guarded argv: %s", got)
	}
	guarded.ChatAllowStart = true
	if args := guarded.ChatAttachArgs(); args[len(args)-1] != "--chat-allow-start" {
		t.Fatalf("confirmed start is not carried: %v", args)
	}
}

// A chat attach reuses only a tab with the identical guarded argv; a plain
// attach tab of the same box name is not proof of the resolved identity.
func TestChatTabRequestReusesOnlyTheSameGuardedAttach(t *testing.T) {
	r := termio.TabRequest{Op: "attach", Space: "dev", Box: "api", ChatRoomID: testRoomID, ChatMemberID: testMemberID, ChatBoxID: testBoxID}
	m := &model{focusPalette: true}
	m.tabs.Add(testTab(nil, "yoloctl chat", false))
	m.tabs.Add(testTab(nil, "yoloctl "+strings.Join(r.ChatAttachArgs(), " "), false))
	m.tabs.Select(0)
	m.handleTabRequest(r, true, nil)
	if m.tabs.Active != 1 || m.focusPalette || len(m.tabs.Tabs) != 2 {
		t.Fatalf("identical guarded attach was not reused: active=%d tabs=%d", m.tabs.Active, len(m.tabs.Tabs))
	}
	if plain := "yoloctl box attach api --space dev"; plain == "yoloctl "+strings.Join(r.ChatAttachArgs(), " ") {
		t.Fatal("a plain attach tab would satisfy a guarded request")
	}
}

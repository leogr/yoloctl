package main

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// A stale active membership is never revoked implicitly; status and the enroll
// refusal name the exact command for that immutable member.
func TestStaleMemberMessagesNameTheExactRevokeCommand(t *testing.T) {
	const member = "0123456789abcdef0123456789abcdef"
	want := "declaration identity is missing or its name was reused; if this membership is stale, run yoloctl chat revoke " + member + " --space yolospace"
	if got := staleMemberReason(member, "yolospace"); got != want {
		t.Fatalf("status reason\n got %q\nwant %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("YOLOCTL_HOME", "")
	cfg := &agentbox.Config{Name: "sol2", ID: agentbox.NewIdentity(), Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("sol2")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	r := &managedchat.Room{Generation: 7, Server: managedchat.Box{Name: "chat"}, Human: managedchat.Member{Name: "leogr"},
		Members: []managedchat.Member{{ID: member, Name: "sol2", BoxID: agentbox.NewIdentity()}}}
	inventory := managedchat.Inventory{Declarations: map[string]string{"sol2": cfg.ID}}
	_, err := resolveChatEnrollment("work", "sol2", r, 7, inventory)
	refusal := "box sol2's previous membership (member " + member + ") is still active; if it is stale, run yoloctl chat revoke " + member + " --space work, then enroll again"
	if err == nil || err.Error() != refusal {
		t.Fatalf("enroll refusal\n got %v\nwant %s", err, refusal)
	}
	// After the explicit revoke, the same enrollment resolves the new box.
	r.Members[0].Revoked = true
	got, err := resolveChatEnrollment("work", "sol2", r, 7, inventory)
	if err != nil || got == nil || got.ID != cfg.ID {
		t.Fatalf("enroll after revoke: %+v %v", got, err)
	}
	if strings.Contains(staleMemberReason(member, "work"), "revoke this immutable member explicitly") {
		t.Fatal("old wording kept")
	}
}

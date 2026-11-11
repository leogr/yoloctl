package main

import (
	"bytes"
	"testing"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestChatRevokeByNameNamesTheResolvedID(t *testing.T) {
	older, newest := managedchat.NewID(), managedchat.NewID()
	status := managedchat.Status{Room: &managedchat.Room{Members: []managedchat.Member{
		{ID: older, Name: "wk1", Revoked: true},
		{ID: managedchat.NewID(), Name: "other"},
		{ID: newest, Name: "wk1", Revoked: true},
	}}}
	for _, tc := range []struct {
		arg  string
		json bool
		want string
	}{
		// The newest same-name membership is the one the name just revoked.
		{"wk1", false, "chat: Revoked chat member wk1 (ID " + newest + ").\n"},
		{newest, false, ""}, // an ID argument keeps the plain status output
		{"wk1", true, ""},   // JSON stays one document
	} {
		cmd := newChatCmd()
		revoke, _, err := cmd.Find([]string{"revoke"})
		if err != nil {
			t.Fatal(err)
		}
		if tc.json {
			if err := revoke.Flags().Set("json", "true"); err != nil {
				t.Fatal(err)
			}
		}
		var out, logs bytes.Buffer
		t.Cleanup(bindCLITestLogger(t, &logs))
		revoke.SetOut(&out)
		if err := writeChatRevokedName(revoke, tc.arg, status); err != nil || out.Len() != 0 || logs.String() != tc.want {
			t.Fatalf("revoke %q json=%v: %q, %v; want %q", tc.arg, tc.json, logs.String(), err, tc.want)
		}
	}
}

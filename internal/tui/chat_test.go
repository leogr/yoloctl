package tui

import "testing"

func TestManagedChatLiveSurfaceRouting(t *testing.T) {
	for _, tc := range []struct {
		args []string
		live bool
	}{
		{[]string{"chat"}, true},
		{[]string{"chat", "--space", "work", "--box=room", "--human", "person"}, true},
		{[]string{"chat", "status"}, false},
		{[]string{"chat", "ensure", "--box", "room"}, false},
		{[]string{"chat", "--space", "work", "enroll", "coder"}, false},
		{[]string{"chat", "revoke", "immutable-id"}, false},
		{[]string{"chat", "--help"}, false},
		{[]string{"chat", "--space"}, false},
		{[]string{"chat", "--unknown"}, false},
	} {
		if got := opensLiveSurface(tc.args); got != tc.live {
			t.Fatal(tc.args, got)
		}
	}
}

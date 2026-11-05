package tui

import "testing"

func TestAuditLiveSurfaceRouting(t *testing.T) {
	for _, tc := range []struct {
		args []string
		live bool
	}{
		{[]string{"audit"}, true},
		{[]string{"audit", "--space", "test"}, true},
		{[]string{"audit", "status"}, false},
		{[]string{"audit", "enable"}, false},
		{[]string{"audit", "disable"}, false},
		{[]string{"audit", "export", "--output", "audit.jsonl"}, false},
		{[]string{"audit", "logs"}, false},
		{[]string{"audit", "logs", "--follow"}, true},
		{[]string{"audit", "logs", "--follow=true", "--json"}, true},
		{[]string{"audit", "logs", "--follow=false"}, false},
		{[]string{"audit", "logs", "--follow", "--follow=false"}, false},
		{[]string{"audit", "--help"}, false},
		{[]string{"audit", "logs", "--follow", "--help"}, false},
	} {
		if got := opensLiveSurface(tc.args); got != tc.live {
			t.Errorf("%v: got %v want %v", tc.args, got, tc.live)
		}
	}
}

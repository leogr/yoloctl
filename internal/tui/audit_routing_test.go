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

func TestAuditLiveSurfaceConsumesInheritedLogFlagValues(t *testing.T) {
	for _, flags := range [][]string{
		{"--log-level", "debug"}, {"--log-level=debug"},
		{"--log-level", "error"}, {"--color", "never"}, {"--color=never"},
		{"--color", "always", "--log-level", "warn"},
	} {
		for _, tc := range []struct {
			command []string
			live    bool
		}{
			{nil, true}, {[]string{"logs", "--follow"}, true},
			{[]string{"logs", "--follow=false"}, false}, {[]string{"logs"}, false},
			{[]string{"status"}, false}, {[]string{"enable"}, false},
			{[]string{"--help"}, false}, {[]string{"logs", "--follow", "--help"}, false},
		} {
			for _, before := range []bool{false, true} {
				args := []string{"audit"}
				if before {
					args = append(args, flags...)
					args = append(args, tc.command...)
				} else {
					args = append(args, tc.command...)
					args = append(args, flags...)
				}
				if got := opensLiveSurface(args); got != tc.live {
					t.Fatalf("%v: got %v want %v", args, got, tc.live)
				}
			}
		}
	}
}

package tui

import (
	"slices"
	"strings"
	"testing"
)

func TestParseReopenView(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		ok         bool
		space      string
		positional bool
	}{
		{[]string{"chat"}, true, "", false},
		{[]string{"attach", "api", "--new-session", "--no-autoupdate"}, true, "", false},
		{[]string{"box", "attach", "api", "--space=dev", "--passthrough", "--chat-room-id", "r", "--chat-member-id", "m", "--chat-box-id", "b", "--chat-allow-start"}, true, "dev", false},
		{[]string{"box", "attach", "api", "--box-use=false"}, false, "", false},
		{[]string{"chat", "--space=dev", "--human", "leo"}, true, "dev", false},
		{[]string{"box", "logs", "api", "-f", "--space", "dev"}, true, "dev", false},
		{[]string{"space", "logs", "-fn", "25", "dev"}, true, "dev", true},
		{[]string{"space", "logs", "-f", "--follow=false"}, false, "", false},
		{[]string{"space", "logs", "--follow=false", "-f"}, true, "", true},
		{[]string{"space", "logs", "--follow=bad", "-f"}, false, "", false},
		{[]string{"space", "logs", "--", "-f"}, false, "", false},
		{[]string{"space", "view", "--interval", "2s", "--no-motion", "dev"}, true, "dev", true},
		{[]string{"sp", "view"}, true, "", true},
		{[]string{"space", "ssh", "dev", "--root", "--start"}, true, "dev", true},
		{[]string{"sp", "shell", "--root"}, true, "", true},
		{[]string{"space", "ssh", "dev", "--"}, true, "dev", true},
		{[]string{"space", "ssh", "--", "uname"}, false, "", false},
		{[]string{"space", "ssh", "dev", "--", "bash"}, false, "", false},
		{[]string{"space", "ssh", "dev", "prod"}, false, "", false},
		{[]string{"box", "exec", "api", "--", "bash", "-il"}, true, "", false},
		{[]string{"box", "exec", "api", "--space=dev", "--", "/bin/sh", "-i"}, true, "dev", false},
		{[]string{"box", "exec", "api", "bash"}, true, "", false},
		{[]string{"box", "exec", "api", "--", "/usr/bin/fish", "--login"}, true, "", false},
		{[]string{"box", "exec", "api", "--", "bash", "-c", "echo hi"}, false, "", false},
		{[]string{"box", "exec", "api", "--", "bash", "-s"}, false, "", false},
		{[]string{"box", "exec", "api", "--", "bash", "script.sh"}, false, "", false},
		{[]string{"box", "exec", "api", "--", "bash", "--rcfile", "x"}, false, "", false},
		{[]string{"box", "exec", "api", "--", "/tmp/bash"}, false, "", false},
		{[]string{"box", "exec", "api", "--", "echo", "hi"}, false, "", false},
		{[]string{"box", "exec", "api", "bash", "-i"}, false, "", false},
		{[]string{"box", "start", "api"}, false, "", false},
		{[]string{"box", "edit", "api"}, false, "", false},
		{[]string{"account", "secret", "set", "x"}, false, "", false},
		{[]string{"chat", "status"}, false, "", false},
		{[]string{"space", "logs"}, false, "", false},
		{[]string{"space", "view", "--help"}, false, "", false},
		{nil, false, "", false},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			got, ok := parseReopenView(tc.argv)
			if ok != tc.ok {
				t.Fatalf("eligible=%v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.space != tc.space || got.positionalSpace != tc.positional || !slices.Equal(got.args, tc.argv) {
				t.Fatalf("parsed %+v", got)
			}
		})
	}
}

func TestPinReopenArgsRetainsLaunchSpace(t *testing.T) {
	for _, tc := range []struct{ args, want []string }{
		{[]string{"chat"}, []string{"chat", "--space", "first"}},
		{[]string{"attach", "api", "--new-session"}, []string{"attach", "api", "--new-session", "--space", "first"}},
		{[]string{"box", "attach", "--chat-room-id", "r", "--chat-member-id", "m", "--chat-box-id", "b", "--chat-allow-start", "--", "api"}, []string{"box", "attach", "--chat-room-id", "r", "--chat-member-id", "m", "--chat-box-id", "b", "--chat-allow-start", "--space", "first", "--", "api"}},
		{[]string{"box", "logs", "api", "-f"}, []string{"box", "logs", "api", "-f", "--space", "first"}},
		{[]string{"space", "logs", "-f"}, []string{"space", "logs", "-f", "first"}},
		{[]string{"sp", "view", "--no-motion"}, []string{"sp", "view", "--no-motion", "first"}},
		{[]string{"space", "shell", "--root", "--start", "--"}, []string{"space", "shell", "--root", "--start", "first", "--"}},
		{[]string{"box", "exec", "api", "--", "bash", "-il"}, []string{"box", "exec", "api", "--space", "first", "--", "bash", "-il"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			calls := 0
			got := pinReopenArgs(tc.args, func() (string, error) { calls++; return "first", nil })
			if calls != 1 || !slices.Equal(got, tc.want) {
				t.Fatalf("pin=%q calls=%d", got, calls)
			}
			// A later palette context is irrelevant to both the stored initial argv
			// and a repeated launch: it is already an explicit target.
			again := pinReopenArgs(got, func() (string, error) { calls++; return "other", nil })
			if calls != 1 || !slices.Equal(again, got) {
				t.Fatalf("reopen redirected: %q", again)
			}
		})
	}
}

func TestPinReopenArgsPreservesExplicitAndInvalid(t *testing.T) {
	for _, args := range [][]string{
		{"chat", "--space=dev"}, {"space", "view", "dev"}, {"space", "ssh", "dev", "--root"},
		{"chat", "--space", ""}, {"space", "view", "bad/name"},
		{"box", "exec", "api", "--", "bash", "-c", "echo hi"}, {"box", "edit", "api"},
		{"space", "ssh", "--", "uname"}, {"space", "logs"}, {"chat", "--unknown"},
	} {
		got := pinReopenArgs(args, func() (string, error) { t.Fatal("explicit or invalid argv consulted current context"); return "", nil })
		if !slices.Equal(got, args) {
			t.Fatalf("argv changed: %q -> %q", args, got)
		}
	}
}

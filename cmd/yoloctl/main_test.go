package main

import (
	"strings"
	"testing"
)

func TestUnknownCommandGroupSubcommandsFail(t *testing.T) {
	for _, args := range [][]string{
		{"port", "stop", "coder"}, // removed command must not silently succeed
		{"box", "bogus"},
		{"account", "secret", "bogus"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := execCmd(t, newRootCmd(), "", args...)
			if err == nil || !strings.Contains(err.Error(), "unknown command") || !IsUserError(err) {
				t.Fatalf("%q error = %v, want an unknown-command user error", args, err)
			}
			if code := classifyError(err); code != exitUser {
				t.Errorf("%q exit code = %d, want %d", args, code, exitUser)
			}
		})
	}

	out, _, err := execCmd(t, newRootCmd(), "", "port")
	if err != nil || !strings.Contains(out, "Available Commands:") {
		t.Errorf("bare port group should still show help: output %q, error %v", out, err)
	}
}

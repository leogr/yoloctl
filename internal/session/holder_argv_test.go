package session

import "testing"

func TestAgentLaunchSummaryHidesArguments(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"/usr/local/bin/claude", "--dangerously-skip-permissions", "secret prompt"}, "claude (2 arguments)"},
		{[]string{"codex", "resume"}, "codex (1 argument)"},
		{[]string{"pi"}, "pi (0 arguments)"},
		{nil, "no command"},
	} {
		if got := agentLaunchSummary(c.argv); got != c.want {
			t.Errorf("%v: got %q want %q", c.argv, got, c.want)
		}
	}
}

package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/termio"
)

// The post-attach reset fires exactly when an interactive attach ended on a
// screen the agent painted: every message printed next (Cobra's "Error:",
// the detach notice) must land on a fresh bottom line, not inside the dead
// frame. Embedded in a TUI tab it must stay on the screen the agent died on
// (switching would blank the tab and hide the final frame); standalone it
// must also return the host terminal to the main screen, with the park
// preceding ?1049l so an alt-screen death restores the saved main-screen
// cursor instead of gapping to the terminal bottom. Regression: box stop
// under an attached agent spliced "Error: session: …" into the agent's
// input box.
func TestPostAttachReset(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		err                    error
		tty, painted, embedded bool
		want                   bool
	}{
		{"holder loss in a TUI tab", session.ErrSessionClosed, true, true, true, true},
		{"holder loss standalone", session.ErrSessionClosed, true, true, false, true},
		{"agent nonzero exit", &session.ExitError{Status: 7}, true, true, false, true},
		{"detach", context.Canceled, true, true, false, true},
		{"clean exit prints nothing", nil, true, true, false, false},
		{"redirected stdout stays escape-free", session.ErrSessionClosed, false, true, false, false},
		{"pre-paint failure leaves the cursor alone", session.ErrSessionClosed, true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postAttachReset(tc.err, tc.tty, tc.painted, tc.embedded)
			if (got != "") != tc.want {
				t.Fatalf("postAttachReset(%v, tty=%v, painted=%v, embedded=%v) = %q, want emit=%v",
					tc.err, tc.tty, tc.painted, tc.embedded, got, tc.want)
			}
			if !tc.want {
				return
			}
			if !strings.HasPrefix(got, termio.SessionEndResetInPlace) {
				t.Error("reset payload must come first so the park precedes the fresh-line scroll")
			}
			if !strings.HasSuffix(got, "\r\n\r\n") {
				t.Error("missing the blank-line separator before the next printed message")
			}
			if leavesAlt := strings.Contains(got, "\x1b[?1049l"); leavesAlt == tc.embedded {
				t.Errorf("leaves alt screen = %v, want %v — embedded resets stay on the agent's final screen",
					leavesAlt, !tc.embedded)
			}
		})
	}
	// The park-before-restore ordering is what the host variant exists for.
	park, leave := strings.Index(termio.SessionEndResetHost, "\x1b[999;1H"), strings.Index(termio.SessionEndResetHost, "\x1b[?1049l")
	if park == -1 || leave == -1 || park > leave {
		t.Errorf("host reset must park (index %d) before leaving the alt screen (index %d)", park, leave)
	}
}

func TestPaintedWriterTracksFirstByte(t *testing.T) {
	var sink strings.Builder
	p := &paintedWriter{w: &sink}
	if p.painted() {
		t.Fatal("painted before any write")
	}
	if _, err := p.Write(nil); err != nil {
		t.Fatalf("empty write: %v", err)
	}
	if p.painted() {
		t.Error("zero-byte write must not count as painting")
	}
	if _, err := p.Write([]byte("frame")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !p.painted() {
		t.Error("write did not mark the terminal painted")
	}
	if sink.String() != "frame" {
		t.Errorf("passthrough mangled: %q", sink.String())
	}
}

// A launch carries the declared daemons of the box's agent to the holder;
// other agents and agentless boxes carry none.
func TestSessionDaemonsFollowTheAgentProfile(t *testing.T) {
	for agent, want := range map[string][]string{"codex": {"codex app-server"}, "claude": nil, account.None: nil} {
		got, err := sessionDaemons(&agentbox.Config{Agent: agent})
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q daemons = %q, %v; want %q", agent, got, err, want)
		}
	}
}

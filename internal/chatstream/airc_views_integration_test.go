package chatstream

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// One human session per saved nickname on the pinned AIRC, through the
// production launcher. A second view is refused distinctly (NICK_HELD)
// without disturbing the first. An explicit take-over closes the first view
// loudly and for good, and the new view holds the saved nickname. A different
// account holding it stays an ordinary 433, even for a take-over.
func TestRealAIRCHumanViewTakeOver(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the client launcher's inherited /proc/self/fd descriptor is Linux-only")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	f := newRealChannelFixture(t)
	p := f.peer("#space")
	human, err := headerHuman(f.header)
	if err != nil {
		t.Fatal(err)
	}
	view := func(takeOver bool, stderr *bytes.Buffer) *channelStream {
		t.Helper()
		argv := ClientArgv(python, filepath.Join(f.bin, "airc"), filepath.Join(f.dir, "human.toml"), human.Nick, takeOver)
		client := exec.Command(argv[0], argv[1:]...)
		client.Stderr = stderr
		return f.stream(f.header, client)
	}
	connected := func(h *channelStream, what string) {
		t.Helper()
		h.next(t, what+" connection", func(e Event) bool { return e.Kind == "connection" && e.Connected })
		h.next(t, what+" #space membership", func(e Event) bool {
			return e.Kind == "membership" && e.Channel == "#space" && e.State == "joined"
		})
	}
	refused := func(h *channelStream, stderr *bytes.Buffer, what, want string) {
		t.Helper()
		select {
		case err := <-h.done:
			// Wait for exec.Cmd to finish draining stderr before inspecting it.
			h.close()
			if err == nil || !strings.Contains(stderr.String(), want) {
				t.Fatalf("%s: registration must fail visibly with %q: %v: %s", what, want, err, stderr)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: the refused view did not end", what)
		}
	}

	var first bytes.Buffer
	a := view(false, &first)
	connected(a, "first view")
	if e := a.deliver(t, "#space", "from the first view"); e.State != "confirmed" {
		t.Fatalf("first view send was not confirmed: %+v", e)
	}
	p.until(t, ":"+human.Nick+"!"+f.humanAccount+"@airc PRIVMSG #space from the first view")

	// Without the user's take-over the second view only learns it is held.
	var second bytes.Buffer
	refused(view(false, &second), &second, "second view", "IRC registration failed (NICK_HELD)")
	if e := a.deliver(t, "#space", "first view unaffected"); e.State != "confirmed" {
		t.Fatalf("a refused view disturbed the live one: %+v", e)
	}

	// Take over: the first view ends with ErrTakenOver and stays down.
	var third bytes.Buffer
	c := view(true, &third)
	connected(c, "take-over view")
	select {
	case err := <-a.done:
		if !errors.Is(err, ErrTakenOver) {
			t.Fatalf("the replaced view must end as taken over: %v: %s", err, first.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the replaced view did not end")
	}
	a.close()
	p.until(t, ":"+human.Nick+"!"+f.humanAccount+"@airc QUIT Session taken over")
	if e := c.deliver(t, "#space", "from the take-over view"); e.State != "confirmed" {
		t.Fatalf("take-over view send was not confirmed: %+v", e)
	}
	p.until(t, ":"+human.Nick+"!"+f.humanAccount+"@airc PRIVMSG #space from the take-over view")

	// Another account holding the nickname is never taken over.
	c.close()
	p.until(t, " QUIT ")
	p.send("NICK " + human.Nick)
	p.until(t, ":opus!"+f.peerAccount+"@airc NICK "+human.Nick)
	var fourth bytes.Buffer
	refused(view(true, &fourth), &fourth, "take-over of another account's nickname", "IRC registration failed (433)")
}

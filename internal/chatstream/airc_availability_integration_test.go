package chatstream

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func (h *channelStream) availability(t *testing.T, kind string, reason *string) Event {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; {
		h.serial++
		id := fmt.Sprintf("availability-%d", h.serial)
		h.actions <- Action{ID: id, Kind: kind, Reason: reason}
		e := h.next(t, id, func(e Event) bool { return e.Kind == "control" && e.ID == id })
		if e.State != "refused" || e.Err == nil || !strings.Contains(e.Err.Error(), "pending") || time.Now().After(deadline) {
			return e
		}
		// Only a definite, unsent setup refusal allows another explicit request.
		time.Sleep(20 * time.Millisecond)
	}
}
func TestRealAIRCHumanAvailability(t *testing.T) {
	f := newRealChannelFixture(t)
	peer := f.peer("#space", "#build")
	peer.send("CAP REQ :away-notify")
	peer.until(t, " ACK away-notify")
	h := f.human()
	h.next(t, "connected", func(e Event) bool { return e.Kind == "connection" && e.Connected })
	prefix := f.header.Nick + "!" + f.humanAccount + "@airc AWAY"
	for index, reason := range []string{"away", "lunch  break", "lunch  break"} {
		e := h.availability(t, "away", awayReason(reason))
		if e.State != "confirmed" {
			t.Fatalf("away not confirmed: %+v", e)
		}
		// Repeat sender306 is required even when the peer state is unchanged.
		if index < 2 {
			peer.until(t, prefix+" "+reason)
		}
	}
	if e := h.deliver(t, "#space", "message while away"); e.State != "confirmed" {
		t.Fatalf("away send %+v", e)
	}
	peer.until(t, "PRIVMSG #space message while away")
	peer.send("WHO #space")
	who := peer.until(t, " 352 opus #space "+f.humanAccount+" ")
	fields := strings.Fields(who)
	if len(fields) < 9 || !strings.HasPrefix(fields[8], "G") {
		t.Fatalf("message cleared server away: %q", who)
	}
	if e := h.act(t, "join", "#build", nil); e.State != "confirmed" {
		t.Fatalf("join %+v", e)
	}
	h.next(t, "own away in new channel", func(e Event) bool {
		if e.Kind != "members" || e.Channel != "#build" {
			return false
		}
		for _, m := range e.Members {
			if m.Account == f.humanAccount && m.Nick == f.header.Nick {
				return m.Away && m.AwayReason == "lunch  break"
			}
		}
		return false
	})
	if e := h.availability(t, "back", nil); e.State != "confirmed" {
		t.Fatalf("back %+v", e)
	}
	cleared := false
	for i := 0; i < 16; i++ {
		line := peer.until(t, prefix)
		if strings.HasSuffix(strings.TrimSpace(strings.ReplaceAll(line, " :", " ")), prefix) {
			cleared = true
			break
		}
		// Joining another channel can publish the already-away state to peers.
	}
	if !cleared {
		t.Fatal("peer never cleared away")
	}

	h.close()
	again := f.human()
	e := again.next(t, "new back connection", func(e Event) bool { return e.Kind == "availability" })
	if e.State != "back" || e.AwayReason != "" {
		t.Fatalf("reconnected state %+v", e)
	}
}

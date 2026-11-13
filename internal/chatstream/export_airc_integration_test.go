package chatstream

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRealAIRCExportPagesEveryChannelToItsStart exports a channel the session
// joined at startup and one it joins only for the export, each paged from its
// newest message back to the server's empty page.
func TestRealAIRCExportPagesEveryChannelToItsStart(t *testing.T) {
	f := newRealChannelFixture(t)
	p := f.peer("#space", "#build")
	const built = 130 // LATEST 20, then BEFORE 50, 50, 10 and the empty page
	for i := 1; i <= built; i++ {
		p.send(fmt.Sprintf("PRIVMSG #build :build %03d", i))
	}
	for i := 1; i <= 3; i++ {
		p.send(fmt.Sprintf("PRIVMSG #space :space %d", i))
	}
	p.send("PING :stored")
	p.until(t, "PONG")

	h := f.human()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var results []ExportResult
	if err := Export(ctx, h.events, h.older, h.actions, "#space", []string{"#space", "#build"}, func(r ExportResult) { results = append(results, r) }); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results %+v", results)
	}
	check := func(r ExportResult, channel, prefix string, n int, joined bool) {
		t.Helper()
		if r.Channel != channel || r.Err != nil || r.Joined != joined || r.PartErr != nil || len(r.Messages) != n {
			t.Fatalf("%s: channel %q err %v joined %v part %v, %d messages", channel, r.Channel, r.Err, r.Joined, r.PartErr, len(r.Messages))
		}
		for i, m := range r.Messages {
			want := fmt.Sprintf("%s %d", prefix, i+1)
			if prefix == "build" {
				want = fmt.Sprintf("build %03d", i+1)
			}
			if m.Text != want || m.Channel != channel || m.ID == "" || m.Time.IsZero() || m.Nick != "opus" {
				t.Fatalf("%s message %d: %+v, want %q", channel, i, m, want)
			}
		}
	}
	check(results[0], "#space", "space", 3, false)
	check(results[1], "#build", "build", built, true)

	// The export left #build again; a new session (one export per stream)
	// joins it once more and reads the same history.
	h.close()
	h = f.human()
	results = nil
	if err := Export(ctx, h.events, h.older, h.actions, "#space", []string{"#build"}, func(r ExportResult) { results = append(results, r) }); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("second export %+v", results)
	}
	check(results[0], "#build", "build", built, true)
}

func TestRealAIRCExportOfAnEmptyChannel(t *testing.T) {
	f := newRealChannelFixture(t)
	h := f.human()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var results []ExportResult
	if err := Export(ctx, h.events, h.older, h.actions, "#space", []string{"#build", "#space"}, func(r ExportResult) { results = append(results, r) }); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Err != nil || len(results[0].Messages) != 0 || !results[0].Joined || results[1].Err != nil || len(results[1].Messages) != 0 {
		t.Fatalf("results %+v", results)
	}
}

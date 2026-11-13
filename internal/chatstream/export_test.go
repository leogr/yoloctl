package chatstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeExportStream answers an Export's requests from scripted handlers, as
// the channel driver would, and feeds the events back to it.
type fakeExportStream struct {
	events  chan Event
	older   chan HistoryRequest
	actions chan Action
	onPage  func(HistoryRequest) []Event
	onAct   func(Action) []Event
}

func newFakeExportStream(start ...Event) *fakeExportStream {
	s := &fakeExportStream{events: make(chan Event, 1024), older: make(chan HistoryRequest, 1), actions: make(chan Action, 1)}
	for _, e := range start {
		s.events <- e
	}
	return s
}

func (s *fakeExportStream) run(t *testing.T, channels ...string) ([]ExportResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-s.older:
				for _, e := range s.onPage(r) {
					s.events <- e
				}
			case a := <-s.actions:
				for _, e := range s.onAct(a) {
					s.events <- e
				}
			}
		}
	}()
	var results []ExportResult
	err := Export(ctx, s.events, s.older, s.actions, "#space", channels, func(r ExportResult) { results = append(results, r) })
	if len(results) != len(channels) {
		t.Fatalf("%d results for %d channels: %+v", len(results), len(channels), results)
	}
	return results, err
}

func msg(channel, id, text string, older bool) Event {
	return Event{Kind: "message", Channel: channel, Message: Message{Channel: channel, ID: id, Text: text, Replay: true, Older: older, Time: time.Unix(0, 0)}}
}

// startup is a connected stream whose default channel joined and read its
// initial page with ids, oldest first.
func startup(ids ...string) []Event {
	events := []Event{{Kind: "connection", Connected: true}, {Kind: "membership", Channel: "#space", State: "joined"}}
	for _, id := range ids {
		events = append(events, msg("#space", id, "text "+id, false))
	}
	return append(events, Event{Kind: "history-latest", Channel: "#space", ID: "initial-#space-1"})
}

// pages answers BEFORE requests from a channel's older messages, newest page
// first, each page oldest first.
func pages(channel string, older ...[]string) func(HistoryRequest) []Event {
	next := 0
	return func(r HistoryRequest) []Event {
		var events []Event
		var page []string
		if next < len(older) {
			page = older[next]
		}
		next++
		for _, id := range page {
			events = append(events, msg(channel, id, "text "+id, true))
		}
		return append(events, Event{Kind: "history-page", Channel: r.Channel, ID: r.ID, Count: len(page)})
	}
}

// joins confirms joins and parts, then sends the channel's initial page.
func joins(initial map[string][]string) func(Action) []Event {
	return func(a Action) []Event {
		state := map[string]string{"join": "joined", "part": "left"}[a.Kind]
		events := []Event{{Kind: "membership", Channel: a.Channel, ID: a.ID, State: state}, {Kind: "control", Channel: a.Channel, ID: a.ID, Action: a.Kind, State: "confirmed"}}
		if a.Kind == "join" {
			for _, id := range initial[a.Channel] {
				events = append(events, msg(a.Channel, id, "text "+id, false))
			}
			events = append(events, Event{Kind: "history-latest", Channel: a.Channel, ID: "initial-" + a.Channel})
		}
		return events
	}
}

func ids(messages []Message) string {
	var out []string
	for _, m := range messages {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

func TestExportPagesToTheEmptyPageAndIgnoresLiveMessages(t *testing.T) {
	s := newFakeExportStream(append(startup("s5", "s6"), Event{Kind: "message", Channel: "#space", Message: Message{Channel: "#space", ID: "live", Text: "live"}})...)
	s.onPage = pages("#space", []string{"s3", "s4"}, []string{"s1", "s2"}, nil)
	s.onAct = joins(nil)
	results, err := s.run(t, "#space")
	if err != nil || results[0].Err != nil || results[0].Joined {
		t.Fatalf("err %v result %+v", err, results[0])
	}
	if got := ids(results[0].Messages); got != "s1,s2,s3,s4,s5,s6" {
		t.Fatalf("order %s", got)
	}
}

func TestExportJoinsAndPartsOnlyChannelsItJoined(t *testing.T) {
	s := newFakeExportStream(startup()...)
	var acts []string
	join := joins(map[string][]string{"#ux": {"u2"}})
	s.onAct = func(a Action) []Event { acts = append(acts, a.Kind+" "+a.Channel); return join(a) }
	s.onPage = pages("#ux", []string{"u1"}, nil)
	results, err := s.run(t, "#space", "#ux")
	if err != nil || results[0].Err != nil || len(results[0].Messages) != 0 || results[1].Err != nil || !results[1].Joined || results[1].PartErr != nil {
		t.Fatalf("err %v results %+v", err, results)
	}
	if got := ids(results[1].Messages); got != "u1,u2" || strings.Join(acts, ";") != "join #ux;part #ux" {
		t.Fatalf("messages %s actions %v", got, acts)
	}
}

func TestExportRepeatsAJoinRefusedForPendingSetup(t *testing.T) {
	s := newFakeExportStream(startup()...)
	pending := true
	join := joins(nil)
	s.onAct = func(a Action) []Event {
		if a.Kind == "join" && pending {
			pending = false
			return []Event{{Kind: "control", Channel: a.Channel, ID: a.ID, Action: a.Kind, State: "refused", Code: SetupPendingCode, Err: errors.New("setup pending")}}
		}
		return join(a)
	}
	s.onPage = pages("#ux")
	results, err := s.run(t, "#ux")
	if err != nil || results[0].Err != nil || !results[0].Joined {
		t.Fatalf("err %v result %+v", err, results[0])
	}
}

func TestExportChannelFailuresStayInTheirChannel(t *testing.T) {
	for _, c := range []struct {
		name string
		page func(HistoryRequest) []Event
		act  func(Action) []Event
		want string
	}{
		{"join refused", nil, func(a Action) []Event {
			return []Event{{Kind: "control", Channel: a.Channel, ID: a.ID, Action: a.Kind, State: "refused", Err: errors.New("AIRC refused the channel action (403)")}}
		}, "join #ux was refused"},
		{"page refused", func(r HistoryRequest) []Event {
			return []Event{{Kind: "history-page", Channel: r.Channel, ID: r.ID, Count: -1, Err: errors.New("AIRC refused a history request")}}
		}, nil, "a history page failed: AIRC refused"},
		{"count mismatch", func(r HistoryRequest) []Event {
			return []Event{msg("#ux", "u0", "x", true), {Kind: "history-page", Channel: r.Channel, ID: r.ID, Count: 2}}
		}, nil, "reported 2 messages but delivered 1"},
		{"duplicate", func(r HistoryRequest) []Event {
			return []Event{msg("#ux", "u1", "x", true), {Kind: "history-page", Channel: r.Channel, ID: r.ID, Count: 1}}
		}, nil, "returned twice"},
		{"gap", func(r HistoryRequest) []Event {
			return []Event{{Kind: "gap", Channel: "#ux", Err: errors.New("lost")}, {Kind: "history-page", Channel: r.Channel, ID: r.ID, Count: -1}}
		}, nil, "history continuity was lost"},
		{"kicked", func(r HistoryRequest) []Event {
			return []Event{{Kind: "membership", Channel: "#ux", State: "kicked"}, {Kind: "history-page", Channel: r.Channel, ID: r.ID, Count: -1}}
		}, nil, "the channel was kicked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newFakeExportStream(startup("s1")...)
			join := joins(map[string][]string{"#ux": {"u1"}})
			s.onAct = join
			if c.act != nil {
				s.onAct = func(a Action) []Event {
					if a.Kind == "join" {
						return c.act(a)
					}
					return join(a)
				}
			}
			ux := c.page
			if ux == nil {
				ux = pages("#ux")
			}
			space := pages("#space")
			s.onPage = func(r HistoryRequest) []Event {
				if r.Channel == "#ux" {
					return ux(r)
				}
				return space(r)
			}
			results, err := s.run(t, "#ux", "#space")
			if err != nil || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), c.want) {
				t.Fatalf("err %v #ux %+v, want %q", err, results[0], c.want)
			}
			if results[1].Err != nil || ids(results[1].Messages) != "s1" {
				t.Fatalf("the next channel did not continue: %+v", results[1])
			}
		})
	}
}

func TestExportReportsAFailedLatestPageAndPart(t *testing.T) {
	s := newFakeExportStream(startup()...)
	join := joins(nil)
	s.onAct = func(a Action) []Event {
		if a.Kind == "part" {
			return []Event{{Kind: "control", Channel: a.Channel, ID: a.ID, Action: a.Kind, State: "uncertain", Err: errors.New("timed out")}}
		}
		events := join(a)
		events[len(events)-1].Err = errors.New("history response timed out")
		return events
	}
	s.onPage = pages("#ux")
	results, err := s.run(t, "#ux")
	if err != nil || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "timed out") || results[0].PartErr == nil || !strings.Contains(results[0].PartErr.Error(), "part #ux was uncertain") {
		t.Fatalf("err %v result %+v", err, results[0])
	}
}

func TestExportEndsEveryRemainingChannelWithTheConnection(t *testing.T) {
	s := newFakeExportStream(startup("s1")...)
	s.onAct = joins(nil)
	s.onPage = func(r HistoryRequest) []Event {
		return []Event{{Kind: "connection", Err: errors.New("stream closed")}}
	}
	results, err := s.run(t, "#space", "#ux", "#ops")
	if err == nil || err.Error() != "stream closed" {
		t.Fatalf("err %v", err)
	}
	for i, r := range results {
		if r.Err == nil {
			t.Fatalf("result %d without error: %+v", i, r)
		}
	}
}

func TestExportStopsWhenTheNicknameIsHeld(t *testing.T) {
	s := newFakeExportStream(Event{Kind: "connection", Err: ErrNickHeld})
	results, err := s.run(t, "#space", "#ux")
	if !errors.Is(err, ErrNickHeld) || !errors.Is(results[0].Err, ErrNickHeld) || !errors.Is(results[1].Err, ErrNickHeld) {
		t.Fatalf("err %v results %+v", err, results)
	}
}

func TestExportStopsWithItsContext(t *testing.T) {
	s := newFakeExportStream(startup("s1")...)
	ctx, cancel := context.WithCancel(t.Context())
	s.onAct = joins(nil)
	s.onPage = func(r HistoryRequest) []Event { cancel(); return nil }
	go func() {
		for r := range s.older {
			s.onPage(r)
		}
	}()
	var results []ExportResult
	err := Export(ctx, s.events, s.older, s.actions, "#space", []string{"#space", "#ux"}, func(r ExportResult) { results = append(results, r) })
	if !errors.Is(err, context.Canceled) || len(results) != 2 || results[0].Err == nil || results[1].Err == nil {
		t.Fatalf("err %v results %v", err, fmt.Sprint(results))
	}
}

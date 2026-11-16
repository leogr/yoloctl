package chatui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestAvailabilityCommandsAndDraftConfirmation(t *testing.T) {
	for _, input := range []string{"/away", "/away   lunch  break  ", "/back"} {
		m, sends, actions := channelModel(t)
		paste(m, input)
		press(m, tea.KeyEnter, 0)
		if len(*actions) != 1 || len(*sends) != 0 || m.Draft() != input {
			t.Fatalf("command queued/cleared incorrectly: %+v %q", *actions, m.Draft())
		}
		a := (*actions)[0]
		if a.Channel != "" {
			t.Fatal("connection action bound to channel")
		}
		if strings.HasPrefix(input, "/away") {
			want := "away"
			if strings.Contains(input, "lunch") {
				want = "lunch  break"
			}
			if a.Reason == nil || *a.Reason != want {
				t.Fatalf("reason %+v", a.Reason)
			}
		} else if a.Reason != nil {
			t.Fatal("back has reason")
		}
		alt(m, '2')
		paste(m, "new channel draft")
		m.Update(ControlMsg{ID: a.ID, Kind: a.Kind, State: "confirmed"})
		if m.name != "#build" || m.Draft() != "new channel draft" || m.views["#space"].editor.text != "" {
			t.Fatal("confirmation cleared wrong draft")
		}
	}
}

func TestAvailabilityDraftFailuresAndEdits(t *testing.T) {
	for _, outcome := range []string{"refused", "uncertain", "edited"} {
		m, sends, actions := channelModel(t)
		paste(m, "/away lunch")
		press(m, tea.KeyEnter, 0)
		a := (*actions)[0]
		if outcome == "edited" {
			paste(m, "!")
		}
		state := outcome
		if outcome == "edited" {
			state = "confirmed"
		}
		m.Update(ControlMsg{ID: a.ID, Kind: a.Kind, State: state, Err: errors.New("fixture")})
		if m.Draft() == "" || len(*sends) != 0 {
			t.Fatalf("%s lost original/edited draft %q", outcome, m.Draft())
		}
	}
	for _, input := range []string{"/back extra", "/away " + strings.Repeat("é", 101)} {
		m, sends, actions := channelModel(t)
		paste(m, input)
		press(m, tea.KeyEnter, 0)
		if len(*actions) != 0 || len(*sends) != 0 || m.Draft() != input {
			t.Fatal("invalid action sent/lost")
		}
	}
	m, sends, actions := channelModel(t)
	paste(m, "/away\ncontent")
	press(m, tea.KeyEnter, 0)
	if len(*actions) != 0 || len(*sends) != 1 {
		t.Fatal("multiline became command")
	}
	m, _, actions = channelModel(t)
	m.Update(ConnectionMsg{})
	paste(m, "/away")
	press(m, tea.KeyEnter, 0)
	if len(*actions) != 0 || m.Draft() != "/away" {
		t.Fatal("offline command sent/lost")
	}
}

func TestOwnAvailabilityAcrossSnapshotsAndConnection(t *testing.T) {
	m, _, _ := channelModel(t)
	m.opts.Account = "human-account"
	own := Member{Account: m.opts.Account, Nick: m.opts.Nick}
	other := Member{Account: m.opts.Account, Nick: "another-human-view"}
	m.Update(MembersMsg{Channel: "#space", Members: []Member{own, other}})
	m.Update(AvailabilityMsg{State: "away", Reason: "lunch"})
	m.rosterOpen = false
	if !strings.Contains(ansi.Strip(m.topBar(80)), "you: away") {
		t.Fatal("own state hidden with panel")
	}
	m.Update(MembersMsg{Channel: "#space", Members: []Member{own, other}})
	if !m.members[0].Away || m.members[0].AwayReason != "lunch" || m.members[1].Away {
		t.Fatalf("snapshot/state %+v", m.members)
	}
	alt(m, '2')
	m.Update(MembersMsg{Channel: "#build", Members: []Member{own}})
	if !m.members[0].Away || !strings.Contains(m.availabilityDetail(), "lunch") {
		t.Fatal("channel switch lost own state")
	}
	paste(m, "message while away")
	press(m, tea.KeyEnter, 0)
	if m.ownState != "away" {
		t.Fatal("typing marked back")
	}
	m.Update(ConnectionMsg{})
	if m.ownState != "unknown" || !strings.Contains(m.availabilityDetail(), "unavailable") {
		t.Fatal("offline was back")
	}
	m.Update(AvailabilityMsg{State: "away", Reason: "late"})
	if m.ownState != "unknown" {
		t.Fatal("old offline result applied")
	}
	m.Update(ConnectionMsg{Connected: true})
	if m.ownState != "back" || m.ownReason != "" {
		t.Fatal("reconnect replayed away")
	}
}

package chatui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func selectionModel(t *testing.T) (*Model, *[]Outgoing, *[]Action, *int) {
	t.Helper()
	m, sends, actions := channelModel(t)
	m.Update(CatalogMsg{Generation: 2, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}, {Name: "#other"}}})
	m.Update(MembershipMsg{Channel: "#other", State: "joined"})
	m.views["#build"].membership = "" // catalog-only, no self JOIN
	ran := new(int)
	m.opts.Control = func(a Action) tea.Cmd { *actions = append(*actions, a); return func() tea.Msg { *ran++; return nil } }
	return m, sends, actions, ran
}

func channelClick(t *testing.T, m *Model, area hitArea, item int) tea.Cmd {
	t.Helper()
	m.View()
	for _, h := range m.hits {
		if h.area == area && h.item == item {
			return clickAt(m, h.x0, h.y)
		}
	}
	t.Fatalf("missing %v/%d target", area, item)
	return nil
}

func TestChannelSelectionJoinsThroughEveryHumanRoute(t *testing.T) {
	routes := map[string]func(*testing.T, *Model) tea.Cmd{
		"picker keyboard": func(t *testing.T, m *Model) tea.Cmd {
			press(m, 'g', tea.ModCtrl)
			press(m, tea.KeyDown, 0)
			if m.pending != nil {
				t.Fatal("highlighting joined")
			}
			return press(m, tea.KeyEnter, 0)
		},
		"picker mouse": func(t *testing.T, m *Model) tea.Cmd {
			press(m, 'g', tea.ModCtrl)
			return channelClick(t, m, hitOverlayRow, 1)
		},
		"rail": func(t *testing.T, m *Model) tea.Cmd {
			m.rosterOpen = true
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			return channelClick(t, m, hitChannel, 1)
		},
		"alt number": func(t *testing.T, m *Model) tea.Cmd { return press(m, '2', tea.ModAlt) },
		"cycle forward": func(t *testing.T, m *Model) tea.Cmd {
			m.views["#build"].unread = 1
			return press(m, tea.KeyRight, tea.ModAlt)
		},
		"cycle backward": func(t *testing.T, m *Model) tea.Cmd {
			m.views["#build"].unread = 1
			return press(m, tea.KeyLeft, tea.ModAlt)
		},
	}
	for name, activate := range routes {
		t.Run(name, func(t *testing.T) {
			m, sends, actions, ran := selectionModel(t)
			paste(m, "unsent draft")
			m.replyTo = "old"
			m.Update(IncomingMsg{Message: Message{ID: "old", Nick: "x", Text: "history"}})
			cmd := activate(t, m)
			if len(*actions) != 1 || (*actions)[0].Kind != "join" || (*actions)[0].Channel != "#build" || (*actions)[0].ID == "" || cmd == nil {
				t.Fatalf("selection did not return typed JOIN: %+v cmd=%v", *actions, cmd != nil)
			}
			cmd()
			if *ran != 1 || m.name != "#space" || m.Draft() != "unsent draft" || m.replyTo != "old" || len(m.Messages()) != 1 || len(*sends) != 0 || !strings.Contains(m.notice, "Joining #build") {
				t.Fatalf("selection changed draft/history or failed command propagation: %s %q %q ran=%d", m.name, m.Draft(), m.notice, *ran)
			}
			press(m, '2', tea.ModAlt)
			if len(*actions) != 1 {
				t.Fatal("duplicate selection queued JOIN")
			}
			a := (*actions)[0]
			m.Update(ControlMsg{ID: a.ID, Channel: a.Channel, Kind: "join", State: "confirmed"})
			if m.name != "#space" {
				t.Fatal("request confirmation alone optimistically switched")
			}
			// A confirmed action may precede the authenticated self JOIN. A
			// second selection in that window must still coalesce.
			press(m, '2', tea.ModAlt)
			if len(*actions) != 1 {
				t.Fatal("confirmed-but-not-yet-joined selection duplicated JOIN")
			}
			m.Update(MembershipMsg{Channel: a.Channel, State: "joined", ID: a.ID})
			if m.name != "#build" || m.membership != "joined" || m.views["#space"].editor.text != "unsent draft" || m.views["#space"].replyTo != "old" || len(*sends) != 0 {
				t.Fatal("confirmed switch lost old view")
			}
			press(m, '1', tea.ModAlt)
			press(m, '2', tea.ModAlt)
			if len(*actions) != 1 {
				t.Fatal("joined selection queued another JOIN")
			}
		})
	}
}

func TestChannelSelectionKeepsAdmissionAndNavigation(t *testing.T) {
	for _, state := range []string{"refused", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			m, sends, actions, _ := selectionModel(t)
			paste(m, "draft")
			m.replyTo = "reply"
			press(m, '2', tea.ModAlt)
			a := (*actions)[0]
			m.Update(ControlMsg{ID: a.ID, Channel: a.Channel, Kind: "join", State: state, Code: CodeACLDenied, Err: errors.New("access revoked")})
			if m.name != "#space" || m.Draft() != "draft" || m.replyTo != "reply" || len(*sends) != 0 {
				t.Fatal("join failure lost draft or switched")
			}
			if state == "refused" && !strings.Contains(m.notice, "access") {
				t.Fatal("ACL refusal hidden")
			}
			if state == "uncertain" {
				press(m, '2', tea.ModAlt)
				if len(*actions) != 1 || !strings.Contains(m.notice, "uncertain") {
					t.Fatal("uncertain action retried")
				}
			}
		})
	}
	m, _, actions, _ := selectionModel(t)
	press(m, '2', tea.ModAlt)
	a := (*actions)[0]
	press(m, '3', tea.ModAlt)
	m.Update(MembershipMsg{ID: a.ID, Channel: a.Channel, State: "joined"})
	m.Update(ControlMsg{ID: a.ID, Channel: a.Channel, Kind: "join", State: "confirmed"})
	if m.name != "#other" {
		t.Fatal("late JOIN stole the selected view")
	}
}

func TestChannelSelectionRespectsReadOnlyAndBusyStates(t *testing.T) {
	for _, kind := range []string{"disconnected", "unavailable", "pending send", "deleted", "legacy default"} {
		t.Run(kind, func(t *testing.T) {
			m, sends, actions, _ := selectionModel(t)
			switch kind {
			case "disconnected":
				m.Update(ConnectionMsg{Err: errors.New("offline")})
			case "unavailable":
				m.opts.Control = nil
			case "pending send":
				paste(m, "pending")
				press(m, tea.KeyEnter, 0)
			case "deleted":
				m.Update(MembershipMsg{Channel: "#build", State: "deleted"})
			case "legacy default":
				m.views["#space"].membership = ""
				press(m, '1', tea.ModAlt)
				if len(*actions) != 0 {
					t.Fatal("legacy default joined")
				}
				return
			}
			before := m.Draft()
			n := len(*sends)
			press(m, '2', tea.ModAlt)
			if len(*actions) != 0 || len(*sends) != n {
				t.Fatal("ineligible selection started an action")
			}
			if kind != "deleted" && m.Draft() != before {
				t.Fatal("blocked selection lost draft")
			}
		})
	}
}

func TestPartAndBackgroundEventsNeverAutoJoin(t *testing.T) {
	m, sends, actions, _ := selectionModel(t)
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	m.switchTo("#build")
	paste(m, "/part")
	cmd := press(m, tea.KeyEnter, 0)
	if cmd != nil {
		cmd()
	}
	if len(*actions) != 1 || (*actions)[0].Kind != "part" {
		t.Fatal("explicit part missing")
	}
	a := (*actions)[0]
	m.Update(MembershipMsg{Channel: "#build", State: "left", ID: a.ID})
	m.Update(ControlMsg{ID: a.ID, Kind: "part", Channel: a.Channel, State: "confirmed"})
	m.Update(CatalogMsg{Generation: 3, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}, {Name: "#other"}}})
	m.Update(MembershipMsg{Channel: "#build", State: "kicked"})
	m.View()
	if len(*actions) != 1 || len(*sends) != 0 {
		t.Fatal("part/kick/catalog/render issued JOIN")
	}
	paste(m, "retained draft")
	m.replyTo = "history"
	press(m, '2', tea.ModAlt) // current unjoined view is a deliberate new selection
	if len(*actions) != 2 || (*actions)[1].Kind != "join" || m.Draft() != "retained draft" || m.replyTo != "history" {
		t.Fatal("current unjoined selection did not request JOIN with draft kept")
	}
}

func TestPartSupersedesSelectionWaitingForMembership(t *testing.T) {
	m, _, actions, _ := selectionModel(t)
	press(m, '2', tea.ModAlt)
	join := (*actions)[0]
	m.Update(ControlMsg{ID: join.ID, Channel: join.Channel, Kind: "join", State: "confirmed"})
	paste(m, "/part #build")
	press(m, tea.KeyEnter, 0)
	if len(*actions) != 2 || (*actions)[1].Kind != "part" {
		t.Fatal("part did not use ordinary admission")
	}
	part := (*actions)[1]
	m.Update(MembershipMsg{ID: join.ID, Channel: join.Channel, State: "joined"})
	if m.name != "#space" {
		t.Fatal("superseded late JOIN stole the view")
	}
	m.Update(MembershipMsg{ID: part.ID, Channel: part.Channel, State: "left"})
	m.Update(ControlMsg{ID: part.ID, Channel: part.Channel, Kind: "part", State: "confirmed"})
	press(m, '2', tea.ModAlt)
	if len(*actions) != 3 || (*actions)[2].Kind != "join" {
		t.Fatal("part left a stale join marker blocking later selection")
	}
}

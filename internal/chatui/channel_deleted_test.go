package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A channel deleted while chat is open keeps its view: history and draft stay
// readable, nothing is sent, joined, paged or moved, and no later report or
// in-view reconnect reopens it for a channel created with the same name.
func TestDeletedChannelViewStaysReadOnly(t *testing.T) {
	m, sends, actions := channelModel(t)
	m.opts.LoadOlder = func(HistoryRequest) tea.Cmd { t.Fatal("history requested for a deleted channel"); return nil }
	alt(m, '2')
	m.Update(IncomingMsg{Message: Message{ID: "b1", Channel: "#build", Nick: "x", Text: "kept history"}})
	paste(m, "unsent draft")
	m.replyTo, m.replyRevision = "b1", m.replyRevision+1
	m.Update(MembershipMsg{Channel: "#build", State: "deleted"})
	v := m.views["#build"]
	if !v.deleted || m.Draft() != "unsent draft" || m.replyTo != "b1" || len(v.messages) != 1 || !strings.Contains(m.notice, "was deleted") {
		t.Fatalf("deletion lost the view, its draft or reply: deleted=%v draft=%q reply=%q notice=%q", v.deleted, m.Draft(), m.replyTo, m.notice)
	}
	if !strings.Contains(view(m), "#build was deleted") {
		t.Fatalf("the view does not show the deletion:\n%s", view(m))
	}
	press(m, tea.KeyEnter, 0)
	if len(*sends) != 0 || m.Draft() != "unsent draft" || !strings.Contains(m.notice, "read-only") {
		t.Fatalf("a deleted channel sent or dropped its draft: %v %q", *sends, m.notice)
	}
	for _, command := range []string{"/join #build", "/part", "/topic"} {
		m.edit(m.editor.replace(0, len(m.editor.text), command))
		press(m, tea.KeyEnter, 0)
		if len(*actions) != 0 {
			t.Fatalf("%s started an action on a deleted channel: %+v", command, *actions)
		}
	}
	// Later reports, a recreated name in the catalog and a reconnect leave it
	// deleted; it is never treated as the new channel.
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	m.Update(CatalogMsg{Generation: 3, Default: "#space", Channels: []ChannelInfo{{Name: "#space"}, {Name: "#build"}}, Applied: true})
	m.Update(ConnectionMsg{Connected: false})
	m.Update(ConnectionMsg{Connected: true})
	if !v.deleted || m.canSend() {
		t.Fatal("a later report, catalog or reconnect reopened the deleted view")
	}
	m.scrollUp(100)
	alt(m, '1')
	if m.name != "#space" || !m.canSend() {
		t.Fatal("deletion affected another channel")
	}
	m.openOverlay(overlayState{kind: overlayChannels})
	if !strings.Contains(view(m), "deleted") {
		t.Fatalf("the channel list does not mark the deleted channel:\n%s", view(m))
	}
}

// identityCatalog names #space and, if build is set, #build with that ID.
func identityCatalog(generation uint64, build string, deleted ...string) CatalogMsg {
	channels := []ChannelInfo{{Name: "#space", ID: "space-id"}}
	if build != "" {
		channels = append(channels, ChannelInfo{Name: "#build", ID: build})
	}
	return CatalogMsg{Generation: generation, Default: "#space", Channels: channels, Deleted: deleted, Applied: true}
}

// A deletion this chat never saw live (it was disconnected, or the deleting
// catalog had not arrived) is still found on reconnect: the view keeps the
// identity it showed, and a catalog recording that identity's deletion or
// naming another one makes it read-only. Successor content never reaches it.
func TestChannelDeletedWhileDisconnectedKeepsTheOldViewReadOnly(t *testing.T) {
	for _, test := range []struct {
		name string
		next CatalogMsg
	}{
		{"recreated", identityCatalog(4, "build-new", "build-old")},
		{"deleted", identityCatalog(4, "", "build-old")},
		{"replaced identity without a record", identityCatalog(4, "build-new")},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, sends, actions := channelModel(t)
			m.Update(identityCatalog(2, "build-old"))
			alt(m, '2')
			m.Update(IncomingMsg{Message: Message{ID: "old", Channel: "#build", Nick: "x", Text: "old history"}})
			paste(m, "draft for old identity")
			m.Update(ConnectionMsg{Connected: false})
			m.Update(SessionMsg{Nick: m.opts.Nick})
			m.Update(ConnectionMsg{Connected: true})
			m.Update(test.next)
			m.Update(MembershipMsg{Channel: "#build", State: "joined"})
			m.Update(IncomingMsg{Message: Message{ID: "new", Channel: "#build", Nick: "y", Text: "successor content"}})
			m.Update(TopicMsg{Channel: "#build", Text: "successor topic"})
			press(m, tea.KeyEnter, 0)
			v := m.views["#build"]
			if !v.deleted || len(*sends) != 0 || m.Draft() != "draft for old identity" {
				t.Fatalf("old draft became sendable under the reused name: deleted=%v sends=%+v", v.deleted, *sends)
			}
			if len(v.messages) != 1 || v.messages[0].ID != "old" || v.topic != "" {
				t.Fatalf("successor content reached the old view: %+v topic=%q", v.messages, v.topic)
			}
			m.edit(m.editor.replace(0, len(m.editor.text), "/join #build"))
			press(m, tea.KeyEnter, 0)
			if len(*actions) != 0 {
				t.Fatalf("the old view joined the successor: %+v", *actions)
			}
		})
	}
	// A reconnect without a deletion keeps the same identity usable.
	m, sends, _ := channelModel(t)
	m.Update(identityCatalog(2, "build-id"))
	alt(m, '2')
	m.Update(ConnectionMsg{Connected: false})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(identityCatalog(3, "build-id", "older-unrelated"))
	m.Update(MembershipMsg{Channel: "#build", State: "joined"})
	paste(m, "still usable")
	press(m, tea.KeyEnter, 0)
	if m.views["#build"].deleted || len(*sends) != 1 || (*sends)[0].Channel != "#build" {
		t.Fatalf("an unchanged identity became read-only: %+v", *sends)
	}
}

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

func TestChatJoinControlCarriesOneRefreshInOriginalAction(t *testing.T) {
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	header, err := chatstream.RoomHeader(room)
	if err != nil {
		t.Fatal(err)
	}
	l := &chatLink{actions: make(chan chatstream.Action, 1), catalogs: make(chan chatstream.Catalog, 1)}
	s := &chatSession{ctx: t.Context(), link: l}
	a := chatui.Action{ID: "explicit-join", Kind: "join", Channel: "#new"}
	reads := 0
	msg := s.control(a, func(ctx context.Context) (chatstream.Catalog, error) {
		reads++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("JOIN refresh is unbounded")
		}
		return chatstream.Catalog{Header: header}, nil
	})()
	if msg != nil || reads != 1 || len(l.catalogs) != 0 {
		t.Fatalf("JOIN used a separate catalog queue or repeated read: %v %d", msg, reads)
	}
	queued := <-l.actions
	if queued.ID != a.ID || queued.Channel != a.Channel || queued.Kind != a.Kind || queued.Catalog == nil || queued.Catalog.Header.RoomID != room.ID {
		t.Fatalf("JOIN lost its original identity or catalog: %+v", queued)
	}
}

func TestChatJoinRefreshRefusalNeverMovesToReplacementLink(t *testing.T) {
	for _, failure := range []string{"read", "canceled", "replaced", "missing-reader"} {
		t.Run(failure, func(t *testing.T) {
			old := &chatLink{actions: make(chan chatstream.Action, 1)}
			next := &chatLink{actions: make(chan chatstream.Action, 1)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s := &chatSession{ctx: ctx, link: old}
			reads := 0
			read := func(context.Context) (chatstream.Catalog, error) {
				reads++
				switch failure {
				case "read":
					return chatstream.Catalog{}, errors.New("read failed")
				case "canceled":
					cancel()
				case "replaced":
					old.close()
					s.mu.Lock()
					s.link = next
					s.mu.Unlock()
				}
				return chatstream.Catalog{}, nil
			}
			if failure == "missing-reader" {
				read = nil
			}
			msg, ok := s.control(chatui.Action{ID: "join", Kind: "join", Channel: "#new"}, read)().(chatui.ControlMsg)
			if !ok || msg.ID != "join" || msg.State != "refused" || msg.Err == nil || reads > 1 || len(old.actions) != 0 || len(next.actions) != 0 {
				t.Fatalf("failed refresh queued or migrated a JOIN: %+v", msg)
			}
		})
	}
}

func TestChatControlRefusalCodeMapping(t *testing.T) {
	for _, code := range []string{"unknown-channel", "acl-denied", ""} {
		msg := chatViewEvent(chatstream.Event{Kind: "control", ID: "join", Action: "join", Channel: "#new", State: "refused", Code: code}).(chatui.ControlMsg)
		if msg.Code != code || msg.ID != "join" || msg.Kind != "join" || msg.Channel != "#new" || msg.State != "refused" {
			t.Fatalf("control mapping lost the structured reason: %+v", msg)
		}
	}
}

package main

import (
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

func TestChatAvailabilityHostMapping(t *testing.T) {
	reason := "lunch"
	l := &chatLink{actions: make(chan chatstream.Action, 1)}
	s := &chatSession{ctx: t.Context(), link: l}
	if msg := s.control(chatui.Action{ID: "a", Kind: "away", Reason: &reason}, nil)(); msg != nil {
		t.Fatal(msg)
	}
	got := <-l.actions
	if got.Kind != "away" || got.Channel != "" || got.Reason == nil || *got.Reason != reason || got.Catalog != nil {
		t.Fatalf("action %+v", got)
	}
	mapped, ok := chatViewEvent(chatstream.Event{Kind: "availability", State: "away", AwayReason: reason}).(chatui.AvailabilityMsg)
	if !ok || mapped.State != "away" || mapped.Reason != reason {
		t.Fatalf("event %+v", mapped)
	}
}

func TestChatAvailabilityIgnoresOldLinkAfterReconnect(t *testing.T) {
	old, current := &chatLink{id: 1}, &chatLink{id: 2}
	s := &chatSession{ctx: t.Context(), link: current}
	event := chatstream.Event{Kind: "availability", State: "away", AwayReason: "old"}
	if got := s.viewEvent(old, event); got != nil {
		t.Fatalf("old link published %#v", got)
	}
	if got := s.viewEvent(current, event); got == nil {
		t.Fatal("current link suppressed")
	}
}

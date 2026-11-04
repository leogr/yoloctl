package main

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestRefreshQueueIsNotRecoveryAndKeepsRetainedRead(t *testing.T) {
	l := &chatLink{id: 4, catalogs: make(chan chatstream.Catalog, 1)}
	s := &chatSession{ctx: t.Context(), link: l}
	snap := func(g uint64) chatstream.Catalog {
		return chatstream.Catalog{Header: chatstream.Header{Nick: "human", Bootstrap: managedchat.Bootstrap{RoomID: "r", DiskID: "d", SelfAccount: "h", Generation: g}}}
	}
	fail := s.refreshCatalogRead(func(context.Context) (chatstream.Catalog, error) { return chatstream.Catalog{}, errors.New("failed") })
	f, ok := fail.(chatui.RefreshResultMsg)
	if !ok || f.Read != (chatui.CatalogRead{Link: 4, Sequence: 1}) {
		t.Fatal(fail)
	}
	for _, g := range []uint64{6, 5} {
		if msg := s.refreshCatalogRead(func(context.Context) (chatstream.Catalog, error) { return snap(g), nil }); msg != nil {
			t.Fatal("queue admission emitted recovery", msg)
		}
	}
	retained := <-l.catalogs
	if retained.Read.Sequence != 2 || retained.Header.Generation != 6 {
		t.Fatal("discarded read acknowledged instead of retained read", retained)
	}
	l.closed = true
	if msg := s.refreshCatalogRead(func(context.Context) (chatstream.Catalog, error) { return snap(7), nil }); msg != nil || len(l.catalogs) != 0 {
		t.Fatal("closed link falsely recovered", msg)
	}
	replacement := &chatLink{id: 5, catalogs: make(chan chatstream.Catalog, 1)}
	msg := s.refreshCatalogRead(func(context.Context) (chatstream.Catalog, error) {
		s.mu.Lock()
		s.link = replacement
		s.mu.Unlock()
		return chatstream.Catalog{}, errors.New("old link failure")
	})
	if msg != nil || len(replacement.catalogs) != 0 {
		t.Fatal("old read affected replacement link", msg)
	}
	if s.adoptCatalogFor(l, snap(9).Header) || s.header.RoomID != "" {
		t.Fatal("old link replaced the current host catalog")
	}

}

func TestDriverRefusalMapsToOwnedRefreshWithoutLosingCause(t *testing.T) {
	reason := errors.New("refused catalog")
	msg := chatViewEvent(chatstream.Event{Kind: "catalog", State: "refused", Err: reason, CatalogRead: chatstream.CatalogRead{Link: 8, Sequence: 2}})
	result, ok := msg.(chatui.RefreshResultMsg)
	if !ok || !result.Refused || result.Read != (chatui.CatalogRead{Link: 8, Sequence: 2}) || !errors.Is(result.Err, reason) {
		t.Fatal(msg)
	}
	c := chatstream.Catalog{Read: chatstream.CatalogRead{Link: 8, Sequence: 3}}
	if got := chatCatalog(c); got.Read != (chatui.CatalogRead{Link: 8, Sequence: 3}) {
		t.Fatal("accepted identity missing", got)
	}
}

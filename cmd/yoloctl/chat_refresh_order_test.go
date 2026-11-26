package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestRefreshQueueIsNotRecoveryAndKeepsRetainedRead(t *testing.T) {
	l := &chatLink{id: 4, ctx: t.Context(), catalogs: make(chan chatstream.Catalog, 1)}
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
		if msg := s.refreshCatalogRead(func(ctx context.Context) (chatstream.Catalog, error) {
			deadline, ok := ctx.Deadline()
			if left := time.Until(deadline); !ok || left <= time.Minute || left > chatViewRequestTimeout {
				t.Fatalf("catalog refresh lost its single-wait allowance: %s, %v", left, ok)
			}
			return snap(g), nil
		}); msg != nil {
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

func TestChatBackgroundRefreshCoalescesWithoutSharingJoinRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	l := &chatLink{id: 1, ctx: ctx, catalogs: make(chan chatstream.Catalog, 1), actions: make(chan chatstream.Action, 1)}
	s := &chatSession{ctx: ctx, link: l}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan any, 1)
	go func() {
		done <- s.refreshCatalogRead(func(ctx context.Context) (chatstream.Catalog, error) {
			close(entered)
			select {
			case <-release:
				return chatstream.Catalog{Header: chatstream.Header{Bootstrap: managedchat.Bootstrap{Generation: 4}}}, nil
			case <-ctx.Done():
				return chatstream.Catalog{}, ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("background refresh did not start")
	}
	if msg := s.refreshCatalogRead(func(context.Context) (chatstream.Catalog, error) {
		t.Fatal("overlapping trigger started another background read")
		return chatstream.Catalog{}, nil
	}); msg != nil {
		t.Fatalf("coalesced trigger published a result: %v", msg)
	}
	action := chatui.Action{ID: "join", Kind: "join", Channel: "#new"}
	if msg := s.control(action, func(context.Context) (chatstream.Catalog, error) {
		return chatstream.Catalog{Header: chatstream.Header{Bootstrap: managedchat.Bootstrap{Generation: 5}}}, nil
	})(); msg != nil {
		t.Fatal(msg)
	}
	select {
	case got := <-l.actions:
		if got.ID != action.ID || got.Catalog == nil || got.Catalog.Header.Generation != 5 {
			t.Fatalf("JOIN did not carry its own catalog: %+v", got)
		}
	default:
		t.Fatal("JOIN did not queue its action while the background read was in flight")
	}
	close(release)
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatal(msg)
		}
	case <-ctx.Done():
		t.Fatal("background refresh did not finish")
	}
	select {
	case got := <-l.catalogs:
		if got.Read != (chatstream.CatalogRead{Link: l.id, Sequence: 1}) || got.Header.Generation != 4 {
			t.Fatalf("background refresh lost its correlation: %+v", got)
		}
	default:
		t.Fatal("background refresh did not queue its catalog")
	}
}

func TestChatCatalogReadsCancelWithViewOrLink(t *testing.T) {
	for _, end := range []string{"view closes", "link replaced"} {
		t.Run(end, func(t *testing.T) {
			view, closeView := context.WithCancel(t.Context())
			defer closeView()
			ctx, disconnect := context.WithCancel(view)
			defer disconnect()
			old := &chatLink{id: 1, ctx: ctx, catalogs: make(chan chatstream.Catalog, 1), actions: make(chan chatstream.Action, 1)}
			next := &chatLink{id: 2, ctx: view, catalogs: make(chan chatstream.Catalog, 1), actions: make(chan chatstream.Action, 1)}
			s := &chatSession{ctx: view, link: old}
			entered := make(chan struct{}, 2)
			guard, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			read := func(ctx context.Context) (chatstream.Catalog, error) {
				entered <- struct{}{}
				select {
				case <-ctx.Done():
					return chatstream.Catalog{}, nil // a reply that raced cancellation
				case <-guard.Done():
					return chatstream.Catalog{}, guard.Err()
				}
			}
			refresh, join := make(chan any, 1), make(chan any, 1)
			action := s.control(chatui.Action{ID: "old-join", Kind: "join"}, read)
			go func() { refresh <- s.refreshCatalogRead(read) }()
			go func() { join <- action() }()
			for range 2 {
				select {
				case <-entered:
				case <-guard.Done():
					t.Fatal("reads did not start independently")
				}
			}
			if end == "view closes" {
				closeView()
			} else {
				disconnect()
				old.close()
				s.mu.Lock()
				s.link = next
				s.mu.Unlock()
			}
			select {
			case msg := <-refresh:
				if msg != nil {
					t.Fatalf("ended link published a refresh result: %v", msg)
				}
			case <-guard.Done():
				t.Fatal("refresh did not cancel")
			}
			select {
			case msg := <-join:
				got, ok := msg.(chatui.ControlMsg)
				if !ok || got.ID != "old-join" || got.State != "refused" || !errors.Is(got.Err, context.Canceled) {
					t.Fatalf("canceled JOIN did not refuse the original action: %+v", msg)
				}
			case <-guard.Done():
				t.Fatal("JOIN did not cancel")
			}
			if len(old.actions)+len(old.catalogs)+len(next.actions)+len(next.catalogs) != 0 {
				t.Fatal("a canceled read queued work on an old or replacement link")
			}
		})
	}
}

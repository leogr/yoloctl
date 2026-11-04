package controller

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

func TestOperatorClaimInventoryActualBlockedLegacyAndNativeIncludingKeep(t *testing.T) {
	s, _, o := nativeNonceOwnerFixture(t)
	nonce, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.reserveOperatorPublication(t.Context(), operator.Endpoint{Box: "box", Incarnation: nonce}); err != nil {
		t.Fatal(err)
	}
	legacy := operator.Endpoint{Box: "other", Incarnation: strings.Repeat("a", 32)}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	finished := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); <-finished })
	go func() {
		defer close(finished)
		l, err := s.ListenLegacyOperator(t.Context(), legacy, func() (net.Listener, error) { close(entered); <-release; return &publicationTestListener{}, nil })
		if err == nil {
			err = l.Close()
		}
		done <- err
	}()
	<-entered
	p, err := observeOperatorPublicationClaims(t.Context(), s)
	if err != nil {
		unblock()
		<-done
		t.Fatal(err)
	}
	got, err := p.Observation(t.Context())
	if err != nil {
		unblock()
		<-done
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatal("pre-visible claim or native keep omitted", got)
	}
	found := map[string]bool{}
	for _, v := range got {
		found[v.endpoint.Box] = v.native
	}
	if !found["box"] || found["other"] {
		t.Fatal("native tag/foreign box changed", found)
	}
	got[0].endpoint.Box = "changed-copy"
	again, err := p.Observation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range again {
		if v.endpoint.Box == "changed-copy" {
			t.Fatal("caller mutated original copy")
		}
	}
	o.close() // Occupied native claims survive original owner loss; not live proof.
	if err := p.Check(t.Context()); err != nil {
		t.Fatal("native owner expiry erased occupied claim", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("actual legacy Close/release hidden")
	}
}
func TestOperatorClaimInventoryActualFailedPublisherRemainsObserved(t *testing.T) {
	s, _, _ := nativeNonceOwnerFixture(t)
	e := operator.Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	failure := errors.New("actual creator failed")
	if _, err := s.ListenLegacyOperator(t.Context(), e, func() (net.Listener, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	p, err := observeOperatorPublicationClaims(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Observation(t.Context())
	if err != nil || len(got) != 1 || got[0].endpoint != e || got[0].native {
		t.Fatal("failed original claim omitted", got, err)
	}
}
func TestOperatorClaimInventorySameKeyReplacementCannotRepairStickyLoss(t *testing.T) {
	s := &Server{}
	e := operator.Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	original, err := s.operatorPublications.reserve(e, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := observeOperatorPublicationClaims(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.releaseLegacy(); err != nil {
		t.Fatal(err)
	}
	replacement, err := s.operatorPublications.reserve(e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replacement == original {
		t.Fatal("actual reserve reused original object")
	}
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("same bytes adopted replacement claim")
	}
	if err := replacement.releaseLegacy(); err != nil {
		t.Fatal(err)
	}
	// Restore the original map solely as a negative fixture; actual producers
	// have no such operation. A sticky failed observation still cannot recover.
	s.operatorPublications.mu.Lock()
	s.operatorPublications.entries[e.Incarnation] = original
	s.operatorPublications.mu.Unlock()
	if _, err := p.Observation(t.Context()); err == nil {
		t.Fatal("matching original restoration repaired sticky failure")
	}
}
func TestOperatorClaimInventoryAddedClaimAndOriginalContextExpiry(t *testing.T) {
	s := &Server{}
	ctx, cancel := context.WithCancel(t.Context())
	p, err := observeOperatorPublicationClaims(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Observation(ctx)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	cancel()
	if !errors.Is(p.Check(context.WithoutCancel(ctx)), context.Canceled) {
		t.Fatal("original context revived")
	}
	fresh, err := observeOperatorPublicationClaims(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.operatorPublications.reserve(operator.Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Check(t.Context()); err == nil {
		t.Fatal("new pre-visible publisher hidden")
	}
}
func TestOperatorClaimInventoryRefusesMalformedForeignAndOverboundTables(t *testing.T) {
	for _, which := range []string{"nil", "foreign-table", "wrong-key", "bad-endpoint", "overflow"} {
		t.Run(which, func(t *testing.T) {
			s := &Server{}
			e := operator.Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
			c := &operatorPublicationClaim{table: &s.operatorPublications, endpoint: e}
			s.operatorPublications.entries = map[string]*operatorPublicationClaim{e.Incarnation: c}
			switch which {
			case "nil":
				s.operatorPublications.entries[e.Incarnation] = nil
			case "foreign-table":
				c.table = &operatorPublicationClaims{}
			case "wrong-key":
				c.endpoint.Incarnation = strings.Repeat("b", 32)
			case "bad-endpoint":
				c.endpoint.Box = "invalid/name"
			case "overflow":
				for i := 0; i < 1025; i++ {
					s.operatorPublications.entries[string(rune(i))] = c
				}
			}
			if _, err := observeOperatorPublicationClaims(t.Context(), s); err == nil {
				t.Fatal("unknown table accepted")
			}
		})
	}
}

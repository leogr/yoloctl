package controller

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

type publicationTestListener struct {
	net.Listener
	closes  atomic.Int32
	failure error
}

func (l *publicationTestListener) Close() error {
	l.closes.Add(1)
	return l.failure
}

func TestOperatorPublisherNativeClaimExcludesActualLegacyCreator(t *testing.T) {
	s, _, o := nativeNonceOwnerFixture(t)
	nonce, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	e := operator.Endpoint{Box: "box", Incarnation: nonce}
	c, err := o.reserveOperatorPublication(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	creatorCalls := 0
	create := func() (net.Listener, error) { creatorCalls++; return &publicationTestListener{}, nil }
	for _, closeOwner := range []bool{false, true} {
		if closeOwner {
			o.close()
		}
		if _, err := s.ListenLegacyOperator(t.Context(), e, create); !errors.Is(err, operator.ErrNativeRegistration) {
			t.Fatal("native/expired native claim fell through to legacy", err)
		}
	}
	if creatorCalls != 0 {
		t.Fatal("native identity exposed legacy creator")
	}
	if err := c.releaseLegacy(); err == nil {
		t.Fatal("native claim released as legacy")
	}
	other := operator.Endpoint{Box: "box", Incarnation: "0123456789abcdef0123456789abcdef"}
	l, err := s.ListenLegacyOperator(t.Context(), other, create)
	if err != nil {
		t.Fatal("same box unrelated incarnation was suppressed", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorPublisherClaimPrecedesBlockingCreationAndHoldsNoServerLock(t *testing.T) {
	s, _, o := nativeNonceOwnerFixture(t)
	nonce, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	e := operator.Endpoint{Box: "box", Incarnation: nonce}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		l, err := s.ListenLegacyOperator(t.Context(), e, func() (net.Listener, error) {
			close(entered)
			<-release
			return &publicationTestListener{}, nil
		})
		if err == nil {
			err = l.Close()
		}
		done <- err
	}()
	<-entered
	// The actual creator is still blocked: the competing original owner must
	// return now, without waiting for Listen or retaining Server.mu.
	_, claimErr := o.reserveOperatorPublication(t.Context(), e)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if claimErr == nil {
		t.Fatal("native publication raced an already claimed legacy creator")
	}
	if _, err := o.reserveOperatorPublication(t.Context(), e); err != nil {
		t.Fatal("successful exact legacy Close did not release its claim", err)
	}
}

func TestOperatorPublisherFailedCreationAndCloseNeverRetryOrRelease(t *testing.T) {
	for _, phase := range []string{"create", "partial", "close"} {
		t.Run(phase, func(t *testing.T) {
			s := &Server{}
			e := operator.Endpoint{Box: "box", Incarnation: "0123456789abcdef0123456789abcdef"}
			original := &publicationTestListener{}
			failure := errors.New("original failure")
			calls := 0
			create := func() (net.Listener, error) {
				calls++
				if phase == "create" {
					return nil, failure
				}
				if phase == "partial" {
					return original, failure
				}
				original.failure = failure
				return original, nil
			}
			l, err := s.ListenLegacyOperator(t.Context(), e, create)
			if phase == "close" {
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := l.Close(); !errors.Is(err, failure) {
						t.Fatal(err)
					}
				}
			} else if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if _, err := s.ListenLegacyOperator(t.Context(), e, create); err == nil {
				t.Fatal("failed owner was retried")
			}
			wantClose := int32(1)
			if phase == "create" {
				wantClose = 0
			}
			if calls != 1 || original.closes.Load() != wantClose {
				t.Fatal("original operations replayed", calls, original.closes.Load())
			}
		})
	}
}

func TestOperatorPublisherLegacyDisappearanceAndActualSocketPolicyPreserved(t *testing.T) {
	s := &Server{}
	e := operator.Endpoint{Box: "box", Incarnation: "0123456789abcdef0123456789abcdef"}
	if _, err := s.ListenLegacyOperator(t.Context(), e, func() (net.Listener, error) { return nil, os.ErrNotExist }); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	// Short portable socket path, independent of macOS testing.TempDir length.
	directory, err := os.MkdirTemp("", "ycl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(directory) })
	path := filepath.Join(directory, "s")
	l, err := s.ListenLegacyOperator(t.Context(), e, func() (net.Listener, error) { return Listen(path, 0o600) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy unlink-on-close changed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ListenLegacyOperator(ctx, e, func() (net.Listener, error) { t.Fatal("canceled creator called"); return nil, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestOperatorPublisherRejectsForeignOrExpiredOriginalAllocation(t *testing.T) {
	s, r, o := nativeNonceOwnerFixture(t)
	nonce, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	foreign := s.newNativeNonceOwner(t.Context(), Principal{}, request("test.native-nonce"), r, Operation{Mutates: true, EffectGated: true})
	defer foreign.close()
	e := operator.Endpoint{Box: "box", Incarnation: nonce}
	if _, err := foreign.reserveOperatorPublication(t.Context(), e); err == nil {
		t.Fatal("copied receipt/nonce acquired original claim")
	}
	if _, err := o.reserveOperatorPublication(t.Context(), operator.Endpoint{Box: "other", Incarnation: nonce}); err == nil {
		t.Fatal("claim changed allocated box")
	}
	o.close()
	if _, err := o.reserveOperatorPublication(t.Context(), e); err == nil {
		t.Fatal("closed owner revived allocation")
	}
}

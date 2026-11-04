package controller

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

type serveTestListener struct {
	retireTestListener
	started int
	ctx     context.Context
	serve   func(context.Context, net.Conn)
}

func (l *serveTestListener) Start(ctx context.Context, serve func(context.Context, net.Conn)) error {
	l.started++
	l.ctx, l.serve = ctx, serve
	return nil
}

func nativeServeIdentity(box, incarnation string) operator.RegistrationIdentity {
	return operator.RegistrationIdentity{Endpoint: operator.Endpoint{Box: box, Incarnation: incarnation},
		Mode: operator.RegistrationOpenShellNative, HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: strings.Repeat("b", 64)}
}

func stubNativeServeListener(t *testing.T) *[]*serveTestListener {
	t.Helper()
	var made []*serveTestListener
	old := newNativeOperatorListener
	newNativeOperatorListener = func(identity operator.RegistrationIdentity) (nativeServedListener, error) {
		l := &serveTestListener{retireTestListener: retireTestListener{identity: identity}}
		made = append(made, l)
		return l, nil
	}
	t.Cleanup(func() { newNativeOperatorListener = old })
	return &made
}

// The scanner's native registration gets one started listener, on a native
// claim under its incarnation, for the controller's lifetime; it is never
// served twice, and a legacy identity or a held box gets none.
func TestServeNativeOperatorStartsOneClaimedListener(t *testing.T) {
	s, r, _ := nativeNonceOwnerFixture(t)
	made := stubNativeServeListener(t)
	identity := nativeServeIdentity("other", strings.Repeat("d", 32))
	if err := s.ServeNativeOperator(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	if len(*made) != 1 || (*made)[0].started != 1 || (*made)[0].serve == nil || (*made)[0].ctx == nil {
		t.Fatalf("listener not started once: %+v", *made)
	}
	c := s.operatorPublications.entries[identity.Incarnation]
	if c == nil || !c.scanned || c.listener != (*made)[0] {
		t.Fatalf("claim %+v", c)
	}
	if err := s.ServeNativeOperator(t.Context(), identity); err == nil || len(*made) != 1 {
		t.Fatal("a native registration was served twice")
	}
	if _, err := s.operatorPublications.reserve(identity.Endpoint, nil); !errors.Is(err, operator.ErrNativeRegistration) {
		t.Fatalf("the legacy path claimed a native registration: %v", err)
	}
	legacy := operator.LegacyRegistrationIdentity(operator.Endpoint{Box: "other", Incarnation: strings.Repeat("e", 32)})
	if err := s.ServeNativeOperator(t.Context(), legacy); err == nil || len(*made) != 1 {
		t.Fatal("a legacy identity got a native listener")
	}
	// The fixture's RUNNING receipt on box is like the create that waits for
	// this socket: it is served.
	creating := nativeServeIdentity("box", strings.Repeat("a", 32))
	if err := s.ServeNativeOperator(t.Context(), creating); err != nil || len(*made) != 2 {
		t.Fatal("a running create's own registration was not served", err)
	}
	// A finished unresolved receipt holds box: no listener, no claim.
	r.State = "uncertain"
	if err := s.Receipts.Save(*r); err != nil {
		t.Fatal(err)
	}
	held := nativeServeIdentity("box", strings.Repeat("f", 32))
	if err := s.ServeNativeOperator(t.Context(), held); !errors.Is(err, ErrNativeOperatorPublicationHeld) || len(*made) != 2 || s.operatorPublications.entries[held.Incarnation] != nil {
		t.Fatal("a held box's registration was served")
	}
	// Once the host acknowledges the outcome, the scanner's retry serves it.
	r.State = "acknowledged"
	if err := s.Receipts.Save(*r); err != nil {
		t.Fatal(err)
	}
	if err := s.ServeNativeOperator(t.Context(), held); err != nil || len(*made) != 3 {
		t.Fatal("an acknowledged box's registration was not served on retry", err)
	}
}

// A scanner-served listener is retired by the box's removal like any native
// one, and stays claimed afterwards.
func TestScannedNativeListenerRetiresOnRemoval(t *testing.T) {
	s, _, o := nativeNonceOwnerFixture(t)
	identity := nativeServeIdentity("box", strings.Repeat("a", 32))
	c, err := s.operatorPublications.reserveScanned(identity.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	l := &retireTestListener{identity: identity}
	if err := c.attachNativeListener(l); err != nil {
		t.Fatal(err)
	}
	if err := o.retireOperatorListener(t.Context(), "box", identity.Incarnation, nil, nil); err != nil || l.calls != 1 {
		t.Fatalf("retire: %v (%d calls)", err, l.calls)
	}
	if err := o.retireOperatorListener(t.Context(), "box", identity.Incarnation, nil, nil); err == nil || l.calls != 1 {
		t.Fatal("a retired listener was retired again")
	}
	if _, err := s.operatorPublications.reserveScanned(identity.Endpoint); err == nil {
		t.Fatal("a retired registration was claimed again")
	}
}

// Stopping the controller retires every scanner-served listener once.
func TestCloseNativeOperatorsRetiresScannedListeners(t *testing.T) {
	s, _, _ := nativeNonceOwnerFixture(t)
	made := stubNativeServeListener(t)
	for _, inc := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		if err := s.ServeNativeOperator(t.Context(), nativeServeIdentity("other", inc)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseNativeOperators(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseNativeOperators(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, l := range *made {
		if l.calls != 1 {
			t.Fatalf("%s retired %d times", l.identity.Incarnation, l.calls)
		}
	}
}

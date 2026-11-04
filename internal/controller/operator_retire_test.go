package controller

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

type retireTestListener struct {
	identity operator.RegistrationIdentity
	calls    int
	self     net.Conn
	err      error
}

type retireTestSettlement struct{}

func (retireTestSettlement) SelfPending() bool              { return false }
func (retireTestSettlement) WaitSelf(context.Context) error { return nil }

func (l *retireTestListener) Identity() operator.RegistrationIdentity { return l.identity }
func (l *retireTestListener) RetireListener(_ context.Context, self net.Conn) (operator.NativeRetirement, error) {
	l.calls++
	l.self = self
	if l.err != nil {
		return nil, l.err
	}
	return retireTestSettlement{}, nil
}

func nativeRetireFixture(t *testing.T) (*Server, *nativeNonceOwner, *operatorPublicationClaim, *retireTestListener) {
	t.Helper()
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
	l := &retireTestListener{identity: operator.RegistrationIdentity{Endpoint: e, Mode: operator.RegistrationOpenShellNative, HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: strings.Repeat("b", 64)}}
	if err := c.attachNativeListener(l); err != nil {
		t.Fatal(err)
	}
	return s, o, c, l
}

func TestNativeListenerAttachRequiresItsNativeClaim(t *testing.T) {
	_, _, c, l := nativeRetireFixture(t)
	if err := c.attachNativeListener(l); err == nil {
		t.Fatal("second listener attached to one claim")
	}
	s := &Server{}
	legacy, err := s.operatorPublications.reserve(operator.Endpoint{Box: "box", Incarnation: strings.Repeat("c", 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.attachNativeListener(l); err == nil {
		t.Fatal("native listener attached to a legacy claim")
	}
}

// Retirement is one use, marks the claim so it is never released or
// republished, and exempts the connection only for a self-targeting request.
func TestNativeListenerRetirementIsOneUseAndSuppressesRepublication(t *testing.T) {
	s, o, c, l := nativeRetireFixture(t)
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	self := false
	if err := o.retireOperatorListener(t.Context(), "box", c.endpoint.Incarnation, a, func() { self = true }); err != nil {
		t.Fatal(err)
	}
	if l.calls != 1 || l.self != nil || self {
		t.Fatal("admin removal exempted a connection or signalled self-retirement", l.calls, l.self, self)
	}
	if err := o.retireOperatorListener(t.Context(), "box", c.endpoint.Incarnation, a, nil); err == nil || l.calls != 1 {
		t.Fatal("retirement replayed")
	}
	if err := c.releaseLegacy(); err == nil {
		t.Fatal("retired native claim released")
	}
	if _, err := s.ListenLegacyOperator(t.Context(), c.endpoint, func() (net.Listener, error) { t.Fatal("republished"); return nil, nil }); !errors.Is(err, operator.ErrNativeRegistration) {
		t.Fatal("retired registration republished", err)
	}
}

func TestNativeListenerSelfRetirementExemptsOnlyItsConnection(t *testing.T) {
	s, o, c, l := nativeRetireFixture(t)
	o.principal = Principal{Box: "box", Incarnation: c.endpoint.Incarnation}
	o.receipt.Principal = o.principal
	if err := s.Receipts.Save(*o.receipt); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	self := false
	if err := o.retireOperatorListener(t.Context(), "box", c.endpoint.Incarnation, a, func() { self = true }); err != nil {
		t.Fatal(err)
	}
	if l.self != a || !self {
		t.Fatal("self-target removal lost its exact connection exemption")
	}
}

func TestNativeListenerRetirementRefusals(t *testing.T) {
	_, o, c, l := nativeRetireFixture(t)
	for _, target := range []struct{ box, incarnation string }{
		{"other", c.endpoint.Incarnation}, // outside receipt scope
		{"box", "not-hex"},                // invalid
	} {
		if err := o.retireOperatorListener(t.Context(), target.box, target.incarnation, nil, nil); err == nil {
			t.Fatal("retired", target)
		}
	}
	if l.calls != 0 {
		t.Fatal("refusal reached the listener")
	}
	l.err = errors.New("close failed")
	err := o.retireOperatorListener(t.Context(), "box", c.endpoint.Incarnation, nil, nil)
	var uncertain *EffectsUncertain
	if !errors.As(err, &uncertain) {
		t.Fatal("failed retirement is not uncertain", err)
	}
	o.close()
	if err := o.retireOperatorListener(t.Context(), "box", c.endpoint.Incarnation, nil, nil); err == nil {
		t.Fatal("expired execution retired")
	}
}

// An interrupted removal's unresolved receipt keeps the box's endpoint
// unpublished after a controller restart.
func TestNativeOperatorPublicationHeldByUnresolvedReceipt(t *testing.T) {
	s, r, _ := nativeNonceOwnerFixture(t)
	if err := s.CheckNativeOperatorPublication("other"); err != nil {
		t.Fatal("unrelated box held", err)
	}
	// The fixture receipt is a running effect-gated execution on box, like
	// the create that waits for this socket: it must not hold publication.
	if err := s.CheckNativeOperatorPublication("box"); err != nil {
		t.Fatal("running execution held its own box's publication", err)
	}
	r.State = "uncertain"
	if err := s.Receipts.Save(*r); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckNativeOperatorPublication("box"); !errors.Is(err, ErrNativeOperatorPublicationHeld) {
		t.Fatal("unresolved removal receipt allowed republication or lost its retry marker", err)
	}
}

// Without any claim (stopped box, controller restart) retirement occupies the
// table so the incarnation can never be published afterwards.
func TestNativeListenerRetirementWithoutClaimBlocksLaterPublication(t *testing.T) {
	s, o, _, l := nativeRetireFixture(t)
	nonce, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.retireOperatorListener(t.Context(), "box", nonce, nil, nil); err != nil {
		t.Fatal(err)
	}
	if l.calls != 0 {
		t.Fatal("unrelated listener retired")
	}
	e := operator.Endpoint{Box: "box", Incarnation: nonce}
	if _, err := o.reserveOperatorPublication(t.Context(), e); err == nil {
		t.Fatal("retired incarnation reserved for native publication")
	}
	if _, err := s.ListenLegacyOperator(t.Context(), e, func() (net.Listener, error) { t.Fatal("published"); return nil, nil }); !errors.Is(err, operator.ErrNativeRegistration) {
		t.Fatal("retired incarnation published as legacy", err)
	}
	if err := o.retireOperatorListener(t.Context(), "box", nonce, nil, nil); err == nil {
		t.Fatal("retirement replayed")
	}
}

// A claim still publishing (no listener attached yet) is never settled.
func TestNativeListenerRetirementRefusesClaimWithoutListener(t *testing.T) {
	_, o, _, _ := nativeRetireFixture(t)
	nonce, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.reserveOperatorPublication(t.Context(), operator.Endpoint{Box: "box", Incarnation: nonce}); err != nil {
		t.Fatal(err)
	}
	if err := o.retireOperatorListener(t.Context(), "box", nonce, nil, nil); err == nil {
		t.Fatal("publishing claim settled without its listener")
	}
}

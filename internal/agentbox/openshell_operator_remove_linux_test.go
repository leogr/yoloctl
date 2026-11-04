package agentbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

type fakeRegistrationWithdrawal struct {
	identity  operator.RegistrationIdentity
	withdrawn int
	closed    int
	fail      error
}

func (f *fakeRegistrationWithdrawal) Withdraw(ctx context.Context, current func(context.Context) error) error {
	f.withdrawn++
	if f.fail != nil {
		return f.fail
	}
	return current(ctx)
}

func (f *fakeRegistrationWithdrawal) Close() error { f.closed++; return nil }

func nativeIdentity(box, incarnation string) operator.RegistrationIdentity {
	return operator.RegistrationIdentity{Endpoint: operator.Endpoint{Box: box, Incarnation: incarnation}, Mode: operator.RegistrationOpenShellNative, HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: "q"}
}

func stubRegistrationRemoval(t *testing.T, fail map[string]error) *[]*fakeRegistrationWithdrawal {
	t.Helper()
	var made []*fakeRegistrationWithdrawal
	old := prepareOperatorRegistrationRemoval
	prepareOperatorRegistrationRemoval = func(ctx context.Context, identity operator.RegistrationIdentity, owned func(context.Context) error) (operatorRegistrationWithdrawal, error) {
		if err := owned(ctx); err != nil {
			return nil, err
		}
		if err := fail[identity.Incarnation]; err != nil {
			return nil, err
		}
		f := &fakeRegistrationWithdrawal{identity: identity}
		made = append(made, f)
		return f, nil
	}
	t.Cleanup(func() { prepareOperatorRegistrationRemoval = old })
	return &made
}

// The original registrations are retained before effects, accepted by checks
// only until they are withdrawn, and withdrawn once, only after rm.
func TestNativeOperatorRegistrationRemovalWithdrawsOnlyTheOriginals(t *testing.T) {
	made := stubRegistrationRemoval(t, nil)
	owned := func(context.Context) error { return nil }
	a, b := nativeIdentity("coder", "i1"), nativeIdentity("coder", "i2")
	r, err := prepareNativeOperatorRegistrationRemoval(t.Context(), []operator.RegistrationIdentity{a, b}, owned)
	if err != nil || len(*made) != 2 {
		t.Fatalf("prepare: %v, %d prepared", err, len(*made))
	}
	if !r.retained([]operator.RegistrationIdentity{a, b}) || !r.retained(nil) {
		t.Fatal("the original registrations are retained before withdrawal")
	}
	if r.retained([]operator.RegistrationIdentity{nativeIdentity("coder", "later")}) {
		t.Fatal("a later registration was accepted as an original")
	}
	if err := r.withdraw(t.Context(), false, owned); err == nil || (*made)[0].withdrawn != 0 {
		t.Fatal("withdrawal ran without the original rm acknowledgement")
	}
	if err := r.withdraw(t.Context(), true, owned); err != nil {
		t.Fatal(err)
	}
	for _, f := range *made {
		if f.withdrawn != 1 {
			t.Fatalf("%v withdrawn %d times", f.identity, f.withdrawn)
		}
	}
	if r.retained([]operator.RegistrationIdentity{a}) {
		t.Fatal("a withdrawn registration is still accepted")
	}
	if err := r.withdraw(t.Context(), true, owned); err == nil {
		t.Fatal("a replayed withdrawal was accepted")
	}
	if err := r.Close(); err != nil || (*made)[0].closed != 1 || (*made)[1].closed != 1 {
		t.Fatalf("close: %v", err)
	}
}

// A failed preparation releases the earlier ones; a failed withdrawal poisons
// the removal; no identities or no owner check is refused.
func TestNativeOperatorRegistrationRemovalFailsClosed(t *testing.T) {
	boom := errors.New("boom")
	made := stubRegistrationRemoval(t, map[string]error{"i2": boom})
	owned := func(context.Context) error { return nil }
	if _, err := prepareNativeOperatorRegistrationRemoval(t.Context(), []operator.RegistrationIdentity{nativeIdentity("coder", "i1"), nativeIdentity("coder", "i2")}, owned); !errors.Is(err, boom) {
		t.Fatalf("prepare: %v", err)
	}
	if len(*made) != 1 || (*made)[0].closed != 1 {
		t.Fatal("the earlier preparation was not released")
	}
	if _, err := prepareNativeOperatorRegistrationRemoval(t.Context(), nil, owned); err == nil {
		t.Fatal("no identities accepted")
	}
	if _, err := prepareNativeOperatorRegistrationRemoval(t.Context(), []operator.RegistrationIdentity{nativeIdentity("coder", "i1")}, nil); err == nil {
		t.Fatal("no owner check accepted")
	}

	made = stubRegistrationRemoval(t, nil)
	r, err := prepareNativeOperatorRegistrationRemoval(t.Context(), []operator.RegistrationIdentity{nativeIdentity("coder", "i1")}, owned)
	if err != nil {
		t.Fatal(err)
	}
	(*made)[0].fail = boom
	if err := r.withdraw(t.Context(), true, owned); !errors.Is(err, boom) {
		t.Fatalf("withdraw: %v", err)
	}
	if r.retained(nil) {
		t.Fatal("a failed withdrawal still accepts retained registrations")
	}
	if err := r.withdraw(t.Context(), true, owned); err == nil {
		t.Fatal("a poisoned withdrawal was retried")
	}
}

func TestLiveNativeOperatorIdentitySelectsOnlyTheNativeLiveRegistration(t *testing.T) {
	live := operator.Endpoint{Box: "dev", Incarnation: strings.Repeat("a", 32)}
	old := operator.Endpoint{Box: "dev", Incarnation: strings.Repeat("c", 32)}
	native := func(e operator.Endpoint) operator.RegistrationIdentity {
		return operator.RegistrationIdentity{Endpoint: e, Mode: operator.RegistrationOpenShellNative, HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: strings.Repeat("b", 64)}
	}
	got := liveNativeOperatorIdentity([]operator.RegistrationIdentity{operator.LegacyRegistrationIdentity(old), native(live)}, live)
	if got == nil || *got != native(live) {
		t.Fatal("live native registration not selected", got)
	}
	for _, ids := range [][]operator.RegistrationIdentity{
		{native(old)}, // live registration missing
		{operator.LegacyRegistrationIdentity(live)}, // legacy: nothing to retire
	} {
		if got := liveNativeOperatorIdentity(ids, live); got != nil {
			t.Fatal("selected", ids)
		}
	}
	// Without the original controller request there is no local fallback.
	if err := operator.RetireNativeListener(t.Context(), native(live)); err == nil {
		t.Fatal("retired without the original controller request")
	}
}

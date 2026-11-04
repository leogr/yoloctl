package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

type nativePolicyManagedInner struct {
	*nativeRestartPreflightInner
	fault               error
	events              *[]string
	qualified, launched []byte
}

func (a *nativePolicyManagedInner) PreflightPolicyRestart(_ context.Context, _ *agentbox.Config, policy []byte) error {
	*a.events = append(*a.events, "policy-qualification")
	a.qualified = bytes.Clone(policy)
	if len(policy) > 0 {
		policy[0] = 'X'
	}
	return a.fault
}
func (a *nativePolicyManagedInner) Restart(ctx context.Context, c *agentbox.Config, policy []byte) error {
	*a.events = append(*a.events, "restart")
	a.launched = bytes.Clone(policy)
	return a.fakeManagedInner.Restart(ctx, c, policy)
}
func TestManagedNativePolicyPreflightUsesActualPolicyBeforeHolderAndPrefersPolicyAwareInterface(t *testing.T) {
	for _, kind := range []string{"success", "known-refusal", "uncertain-refusal"} {
		t.Run(kind, func(t *testing.T) {
			var events []string
			cause := errors.New("native policy/helper qualification failed")
			var failure error
			if kind == "known-refusal" {
				failure = cause
			}
			if kind == "uncertain-refusal" {
				failure = &agentbox.NativeEffectsUncertain{Cause: cause}
			}
			inner := &nativePolicyManagedInner{nativeRestartPreflightInner: &nativeRestartPreflightInner{fakeManagedInner: &fakeManagedInner{}, failure: errors.New("old optional interface must not be selected")}, fault: failure, events: &events}
			a := managedApplier{inner: inner, cleanup: func(context.Context, *agentbox.Config) (bool, error) {
				events = append(events, "holder")
				return true, nil
			}}
			policy := []byte("actual desired policy")
			err := a.Restart(t.Context(), &agentbox.Config{Name: "coder"}, policy)
			want := []string{"policy-qualification"}
			if kind == "success" {
				want = append(want, "holder", "restart")
			}
			if !reflect.DeepEqual(events, want) || inner.checked || !bytes.Equal(inner.qualified, policy) || string(policy) != "actual desired policy" {
				t.Fatal("policy qualification did not precede holder/effects or used wrong policy", events, want, err)
			}
			if kind == "success" {
				if err != nil || !bytes.Equal(inner.launched, policy) {
					t.Fatal("policy qualification changed actual restart policy", err, string(inner.launched))
				}
			} else {
				if !errors.Is(err, cause) || len(inner.calls) != 0 {
					t.Fatal("policy refusal continued session/runtime effects", err, inner.calls)
				}
				if workerEffectsUncertain(err) != (kind == "uncertain-refusal") {
					t.Fatal("policy qualification lost typed uncertainty", err)
				}
			}
		})
	}
}

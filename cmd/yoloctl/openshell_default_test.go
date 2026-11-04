package main

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

func TestNativeDefaultUnavailablePrecedesCreateAndRecreateEffects(t *testing.T) {
	failure := errors.New("embedded native runtime unavailable")
	for _, method := range []string{"create", "recreate"} {
		t.Run(method, func(t *testing.T) {
			inner := &fakeManagedInner{}
			a := managedApplier{inner: inner, creationErr: failure,
				cleanup: func(context.Context, *agentbox.Config) (bool, error) {
					t.Fatal("holder ended despite unavailable replacement")
					return false, nil
				}}
			cfg := &agentbox.Config{Name: "trial"}
			var err error
			if method == "create" {
				err = a.Create(t.Context(), cfg, nil)
			} else {
				err = a.Recreate(t.Context(), cfg, nil)
			}
			if !errors.Is(err, failure) || len(inner.calls) != 0 {
				t.Fatalf("err=%v inner calls=%v", err, inner.calls)
			}
		})
	}
}

func TestNativeDefaultUnavailableDoesNotChangeExistingRestart(t *testing.T) {
	inner := &fakeManagedInner{}
	a := managedApplier{inner: inner, creationErr: errors.New("new allocation unavailable")}
	if err := a.Restart(t.Context(), &agentbox.Config{Name: "existing"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "restart" {
		t.Fatalf("calls=%v", inner.calls)
	}
}

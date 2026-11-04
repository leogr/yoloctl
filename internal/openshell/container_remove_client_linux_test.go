package openshell

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeContainerRemovalClientRequiresActualInfrastructureBeforeCallbacks(t *testing.T) {
	f, _ := generationFixture(t)
	calls := 0
	read := func(context.Context) error { calls++; return nil }
	owner := NativeContainerRemoval{VerifyOwned: read, Stop: read, VerifyInactive: read, RemoveContainerID: func(context.Context, string) error { calls++; return nil }, VerifyRemaining: read, WithdrawEndpoints: read}
	if err := RemoveNativeContainer(t.Context(), f.record, f.release, strings.Repeat("a", 64), owner); err == nil || calls != 0 {
		t.Fatal("ordinary executor reached native removal effects/owner callbacks", err, calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RemoveNativeContainer(ctx, f.record, f.release, strings.Repeat("a", 64), owner); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled removal reached native callbacks", err, calls)
	}
}

func TestNativeContainerRemovalClientRefusesMissingActualCallbacks(t *testing.T) {
	f, _ := generationFixture(t)
	for _, name := range []string{"owner", "Stop", "inactive", "rm", "remaining", "endpoints"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			read := func(context.Context) error { calls++; return nil }
			owner := NativeContainerRemoval{read, read, read, func(context.Context, string) error { calls++; return nil }, read, read}
			switch name {
			case "owner":
				owner.VerifyOwned = nil
			case "Stop":
				owner.Stop = nil
			case "inactive":
				owner.VerifyInactive = nil
			case "rm":
				owner.RemoveContainerID = nil
			case "remaining":
				owner.VerifyRemaining = nil
			case "endpoints":
				owner.WithdrawEndpoints = nil
			}
			if err := RemoveNativeContainer(t.Context(), f.record, f.release, strings.Repeat("a", 64), owner); err == nil || calls != 0 {
				t.Fatal("incomplete callback set reached native removal", name, err, calls)
			}
		})
	}
}

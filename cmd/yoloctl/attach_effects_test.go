package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

func TestBoxAttachRegistrationOwnsAuthorizedSetup(t *testing.T) {
	// internal/controller's real-worker owned-setup regressions use this policy.
	want := controller.Operation{BoxAllowed: true, Mutates: true, EffectGated: true, OwnedSetup: true,
		Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages}
	if got := controllerOperations["box.attach"]; got != want {
		t.Fatalf("box.attach policy %+v, want %+v", got, want)
	}
	for method, op := range controllerOperations {
		if op.OwnedSetup && method != "box.attach" {
			t.Fatalf("%s opted into owned setup", method)
		}
	}
}

// attachWrapperFixture runs a recording `box attach` through the real command
// wrapper inside a controller worker context with the worker's gate and
// acknowledged setup checkpoint.
type attachWrapperFixture struct {
	t        *testing.T
	events   []string
	gateErr  error
	syncErr  error
	ack      error
	operator bool
}

func (f *attachWrapperFixture) run(args ...string) error {
	f.t.Helper()
	f.t.Setenv("XDG_CONFIG_HOME", f.t.TempDir())
	previousSync, previousRevalidate := syncNativeChatRolesFn, revalidateOperatorFn
	f.t.Cleanup(func() { syncNativeChatRolesFn, revalidateOperatorFn = previousSync, previousRevalidate })
	locked := func(ctx context.Context) bool {
		probe, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		unlock, err := spacelock.Acquire(probe, "work", true)
		if err == nil {
			unlock()
		}
		return err != nil
	}
	revalidateOperatorFn = func(ctx context.Context) error {
		f.events = append(f.events, "revalidate")
		return nil
	}
	syncNativeChatRolesFn = func(ctx context.Context, _ string) error {
		if !locked(ctx) {
			f.t.Error("role sync ran without the native lock")
		}
		f.events = append(f.events, "role-sync")
		return f.syncErr
	}
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	box := &cobra.Command{Use: "box"}
	attach := &cobra.Command{Use: "attach", RunE: func(cmd *cobra.Command, _ []string) error {
		// Stands for every attach write: live-launch binding, observation
		// folding, tombstone removal, container start and holder start.
		f.events = append(f.events, "write")
		return releaseSpaceOperation(cmd.Context())
	}}
	attach.Flags().String("space", "work", "")
	attach.Flags().Bool(newSessionFlag, false, "")
	box.AddCommand(attach)
	root.AddCommand(box)
	registerSpaceOperations(root)
	ctx := context.WithValue(f.t.Context(), controllerRequestKey{}, "request-0123456789abcdef")
	if f.operator {
		ctx = withControllerPrincipal(ctx, controller.Principal{Box: "caller", Incarnation: "live"})
	}
	ctx = controller.WithEffectGate(ctx, func(ctx context.Context, _ controller.EffectIntent) error {
		if !locked(ctx) {
			f.t.Error("the gate ran before the native lock was held")
		}
		f.events = append(f.events, "gate")
		return f.gateErr
	})
	ctx = context.WithValue(ctx, controllerReleaseKey{}, func() error {
		f.events = append(f.events, "setup-checkpoint")
		return f.ack
	})
	root.SetArgs(append([]string{"box", "attach", "coder", "--space", "work"}, args...))
	err := root.ExecuteContext(ctx)
	if locked(f.t.Context()) {
		f.t.Error("the wrapper kept its native lock")
	}
	return err
}

func TestAttachWrapperGatesEveryEffect(t *testing.T) {
	for _, operator := range []bool{false, true} {
		f := &attachWrapperFixture{t: t, operator: operator}
		if err := f.run(); err != nil {
			t.Fatal(err)
		}
		want := []string{"gate", "write", "role-sync", "setup-checkpoint"}
		if operator {
			want = append([]string{"revalidate"}, want...)
		}
		if !slices.Equal(f.events, want) {
			t.Fatalf("operator=%v order %v, want %v", operator, f.events, want)
		}
	}
}

func TestDeniedAttachGateWritesNothing(t *testing.T) {
	denied := errors.New("client left before permission")
	f := &attachWrapperFixture{t: t, gateErr: denied}
	if err := f.run(); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if !slices.Equal(f.events, []string{"gate"}) {
		t.Fatal("a denied attach ran setup, role sync or its checkpoint", f.events)
	}
}

func TestFailedRoleSyncNeverAnnouncesSetup(t *testing.T) {
	sentinel := errors.New("role reload uncertain")
	f := &attachWrapperFixture{t: t, syncErr: sentinel}
	if err := f.run(); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if !slices.Equal(f.events, []string{"gate", "write", "role-sync"}) {
		t.Fatal("a failed role sync reached the setup checkpoint", f.events)
	}
}

func TestDetachedAcknowledgmentEndsTheAttach(t *testing.T) {
	f := &attachWrapperFixture{t: t, ack: controller.ErrClientDetached}
	if err := f.run(); !errors.Is(err, controller.ErrClientDetached) {
		t.Fatal("the attach streamed after its client left", err)
	}
	if !slices.Equal(f.events, []string{"gate", "write", "role-sync", "setup-checkpoint"}) {
		t.Fatal(f.events)
	}
}

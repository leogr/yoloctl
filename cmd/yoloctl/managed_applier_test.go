package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

type fakeManagedInner struct {
	calls []string
}

func (f *fakeManagedInner) Create(context.Context, *agentbox.Config, []byte) error {
	f.calls = append(f.calls, "create")
	return nil
}

func (f *fakeManagedInner) Recreate(context.Context, *agentbox.Config, []byte) error {
	f.calls = append(f.calls, "recreate")
	return nil
}

func (f *fakeManagedInner) Restart(context.Context, *agentbox.Config, []byte) error {
	f.calls = append(f.calls, "restart")
	return nil
}

func (f *fakeManagedInner) AddPortForward(context.Context, *agentbox.Config, agentbox.Port) error {
	f.calls = append(f.calls, "add-port")
	return nil
}

func (f *fakeManagedInner) RemovePortForward(context.Context, *agentbox.Config, agentbox.Port) error {
	f.calls = append(f.calls, "remove-port")
	return nil
}

func TestManagedApplierCleansBeforeRecreateAndRestart(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, managedApplier, *agentbox.Config) error
		want []string
	}{
		{
			name: "recreate",
			run: func(ctx context.Context, a managedApplier, cfg *agentbox.Config) error {
				return a.Recreate(ctx, cfg, []byte("policy"))
			},
			want: []string{"cleanup:coder", "recreate"},
		},
		{
			name: "restart",
			run: func(ctx context.Context, a managedApplier, cfg *agentbox.Config) error {
				return a.Restart(ctx, cfg, []byte("policy"))
			},
			want: []string{"cleanup:coder", "restart"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &fakeManagedInner{}
			calls := []string{}
			applier := managedApplier{
				inner: inner,
				cleanup: func(_ context.Context, cfg *agentbox.Config) (bool, error) {
					calls = append(calls, "cleanup:"+cfg.Name)
					return true, nil
				},
			}
			if err := tc.run(context.Background(), applier, &agentbox.Config{Name: "coder"}); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, inner.calls...)
			if !slices.Equal(calls, tc.want) {
				t.Fatalf("calls = %v, want %v", calls, tc.want)
			}
		})
	}
}

func TestManagedApplierCreateDoesNotCleanSession(t *testing.T) {
	inner := &fakeManagedInner{}
	applier := managedApplier{
		inner: inner,
		cleanup: func(context.Context, *agentbox.Config) (bool, error) {
			t.Fatal("cleanup called for create")
			return false, nil
		},
	}

	if err := applier.Create(context.Background(), &agentbox.Config{Name: "coder"}, []byte("policy")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inner.calls, []string{"create"}) {
		t.Fatalf("calls = %v, want [create]", inner.calls)
	}
}

func TestManagedApplierCleanupErrorStopsRecreate(t *testing.T) {
	inner := &fakeManagedInner{}
	want := errors.New("cleanup failed")
	applier := managedApplier{
		inner: inner,
		cleanup: func(context.Context, *agentbox.Config) (bool, error) {
			return false, want
		},
	}

	err := applier.Recreate(context.Background(), &agentbox.Config{Name: "coder"}, []byte("policy"))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("inner called after cleanup failure: %v", inner.calls)
	}
}

func TestManagedRecreateChecksMemoryPoolBeforeSessionCleanup(t *testing.T) {
	want := errors.New("old VM image has no workload pool")
	a := managedApplier{
		prepareRecreate: func(context.Context) error { return want },
		cleanup: func(context.Context, *agentbox.Config) (bool, error) {
			t.Fatal("session stopped before memory preflight")
			return false, nil
		},
	}
	if err := a.Recreate(t.Context(), &agentbox.Config{Name: "coder"}, nil); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

type qualifiedRecreateInner struct {
	fakeManagedInner
	preflight func(context.Context, *agentbox.Config, []byte) error
	recreate  func(context.Context, *agentbox.Config, []byte) error
}

func (f *qualifiedRecreateInner) PreflightRecreate(ctx context.Context, c *agentbox.Config, policy []byte) error {
	return f.preflight(ctx, c, policy)
}
func (f *qualifiedRecreateInner) Recreate(ctx context.Context, c *agentbox.Config, policy []byte) error {
	return f.recreate(ctx, c, policy)
}

func TestManagedRecreateQualifiesActualReplacementBeforeHolder(t *testing.T) {
	for _, refusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "qualified", true: "refused"}[refusal], func(t *testing.T) {
			want := errors.New("selected image or helper not qualified")
			cfg := &agentbox.Config{Name: "coder"}
			policy := []byte("actual desired policy")
			var calls []string
			inner := &qualifiedRecreateInner{
				preflight: func(_ context.Context, c *agentbox.Config, p []byte) error {
					calls = append(calls, "qualify")
					if c != cfg || string(p) != string(policy) {
						t.Fatal("replacement prerequisite received another declaration/policy")
					}
					p[0] = '!'
					if refusal {
						return want
					}
					return nil
				},
				recreate: func(_ context.Context, _ *agentbox.Config, p []byte) error {
					calls = append(calls, "replace")
					if string(p) != "actual desired policy" {
						t.Fatal("prerequisite mutated replacement policy")
					}
					return nil
				},
			}
			a := managedApplier{inner: inner,
				prepareRecreate: func(context.Context) error { calls = append(calls, "pool"); return nil },
				cleanup: func(context.Context, *agentbox.Config) (bool, error) {
					calls = append(calls, "holder")
					return true, nil
				},
			}
			err := a.Recreate(t.Context(), cfg, policy)
			if refusal {
				if !errors.Is(err, want) || !slices.Equal(calls, []string{"pool", "qualify"}) {
					t.Fatal("refusal ended holder or replaced runtime", err, calls)
				}
			} else if err != nil || !slices.Equal(calls, []string{"pool", "qualify", "holder", "replace"}) {
				t.Fatal("replacement ordering", err, calls)
			}
			if string(policy) != "actual desired policy" {
				t.Fatal("prerequisite mutated caller policy")
			}
		})
	}
}

func TestManagedRecreateCancellationAfterQualificationPreservesHolder(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inner := &qualifiedRecreateInner{
		preflight: func(context.Context, *agentbox.Config, []byte) error { cancel(); return nil },
		recreate: func(context.Context, *agentbox.Config, []byte) error {
			t.Fatal("canceled replacement reached container effects")
			return nil
		},
	}
	a := managedApplier{inner: inner, cleanup: func(context.Context, *agentbox.Config) (bool, error) {
		t.Fatal("canceled prerequisite ended healthy holder")
		return false, nil
	}}
	if err := a.Recreate(ctx, &agentbox.Config{Name: "coder"}, []byte("policy")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Old path: recreating a box whose container is gone (after a space
// recreate) creates it instead of failing the inspect.
func TestManagedRecreateCreatesWhenContainerIsMissing(t *testing.T) {
	inner := &fakeManagedInner{}
	calls := []string{}
	applier := managedApplier{
		inner: inner,
		inspect: func(context.Context, string) (*container.Details, error) {
			return nil, errors.New(`inspect coder: exit status 1 (time="..." level=fatal msg="1 errors:\nno such object: coder")`)
		},
		cleanup: func(_ context.Context, cfg *agentbox.Config) (bool, error) {
			calls = append(calls, "cleanup:"+cfg.Name)
			return true, nil
		},
	}
	if err := applier.Recreate(context.Background(), &agentbox.Config{Name: "coder"}, []byte("policy")); err != nil {
		t.Fatal(err)
	}
	if calls = append(calls, inner.calls...); !slices.Equal(calls, []string{"cleanup:coder", "create"}) {
		t.Fatalf("calls = %v, want [cleanup:coder create]", calls)
	}
	// Any other inspect failure still stops the recreate before effects.
	inner.calls = nil
	applier.inspect = func(context.Context, string) (*container.Details, error) {
		return nil, errors.New("engine unavailable")
	}
	if err := applier.Recreate(context.Background(), &agentbox.Config{Name: "coder"}, []byte("policy")); err == nil || len(inner.calls) != 0 {
		t.Fatal("inspect failure reached effects", err, inner.calls)
	}
}

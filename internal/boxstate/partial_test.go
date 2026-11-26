package boxstate

import (
	"context"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

// A FullDiff read whose context ends part way keeps the boxes finished before
// then, and reports every other existing container as a drift error: never as
// resolved with a step (the launcher check) silently lost to the expiry.
func TestResolveNamedIsPartialWhenItsContextEnds(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"alpha", "beta", "gamma"} {
		saveBox(t, "default", name)
	}
	containers := []container.Container{{Name: "alpha", Status: "Up"}, {Name: "beta", Status: "Up"}, {Name: "gamma", Status: "Up"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testResolver(containers)
	inspect := r.InspectActual
	r.InspectActual = func(ctx context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
		if cfg.Name == "beta" {
			// beta's inspection finishes, but the context ends while its
			// evidence is still being read (before the launcher check).
			actual, err := inspect(ctx, cfg)
			cancel()
			return actual, err
		}
		if err := ctx.Err(); err != nil {
			return agentbox.ActualState{}, err
		}
		return inspect(ctx, cfg)
	}
	launched := map[string]bool{}
	r.Launcher = func(ctx context.Context, _ string, name string) (agentbox.LauncherSupport, error) {
		if err := ctx.Err(); err != nil {
			return agentbox.LauncherSupport{}, err
		}
		launched[name] = true
		return agentbox.LauncherSupport{}, nil
	}
	got, err := r.ResolveNamed(ctx, "default", []string{"alpha", "beta", "gamma"}, FullDiff)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Box{}
	for _, b := range got {
		byName[b.Name] = b
	}
	if a := byName["alpha"]; a.Drift != DriftInSync || a.DriftError != "" || a.DiffPlan == nil || !launched["alpha"] {
		t.Fatalf("a box finished before the end lost its result: %+v", a)
	}
	for _, name := range []string{"beta", "gamma"} {
		b := byName[name]
		if b.Drift != DriftError || b.DriftError != context.Canceled.Error() || b.DiffPlan != nil {
			t.Fatalf("%s read across the end of its context shows %s (%q, plan %v)", name, b.Drift, b.DriftError, b.DiffPlan != nil)
		}
	}
	if launched["beta"] || launched["gamma"] {
		t.Fatalf("launcher ran on an ended context: %v", launched)
	}

	// The same read on a live context is unchanged.
	full, err := testResolver(containers).ResolveNamed(context.Background(), "default", []string{"alpha", "beta", "gamma"}, FullDiff)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range full {
		if b.Drift != DriftInSync || b.DriftError != "" {
			t.Fatalf("live read of %s = %s (%q)", b.Name, b.Drift, b.DriftError)
		}
	}
}

// Each sees every box of the per-box pass right after that box's own
// resolution, in order and on the read's context: a box finished before the
// context ended is handed over while it is still live, its final state
// included.
func TestResolveNamedEachSeesEveryBoxAsItEnds(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"alpha", "beta"} {
		saveBox(t, "default", name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testResolver([]container.Container{{Name: "alpha", Status: "Up"}, {Name: "beta", Status: "Up"}})
	inspect := r.InspectActual
	r.InspectActual = func(ctx context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
		if cfg.Name == "beta" {
			cancel()
			return agentbox.ActualState{}, ctx.Err()
		}
		return inspect(ctx, cfg)
	}
	type seen struct {
		name  string
		live  bool
		drift DriftState
	}
	var got []seen
	r.Each = func(ctx context.Context, b Box) {
		got = append(got, seen{b.Name, ctx.Err() == nil, b.Drift})
	}
	if _, err := r.ResolveNamed(ctx, "default", []string{"beta", "alpha"}, FullDiff); err != nil {
		t.Fatal(err)
	}
	want := []seen{{"alpha", true, DriftInSync}, {"beta", false, DriftError}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Each saw %+v, want %+v", got, want)
	}
}

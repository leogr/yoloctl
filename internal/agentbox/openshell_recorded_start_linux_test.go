package agentbox

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Runs the actual shared reconcile planner and accepted restart/rollback core
// together. SDK/kernel effects are scripted here; this is no native QA proof.
type recordedStartPolicyApplier struct {
	fakeApplier
	restart func(context.Context, *Config, []byte) error
}

func (a *recordedStartPolicyApplier) Restart(ctx context.Context, c *Config, p []byte) error {
	return a.restart(ctx, c, p)
}
func TestNativeStartRecordedPolicyThenActualReconcileAndFreshRollback(t *testing.T) {
	for _, caller := range []string{"start", "restart", "running-reconcile"} {
		for _, ports := range [][]Port{nil, {{Published: 9000, Target: 8000}}} {
			for _, failure := range []string{"known-readiness", "unknown-launch", "unknown-cleanup", "failed-restore", "success"} {
				t.Run(caller+"/"+failure+map[bool]string{true: "/empty", false: "/changed"}[len(ports) == 0], func(t *testing.T) {
					cfg := validConfig()
					cfg.Ports = append([]Port(nil), ports...)
					desired := mustCompose(t, cfg)
					original := append([]byte("# recorded A\n"), desired...)
					observedPorts := []Port{{Published: 9100, Target: 7100}}
					actual := ActualState{Exists: true, Image: mustResolveImage(t, cfg), PolicyContent: original, WorkloadPoolKnown: true, WorkloadPool: true, PortForwards: append([]Port(nil), observedPorts...)}
					inspector := &fakeInspector{state: actual}
					applied := LiveApplier{}
					if caller != "running-reconcile" {
						applied = applied.WithDeclaredForwardRestoration(cfg)
					}
					var launches []string
					boot := &NativeRecordedStart{space: cfg.Space, name: cfg.Name, boxID: cfg.ID, check: func(context.Context) error { return nil }, start: func(context.Context) error {
						launches = append(launches, "A-fresh")
						inspector.state.PolicyContent = bytes.Clone(original)
						inspector.state.PortForwards = nil
						return nil
					}}
					if caller != "running-reconcile" {
						if err := boot.Start(t.Context(), cfg); err != nil {
							t.Fatal(err)
						}
					}
					cause := errors.New("B readiness failed after proved cleanup")
					restores := 0
					applier := &recordedStartPolicyApplier{}
					applier.restart = func(ctx context.Context, c *Config, p []byte) error {
						if !bytes.Equal(p, desired) {
							t.Fatal("reconcile did not select desired B")
						}
						// Production qualifier supplies a complete observed snapshot before any
						// withdrawal. The explicit intent must override even an empty inventory.
						observed := *c
						observed.Ports = append([]Port(nil), inspector.state.PortForwards...)
						rollback, err := applied.nativeRollbackConfig(c, &observed)
						if err != nil {
							return err
						}
						return completeNativePolicyRestart(ctx, original, p, nativePolicyRestartActions{
							preflight: func(context.Context) error { return nil }, withdraw: func(context.Context) error { inspector.state.PortForwards = nil; return nil }, stop: func(context.Context) error { return nil },
							launch: func(_ context.Context, policy []byte, restore bool) error {
								if !restore {
									launches = append(launches, "B-fresh")
									if failure == "success" {
										inspector.state.PolicyContent = bytes.Clone(policy)
										inspector.state.PortForwards = append([]Port(nil), c.Ports...)
										return nil
									}
									if failure == "unknown-launch" || failure == "unknown-cleanup" {
										return &NativeEffectsUncertain{Cause: cause}
									}
									return cause
								}
								restores++
								launches = append(launches, "A-restored-fresh")
								if failure == "failed-restore" {
									return errors.New("restoration readiness failed")
								}
								selected, err := nativePolicyLaunchConfig(c, rollback, true)
								if err != nil {
									return err
								}
								if !bytes.Equal(policy, original) {
									t.Fatal("rollback lost A")
								}
								inspector.state.PolicyContent = bytes.Clone(policy)
								inspector.state.PortForwards = append([]Port(nil), selected.Ports...)
								return nil
							},
						})
					}
					_, err := ReconcileForStart(t.Context(), cfg, inspector, applier)
					switch failure {
					case "success":
						if err != nil || restores != 0 || !bytes.Equal(inspector.state.PolicyContent, desired) {
							t.Fatal("B reconcile failed", err, launches)
						}
					case "unknown-launch", "unknown-cleanup":
						var u *NativeEffectsUncertain
						if !errors.As(err, &u) || restores != 0 {
							t.Fatal("unknown result restored", err, launches)
						}
					case "failed-restore":
						var u *NativeEffectsUncertain
						if !errors.As(err, &u) || restores != 1 || strings.Contains(err.Error(), "restored with") {
							t.Fatal("failed restoration claimed success", err, launches)
						}
					default:
						want := ports
						if caller == "running-reconcile" {
							want = observedPorts
						}
						if !errors.Is(err, cause) || restores != 1 || !strings.Contains(err.Error(), "restored with a fresh generation") || !bytes.Equal(inspector.state.PolicyContent, original) || !reflect.DeepEqual(inspector.state.PortForwards, want) {
							t.Fatal("actual reconcile lost recorded-policy/forward restoration", err, launches, inspector.state.PortForwards, want)
						}
					}
				})
			}
		}
	}
}

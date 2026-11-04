package agentbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/port"
)

// Scripted policy orchestration only. Actual SDK preparation/file readback and
// finite engine Start boundaries are covered separately; no root/VM claim.
func TestNativeGenerationPolicyRestartSingleFreshRollbackRetainsOriginalPolicy(t *testing.T) {
	for _, stage := range []string{"success", "preflight", "withdraw", "stop", "launch-known", "launch-uncertain", "rollback-known", "rollback-uncertain"} {
		t.Run(stage, func(t *testing.T) {
			original, newPolicy := []byte("original-private-policy"), []byte("new-private-policy")
			cause, rollbackCause := errors.New("new policy refused"), errors.New("rollback failed")
			var events []string
			launches := 0
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					events = append(events, name)
					if stage == name {
						return cause
					}
					return nil
				}
			}
			a := nativePolicyRestartActions{preflight: step("preflight"), withdraw: step("withdraw"), stop: step("stop"), launch: func(_ context.Context, policy []byte, restore bool) error {
				launches++
				events = append(events, fmt.Sprintf("launch-%d", launches))
				expected := newPolicy
				if launches == 2 {
					expected = original
				}
				if restore != (launches == 2) {
					t.Fatal("rollback launch role differed from original call order")
				}
				if !bytes.Equal(policy, expected) {
					t.Fatal("rollback lost original policy or launched recursively", launches, string(policy))
				}
				policy[0] = 'X' // Callback cannot mutate checkpoint/caller policy.
				if launches == 1 {
					if stage == "success" {
						return nil
					}
					if stage == "launch-uncertain" {
						return &NativeEffectsUncertain{Cause: cause}
					}
					return cause
				}
				if stage == "rollback-known" {
					return rollbackCause
				}
				if stage == "rollback-uncertain" {
					return &NativeEffectsUncertain{Cause: rollbackCause}
				}
				return nil
			}}
			err := completeNativePolicyRestart(t.Context(), original, newPolicy, a)
			want := []string{"preflight", "withdraw", "stop", "launch-1"}
			switch stage {
			case "preflight":
				want = want[:1]
			case "withdraw":
				want = want[:2]
			case "stop":
				want = want[:3]
			case "success", "launch-uncertain":
			default:
				want = append(want, "launch-2")
			}
			if !reflect.DeepEqual(events, want) || string(original) != "original-private-policy" || string(newPolicy) != "new-private-policy" {
				t.Fatal("policy restart lost ordering/original bytes", events, want)
			}
			var uncertain *NativeEffectsUncertain
			switch stage {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "launch-uncertain":
				if !errors.As(err, &uncertain) || !errors.Is(err, cause) || launches != 1 {
					t.Fatal("uncertain policy launch retried/rolled back", err, launches)
				}
			case "rollback-known", "rollback-uncertain":
				if !errors.As(err, &uncertain) || !errors.Is(err, cause) || !errors.Is(err, rollbackCause) || launches != 2 || strings.Contains(err.Error(), "restored with") {
					t.Fatal("unproved rollback claimed restoration or lost uncertainty", err, launches)
				}
			case "launch-known":
				if err == nil || errors.As(err, &uncertain) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "restored with a fresh generation") {
					t.Fatal("known restored rollback result was not truthful", err)
				}
			default:
				if !errors.Is(err, cause) || launches != 0 {
					t.Fatal("pre-launch failure continued effects", err, launches)
				}
			}
		})
	}
}

func TestNativeGenerationPolicyRestartCancellationAfterOldACKUsesBoundedOriginalAdmission(t *testing.T) {
	for _, point := range []string{"before-preflight", "after-preflight", "after-withdraw", "after-stop", "after-new-cleanup"} {
		t.Run(point, func(t *testing.T) {
			key := nativeStopContextKey{}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key, "admitted"))
			defer cancel()
			events := []string{}
			launches := 0
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					events = append(events, name)
					if point == "after-"+name {
						cancel()
					}
					return nil
				}
			}
			if point == "before-preflight" {
				cancel()
			}
			a := nativePolicyRestartActions{preflight: step("preflight"), withdraw: step("withdraw"), stop: step("stop"), launch: func(c context.Context, p []byte, restore bool) error {
				launches++
				if string(p) == "new" {
					if restore {
						t.Fatal("new policy marked as rollback")
					}
					if point != "after-new-cleanup" {
						t.Fatal("cancelled new launch executed")
					}
					cancel()
					return context.Canceled
				}
				deadline, ok := c.Deadline()
				if !restore || string(p) != "old" || c.Err() != nil || c.Value(key) != "admitted" || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 90*time.Second {
					t.Fatal("rollback lost original bounded admission")
				}
				return nil
			}}
			err := completeNativePolicyRestart(ctx, []byte("old"), []byte("new"), a)
			if !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			expected := 0
			if point == "after-stop" {
				expected = 1
			}
			if point == "after-new-cleanup" {
				expected = 2
			}
			if launches != expected {
				t.Fatal("rollback ran without old Stop ACK or repeated launch", events, launches, expected)
			}
		})
	}
}

func TestNativeGenerationPolicyRestartRequiresCompleteWiringBeforeEffects(t *testing.T) {
	for _, missing := range []string{"preflight", "withdraw", "stop", "launch", "old-policy", "new-policy"} {
		t.Run(missing, func(t *testing.T) {
			effects := 0
			effect := func(context.Context) error { effects++; return nil }
			a := nativePolicyRestartActions{preflight: effect, withdraw: effect, stop: effect, launch: func(context.Context, []byte, bool) error { effects++; return nil }}
			old, next := []byte("old"), []byte("new")
			switch missing {
			case "preflight":
				a.preflight = nil
			case "withdraw":
				a.withdraw = nil
			case "stop":
				a.stop = nil
			case "launch":
				a.launch = nil
			case "old-policy":
				old = nil
			case "new-policy":
				next = nil
			}
			if err := completeNativePolicyRestart(t.Context(), old, next, a); err == nil || effects != 0 {
				t.Fatal("incomplete policy transaction reached effects", missing, err, effects)
			}
		})
	}
}

func TestNativeGenerationPolicyRestartActualAdapterRequiresNativeRootBeforeCallbacks(t *testing.T) {
	calls := 0
	a := LiveApplier{VerifyStorage: func(context.Context) error { calls++; return nil }}
	if err := a.restartNativeGeneration(t.Context(), &Config{Name: "fixture"}, []byte("policy")); err == nil || calls != 0 {
		t.Fatal("private restart bypassed native root/client admission", err, calls)
	}
	if err := a.startNativeStoppedGenerationWithCheckpoint(t.Context(), &Config{Name: "fixture"}, []byte("policy"), nil, strings.Repeat("a", 64)); err == nil || calls != 0 {
		t.Fatal("cold metadata substituted original checkpoint", err, calls)
	}
}

func TestNativeGenerationPolicyRestartComposesOriginalForwardSnapshotWithChangedDesiredPorts(t *testing.T) {
	desired := &Config{Name: "fixture", Ports: []Port{{Published: 9000, Target: 8000}, {Published: 9300, Target: 8300}}}
	old := []Port{{Published: 9000, Target: 7000}, {Published: 9100, Target: 7100}}
	var known []port.ForwardService
	for index, pf := range old {
		spec := port.Spec{Agentbox: desired.Name, HostPort: pf.Published, ContainerPort: pf.Target}
		unit, err := port.OpenShellUnitName(spec)
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			unit = port.UnitName(spec)
		}
		known = append(known, port.ForwardService{Spec: spec, Unit: unit, OpenShell: index == 0})
	}
	foreign := port.Spec{Agentbox: "other", HostPort: 9200, ContainerPort: 7200}
	unit, _ := port.OpenShellUnitName(foreign)
	known = append(known, port.ForwardService{Spec: foreign, Unit: unit, OpenShell: true})
	rollback, err := nativePolicyRollbackConfig(desired, known)
	if err != nil || !reflect.DeepEqual(rollback.Ports, old) {
		t.Fatal("original inventory was replaced by desired mappings", rollback, err)
	}
	cause := errors.New("known new-policy refusal after original cleanup")
	launches := 0
	err = completeNativePolicyRestart(t.Context(), []byte("old"), []byte("new"), nativePolicyRestartActions{
		preflight: func(context.Context) error { return nil }, withdraw: func(context.Context) error { return nil }, stop: func(context.Context) error { return nil },
		launch: func(_ context.Context, _ []byte, restore bool) error {
			launches++
			config, err := nativePolicyLaunchConfig(desired, rollback, restore)
			if err != nil {
				t.Fatal(err)
			}
			want := desired.Ports
			if restore {
				want = old
			}
			if !reflect.DeepEqual(config.Ports, want) {
				t.Fatal("rollback lost original observed forwards", restore, config.Ports, want)
			}
			config.Ports[0].Target = 1
			if restore {
				return nil
			}
			return cause
		},
	})
	if !errors.Is(err, cause) || launches != 2 || !reflect.DeepEqual(rollback.Ports, old) || desired.Ports[0].Target != 8000 {
		t.Fatal("policy/port snapshot composition lost original or repeated rollback", err, launches)
	}
	// An originally empty set stays empty during rollback, even if new ports
	// were requested. Missing snapshot is never silently replaced by desired.
	empty, err := nativePolicyRollbackConfig(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := nativePolicyLaunchConfig(desired, empty, true)
	if err != nil || len(selected.Ports) != 0 {
		t.Fatal("empty original forwards became desired mappings", selected, err)
	}
	if _, err := nativePolicyLaunchConfig(desired, nil, true); err == nil {
		t.Fatal("missing original forward snapshot adopted desired ports")
	}
}

func TestNativeGenerationPolicyRestartOriginalForwardsRefuseAmbiguousIdentity(t *testing.T) {
	for _, change := range []string{"duplicate", "foreign-unit", "direct", "bad-port", "bad-native-flag"} {
		t.Run(change, func(t *testing.T) {
			spec := port.Spec{Agentbox: "fixture", HostPort: 9000, ContainerPort: 7000}
			unit, _ := port.OpenShellUnitName(spec)
			service := port.ForwardService{Spec: spec, Unit: unit, OpenShell: true}
			known := []port.ForwardService{service}
			switch change {
			case "duplicate":
				known = append(known, service)
			case "foreign-unit":
				known[0].Unit += "-other"
			case "direct":
				known[0].Spec.Direct = true
			case "bad-port":
				known[0].Spec.ContainerPort = 0
			case "bad-native-flag":
				known[0].OpenShell = false
			}
			if _, err := nativePolicyRollbackConfig(&Config{Name: "fixture"}, known); err == nil {
				t.Fatal("ambiguous original forwards granted rollback mapping", change)
			}
		})
	}
}

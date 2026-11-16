package agentbox

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
)

// Scripted policy orchestration only. Actual SDK preparation/file readback and
// finite engine Start boundaries are covered separately; no root/VM claim.
// The saved policy is the only policy ever launched: a failure at any stage,
// before or after the new engine start, never launches the previous one.
func TestNativeGenerationPolicyRestartLaunchesOnlyTheSavedPolicy(t *testing.T) {
	for _, stage := range []string{"success", "preflight", "withdraw", "stop", "launch-before-engine", "launch-after-engine", "launch-uncertain"} {
		t.Run(stage, func(t *testing.T) {
			newPolicy := []byte("new-private-policy")
			cause := errors.New("new policy refused")
			var events []string
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					events = append(events, name)
					if stage == name {
						return cause
					}
					return nil
				}
			}
			outer := &container.EngineCalls{}
			var launched [][]byte
			a := nativePolicyRestartActions{preflight: step("preflight"), withdraw: step("withdraw"), stop: step("stop"), launch: func(ctx context.Context, policy []byte) error {
				events = append(events, "launch")
				launched = append(launched, bytes.Clone(policy))
				policy[0] = 'X' // the callback cannot mutate the caller's policy
				switch stage {
				case "success":
					return nil
				case "launch-after-engine":
					container.EngineCallsFrom(ctx).MarkStart()
					return cause
				case "launch-uncertain":
					return &NativeEffectsUncertain{Cause: cause}
				}
				return cause
			}}
			err := completeNativePolicyRestart(container.WithEngineCalls(t.Context(), outer), newPolicy, a)
			want := []string{"preflight", "withdraw", "stop", "launch"}
			switch stage {
			case "preflight":
				want = want[:1]
			case "withdraw":
				want = want[:2]
			case "stop":
				want = want[:3]
			}
			if !reflect.DeepEqual(events, want) || string(newPolicy) != "new-private-policy" {
				t.Fatal("policy restart lost ordering or bytes", events, want)
			}
			for _, policy := range launched {
				if string(policy) != "new-private-policy" {
					t.Fatal("a launch used another policy", string(policy))
				}
			}
			var uncertain *NativeEffectsUncertain
			switch stage {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "launch-uncertain":
				if !errors.As(err, &uncertain) || !errors.Is(err, cause) {
					t.Fatal("uncertain launch lost its marker", err)
				}
			case "launch-before-engine", "launch-after-engine":
				if !errors.Is(err, cause) || errors.As(err, &uncertain) || !strings.Contains(err.Error(), "previous policy is not restored") {
					t.Fatal("known launch failure was not reported truthfully", err)
				}
			default:
				if !errors.Is(err, cause) {
					t.Fatal("pre-launch failure lost its cause", err)
				}
			}
			// The enclosing operation (box start) still sees the launch's engine start.
			if outer.Started() != (stage == "launch-after-engine") {
				t.Fatal("the enclosing record missed or invented an engine start")
			}
		})
	}
}

// Cancellation never launches anything after the old runtime stopped: no
// previous policy, and no saved policy either.
func TestNativeGenerationPolicyRestartCancellationLaunchesNothing(t *testing.T) {
	for _, point := range []string{"before-preflight", "after-preflight", "after-withdraw", "after-stop"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					if point == "after-"+name {
						cancel()
					}
					return nil
				}
			}
			if point == "before-preflight" {
				cancel()
			}
			launches := 0
			a := nativePolicyRestartActions{preflight: step("preflight"), withdraw: step("withdraw"), stop: step("stop"),
				launch: func(context.Context, []byte) error { launches++; return nil }}
			err := completeNativePolicyRestart(ctx, []byte("new"), a)
			if !errors.Is(err, context.Canceled) || launches != 0 {
				t.Fatal("cancellation launched or lost", err, launches)
			}
			if point == "after-stop" && !strings.Contains(err.Error(), "previous policy is not restored") {
				t.Fatal("stopped-and-canceled result hid the stopped state", err)
			}
		})
	}
}

func TestNativeGenerationPolicyRestartRequiresCompleteWiringBeforeEffects(t *testing.T) {
	for _, missing := range []string{"preflight", "withdraw", "stop", "launch", "new-policy"} {
		t.Run(missing, func(t *testing.T) {
			effects := 0
			effect := func(context.Context) error { effects++; return nil }
			a := nativePolicyRestartActions{preflight: effect, withdraw: effect, stop: effect, launch: func(context.Context, []byte) error { effects++; return nil }}
			next := []byte("new")
			switch missing {
			case "preflight":
				a.preflight = nil
			case "withdraw":
				a.withdraw = nil
			case "stop":
				a.stop = nil
			case "launch":
				a.launch = nil
			case "new-policy":
				next = nil
			}
			if err := completeNativePolicyRestart(t.Context(), next, a); err == nil || effects != 0 {
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

// The observed forward snapshot (revive relaunches it) is the actual
// inventory of this box, never the desired mappings.
func TestNativeObservedForwardsReadTheActualInventory(t *testing.T) {
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
	observed, err := nativeObservedForwards(desired, known)
	if err != nil || !reflect.DeepEqual(observed.Ports, old) {
		t.Fatal("observed inventory was replaced by desired mappings", observed, err)
	}
	empty, err := nativeObservedForwards(desired, nil)
	if err != nil || len(empty.Ports) != 0 {
		t.Fatal("empty observed forwards became desired mappings", empty, err)
	}
}

func TestNativeObservedForwardsRefuseAmbiguousIdentity(t *testing.T) {
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
			if _, err := nativeObservedForwards(&Config{Name: "fixture"}, known); err == nil {
				t.Fatal("ambiguous observed forwards accepted", change)
			}
		})
	}
}

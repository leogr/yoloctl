package agentbox

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func TestNativePolicyPreflightRouteQualifiesNativeAndNeverFallsBackOnFailure(t *testing.T) {
	for _, kind := range []string{"native", "native-failure", "native-uncertain", "mixed-label", "mixed-bind", "privileged-native", "legacy", "nil"} {
		t.Run(kind, func(t *testing.T) {
			d := openShellDetailsFixture(t)
			cause := errors.New("qualification unavailable")
			switch kind {
			case "mixed-label":
				delete(d.Labels, openshell.LabelQualification)
			case "mixed-bind":
				d.Mounts = d.Mounts[1:]
			case "privileged-native":
				*d.Privileged = true
			case "legacy":
				d.Command = sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")
				d.Labels = nil
				d.Mounts = nil
			case "nil":
				d = nil
			}
			calls := 0
			native, err := preflightPolicyRestartRoute(t.Context(), d, func(context.Context) error {
				calls++
				if kind == "native-failure" {
					return cause
				}
				if kind == "native-uncertain" {
					return &NativeEffectsUncertain{Cause: cause}
				}
				return nil
			})
			switch kind {
			case "native":
				if err != nil || !native || calls != 1 {
					t.Fatal("qualified native policy route was lost", native, err, calls)
				}
			case "native-failure", "native-uncertain":
				if !native || calls != 1 || !errors.Is(err, cause) {
					t.Fatal("failed native qualification permitted legacy fallback", native, err, calls)
				}
			case "legacy":
				if native || err != nil || calls != 0 {
					t.Fatal("legacy policy route changed", native, err, calls)
				}
			case "mixed-label", "mixed-bind":
				// Mixed runtime records never block (leogr's rule): the box
				// stays on the native route, whose own qualification runs.
				if !native || err != nil || calls != 1 {
					t.Fatal("mixed native records were refused or fell back to legacy", native, err, calls)
				}
			default:
				if err == nil || calls != 0 {
					t.Fatal("privileged or missing evidence reached native/legacy effects", native, err, calls)
				}
			}
		})
	}
}
func TestNativePolicyPreflightRequiresEvidenceAndContextBeforeCallbacks(t *testing.T) {
	calls := 0
	read := func(context.Context) error { calls++; return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		ctx     context.Context
		details *container.Details
		verify  func(context.Context) error
	}{{t.Context(), nil, read}, {t.Context(), openShellDetailsFixture(t), nil}, {ctx, openShellDetailsFixture(t), read}} {
		if _, err := preflightPolicyRestartRoute(test.ctx, test.details, test.verify); err == nil || calls != 0 {
			t.Fatal("unqualified policy restart reached callbacks", err, calls)
		}
	}
}

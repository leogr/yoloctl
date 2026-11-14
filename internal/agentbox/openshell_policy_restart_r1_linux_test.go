package agentbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// Decision R1: the previous policy is launched again only when the new
// launch failed before its engine start was submitted. After it (a policy
// rejected at run time, a stop beside the launch, a kill from outside: none
// can be told apart) there is no restoration and the failure says so.
func TestPolicyRestartRestoresOnlyBeforeTheNewEngineStart(t *testing.T) {
	failure := errors.New("launch failed")
	for _, c := range []struct {
		name      string
		submitted bool
		restored  bool
	}{
		{"validation before the engine start", false, true},
		{"failure after the engine start", true, false},
	} {
		var launches []bool
		outer := &container.EngineCalls{}
		a := nativePolicyRestartActions{
			preflight: func(context.Context) error { return nil },
			withdraw:  func(context.Context) error { return nil },
			stop:      func(context.Context) error { return nil },
			launch: func(ctx context.Context, _ []byte, restore bool) error {
				launches = append(launches, restore)
				if restore {
					return nil
				}
				if c.submitted {
					container.EngineCallsFrom(ctx).MarkStart() // as the client marks it
				}
				return failure
			},
		}
		err := completeNativePolicyRestart(container.WithEngineCalls(t.Context(), outer), []byte("old"), []byte("new"), a)
		restored := len(launches) == 2 && launches[1]
		var uncertain *NativeEffectsUncertain
		if !errors.Is(err, failure) || restored != c.restored || errors.As(err, &uncertain) {
			t.Fatalf("%s: %v launches %v", c.name, err, launches)
		}
		if !c.restored && !strings.Contains(err.Error(), "not restored automatically") {
			t.Fatalf("%s: %v", c.name, err)
		}
		// The enclosing operation (box start) still sees the nested launch.
		if outer.Started() != c.submitted {
			t.Fatalf("%s: the enclosing record missed the launch's engine start", c.name)
		}
	}
	// An uncertainty marker keeps today's "no restoration".
	launches := 0
	marker := &NativeEffectsUncertain{Cause: errors.New("lost")}
	a := nativePolicyRestartActions{
		preflight: func(context.Context) error { return nil },
		withdraw:  func(context.Context) error { return nil },
		stop:      func(context.Context) error { return nil },
		launch:    func(context.Context, []byte, bool) error { launches++; return marker },
	}
	if err := completeNativePolicyRestart(t.Context(), []byte("old"), []byte("new"), a); !errors.Is(err, marker) || launches != 1 {
		t.Fatalf("uncertain: %v launches %d", err, launches)
	}
}

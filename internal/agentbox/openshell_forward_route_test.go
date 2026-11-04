package agentbox

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
)

func TestRuntimePortDispatchPreservesNativeLegacyAndPrivilegedRoutes(t *testing.T) {
	for _, mode := range []string{"native-confined", "native-unconfined", "legacy", "privileged"} {
		t.Run(mode, func(t *testing.T) {
			c := validConfig()
			var d *container.Details
			switch mode {
			case "privileged":
				c = privilegedConfig()
				d = privilegedDetails(c)
			case "legacy":
				d = confinedDetails(c)
			default:
				d = openShellDetailsFixture(t)
				c.UnconfinedNetwork = mode == "native-unconfined"
			}
			d.Name, d.Running = c.Name, true
			d.Labels[LabelAgentbox] = c.Name
			called := ""
			marker := errors.New("selected transport result")
			invoke := func(route string) func(context.Context, port.Spec) error {
				return func(_ context.Context, spec port.Spec) error {
					if called != "" || spec.Agentbox != c.Name || spec.HostPort != 8080 || spec.ContainerPort != 80 || spec.Direct != c.Privileged {
						t.Fatal("wrong or repeated route", called, spec)
					}
					called = route
					return marker
				}
			}
			err := dispatchRuntimePortForward(t.Context(), d, c, Port{Published: 8080, Target: 80}, invoke("native"), invoke("legacy"))
			want := "legacy"
			if mode == "native-confined" || mode == "native-unconfined" {
				want = "native"
			}
			if called != want || !errors.Is(err, marker) {
				t.Fatal("transport/failure was replaced", called, err)
			}
		})
	}
}

func TestRuntimePortDispatchRefusesPartialNativeOrModeDriftWithoutFallback(t *testing.T) {
	for _, mode := range []string{"partial-native", "legacy-command-native-label", "stopped", "different-name", "wrong-owner", "declared-privileged", "privileged-with-native-label"} {
		t.Run(mode, func(t *testing.T) {
			c := validConfig()
			d := openShellDetailsFixture(t)
			d.Name, d.Running, d.Labels[LabelAgentbox] = c.Name, true, c.Name
			switch mode {
			case "partial-native":
				delete(d.Labels, openshell.LabelIncarnation)
			case "legacy-command-native-label":
				d.Command = confinedDetails(c).Command
			case "stopped":
				d.Running = false
			case "different-name":
				d.Name = "other"
			case "wrong-owner":
				d.Labels[LabelAgentbox] = "other"
			case "declared-privileged":
				c.Privileged = true
			case "privileged-with-native-label":
				c = privilegedConfig()
				d = privilegedDetails(c)
				d.Name, d.Running = c.Name, true
				d.Labels[LabelAgentbox], d.Labels[openshell.LabelProtocol] = c.Name, openshell.HelperProtocol
			}
			called := false
			invoke := func(context.Context, port.Spec) error { called = true; return nil }
			if err := dispatchRuntimePortForward(t.Context(), d, c, Port{Published: 8080, Target: 80}, invoke, invoke); err == nil || called {
				t.Fatal("unproved mode selected a transport", called, err)
			}
		})
	}
}

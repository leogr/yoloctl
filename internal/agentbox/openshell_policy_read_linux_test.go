package agentbox

import (
	"maps"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/openshell"
)

func TestNativePolicyReadObservationBindsActualContainerLaunchAndStart(t *testing.T) {
	for _, change := range []string{"same", "id", "name", "space", "box", "running", "pid", "started", "ambiguous", "control", "launch"} {
		t.Run(change, func(t *testing.T) {
			current, d, raw, manifest := nativeRuntimeFixture(t)
			observed, err := validateOpenShellRuntime(current, d.Name, d, raw, manifest)
			if err != nil {
				t.Fatal(err)
			}
			d.Labels = maps.Clone(d.Labels)
			switch change {
			case "id":
				d.ID = strings.Repeat("b", 64)
			case "name":
				d.Name = "other"
			case "space":
				d.Labels[LabelSpace] = "other"
			case "box":
				d.Labels[LabelAgentbox] = "other"
			case "running":
				d.Running = !d.Running
			case "pid":
				d.PID++
			case "started":
				d.StartedAt = ""
			case "ambiguous":
				d.AmbiguousTaskStateObserved = true
			case "control":
				d.Labels[openshell.LabelControlRoot] = "/other"
			case "launch":
				d.Command = append([]string(nil), d.Command...)
				d.Command[len(d.Command)-1] = "changed"
			}
			err = sameNativePolicyObservation(d, observed)
			if (err == nil) != (change == "same") {
				t.Fatal("policy view mixed original and current process/launch observation", change, err)
			}
		})
	}
}

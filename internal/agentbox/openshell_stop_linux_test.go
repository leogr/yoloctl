package agentbox

import (
	"testing"
)

func TestNativeStopRequiresAdmittedRootBeforeEffects(t *testing.T) {
	if err := (LiveApplier{}).stopNativeRuntime(t.Context(), "fixture", ""); err == nil {
		t.Fatal("ordinary executor reached native stop")
	}
}
func TestNativeStopBindingRejectsDifferentRuntimeMaterial(t *testing.T) {
	current, details, record, manifest := nativeRuntimeFixture(t)
	observed, err := validateOpenShellRuntime(current, details.Name, details, record, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !sameNativeStopBinding(observed, observed) {
		t.Fatal("same runtime differs")
	}
	for _, change := range []string{"id", "root", "record", "launch", "generation"} {
		t.Run(change, func(t *testing.T) {
			other := observed
			switch change {
			case "id":
				other.ContainerID = "other"
			case "root":
				other.ControlRoot = "other"
			case "record":
				other.RecordSHA256 = "other"
			case "launch":
				other.Launch.Workdir = "/other"
			case "generation":
				other.Record.Generation = "other"
			}
			if sameNativeStopBinding(observed, other) {
				t.Fatal("changed runtime binding accepted")
			}
		})
	}
}

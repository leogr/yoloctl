package agentbox

import (
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

// nativeInactiveGenerationTarget is data assembly only. Its admitted caller
// must independently verify actual storage, selected record/launch/binds and
// lifecycle ownership around preparation. This explicit observation does not
// acknowledge Stop, authorize Start, or reconcile any earlier unknown result.
func nativeInactiveGenerationTarget(d *container.Details, cid string) (openshell.GenerationTarget, error) {
	if d == nil || !nativeLowerHex(cid, 32) || d.ID != cid || !nativeInactiveCleanupObservation(d) {
		return openshell.GenerationTarget{}, errors.New("generation input requires the exact inspected CID and explicit inactive task fields")
	}
	return openshell.GenerationTarget{ContainerID: d.ID, Status: d.Status, Running: d.Running, PID: d.PID, EmptyTaskStateObserved: d.EmptyTaskStateObserved}, nil
}

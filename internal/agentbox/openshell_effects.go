package agentbox

import "github.com/leogr/yoloctl/internal/openshell"

// NativeEffectsUncertain preserves an unresolved external runtime action
// across the agentbox/controller dependency boundary. The trusted CLI worker
// forwards this distinction over its existing private effect checkpoint;
// neither message text nor a current-state observation can complete it.
type NativeEffectsUncertain = openshell.NativeEffectsUncertain

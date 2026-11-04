package port

import "github.com/leogr/yoloctl/internal/openshell"

// NativeEffectsUncertain is shared with the native SDK so generation, runtime
// and forwarding callers preserve one trusted marker across package boundaries.
type NativeEffectsUncertain = openshell.NativeEffectsUncertain

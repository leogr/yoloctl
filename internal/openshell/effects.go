package openshell

import "fmt"

// NativeEffectsUncertain crosses the runtime/controller boundary when an
// external native action was not acknowledged. Current state or error text
// cannot replace the existing private effect checkpoint.
type NativeEffectsUncertain struct{ Cause error }

func (e *NativeEffectsUncertain) Error() string {
	return fmt.Sprintf("native runtime effects are uncertain: %v", e.Cause)
}

func (e *NativeEffectsUncertain) Unwrap() error { return e.Cause }

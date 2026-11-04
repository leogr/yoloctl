package controller

import "fmt"

// EffectsUncertain is returned by a native service when an external action may
// have happened but its result was lost. A normal nonzero exit alone cannot
// convey this distinction. The worker forwards it over its private checkpoint.
type EffectsUncertain struct{ Cause error }

func (e *EffectsUncertain) Error() string {
	return fmt.Sprintf("operation effects are uncertain: %v", e.Cause)
}
func (e *EffectsUncertain) Unwrap() error { return e.Cause }

package main

import "github.com/leogr/yoloctl/internal/vmanchor"

// Fixed serve exec only. No os.File wrapper or unrelated FD allocation may
// precede the receiver's raw full-pair eligibility and ownership decision.
func consumeEarlyControllerLaunch() (controllerLaunchObservation, bool, error) {
	anchor, claimed, err := vmanchor.ConsumeInheritedFirecrackerLaunch(3, 4)
	if anchor == nil {
		return nil, claimed, err
	}
	return anchor, claimed, err
}

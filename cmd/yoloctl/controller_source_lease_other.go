//go:build !linux

package main

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func readOriginalControllerActivation(meta string) (controllerActivationObservation, error) {
	_, err := safepath.ReadFile(meta, "activated", 4096)
	return nil, err
}
func captureControllerServiceSource(context.Context, *spaceenv.Context, int, controllerActivationObservation) (controllerServiceSource, error) {
	return nil, errors.New("original controller source lease is unavailable on this platform")
}

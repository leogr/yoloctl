//go:build !linux

package main

import "errors"

func consumeEarlyControllerLaunch() (controllerLaunchObservation, bool, error) {
	return nil, false, errors.New("controller launch observation requires Linux")
}

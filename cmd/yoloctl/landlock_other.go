//go:build !linux

package main

import "errors"

func queryLandlockABI() landlockKernelProbe {
	return landlockKernelProbe{Status: landlockKernelUnknown, Err: errors.New("the Landlock ABI query requires the native Linux controller")}
}

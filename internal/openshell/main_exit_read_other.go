//go:build !linux

package openshell

import "errors"

// ReadMainExit is a Linux VM operation.
func ReadMainExit(string, string) (int, bool, error) {
	return 0, false, errors.New("reading PID 1's runtime status requires the Linux VM")
}

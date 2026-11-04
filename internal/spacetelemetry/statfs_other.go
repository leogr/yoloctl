//go:build !linux

package spacetelemetry

import "errors"

// StatfsUsage is only used inside Linux VM controllers.
func StatfsUsage(string) (Usage, error) {
	return Usage{}, errors.New("filesystem usage is only collected inside the VM")
}

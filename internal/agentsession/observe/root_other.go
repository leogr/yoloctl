//go:build !linux

package observe

import "errors"

// Observation roots exist only inside the Linux VM controller. Host builds
// compile this package but never mount, verify or remove a root.
var errLinuxOnly = errors.New("agent observation roots are managed only inside the Linux VM")

var (
	mountTmpfs  = func(string) error { return errLinuxOnly }
	unmountRoot = func(string) error { return errLinuxOnly }
	mountInfo   = func() ([]byte, error) { return nil, errLinuxOnly }
)

func observationFS(int) bool { return false }

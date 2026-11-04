//go:build linux

package vmanchor

import (
	"errors"
	"runtime"
	"strings"
)

// ObservationIdentity copies correlation from this SAME original retained
// controller anchor. It grants no admission or independent Close ownership;
// equal strings cannot replace the controller/space/lifetime association.
func (a *LaunchAnchor) ObservationIdentity() (bootID, launcherFingerprint string, err error) {
	if a == nil {
		return "", "", errors.New("original controller observation anchor unavailable")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.check(); err != nil {
		return "", "", err
	}
	fingerprint, err := observationFingerprintV1(a.boot, a.launcher.identity)
	if err != nil {
		return "", "", err
	}
	if err = a.check(); err != nil {
		return "", "", err
	}
	return strings.Clone(a.boot), fingerprint, nil
}

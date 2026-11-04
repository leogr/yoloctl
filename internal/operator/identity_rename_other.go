//go:build !linux && !darwin

package operator

import "errors"

func renameLegacyIdentityNoReplace(int, string, string) error {
	return errors.New("operator identity no-replace publication requires Linux or Darwin")
}

// No supported publication operation exists on these platforms.
func legacyIdentityAncestorAccess() int { return 0 }

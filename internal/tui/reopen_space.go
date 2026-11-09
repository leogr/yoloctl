package tui

import (
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// reopenSpace mirrors the CLI's implicit selectedSpace/resolveSpacePositional
// order. Production passes only the trusted spaceenv.Current(), never client
// argv/environment pretending to be a native context.
func reopenSpace(native *spaceenv.Context) (string, error) {
	if native != nil {
		return native.Space, nil
	}
	space, _, err := config.ResolveSpace("", false)
	return space, err
}

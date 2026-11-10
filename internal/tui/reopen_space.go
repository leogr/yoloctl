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

// reopenSpace uses the same implicit target as this TUI's child environment.
// Native clients keep their trusted scope; explicit argv bypasses this callback.
func (m *model) reopenSpace(native *spaceenv.Context) (string, error) {
	if native != nil || m.ctxSpace == "" {
		return reopenSpace(native)
	}
	space, _, err := config.ResolveSpace(m.ctxSpace, true)
	return space, err
}

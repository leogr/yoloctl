package layout

import (
	"os"
	"testing"
)

// A test binary never inherits the developer's YOLOCTL_HOME: ordinary
// fixtures isolate only XDG_*_HOME, and the override would otherwise route
// their writes into a live home (archyAstra R1). Clearing it here, before any
// test runs, also cleans the environment of every subprocess a test starts.
// Tests exercising the override set their own temporary YOLOCTL_HOME.
// Production binaries (testing.Testing() false) keep the override's precedence.
func init() {
	if testing.Testing() {
		_ = os.Unsetenv("YOLOCTL_HOME")
	}
}

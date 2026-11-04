package lima

import (
	"fmt"
	"strings"

	"github.com/leogr/yoloctl/internal/vmapi"
)

// Mount is a single entry in a Lima YAML `mounts:` block. Mirrors the
// Lima schema fields yoloctl cares about.
//
// We do not import or generate the full Lima schema; this struct is
// deliberately narrow to keep us decoupled from Lima's evolving
// configuration surface.
type Mount = vmapi.Mount

// MarshalMounts emits a YAML fragment for a Lima `mounts:` block,
// rendered without the top-level `mounts:` key so callers can splice it
// into a larger document.
//
// The output is hand-rendered (not via yaml.Marshal) so it produces the
// exact two-space indentation Lima's own examples use, with no quoting
// flutter from gopkg.in/yaml.v3 around tilde-prefixed paths.
func MarshalMounts(mounts []Mount) string {
	if len(mounts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range mounts {
		fmt.Fprintf(&b, "- location: %q\n", m.Location)
		if m.MountPoint != "" {
			fmt.Fprintf(&b, "  mountPoint: %q\n", m.MountPoint)
		}
		fmt.Fprintf(&b, "  writable: %t\n", m.Writable)
		if m.Uncached {
			b.WriteString("  9p:\n    cache: none\n")
		}
	}
	return b.String()
}

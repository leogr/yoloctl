package agentbox

import (
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
)

// TestCompose_VarTmpReadWrite locks /var/tmp into every box's writable
// baseline, next to /tmp. SQLite (Codex's state database among others) sorts
// through a temporary file in /var/tmp when SQLITE_TMPDIR and TMPDIR are
// unset: its mode passes access(), so a Landlock denial there failed Codex's
// index-building state upgrade and every Codex session exited at launch.
func TestCompose_VarTmpReadWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := capabilities.WorkdirWriteCapability
	for _, cfg := range []*Config{
		{Name: "x", Space: "default"},
		{Name: "x", Space: "default", Capabilities: []string{wd}},
		{Name: "x", Space: "default", Agent: "codex", Capabilities: []string{wd}},
		{Name: "x", Space: "default", Agent: "claude", Features: []string{"go", "github", "npm", "pypi", "cargo"}},
		{Name: "x", Space: "default", Agent: "none", Account: "none", Capabilities: []string{capabilities.PTYAllocateCapability}},
	} {
		doc := roundTripCompose(t, cfg)
		rw := doc.FilesystemPolicy.ReadWrite
		i, tmp := slices.Index(rw, "/var/tmp"), slices.Index(rw, "/tmp")
		if i < 0 || tmp < 0 || i != tmp+1 {
			t.Errorf("agent=%q features=%v capabilities=%v: /var/tmp is not read_write next to /tmp: %v", cfg.Agent, cfg.Features, cfg.Capabilities, rw)
		}
		if slices.Contains(doc.FilesystemPolicy.ReadOnly, "/var/tmp") {
			t.Errorf("agent=%q: /var/tmp is also read_only", cfg.Agent)
		}
	}
}

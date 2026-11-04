package agentbox

import (
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
)

// terminalDevicePaths are the /dev entries that give a sandboxed process a
// terminal it did not inherit. None of them may appear in a composed policy
// unless the box opted into fs.pty.allocate.
var terminalDevicePaths = []string{"/dev", "/dev/pts", "/dev/ptmx", "/dev/pts/ptmx", "/dev/tty", "/dev/console"}

// TestCompose_PTYDeniedByDefault locks the default: PTY allocation is NOT part
// of the confinement baseline. A box without fs.pty.allocate (including every
// agent/feature combination in the golden table) grants no terminal device
// path, read-only or read-write.
func TestCompose_PTYDeniedByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfgs := []*Config{
		{Name: "x", Space: "default"},
		{Name: "x", Space: "default", Capabilities: []string{capabilities.WorkdirWriteCapability}},
		{Name: "x", Space: "default", Agent: "claude", Features: []string{"go", "github", "npm", "pypi", "cargo"}, Capabilities: []string{capabilities.WorkdirWriteCapability}},
		{Name: "x", Space: "default", Agent: "codex", Capabilities: []string{capabilities.WorkdirWriteCapability}},
	}
	for _, cfg := range cfgs {
		doc := roundTripCompose(t, cfg)
		for _, p := range terminalDevicePaths {
			if slices.Contains(doc.FilesystemPolicy.ReadWrite, p) || slices.Contains(doc.FilesystemPolicy.ReadOnly, p) {
				t.Errorf("agent=%q features=%v: %q is granted without %s (ro=%v rw=%v)",
					cfg.Agent, cfg.Features, p, capabilities.PTYAllocateCapability,
					doc.FilesystemPolicy.ReadOnly, doc.FilesystemPolicy.ReadWrite)
			}
		}
	}
}

// TestCompose_PTYAllocateGrant pins what the capability adds — and nothing
// broader: exactly /dev/pts and /dev/tty, read-write, with the rest of the
// policy untouched. /dev/ptmx must never be emitted: it is a symlink in the
// box container and openshell-sandbox refuses to start on a symlinked
// read_write path (see lintReadWriteDevSymlink).
func TestCompose_PTYAllocateGrant(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := capabilities.WorkdirWriteCapability
	without := roundTripCompose(t, &Config{Name: "x", Space: "default", Features: []string{"claude"}, Capabilities: []string{wd}})
	with := roundTripCompose(t, &Config{Name: "x", Space: "default", Features: []string{"claude"}, Capabilities: []string{wd, capabilities.PTYAllocateCapability}})

	wantRW := append(slices.Clone(without.FilesystemPolicy.ReadWrite), "/dev/pts", "/dev/tty")
	if !slices.Equal(with.FilesystemPolicy.ReadWrite, wantRW) {
		t.Errorf("read_write = %v, want %v", with.FilesystemPolicy.ReadWrite, wantRW)
	}
	if !slices.Equal(with.FilesystemPolicy.ReadOnly, without.FilesystemPolicy.ReadOnly) {
		t.Errorf("read_only changed: %v -> %v", without.FilesystemPolicy.ReadOnly, with.FilesystemPolicy.ReadOnly)
	}
	for _, p := range []string{"/dev", "/dev/ptmx", "/dev/pts/ptmx"} {
		if slices.Contains(with.FilesystemPolicy.ReadWrite, p) || slices.Contains(with.FilesystemPolicy.ReadOnly, p) {
			t.Errorf("%q must never be granted (ro=%v rw=%v)", p, with.FilesystemPolicy.ReadOnly, with.FilesystemPolicy.ReadWrite)
		}
	}
	// A filesystem capability must not touch egress or the other blocks.
	if len(with.NetworkPolicies) != len(without.NetworkPolicies) {
		t.Errorf("network_policies changed: %v -> %v", policyKeys(without.NetworkPolicies), policyKeys(with.NetworkPolicies))
	}
	if with.Landlock != without.Landlock || with.Process != without.Process {
		t.Errorf("landlock/process blocks changed: %+v %+v -> %+v %+v", without.Landlock, without.Process, with.Landlock, with.Process)
	}
}

// TestCompose_PTYAllocateWithReadOnlyWorkdir: the PTY grant is independent of
// workdir placement — a read-only box can still opt into PTY allocation and
// /workdir stays read_only.
func TestCompose_PTYAllocateWithReadOnlyWorkdir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	doc := roundTripCompose(t, &Config{Name: "x", Space: "default", Capabilities: []string{capabilities.PTYAllocateCapability}})
	if !slices.Contains(doc.FilesystemPolicy.ReadOnly, "/workdir") || slices.Contains(doc.FilesystemPolicy.ReadWrite, "/workdir") {
		t.Errorf("/workdir must stay read_only without fs.workdir.write (ro=%v rw=%v)", doc.FilesystemPolicy.ReadOnly, doc.FilesystemPolicy.ReadWrite)
	}
	for _, p := range []string{"/dev/pts", "/dev/tty"} {
		if !slices.Contains(doc.FilesystemPolicy.ReadWrite, p) {
			t.Errorf("%q missing from read_write: %v", p, doc.FilesystemPolicy.ReadWrite)
		}
	}
}

// TestLintReadWriteDevSymlink covers the fail-fast guard for the upstream
// startup trap (NVIDIA/OpenShell#749): a symlinked read_write path aborts
// openshell-sandbox before its SSH socket opens.
func TestLintReadWriteDevSymlink(t *testing.T) {
	for _, ok := range [][]string{
		nil,
		{"/tmp", "/home/sandbox", "/run", "/dev/null", "/dev/zero", "/workdir"},
		{"/dev/pts", "/dev/tty"},
		{"/dev/ptmx-not-the-symlink"},
	} {
		if err := lintReadWriteDevSymlink(ok); err != nil {
			t.Errorf("lintReadWriteDevSymlink(%v) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"/dev/ptmx", "/dev/fd", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/core"} {
		err := lintReadWriteDevSymlink([]string{"/tmp", bad})
		if err == nil {
			t.Errorf("lintReadWriteDevSymlink(%q) = nil, want a rejection", bad)
			continue
		}
		if !strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("error %q should name %q and explain the symlink", err, bad)
		}
	}
}

// TestBuiltinFilesystemGrantsPassDevSymlinkLint: no BUILT-IN capability may
// carry a grant the lint rejects — otherwise a box declaring it could never be
// composed.
func TestBuiltinFilesystemGrantsPassDevSymlinkLint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range capabilities.BuiltinNames() {
		c, err := capabilities.Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		var rw []string
		for _, g := range c.Filesystem {
			if g.Access == capabilities.ReadWrite {
				rw = append(rw, g.Path)
			}
		}
		if err := lintReadWriteDevSymlink(rw); err != nil {
			t.Errorf("built-in %q: %v", name, err)
		}
	}
}

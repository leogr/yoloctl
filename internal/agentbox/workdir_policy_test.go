package agentbox

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// TestWorkdirPolicyAgreement locks the security invariant that the in-container
// workdir path, the composed sandbox policy's writable/readonly entry, and the
// host bind / openshell --workdir all agree on sandbox.WorkdirDir (/workdir).
// If any diverged, the fs.workdir.write grant would not cover the real workdir
// and a "writable" box would silently behave read-only (or vice versa).
//
// The invariant is checked end to end:
//   - a HOST workdir WITH fs.workdir.write → /workdir in filesystem_policy
//     read_write and NOT read_only;
//   - the SAME host workdir WITHOUT the capability → /workdir in read_only and
//     NOT read_write;
//   - Workdir.ContainerPath() mirrors the host path under sandbox.WorkdirDir for
//     a host workdir, and is exactly sandbox.WorkdirDir for an ephemeral one.
func TestWorkdirPolicyAgreement(t *testing.T) {
	const hostPath = "/home/me/code/repo"
	hostWorkdir := Workdir{Mode: WorkdirModeHost, Source: hostPath, Canonical: hostPath}

	t.Run("host workdir writable", func(t *testing.T) {
		cfg := &Config{
			Name:         "coder",
			Space:        "default",
			Features:     []string{"claude"},
			Capabilities: []string{capabilities.WorkdirWriteCapability},
			Workdir:      hostWorkdir,
		}
		fp := composedFilesystemPolicy(t, cfg)
		if !fp.IncludeWorkdir {
			t.Fatal("writable workdir lost its explicit include_workdir intent")
		}
		if !slices.Contains(fp.ReadWrite, sandbox.WorkdirDir) {
			t.Errorf("writable host workdir: read_write = %v, want it to contain %q", fp.ReadWrite, sandbox.WorkdirDir)
		}
		if slices.Contains(fp.ReadOnly, sandbox.WorkdirDir) {
			t.Errorf("writable host workdir: %q must NOT be in read_only (%v)", sandbox.WorkdirDir, fp.ReadOnly)
		}
	})

	t.Run("host workdir read-only", func(t *testing.T) {
		// Same config, MINUS the fs.workdir.write capability → read-only.
		cfg := &Config{
			Name:     "coder",
			Space:    "default",
			Features: []string{"claude"},
			Workdir:  hostWorkdir,
		}
		fp := composedFilesystemPolicy(t, cfg)
		if fp.IncludeWorkdir {
			t.Fatal("read-only workdir gained OpenShell's implicit writable cwd grant")
		}
		if !slices.Contains(fp.ReadOnly, sandbox.WorkdirDir) {
			t.Errorf("read-only host workdir: read_only = %v, want it to contain %q", fp.ReadOnly, sandbox.WorkdirDir)
		}
		if slices.Contains(fp.ReadWrite, sandbox.WorkdirDir) {
			t.Errorf("read-only host workdir: %q must NOT be in read_write (%v)", sandbox.WorkdirDir, fp.ReadWrite)
		}
	})

	t.Run("container path agrees with bind / --workdir", func(t *testing.T) {
		// A host workdir mirrors its canonical host path under /workdir; this
		// path is the bind destination AND openshell's --workdir, and the policy
		// grants the /workdir subtree that contains it.
		wantHost := filepath.Join(sandbox.WorkdirDir, strings.TrimPrefix(hostPath, "/"))
		if got := hostWorkdir.ContainerPath(); got != wantHost {
			t.Errorf("host ContainerPath() = %q, want %q", got, wantHost)
		}
		if !strings.HasPrefix(hostWorkdir.ContainerPath(), sandbox.WorkdirDir+"/") {
			t.Errorf("host ContainerPath() %q must live under the granted %q subtree", hostWorkdir.ContainerPath(), sandbox.WorkdirDir)
		}
		// An ephemeral workdir binds at bare /workdir — exactly the granted path.
		eph := NewEphemeralWorkdir("box")
		if got := eph.ContainerPath(); got != sandbox.WorkdirDir {
			t.Errorf("ephemeral ContainerPath() = %q, want %q", got, sandbox.WorkdirDir)
		}
	})
}

// composedFilesystemPolicy composes cfg's policy and returns the decoded
// filesystem_policy block, failing the test on any compose/parse error.
func composedFilesystemPolicy(t *testing.T, cfg *Config) filesystemPolicy {
	t.Helper()
	raw, err := ComposePolicy(cfg)
	if err != nil {
		t.Fatalf("ComposePolicy: %v", err)
	}
	var doc policyDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal composed policy: %v", err)
	}
	return doc.FilesystemPolicy
}

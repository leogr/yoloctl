package agentbox

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func TestBuildMounts_MinimumSet(t *testing.T) {
	c := &Config{
		Name:  "coder",
		Space: "default",
		Workdir: Workdir{
			Mode:      WorkdirModeHost,
			Source:    "/home/leogr/projects/myrepo",
			Canonical: "/home/leogr/projects/myrepo",
		},
		// Writable workdir (fs.workdir.write granted) → bind read-write.
		Capabilities: []string{capabilities.WorkdirWriteCapability},
	}
	got := buildMounts(c)
	if len(got) != 5 {
		t.Fatalf("got %d mounts, want 5 (binary, policy YAML, rego, socket dir, workdir)", len(got))
	}
	// Order matters — buildMounts is the single source of truth and
	// downstream readers (e.g. `box show`) lean on the order
	// matching the YAML's documented mount list.
	wantDests := []string{
		sandbox.BinaryDirContainerPath,
		sandbox.PolicyContainerYAMLPath,
		sandbox.PolicyContainerRegoPath,
		sandbox.SocketDirContainer,
		// A host workdir mirrors its canonical path under /workdir.
		"/workdir/home/leogr/projects/myrepo",
	}
	for i, want := range wantDests {
		if got[i].Destination != want {
			t.Errorf("mount[%d].Destination = %q, want %q", i, got[i].Destination, want)
		}
	}
	// The workdir mount must reflect c.ReadOnly (false here); its source is
	// the canonical host path, its destination the mirrored container path.
	if got[4].Source != "/home/leogr/projects/myrepo" || got[4].ReadOnly {
		t.Errorf("workdir mount = %+v, want source=/home/leogr/projects/myrepo readonly=false", got[4])
	}
}

func TestBuildMounts_ReadOnlyWorkdir(t *testing.T) {
	c := &Config{
		Name:    "reviewer",
		Space:   "default",
		Workdir: Workdir{Mode: WorkdirModeHost, Source: "/host/proj", Canonical: "/host/proj"},
		// No fs.workdir.write capability → workdir bound read-only.
	}
	mounts := buildMounts(c)
	// Workdir is the last fixed mount (index 4).
	if !mounts[4].ReadOnly {
		t.Errorf("workdir mount ReadOnly = false, want true")
	}
}

func TestBuildMounts_ExtraMountsAppended(t *testing.T) {
	c := &Config{
		Name:    "coder",
		Space:   "default",
		Workdir: Workdir{Mode: WorkdirModeHost, Source: "/host/proj", Canonical: "/host/proj"},
		Mounts: []Mount{
			{Source: "/var/log/external", Target: "/var/log/external", ReadOnly: true},
			{Source: "/host/cache", Target: "/cache"},
		},
	}
	mounts := buildMounts(c)
	if len(mounts) != 7 {
		t.Fatalf("got %d mounts, want 7 (5 fixed + 2 extra)", len(mounts))
	}
	if mounts[5].Source != "/var/log/external" || !mounts[5].ReadOnly {
		t.Errorf("first extra mount = %+v, want ro /var/log/external", mounts[5])
	}
	if mounts[6].Source != "/host/cache" || mounts[6].ReadOnly {
		t.Errorf("second extra mount = %+v, want rw /host/cache", mounts[6])
	}
}

func TestBuildRunOptions_LabelsAndFixedFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{
		Name:      "coder",
		Space:     "default",
		Image:     "yoloctl/agentbox:ubuntu-24.04",
		Workdir:   Workdir{Mode: WorkdirModeHost, Source: "/host/proj", Canonical: "/host/proj"},
		Resources: Resources{CPUs: "2", Memory: "2g"},
		Features:  []string{"claude"}, // policy source so Resolve composes labels
	}
	const runtimeImage = "yoloctl/agentbox-composed:deadbeef0123"
	opts, err := buildRunOptions(c, runtimeImage, nil)
	if err != nil {
		t.Fatalf("buildRunOptions: %v", err)
	}
	// nerdctl runs the RESOLVED runtime image (the composed ref), not the
	// manifest base — this is what the live inspector reads back as a.Image.
	if opts.Image != runtimeImage {
		t.Errorf("RunOptions.Image = %q, want the resolved runtime image %q", opts.Image, runtimeImage)
	}
	// Labels include kind + agentbox + space (the workdir-hash cache label was
	// removed by the scope-first layout).
	if opts.Labels[LabelKind] != KindAgentbox {
		t.Errorf("label kind = %q, want %q", opts.Labels[LabelKind], KindAgentbox)
	}
	if opts.Labels[LabelAgentbox] != "coder" {
		t.Errorf("label agentbox = %q, want coder", opts.Labels[LabelAgentbox])
	}
	if opts.Labels[LabelSpace] != "default" {
		t.Errorf("label space = %q, want default", opts.Labels[LabelSpace])
	}
	// The sandbox entrypoint's --workdir is the mirrored container path.
	if want := "--workdir " + c.Workdir.ContainerPath(); !strings.Contains(strings.Join(opts.Command, " "), want) {
		t.Errorf("Command %v missing %q", opts.Command, want)
	}
	// Caps + security opts + user are fixed across all agentboxes.
	expectedCaps := []string{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE", "SYSLOG"}
	if len(opts.AddCaps) != len(expectedCaps) {
		t.Errorf("AddCaps = %v, want %v", opts.AddCaps, expectedCaps)
	} else {
		for i := range expectedCaps {
			if opts.AddCaps[i] != expectedCaps[i] {
				t.Errorf("AddCaps[%d] = %q, want %q", i, opts.AddCaps[i], expectedCaps[i])
			}
		}
	}
	if opts.SeccompProfile != "unconfined" || opts.AppArmorProfile != "unconfined" {
		t.Errorf("security opts = %s/%s, want unconfined/unconfined", opts.SeccompProfile, opts.AppArmorProfile)
	}
	if opts.User != "0" {
		t.Errorf("User = %q, want 0", opts.User)
	}
	if len(opts.Sysctl) != 1 || opts.Sysctl[0] != "net.ipv4.ip_forward=1" {
		t.Errorf("Sysctl = %v, want [net.ipv4.ip_forward=1]", opts.Sysctl)
	}
	if !opts.Detach {
		t.Errorf("Detach = false, want true")
	}
	if opts.CPUs != "2" || opts.Memory != "2g" {
		t.Errorf("resources = %s/%s, want 2/2g", opts.CPUs, opts.Memory)
	}
}

func TestBuildRunOptions_EphemeralWorkdir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{
		Name:         "ephemeral",
		Space:        "default",
		Image:        "yoloctl/agentbox:ubuntu-24.04",
		Workdir:      NewEphemeralWorkdir("ephemeral"),
		Capabilities: []string{capabilities.WorkdirWriteCapability}, // policy source
	}
	opts, err := buildRunOptions(c, c.Image, nil)
	if err != nil {
		t.Fatalf("buildRunOptions: %v", err)
	}
	// An ephemeral workdir binds at bare /workdir, and the entrypoint
	// --workdir matches.
	if want := "--workdir " + sandbox.WorkdirDir + " "; !strings.Contains(strings.Join(opts.Command, " ")+" ", want) {
		t.Errorf("Command %v should set --workdir %s for ephemeral", opts.Command, sandbox.WorkdirDir)
	}
}

// TestResolveEnv_Literals confirms env renders literal values verbatim (env is
// literal-only — a box owns no secrets, so there is no credential-store lookup)
// and sorts them before the UID/GID trailers.
func TestResolveEnv_Literals(t *testing.T) {
	c := &Config{
		Name:  "coder",
		Space: "default",
		Env: map[string]EnvVar{
			"DEBUG": {Literal: "1"},
			"PATH":  {Literal: "/usr/bin"},
			"EMPTY": {Literal: ""},
		},
	}
	got, err := resolveEnv(c)
	if err != nil {
		t.Fatalf("resolveEnv: %v", err)
	}
	// Expect: sorted env entries first, then the two UID/GID trailers.
	want := []string{
		"DEBUG=1",
		"EMPTY=",
		"PATH=/usr/bin",
		fmt.Sprintf("YOLOCTL_HOST_UID=%d", os.Getuid()),
		fmt.Sprintf("YOLOCTL_HOST_GID=%d", os.Getgid()),
	}
	if len(got) != len(want) {
		t.Fatalf("env count = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveEnv_AlwaysAppendsUIDGID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "x", Space: "default"} // no env declared
	got, err := resolveEnv(c)
	if err != nil {
		t.Fatalf("resolveEnv: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (UID+GID), got %v", got)
	}
	if !strings.HasPrefix(got[0], "YOLOCTL_HOST_UID=") {
		t.Errorf("got[0] = %q, want YOLOCTL_HOST_UID=...", got[0])
	}
	if !strings.HasPrefix(got[1], "YOLOCTL_HOST_GID=") {
		t.Errorf("got[1] = %q, want YOLOCTL_HOST_GID=...", got[1])
	}
}

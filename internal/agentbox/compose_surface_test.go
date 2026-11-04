package agentbox

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
)

// Explicit agentless grants use the same endpoint/binary scope already pinned
// for their feature equivalents. Reuse those goldens without regenerating them.
func TestComposeExplicitNetworkCapabilitiesMatchGoldens(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"github", "npm"} {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{
				Name: "policy", Space: "default", Agent: "none", Account: "none",
				Capabilities: []string{capabilities.WorkdirWriteCapability, "network." + name},
			}
			got, err := ComposePolicy(cfg)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "policy_golden", name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("explicit network.%s changed the feature golden's policy scope:\n%s", name, got)
			}
		})
	}
}

func TestComposeUnconfinedNetworkKeepsFilesystemPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		caps []string
	}{
		{"read-only workdir", nil},
		{"writable workdir", []string{capabilities.WorkdirWriteCapability}},
		{"PTY with read-only workdir", []string{capabilities.PTYAllocateCapability}},
		{"writable workdir with PTY and egress", []string{capabilities.WorkdirWriteCapability,
			capabilities.PTYAllocateCapability, "network.github", "network.npm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Name: "policy", Space: "default", Agent: "none", Account: "none", Capabilities: tc.caps}
			confined := roundTripCompose(t, cfg)
			cfg.UnconfinedNetwork = true
			unconfined := roundTripCompose(t, cfg)
			if !reflect.DeepEqual(unconfined.FilesystemPolicy, confined.FilesystemPolicy) {
				t.Fatalf("unconfined networking changed filesystem policy: confined=%+v unconfined=%+v",
					confined.FilesystemPolicy, unconfined.FilesystemPolicy)
			}
		})
	}
}

func extraReadOnlyMountPolicyConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Name: "policy", Space: "default", Agent: "none", Account: "none",
		Workdir:      NewEphemeralWorkdir("policy"),
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Mounts:       []Mount{{Source: t.TempDir(), Target: "/mnt/x", ReadOnly: true}},
	}
}

// A declared read-only extra mount receives exactly one read-only entry.
func TestComposeExtraReadOnlyMountDesiredPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	policy := roundTripCompose(t, extraReadOnlyMountPolicyConfig(t)).FilesystemPolicy
	if !slices.Contains(policy.ReadOnly, "/mnt/x") || slices.Contains(policy.ReadWrite, "/mnt/x") {
		t.Fatalf("read-only extra mount should receive only a read-only policy entry: %+v", policy)
	}
}

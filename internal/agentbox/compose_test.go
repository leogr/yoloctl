package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
	"gopkg.in/yaml.v3"
)

// roundTripCompose marshals + parses the composer output so tests
// can inspect typed values rather than string-matching the YAML.
func roundTripCompose(t *testing.T, c *Config) policyDoc {
	t.Helper()
	raw, err := ComposePolicy(c)
	if err != nil {
		t.Fatalf("ComposePolicy: %v", err)
	}
	var got policyDoc
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal compose output: %v", err)
	}
	return got
}

func TestCompose_ClaudeFeatureOnly(t *testing.T) {
	c := &Config{
		Name:     "coder",
		Space:    "default",
		Features: []string{"claude"},
	}
	got := roundTripCompose(t, c)
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	np, ok := got.NetworkPolicies["anthropic"]
	if !ok {
		t.Fatalf("expected an `anthropic` network policy, got keys=%v", policyKeys(got.NetworkPolicies))
	}
	// Per-binary attribution: anthropic egress restricted to the
	// claude binary itself (v2.x ELF) plus the legacy /usr/bin/node
	// path. See the claude feature in internal/features/features.go.
	wantBinaries := []string{
		"/usr/bin/node",
		"/home/sandbox/.npm-global/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe",
	}
	gotBinaries := make([]string, 0, len(np.Binaries))
	for _, b := range np.Binaries {
		gotBinaries = append(gotBinaries, b.Path)
	}
	if len(gotBinaries) != len(wantBinaries) {
		t.Errorf("anthropic binaries = %v, want %v", gotBinaries, wantBinaries)
	} else {
		for i, want := range wantBinaries {
			if gotBinaries[i] != want {
				t.Errorf("anthropic binaries[%d] = %q, want %q", i, gotBinaries[i], want)
			}
		}
	}
	// Endpoint set should include the API and the login hosts.
	hosts := endpointHosts(np.Endpoints)
	for _, want := range []string{"api.anthropic.com", "*.anthropic.com", "claude.com"} {
		if !contains(hosts, want) {
			t.Errorf("anthropic endpoints missing %q; got %v", want, hosts)
		}
	}
}

func TestCompose_HardeningBaseline_ReadOnlyFlip(t *testing.T) {
	// Workdir placement is governed by the fs.workdir.write capability:
	// present → read_write, absent → read_only.
	cRW := &Config{Name: "x", Space: "default", Features: []string{"claude"}, Capabilities: []string{capabilities.WorkdirWriteCapability}}
	cRO := &Config{Name: "x", Space: "default", Features: []string{"claude"}}

	rw := roundTripCompose(t, cRW)
	ro := roundTripCompose(t, cRO)

	// /workdir should land in read_write when fs.workdir.write is granted…
	if !contains(rw.FilesystemPolicy.ReadWrite, "/workdir") {
		t.Errorf("rw config: /workdir not in read_write; got %v", rw.FilesystemPolicy.ReadWrite)
	}
	if contains(rw.FilesystemPolicy.ReadOnly, "/workdir") {
		t.Errorf("rw config: /workdir shouldn't be in read_only; got %v", rw.FilesystemPolicy.ReadOnly)
	}

	// …and in read_only when it's absent.
	if !contains(ro.FilesystemPolicy.ReadOnly, "/workdir") {
		t.Errorf("ro config: /workdir not in read_only; got %v", ro.FilesystemPolicy.ReadOnly)
	}
	if contains(ro.FilesystemPolicy.ReadWrite, "/workdir") {
		t.Errorf("ro config: /workdir shouldn't be in read_write; got %v", ro.FilesystemPolicy.ReadWrite)
	}

	// Hardening invariants: /usr, /etc must always be RO; /home/sandbox
	// and /tmp must always be RW. Verified independent of the workdir
	// flip.
	for _, c := range []policyDoc{rw, ro} {
		for _, p := range []string{"/usr", "/etc", "/proc"} {
			if !contains(c.FilesystemPolicy.ReadOnly, p) {
				t.Errorf("hardening baseline missing %q in read_only; got %v", p, c.FilesystemPolicy.ReadOnly)
			}
		}
		for _, p := range []string{"/home/sandbox", "/tmp", "/run"} {
			if !contains(c.FilesystemPolicy.ReadWrite, p) {
				t.Errorf("hardening baseline missing %q in read_write; got %v", p, c.FilesystemPolicy.ReadWrite)
			}
		}
	}
}

func TestCompose_NoNetworkCapsEmptiesNetworkPolicies(t *testing.T) {
	// A manifest with no network-bearing features, agent, or capabilities has
	// no egress: the composed output has empty network_policies (the rego
	// signal for deny-all). Only the workdir-write capability is granted.
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
	}
	got := roundTripCompose(t, c)
	if len(got.NetworkPolicies) != 0 {
		t.Errorf("no network capabilities should yield empty network_policies; got %d entries", len(got.NetworkPolicies))
	}
	// Hardening + landlock + process are unaffected.
	if got.Landlock.Compatibility != "best_effort" {
		t.Errorf("Landlock.Compatibility = %q, want best_effort", got.Landlock.Compatibility)
	}
}

func TestCompose_MultipleFeaturesStack(t *testing.T) {
	// Features stack additively: the composed network_policies contains each
	// feature's named policy.
	c := &Config{
		Name:     "coder",
		Space:    "default",
		Features: []string{"github", "claude"},
	}
	got := roundTripCompose(t, c)
	for _, want := range []string{"github", "anthropic"} {
		if _, ok := got.NetworkPolicies[want]; !ok {
			t.Errorf("network_policies missing %q; got %v", want, policyKeys(got.NetworkPolicies))
		}
	}
}

func TestCompose_UnknownFeatureErrors(t *testing.T) {
	c := &Config{
		Name:     "coder",
		Space:    "default",
		Features: []string{"not-a-real-feature"},
	}
	_, err := ComposePolicy(c)
	if err == nil {
		t.Fatal("expected unknown-feature error, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-real-feature") {
		t.Errorf("error %q does not name the bad feature", err.Error())
	}
}

func TestCompose_ProducesValidYAML(t *testing.T) {
	// Sanity: the composer emits parseable YAML that round-trips
	// through yaml.v3 without losing fields. Useful as a smoke test
	// before piping through openshell-sandbox.
	c := &Config{
		Name:     "coder",
		Space:    "default",
		Features: []string{"claude", "github", "go"},
	}
	raw, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "version: 1") {
		t.Errorf("output missing `version: 1`; got:\n%s", raw)
	}
	if !strings.Contains(string(raw), "filesystem_policy") {
		t.Errorf("output missing filesystem_policy block")
	}
	if !strings.Contains(string(raw), "network_policies") {
		t.Errorf("output missing network_policies block")
	}
}

// TestLintPathOverlap covers the lint that flags
// $PATH-lookup directories in the composed read_write set. Equality
// vs ancestor vs unrelated paths are the three relevant categories.
func TestLintPathOverlap(t *testing.T) {
	cases := []struct {
		name      string
		readWrite []string
		wantErr   bool
		wantNote  string // substring in error when wantErr
	}{
		{
			name:      "no_overlap_with_pathdirs",
			readWrite: []string{"/tmp", "/home/sandbox", "/run"},
		},
		{
			name:      "equality_with_usr_bin_errors",
			readWrite: []string{"/usr/bin"},
			wantErr:   true,
			wantNote:  "$PATH lookup directory",
		},
		{
			name:      "ancestor_of_bin_errors",
			readWrite: []string{"/usr"},
			wantErr:   true,
			wantNote:  "contains $PATH lookup directory",
		},
		{
			name:      "root_ancestor_errors",
			readWrite: []string{"/"},
			wantErr:   true,
			wantNote:  "contains $PATH lookup directory",
		},
		{
			name:      "subpath_of_bin_is_ok",
			readWrite: []string{"/usr/bin-custom-data"},
			wantErr:   false,
		},
		{
			name:      "sandbox_subdir_is_ok",
			readWrite: []string{"/home/sandbox/.npm-global/bin"},
			wantErr:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := lintPathOverlap(c.readWrite)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if !strings.Contains(err.Error(), c.wantNote) {
					t.Errorf("error %q missing %q", err.Error(), c.wantNote)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func policyKeys(m map[string]networkPolicy) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func endpointHosts(eps []endpointSpec) []string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.Host)
	}
	return out
}

func contains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

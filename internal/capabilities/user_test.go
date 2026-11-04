package capabilities

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

// writeCapability drops a custom-capability YAML into the (isolated) user
// capability dir under filename <name>.yaml and returns its path.
func writeCapability(t *testing.T, name, body string) {
	t.Helper()
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		t.Fatalf("CapabilitiesDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestLoadUserEgressGrantTranslates: a custom grants.network.egress YAML loads
// and lowers to an internal NetworkGrant with one Endpoint per host (sharing
// the entry's ports), the entry's binaries, and a PolicyName derived from the
// capability name (leading "network." stripped).
func TestLoadUserEgressGrantTranslates(t *testing.T) {
	isolate(t)
	writeCapability(t, "network.mytools", `schema_version: 1
name: network.mytools
grants:
  network:
    egress:
      - hosts: [a.example.com, b.example.com]
        ports: [443]
        binaries: [/usr/bin/mytool]
`)
	c, err := Lookup("network.mytools")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(c.Network) != 1 {
		t.Fatalf("want 1 network grant, got %d", len(c.Network))
	}
	g := c.Network[0]
	if g.PolicyName != "mytools" {
		t.Errorf("PolicyName = %q, want %q (leading network. stripped)", g.PolicyName, "mytools")
	}
	if len(g.Endpoints) != 2 {
		t.Fatalf("want 2 endpoints (one per host), got %d", len(g.Endpoints))
	}
	if g.Endpoints[0].Host != "a.example.com" || g.Endpoints[1].Host != "b.example.com" {
		t.Errorf("hosts = %q, %q; want a/b.example.com", g.Endpoints[0].Host, g.Endpoints[1].Host)
	}
	for _, e := range g.Endpoints {
		if len(e.Ports) != 1 || e.Ports[0] != 443 {
			t.Errorf("endpoint %q ports = %v, want [443]", e.Host, e.Ports)
		}
	}
	if len(g.Binaries) != 1 || g.Binaries[0] != "/usr/bin/mytool" {
		t.Errorf("binaries = %v, want [/usr/bin/mytool]", g.Binaries)
	}
	// Must validate as a positive grant.
	if err := c.Validate(); err != nil {
		t.Errorf("translated capability did not validate: %v", err)
	}
}

// TestLoadUserMultipleEgressGrantsUniquePolicyNames: multiple egress entries
// translate to distinct NetworkGrants with unique, suffixed PolicyNames so the
// composer never sees a duplicate network_policies key within one capability.
func TestLoadUserMultipleEgressGrantsUniquePolicyNames(t *testing.T) {
	isolate(t)
	writeCapability(t, "network.multi", `schema_version: 1
name: network.multi
grants:
  network:
    egress:
      - hosts: [a.example.com]
        ports: [443]
      - hosts: [b.example.com]
        ports: [8443]
`)
	c, err := Lookup("network.multi")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(c.Network) != 2 {
		t.Fatalf("want 2 network grants, got %d", len(c.Network))
	}
	seen := map[string]bool{}
	for _, g := range c.Network {
		if seen[g.PolicyName] {
			t.Errorf("duplicate PolicyName %q across egress grants", g.PolicyName)
		}
		seen[g.PolicyName] = true
	}
	if !seen["multi-0"] || !seen["multi-1"] {
		t.Errorf("PolicyNames = %v, want multi-0 and multi-1", seen)
	}
}

// TestLoadUserWorkdirWriteTranslates: grants.workdir.write:true lowers to the
// same FSGrant the built-in fs.workdir.write produces.
func TestLoadUserWorkdirWriteTranslates(t *testing.T) {
	isolate(t)
	writeCapability(t, "my.workdir", `schema_version: 1
name: my.workdir
grants:
  workdir:
    write: true
`)
	c, err := Lookup("my.workdir")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(c.Filesystem) != 1 {
		t.Fatalf("want 1 filesystem grant, got %d", len(c.Filesystem))
	}
	g := c.Filesystem[0]
	if g.Path != "/workdir" || g.Access != ReadWrite {
		t.Errorf("FSGrant = {%q, %q}, want {/workdir, read_write}", g.Path, g.Access)
	}
	if len(c.Network) != 0 {
		t.Errorf("workdir-only capability should have no network grants, got %v", c.Network)
	}
}

// TestLoadUserRejectsLowLevelShape: a custom YAML authored in the OLD low-level
// shape (top-level network:/policy_name/endpoints) is rejected — custom
// capabilities must NOT expose raw OpenShell policy fragments (D2).
func TestLoadUserRejectsLowLevelShape(t *testing.T) {
	isolate(t)
	writeCapability(t, "network.legacy", `schema_version: 1
name: network.legacy
network:
  - policy_name: legacy
    endpoints:
      - host: a.example.com
        ports: [443]
    binaries: [/usr/bin/legacy]
`)
	_, err := Lookup("network.legacy")
	if err == nil {
		t.Fatal("expected the old low-level network:/policy_name shape to be rejected")
	}
	if !strings.Contains(err.Error(), "grants") {
		t.Errorf("rejection error %q should point at the high-level grants: shape", err)
	}
}

// TestLoadUserRejectsTopLevelFilesystem: the old low-level filesystem: field is
// likewise rejected.
func TestLoadUserRejectsTopLevelFilesystem(t *testing.T) {
	isolate(t)
	writeCapability(t, "fs.legacy", `schema_version: 1
name: fs.legacy
filesystem:
  - path: /workdir
    access: read_write
`)
	if _, err := Lookup("fs.legacy"); err == nil {
		t.Fatal("expected the old low-level filesystem: shape to be rejected")
	}
}

// TestParseUserCapabilityValidation: empty grants and malformed egress entries
// (no hosts / no ports) are rejected with clear errors.
func TestParseUserCapabilityValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty-grants",
			body: "schema_version: 1\nname: empty-grants\ngrants: {}\n",
			want: "grants is empty",
		},
		{
			name: "network.no-hosts",
			body: "schema_version: 1\nname: network.no-hosts\ngrants:\n  network:\n    egress:\n      - ports: [443]\n",
			want: "no hosts",
		},
		{
			name: "network.no-ports",
			body: "schema_version: 1\nname: network.no-ports\ngrants:\n  network:\n    egress:\n      - hosts: [a.example.com]\n",
			want: "no ports",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseUserCapability(tc.name, []byte(tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestLoadUserRejectsBuiltinName: a custom capability whose name collides with a
// built-in is REJECTED (same-name overrides of built-ins are
// not allowed). Note: it never reaches the grants parser; the built-in-name
// guard fires first, so even a structurally-valid grants file is refused.
func TestLoadUserRejectsBuiltinName(t *testing.T) {
	isolate(t)
	writeCapability(t, "network.github", `schema_version: 1
name: network.github
grants:
  network:
    egress:
      - hosts: [github.example]
        ports: [443]
`)
	_, err := Lookup("network.github")
	if err == nil {
		t.Fatal("expected a custom capability shadowing a built-in name to be rejected")
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("rejection error %q should mention it is a built-in", err)
	}
	if _, err := LoadUser(); err == nil {
		t.Error("LoadUser should reject a custom file with a built-in name")
	}
}

// TestIsBuiltin: built-in names report true; unknown/custom names report false.
func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("network.github") {
		t.Error("network.github should be a built-in")
	}
	if !IsBuiltin(WorkdirWriteCapability) {
		t.Errorf("%s should be a built-in", WorkdirWriteCapability)
	}
	if IsBuiltin("network.mine") {
		t.Error("network.mine should not be a built-in")
	}
}

// TestCopyBuiltinToCustomLoads: SaveUser projecting a BUILT-IN capability under
// a new name writes a valid custom YAML the loader re-reads — the `copy` path.
// The synthesized grants must round-trip the network endpoints + binaries.
func TestCopyBuiltinToCustomLoads(t *testing.T) {
	isolate(t)
	src, err := Lookup("network.github")
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	src.Name = "network.mygithub"
	if err := SaveUser(src); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	got, err := Lookup("network.mygithub")
	if err != nil {
		t.Fatalf("Lookup copied capability: %v", err)
	}
	if IsBuiltin("network.mygithub") {
		t.Error("the copy must be a custom capability, not a built-in")
	}
	if len(got.Network) != 1 {
		t.Fatalf("want 1 network grant, got %d", len(got.Network))
	}
	// PolicyName re-derives from the new name (leading network. stripped).
	if got.Network[0].PolicyName != "mygithub" {
		t.Errorf("PolicyName = %q, want mygithub", got.Network[0].PolicyName)
	}
	hosts := map[string]bool{}
	for _, e := range got.Network[0].Endpoints {
		hosts[e.Host] = true
	}
	for _, want := range []string{"github.com", "*.github.com", "*.githubusercontent.com"} {
		if !hosts[want] {
			t.Errorf("copied capability missing host %q (got %v)", want, hosts)
		}
	}
	if len(got.Network[0].Binaries) != 2 {
		t.Errorf("binaries = %v, want git+gh", got.Network[0].Binaries)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("copied capability does not validate: %v", err)
	}
}

// TestCopyWorkdirWriteBuiltin: copying the fs.workdir.write built-in produces a
// custom capability whose grants.workdir.write round-trips to the same FSGrant.
func TestCopyWorkdirWriteBuiltin(t *testing.T) {
	isolate(t)
	src, err := Lookup(WorkdirWriteCapability)
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	src.Name = "my.workdir.write"
	if err := SaveUser(src); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	got, err := Lookup("my.workdir.write")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got.Filesystem) != 1 || got.Filesystem[0].Path != "/workdir" || got.Filesystem[0].Access != ReadWrite {
		t.Errorf("workdir-write copy filesystem = %+v, want /workdir read_write", got.Filesystem)
	}
}

// TestLoadUserPTYAllocateTranslates: grants.pty.allocate:true lowers to the
// same FSGrants the built-in fs.pty.allocate produces; the authoring document
// never names a /dev path.
func TestLoadUserPTYAllocateTranslates(t *testing.T) {
	isolate(t)
	writeCapability(t, "my.pty", `schema_version: 1
name: my.pty
grants:
  pty:
    allocate: true
`)
	c, err := Lookup("my.pty")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	builtin, err := Lookup(PTYAllocateCapability)
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	if len(c.Filesystem) != len(builtin.Filesystem) {
		t.Fatalf("filesystem = %+v, want %+v", c.Filesystem, builtin.Filesystem)
	}
	for i := range builtin.Filesystem {
		if c.Filesystem[i] != builtin.Filesystem[i] {
			t.Errorf("filesystem[%d] = %+v, want %+v", i, c.Filesystem[i], builtin.Filesystem[i])
		}
	}
	if len(c.Network) != 0 {
		t.Errorf("pty-only capability should have no network grants, got %v", c.Network)
	}
}

// TestLoadUserRejectsRawDevicePath: a custom capability cannot smuggle a raw
// device path — the strict decoder rejects any field outside the grant kinds.
func TestLoadUserRejectsRawDevicePath(t *testing.T) {
	for name, body := range map[string]string{
		"pty.paths": "schema_version: 1\nname: pty.paths\ngrants:\n  pty:\n    allocate: true\n    paths: [/dev]\n",
		"fs.dev":    "schema_version: 1\nname: fs.dev\ngrants:\n  filesystem:\n    - path: /dev\n      access: read_write\n",
	} {
		if _, err := parseUserCapability(name, []byte(body)); err == nil {
			t.Errorf("%s: expected the raw path field to be rejected", name)
		}
	}
}

// TestCopyPTYAllocateBuiltin: copying the fs.pty.allocate built-in produces a
// custom capability that round-trips through grants.pty.allocate, and the
// document written for a NON-PTY copy carries no pty key (serialized output for
// pre-existing capabilities is unchanged).
func TestCopyPTYAllocateBuiltin(t *testing.T) {
	isolate(t)
	src, err := Lookup(PTYAllocateCapability)
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	src.Name = "my.pty.allocate"
	if err := SaveUser(src); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	got, err := Lookup("my.pty.allocate")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := PTYGrants()
	if len(got.Filesystem) != len(want) {
		t.Fatalf("copied filesystem = %+v, want %+v", got.Filesystem, want)
	}
	for i := range want {
		if got.Filesystem[i] != want[i] {
			t.Errorf("copied filesystem[%d] = %+v, want %+v", i, got.Filesystem[i], want[i])
		}
	}

	gh, err := Lookup("network.github")
	if err != nil {
		t.Fatalf("Lookup network.github: %v", err)
	}
	gh.Name = "network.ghcopy"
	if err := SaveUser(gh); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "network.ghcopy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pty:") {
		t.Errorf("a non-PTY copy must not serialize a pty grant:\n%s", raw)
	}
}

// TestSaveUserRejectsBuiltinName: SaveUser refuses to write under a built-in
// name (the disjoint-namespace invariant), and DeleteUser refuses to delete one.
func TestSaveUserRejectsBuiltinName(t *testing.T) {
	isolate(t)
	c, err := Lookup("network.github")
	if err != nil {
		t.Fatalf("Lookup built-in: %v", err)
	}
	if err := SaveUser(c); err == nil {
		t.Error("SaveUser should refuse a built-in name")
	}
	if err := DeleteUser("network.github"); err == nil {
		t.Error("DeleteUser should refuse a built-in name")
	}
}

// TestRetiredPrivilegedNameIsReserved: the retired yolo.dev.privileged name is
// neither a built-in nor available to a custom item, so no grant can select a
// privileged box; every lookup explains the box setting instead.
func TestRetiredPrivilegedNameIsReserved(t *testing.T) {
	isolate(t)
	if _, err := Lookup(RetiredPrivilegedCapability); !errors.Is(err, ErrRetiredPrivileged) || !strings.Contains(err.Error(), "privileged: true") {
		t.Fatalf("Lookup = %v", err)
	}
	if IsBuiltin(RetiredPrivilegedCapability) || slices.Contains(Names(), RetiredPrivilegedCapability) || slices.Contains(BuiltinNames(), RetiredPrivilegedCapability) {
		t.Fatal("retired name is still listed")
	}
	if err := NewEffectiveSet().Add(RetiredPrivilegedCapability, OriginExplicit); !errors.Is(err, ErrRetiredPrivileged) {
		t.Fatalf("effective set accepted the retired name: %v", err)
	}
	custom := Capability{Name: RetiredPrivilegedCapability, Filesystem: []FSGrant{{Path: "/workdir", Access: ReadWrite}}}
	if err := SaveUser(custom); !errors.Is(err, ErrRetiredPrivileged) {
		t.Fatalf("SaveUser = %v", err)
	}
	writeCapability(t, RetiredPrivilegedCapability, `schema_version: 1
name: yolo.dev.privileged
grants:
  workdir:
    write: true
`)
	if _, err := LoadUser(); !errors.Is(err, ErrRetiredPrivileged) {
		t.Fatalf("LoadUser accepted a custom item with the retired name: %v", err)
	}
}

// TestDeleteUserCustom: a custom capability written by SaveUser is removed by
// DeleteUser, after which Lookup fails.
func TestDeleteUserCustom(t *testing.T) {
	isolate(t)
	writeCapability(t, "network.tmp", `schema_version: 1
name: network.tmp
grants:
  network:
    egress:
      - hosts: [a.example.com]
        ports: [443]
`)
	if _, err := Lookup("network.tmp"); err != nil {
		t.Fatalf("precondition Lookup: %v", err)
	}
	if err := DeleteUser("network.tmp"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := Lookup("network.tmp"); err == nil {
		t.Error("expected Lookup to fail after DeleteUser")
	}
}

// TestParseUserCapabilityDefaultsName: a document with no name: field inherits
// the filename, and a mismatching name: is rejected.
func TestParseUserCapabilityDefaultsName(t *testing.T) {
	c, err := parseUserCapability("network.fromfile", []byte("schema_version: 1\ngrants:\n  workdir:\n    write: true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Name != "network.fromfile" {
		t.Errorf("name = %q, want it to default to the filename", c.Name)
	}
	if _, err := parseUserCapability("network.fromfile", []byte("schema_version: 1\nname: other\ngrants:\n  workdir:\n    write: true\n")); err == nil {
		t.Fatal("expected a name/filename mismatch to be rejected")
	}
}

// A copied grant must retain the registry's parsing opt-in without spreading it
// to provider endpoints that share port 443.
func TestCopyEncodedSlashGrantRoundTrip(t *testing.T) {
	isolate(t)
	original, err := Lookup("network.opencode")
	if err != nil {
		t.Fatal(err)
	}
	original.Name = "network.custom-opencode"
	if err := SaveUser(original); err != nil {
		t.Fatal(err)
	}
	copied, err := Lookup(original.Name)
	if err != nil {
		t.Fatal(err)
	}
	var endpoints int
	for _, grant := range copied.Network {
		if len(grant.Binaries) != 1 || grant.Binaries[0] != "/home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe" {
			t.Fatalf("copy changed binary attribution: %v", grant.Binaries)
		}
		for _, endpoint := range grant.Endpoints {
			endpoints++
			if endpoint.AllowEncodedSlash != (endpoint.Host == "registry.npmjs.org") {
				t.Fatalf("copy lost or broadened encoded slash permission: %+v", endpoint)
			}
		}
	}
	if endpoints != len(original.Network[0].Endpoints) {
		t.Fatalf("copy lost endpoints: %d", endpoints)
	}
}

func TestParseEncodedSlashGrant(t *testing.T) {
	const raw = `schema_version: 1
name: network.packages
grants:
  network:
    egress:
      - hosts: [registry.npmjs.org]
        ports: [443]
        binaries: [/usr/bin/node]
        allow_encoded_slash: true
`
	c, err := parseUserCapability("network.packages", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Network[0].Endpoints[0].AllowEncodedSlash {
		t.Fatal("encoded slash permission was lost during translation")
	}
	if _, err := parseUserCapability("network.packages", []byte(strings.ReplaceAll(raw, "allow_encoded_slash: true", "allow_encoded_slash: invalid"))); err == nil {
		t.Fatal("invalid URL parsing permission was accepted")
	}
}

package agentbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
	"gopkg.in/yaml.v3"
)

func TestConfig_SaveLoadRoundtrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := &Config{
		Name:           "coder",
		Space:          "default",
		Agent:          "claude",
		Account:        "default",
		Image:          "yoloctl/agentbox:ubuntu-24.04",
		AgentExtraArgs: []string{"--model", "opus"},
		Workdir: Workdir{
			Mode:      WorkdirModeHost,
			Source:    "/home/leogr/projects/myrepo",
			Canonical: "/home/leogr/projects/myrepo",
		},
		Resources: Resources{
			CPUs:   "2",
			Memory: "2g",
		},
		Features:     []string{"claude", "go", "github"},
		Capabilities: []string{capabilities.WorkdirWriteCapability, "network.github"},
		Ports: []Port{
			{Target: 3000, Published: 3000},
			{Target: 8080, Published: 8080},
		},
		Env: map[string]EnvVar{
			"DEBUG":     {Literal: "1"},
			"LOG_LEVEL": {Literal: "info"},
		},
		Mounts: []Mount{
			{Source: "/var/log/external", Target: "/var/log/external", ReadOnly: true},
		},
	}
	if err := want.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want.SchemaVersion != SchemaVersion {
		t.Errorf("Save did not auto-fill SchemaVersion: got %d", want.SchemaVersion)
	}
	got, err := Load("default", "coder")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The path field is set by Save/Open from the filesystem; clear
	// both before comparing the declared content.
	if got.Path() == "" {
		t.Errorf("Load: got.path is empty")
	}
	got.path = ""
	want.path = ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roundtrip mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestConfig_SaveSetsMode0644(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "coder", Space: "default", Image: "img:1"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(c.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != fileMode {
		t.Errorf("file mode = %v, want %v", info.Mode().Perm(), fileMode)
	}
}

func TestOpen_RejectsUnknownSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coder.yaml")
	for _, raw := range []string{
		"",                              // empty (implicit schema_version: 0)
		"schema_version: 0\nname: x\n",  // explicit zero
		"schema_version: 99\nname: x\n", // future version
	} {
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Errorf("Open(%q): expected schema_version error, got nil", raw)
		}
	}
}

// TestOpen_RejectsRemovedFields is the fail-closed guard for the
// manifest/runtime migration: a manifest carrying a field the model removed
// (read_only / no_egress / allow / policy_file / template) must ERROR on load,
// not be silently dropped — otherwise an old locked-down manifest would be
// reinterpreted as a new positive-capability one (e.g. an old `no_egress: true`
// box silently gaining egress).
func TestOpen_RejectsRemovedFields(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"read_only":   "read_only: true",
		"no_egress":   "no_egress: true",
		"policy_file": "policy_file: /etc/p.yaml",
		"template":    "template: dev",
		"allow":       "allow:\n  - host: x\n    port: 443",
	}
	for name, field := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".yaml")
			raw := "schema_version: 1\nname: coder\nspace: default\nfeatures: [claude]\n" + field + "\n"
			if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Errorf("Open with removed field %q: expected error, got nil", name)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("Open error %q should name the removed field %q", err.Error(), name)
			}
		})
	}
}

// TestMounts_YAMLKeysRenamed locks the manifest schema rename: the extra
// bind-mount block marshals as `mounts:` with `{source, target, read_only}`
// entries (the old `extra_mounts:` / `destination:` spellings must not appear).
func TestMounts_YAMLKeysRenamed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{
		Image:   "img:1",
		Name:    "coder",
		Space:   "default",
		Workdir: Workdir{Mode: WorkdirModeEphemeral, Source: "/var/lib/yoloctl/agentboxes/coder"},
		Mounts:  []Mount{{Source: "/h/src", Target: "/c/dst", ReadOnly: true}},
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(raw)
	for _, want := range []string{"mounts:", "source:", "target:", "read_only:"} {
		if !strings.Contains(got, want) {
			t.Errorf("marshaled YAML missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"extra_mounts:", "destination:"} {
		if strings.Contains(got, banned) {
			t.Errorf("marshaled YAML must not contain the old key %q:\n%s", banned, got)
		}
	}
}

// TestMounts_RoundTrip checks a mounts block survives Save→Load with source,
// target, and read_only intact under the renamed field names.
func TestMounts_RoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{
		Image:   "img:1",
		Name:    "coder",
		Space:   "default",
		Workdir: Workdir{Mode: WorkdirModeEphemeral, Source: "/var/lib/yoloctl/agentboxes/coder"},
		Mounts: []Mount{
			{Source: "/h/a", Target: "/c/a"},
			{Source: "/h/b", Target: "/c/b", ReadOnly: true},
		},
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("default", "coder")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Mounts, c.Mounts) {
		t.Errorf("mounts round-trip mismatch:\ngot:  %+v\nwant: %+v", got.Mounts, c.Mounts)
	}
}

// TestOpen_RejectsExtraMountsField is the fail-closed guard for the
// extra_mounts→mounts rename: a manifest carrying the OLD `extra_mounts:` block
// must ERROR on strict decode, not silently drop the user's bind mounts.
func TestOpen_RejectsExtraMountsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coder.yaml")
	raw := "schema_version: 1\nname: coder\nspace: default\nfeatures: [claude]\n" +
		"extra_mounts:\n  - source: /h/src\n    destination: /c/dst\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if err == nil {
		t.Fatal("Open with old extra_mounts field: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "extra_mounts") {
		t.Errorf("error %q should name the removed field extra_mounts", err.Error())
	}
}

func TestLoad_NotExist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Load("default", "missing")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load missing: want fs.ErrNotExist, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	minimal := func() *Config {
		return &Config{
			Name:    "coder",
			Space:   "default",
			Workdir: Workdir{Mode: WorkdirModeEphemeral, Source: "/var/lib/yoloctl/agentboxes/coder"},
		}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring; empty = expect success
	}{
		{
			name:   "minimal valid",
			mutate: func(c *Config) {},
		},
		{
			name:    "missing name",
			mutate:  func(c *Config) { c.Name = "" },
			wantErr: "agentbox",
		},
		{
			name:    "one-character box name",
			mutate:  func(c *Config) { c.Name = "a" },
			wantErr: "invalid agentbox name",
		},
		{
			name:   "two-character box name",
			mutate: func(c *Config) { c.Name = "a." },
		},
		{
			name:    "missing space",
			mutate:  func(c *Config) { c.Space = "" },
			wantErr: "space is required",
		},
		{
			name:    "bad space name",
			mutate:  func(c *Config) { c.Space = "with space" },
			wantErr: "space",
		},
		{
			name:    "capability name invalid",
			mutate:  func(c *Config) { c.Capabilities = []string{"bad/cap"} },
			wantErr: "invalid capability",
		},
		{
			name:   "capability name ok",
			mutate: func(c *Config) { c.Capabilities = []string{"network.github", "fs.workdir.write"} },
		},
		{
			name:    "duplicate published port",
			mutate:  func(c *Config) { c.Ports = []Port{{Target: 3000, Published: 3000}, {Target: 4000, Published: 3000}} },
			wantErr: "declared twice",
		},
		{
			name:    "port target out of range",
			mutate:  func(c *Config) { c.Ports = []Port{{Target: 0, Published: 3000}} },
			wantErr: "out of range",
		},
		{
			name:    "port published out of range",
			mutate:  func(c *Config) { c.Ports = []Port{{Target: 3000, Published: 70000}} },
			wantErr: "out of range",
		},
		{
			name:    "port bad protocol",
			mutate:  func(c *Config) { c.Ports = []Port{{Target: 3000, Published: 3000, Protocol: "sctp"}} },
			wantErr: "must be tcp or udp",
		},
		{
			name:    "port bad host_ip",
			mutate:  func(c *Config) { c.Ports = []Port{{Target: 3000, Published: 3000, HostIP: "not-an-ip"}} },
			wantErr: "not a valid IP",
		},
		{
			name: "port same published different protocol ok",
			mutate: func(c *Config) {
				c.Ports = []Port{{Target: 3000, Published: 3000, Protocol: "tcp"}, {Target: 3000, Published: 3000, Protocol: "udp"}}
			},
		},
		{
			name:    "mount relative source",
			mutate:  func(c *Config) { c.Mounts = []Mount{{Source: "rel/path", Target: "/abs"}} },
			wantErr: "absolute path",
		},
		{
			name:   "agent name ok",
			mutate: func(c *Config) { c.Agent = "claude"; c.Account = "default" },
		},
		{
			name: "agent extra args with real agent ok",
			mutate: func(c *Config) {
				c.Agent = "claude"
				c.Account = "default"
				c.AgentExtraArgs = []string{"--model", "opus"}
			},
		},
		{
			name: "agent extra args on generic shell rejected",
			mutate: func(c *Config) {
				c.Agent = "none"
				c.Account = "none"
				c.AgentExtraArgs = []string{"--model", "opus"}
			},
			wantErr: "agent_extra_args requires a real agent",
		},
		{
			name:    "agent name invalid",
			mutate:  func(c *Config) { c.Agent = "bad/name"; c.Account = "default" },
			wantErr: "invalid agent",
		},
		{
			// DECISION A: a generic-shell box (no agent) uses account none.
			name:   "generic shell agent none account none ok",
			mutate: func(c *Config) { c.Agent = "none"; c.Account = "none" },
		},
		{
			name:   "generic shell empty agent empty account ok",
			mutate: func(c *Config) { c.Agent = ""; c.Account = "" },
		},
		{
			// DECISION B: a real agent with account none is invalid.
			name:    "real agent with account none rejected",
			mutate:  func(c *Config) { c.Agent = "claude"; c.Account = "none" },
			wantErr: "real agents require real accounts",
		},
		{
			name:    "real agent with empty account rejected",
			mutate:  func(c *Config) { c.Agent = "claude"; c.Account = "" },
			wantErr: "real agents require real accounts",
		},
		{
			// Generic shell with a real account name is incoherent.
			name:    "shell box with real account rejected",
			mutate:  func(c *Config) { c.Agent = "none"; c.Account = "default" },
			wantErr: "only a real agent can select a real account",
		},
		{
			// Any real account serves any agent — a plain name, no agent
			// coupling and no scope segment.
			name:   "cross-agent account name ok",
			mutate: func(c *Config) { c.Agent = "claude"; c.Account = "personal" },
		},
		{
			// A scoped <scope>/<agent>/<name> ref shape is simply an invalid
			// NAME (slashes never validate).
			name:    "scoped ref rejected as a name",
			mutate:  func(c *Config) { c.Agent = "claude"; c.Account = "space/claude/default" },
			wantErr: "invalid account name",
		},
		{
			name:    "reserved name none-adjacent invalid chars rejected",
			mutate:  func(c *Config) { c.Agent = "claude"; c.Account = "-bad" },
			wantErr: "invalid account name",
		},
		{
			name:    "env empty key rejected",
			mutate:  func(c *Config) { c.Env = map[string]EnvVar{"": {Literal: "x"}} },
			wantErr: "empty key",
		},
		{
			name:   "env literal value ok (empty value allowed)",
			mutate: func(c *Config) { c.Env = map[string]EnvVar{"FOO": {Literal: ""}, "BAR": {Literal: "baz"}} },
		},
		{
			name:   "empty workdir ok (CLI fills it at create)",
			mutate: func(c *Config) { c.Workdir = Workdir{} },
		},
		{
			name:    "host workdir without source",
			mutate:  func(c *Config) { c.Workdir = Workdir{Mode: WorkdirModeHost} },
			wantErr: "workdir.source is required",
		},
		{
			name:    "host workdir relative source",
			mutate:  func(c *Config) { c.Workdir = Workdir{Mode: WorkdirModeHost, Source: "rel/dir"} },
			wantErr: "absolute path",
		},
		{
			name:    "unknown workdir mode",
			mutate:  func(c *Config) { c.Workdir = Workdir{Mode: "bogus", Source: "/x"} },
			wantErr: "unknown workdir.mode",
		},
		{
			name: "valid full config",
			mutate: func(c *Config) {
				c.Features = []string{"claude", "go"}
				c.Capabilities = []string{"fs.workdir.write", "network.github"}
				c.Ports = []Port{{Target: 3000, Published: 3000}, {Target: 8080, Published: 8080}}
				c.Env = map[string]EnvVar{
					"DEBUG":     {Literal: "1"},
					"LOG_LEVEL": {Literal: "info"},
				}
				c.Mounts = []Mount{{Source: "/host", Target: "/container", ReadOnly: true}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := minimal()
			tt.mutate(c)
			err := c.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate: unexpected error %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("Validate: expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate: error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestEnvVar_YAMLForms(t *testing.T) {
	// env is LITERAL-ONLY: a scalar value, round-tripped verbatim.
	c := &Config{Name: "a", Space: "default"}
	c.Env = map[string]EnvVar{"FOO": {Literal: "bar"}}
	c.SchemaVersion = SchemaVersion
	raw, err := marshalForTest(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "FOO: bar") {
		t.Errorf("literal env should marshal as `FOO: bar`, got:\n%s", raw)
	}
}

// TestEnvVar_RejectsFromCredentialMapping pins the literal-only env contract:
// the removed `{from_credential: NAME}` reference form (it pointed at the
// deleted global credential store) must fail CLOSED on decode with a migration
// note, never be silently dropped.
func TestEnvVar_RejectsFromCredentialMapping(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "bad.yaml")
	raw := "schema_version: 1\nname: a\nspace: default\nenv:\n  KEY:\n    from_credential: GITHUB_TOKEN\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if err == nil {
		t.Fatal("Open: expected error for from_credential mapping, got nil")
	}
	if !strings.Contains(err.Error(), "from_credential") {
		t.Errorf("error should mention from_credential migration, got: %v", err)
	}
}

// TestEnvVar_LiteralRoundTrip confirms a literal env value survives a
// Save/Load round-trip (the only supported form).
func TestEnvVar_LiteralRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "envbox", Space: "default", Image: "img:1", Env: map[string]EnvVar{"FOO": {Literal: "bar=baz"}}}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("default", "envbox")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Env["FOO"].Literal != "bar=baz" {
		t.Errorf("literal env round-trip: got %q, want %q", got.Env["FOO"].Literal, "bar=baz")
	}
}

func TestListNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Empty (dir doesn't exist yet) -> empty slice, no error.
	got, err := ListNames("default")
	if err != nil {
		t.Fatalf("ListNames empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListNames empty: got %v, want []", got)
	}

	// Save several configs; ListNames returns them sorted.
	for _, name := range []string{"zebra", "alpha", "mango"} {
		c := &Config{Name: name, Space: "default", Image: "img:1"}
		if err := c.Save(); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}
	got, err = ListNames("default")
	if err != nil {
		t.Fatalf("ListNames: %v", err)
	}
	want := []string{"alpha", "mango", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListNames = %v, want %v", got, want)
	}
}

func TestDelete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "ephemeral", Space: "default", Image: "img:1"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(c.Path()); err != nil {
		t.Fatalf("Stat after Save: %v", err)
	}
	if err := c.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(c.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after Delete: file still exists or unexpected error: %v", err)
	}
	// Second delete should report fs.ErrNotExist (idempotency is the
	// caller's responsibility — Delete is honest about the file state).
	if err := c.Delete(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("second Delete: want fs.ErrNotExist, got %v", err)
	}
}

func TestPort_ResolvedDefaults(t *testing.T) {
	// Omitted host_ip/protocol resolve to the documented defaults.
	p := Port{Target: 3000, Published: 8080}
	if got := p.ResolvedHostIP(); got != "127.0.0.1" {
		t.Errorf("ResolvedHostIP() = %q, want 127.0.0.1", got)
	}
	if got := p.ResolvedProtocol(); got != "tcp" {
		t.Errorf("ResolvedProtocol() = %q, want tcp", got)
	}
	// Explicit values are returned verbatim.
	p2 := Port{Target: 53, Published: 53, HostIP: "0.0.0.0", Protocol: "udp"}
	if got := p2.ResolvedHostIP(); got != "0.0.0.0" {
		t.Errorf("ResolvedHostIP() = %q, want 0.0.0.0", got)
	}
	if got := p2.ResolvedProtocol(); got != "udp" {
		t.Errorf("ResolvedProtocol() = %q, want udp", got)
	}
}

func TestPort_OmitsDefaultsInYAML(t *testing.T) {
	// A Port with default host_ip/protocol marshals with only target +
	// published (omitempty keeps the manifest minimal); the resolved form is
	// only materialized by the resolvers / box inspect.
	c := &Config{
		Name:  "coder",
		Space: "default",
		Ports: []Port{{Target: 3000, Published: 3000}},
	}
	out, err := marshalForTest(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(out, "host_ip") || strings.Contains(out, "protocol") {
		t.Errorf("default host_ip/protocol must be omitted from YAML, got:\n%s", out)
	}
	if !strings.Contains(out, "target: 3000") || !strings.Contains(out, "published: 3000") {
		t.Errorf("target/published must be present, got:\n%s", out)
	}
	// A non-default protocol IS emitted.
	c.Ports = []Port{{Target: 53, Published: 53, Protocol: "udp", HostIP: "0.0.0.0"}}
	out, err = marshalForTest(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(out, "protocol: udp") || !strings.Contains(out, "host_ip: 0.0.0.0") {
		t.Errorf("non-default protocol/host_ip must be emitted, got:\n%s", out)
	}
}

// marshalForTest serializes a Config to YAML for assertion in
// TestEnvVar_YAMLForms. Calling Save would also work but requires a
// writable XDG_CONFIG_HOME; this is enough for shape checks.
func marshalForTest(c *Config) (string, error) {
	out, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

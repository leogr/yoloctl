package agentbox

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
	"gopkg.in/yaml.v3"
)

// rw is the explicit fs.workdir.write capability that create/run add by
// default (the positive-grant replacement for the old !read_only posture).
// Golden inputs that should compile to a read-write /workdir must list it,
// exactly as a created box's manifest does; its absence yields a read-only
// /workdir (the claude_read_only case).
var rw = []string{capabilities.WorkdirWriteCapability}

// This file is the generated-policy contract and safety net. It locks the
// CURRENT openshell-sandbox policy output produced by ComposePolicy and
// ComposePolicy.
//
// Two layers of protection:
//
//  1. TestPolicyGolden — byte-exact golden files under testdata/policy_golden/.
//     The capability compiler MUST reproduce these verbatim, or the goldens
//     must be regenerated under explicit review. This is the "exact policy
//     text is the contract OpenShell consumes" guarantee.
//
//  2. TestPolicyBinaryAttribution — semantic assertions on the per-binary
//     egress attribution, independent of the golden text. This is the real
//     anti-exfil property: a network feature confines egress to SPECIFIC
//     binaries (git/gh, not curl), so a compromised agent can't tunnel
//     exfiltration through an allow-listed host. A rewrite that widened these
//     binary lists would weaken egress confinement while leaving the golden
//     comparison green only if someone blindly regenerated — so this layer
//     guards the property directly.
//
// Regenerate goldens after an INTENTIONAL policy change:
//
//	YOLOCTL_UPDATE_GOLDEN=1 go test ./internal/agentbox/ -run TestPolicyGolden

const goldenEnv = "YOLOCTL_UPDATE_GOLDEN"

// policyGoldenCases are the canonical compositions OpenShell consumes today.
// Each combination that the reconciler can feed the sandbox should appear
// here so an accidental broadening/narrowing in Phase 5 trips a diff.
var policyGoldenCases = []struct {
	name string
	cfg  *Config
}{
	// Single-agent defaults (the claude-default / codex-default analogs). The
	// workdir-write capability is what create/run add by default, so the
	// compiled /workdir is read-write — byte-identical to the pre-capability
	// output.
	{"claude", &Config{Name: "coder", Space: "default", Features: []string{"claude"}, Capabilities: rw}},
	{"codex", &Config{Name: "coder", Space: "default", Features: []string{"codex"}, Capabilities: rw}},
	{"agy", &Config{Name: "coder", Space: "default", Features: []string{"agy"}, Capabilities: rw}},
	{"agy_github", &Config{Name: "coder", Space: "default", Agent: "agy", Capabilities: rw}},
	{"agy_read_only", &Config{Name: "coder", Space: "default", Features: []string{"agy"}}},
	{"opencode", &Config{Name: "coder", Space: "default", Features: []string{"opencode"}, Capabilities: rw}},
	{"opencode_github", &Config{Name: "coder", Space: "default", Agent: "opencode", Capabilities: rw}},
	{"opencode_read_only", &Config{Name: "coder", Space: "default", Features: []string{"opencode"}}},
	{"gemini", &Config{Name: "coder", Space: "default", Features: []string{"gemini"}, Capabilities: rw}},
	{"pi", &Config{Name: "coder", Space: "default", Features: []string{"pi"}, Capabilities: rw}},
	{"pi_github", &Config{Name: "coder", Space: "default", Agent: "pi", Capabilities: rw}},
	{"pi_read_only", &Config{Name: "coder", Space: "default", Features: []string{"pi"}}},

	// Agent + ecosystem stack (the common real composition).
	{"claude_github", &Config{Name: "coder", Space: "default", Features: []string{"claude", "github"}, Capabilities: rw}},
	{"codex_github", &Config{Name: "coder", Space: "default", Features: []string{"codex", "github"}, Capabilities: rw}},
	// Also exercise implicit features from the shipped Gemini profile.
	{"gemini_github", &Config{Name: "coder", Space: "default", Agent: "gemini", Capabilities: rw}},

	// Ecosystem / language features, each isolated so its exact grant is frozen.
	{"github", &Config{Name: "coder", Space: "default", Features: []string{"github"}, Capabilities: rw}},
	{"go-modules", &Config{Name: "coder", Space: "default", Features: []string{"go"}, Capabilities: rw}},
	{"npm", &Config{Name: "coder", Space: "default", Features: []string{"npm"}, Capabilities: rw}},
	{"pypi", &Config{Name: "coder", Space: "default", Features: []string{"pypi"}, Capabilities: rw}},
	{"pip", &Config{Name: "coder", Space: "default", Features: []string{"pip"}, Capabilities: rw}},
	{"cargo", &Config{Name: "coder", Space: "default", Features: []string{"cargo"}, Capabilities: rw}},

	// Dockerfile-only feature (no network policy): baseline + empty net.
	{"python-3.12", &Config{Name: "coder", Space: "default", Features: []string{"python-3.12"}, Capabilities: rw}},

	// Read-only workdir: no fs.workdir.write capability → /workdir is
	// placed in read_only. This is the positive-grant replacement for the old
	// read_only posture flag. include_workdir is false so the new boundary
	// cannot add the cwd back as an implicit Landlock write grant.
	{"claude_read_only", &Config{Name: "coder", Space: "default", Features: []string{"claude"}}},
	{"gemini_github_read_only", &Config{Name: "coder", Space: "default", Agent: "gemini"}},

	// Opt-in PTY allocation: fs.pty.allocate appends exactly /dev/pts and
	// /dev/tty to read_write, after /workdir. Every other golden above proves
	// the negative: without the capability no terminal device path is granted.
	{"claude_pty", &Config{Name: "coder", Space: "default", Features: []string{"claude"}, Capabilities: []string{capabilities.WorkdirWriteCapability, capabilities.PTYAllocateCapability}}},

	// Declared extra mounts: a read-only bind outside the baseline is added to
	// read_only and a writable one to read_write, after the capability grants;
	// the account-state slot and any bind already under an equal or broader
	// entry (/home/sandbox) add nothing.
	{"extra_mounts", &Config{Name: "coder", Space: "default", Features: []string{"claude"}, Capabilities: rw, Mounts: []Mount{
		{Source: "/workdir/home/me/docs", Target: "/mnt/x", ReadOnly: true},
		{Source: "/workdir/home/me/out", Target: "/data/out"},
		{Source: "/var/lib/yoloctl/account-state/a/claude", Target: "/home/sandbox/.claude"},
		{Source: "/workdir/home/me/ref", Target: "/home/sandbox/ref", ReadOnly: true},
	}}},
}

func TestPolicyGolden(t *testing.T) {
	update := os.Getenv(goldenEnv) != ""
	for _, tc := range policyGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComposePolicy(tc.cfg)
			if err != nil {
				t.Fatalf("ComposePolicy(%s): %v", tc.name, err)
			}
			path := filepath.Join("testdata", "policy_golden", tc.name+".yaml")
			if update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir golden dir: %v", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", path, err)
				}
				t.Logf("updated golden %s", path)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v\nregenerate with %s=1 go test ./internal/agentbox/ -run TestPolicyGolden", path, err, goldenEnv)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("composed policy for %q drifted from golden %s.\n"+
					"If this change is intentional, regenerate with %s=1.\n--- got ---\n%s\n--- want ---\n%s",
					tc.name, path, goldenEnv, got, want)
			}
		})
	}
}

// TestPolicyBinaryAttribution locks the per-binary egress attribution for every
// network-bearing feature, independent of the golden text. This is the core
// anti-exfil invariant: egress to an allow-listed host
// is confined to the specific tool binaries that legitimately need it, so a
// compromised agent cannot reuse the allowance from an arbitrary process.
func TestPolicyBinaryAttribution(t *testing.T) {
	cases := []struct {
		feature      string
		policyName   string
		wantBinaries []string // exact, in order
		wantHosts    []string // subset that must be present
	}{
		{
			feature:    "claude",
			policyName: "anthropic",
			wantBinaries: []string{
				"/usr/bin/node",
				"/home/sandbox/.npm-global/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe",
			},
			wantHosts: []string{"api.anthropic.com", "*.anthropic.com", "claude.com"},
		},
		{
			feature:    "codex",
			policyName: "openai",
			wantBinaries: []string{
				"/usr/bin/node",
				"/home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex",
				"/home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-arm64/vendor/aarch64-unknown-linux-musl/bin/codex",
				"/home/sandbox/.codex/packages/app-server-daemon/releases/*-x86_64-unknown-linux-musl/bin/codex",
				"/home/sandbox/.codex/packages/app-server-daemon/releases/*-aarch64-unknown-linux-musl/bin/codex",
			},
			wantHosts: []string{"api.openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"},
		},
		{
			feature: "agy", policyName: "agy",
			wantBinaries: []string{"/home/sandbox/.local/share/yoloctl/agy/agy"},
			wantHosts:    []string{"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com", "generativelanguage.googleapis.com", "oauth2.googleapis.com", "www.googleapis.com"},
		},
		{
			feature:      "gemini",
			policyName:   "gemini",
			wantBinaries: []string{"/usr/bin/node"},
			wantHosts:    []string{"generativelanguage.googleapis.com", "cloudcode-pa.googleapis.com", "oauth2.googleapis.com", "www.googleapis.com"},
		},
		{
			feature: "opencode", policyName: "opencode",
			wantBinaries: []string{"/home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe"},
			wantHosts:    []string{"registry.npmjs.org", "opencode.ai", "api.anthropic.com", "api.openai.com", "auth.openai.com", "chatgpt.com", "generativelanguage.googleapis.com", "openrouter.ai"},
		},
		{
			feature:      "github",
			policyName:   "github",
			wantBinaries: []string{"/usr/bin/git", "/usr/bin/gh"},
			wantHosts:    []string{"github.com", "*.github.com", "*.githubusercontent.com"},
		},
		{
			feature: "pi", policyName: "pi",
			wantBinaries: []string{"/usr/bin/node"},
			wantHosts:    []string{"api.anthropic.com", "platform.claude.com", "api.openai.com", "auth.openai.com", "chatgpt.com", "generativelanguage.googleapis.com", "openrouter.ai"},
		},
		{
			feature:      "go",
			policyName:   "go-modules",
			wantBinaries: []string{"/usr/local/go/bin/go"},
			wantHosts:    []string{"proxy.golang.org", "sum.golang.org"},
		},
		{
			feature:    "npm",
			policyName: "npm",
			wantBinaries: []string{
				"/usr/bin/node", "/usr/bin/npm", "/usr/bin/npx", "/usr/bin/pnpm", "/usr/bin/yarn",
			},
			wantHosts: []string{"registry.npmjs.org"},
		},
		{
			feature:    "pypi",
			policyName: "pypi",
			wantBinaries: []string{
				"/usr/bin/python3.13", "/usr/bin/pip", "/usr/bin/pip3", "/usr/bin/python3", "/usr/local/bin/uv",
			},
			wantHosts: []string{"pypi.org", "*.pypi.org", "files.pythonhosted.org"},
		},
		{
			feature:    "pip",
			policyName: "pypi",
			wantBinaries: []string{
				"/usr/bin/python3.13", "/usr/bin/pip", "/usr/bin/pip3", "/usr/bin/python3", "/usr/local/bin/uv",
			},
			wantHosts: []string{"pypi.org", "*.pypi.org", "files.pythonhosted.org"},
		},
		{
			feature:      "cargo",
			policyName:   "cargo",
			wantBinaries: []string{"/usr/bin/cargo"},
			wantHosts:    []string{"crates.io", "static.crates.io", "index.crates.io"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.feature, func(t *testing.T) {
			doc := roundTripCompose(t, &Config{Name: "x", Space: "default", Features: []string{tc.feature}})
			np, ok := doc.NetworkPolicies[tc.policyName]
			if !ok {
				t.Fatalf("feature %q: missing network policy %q; got %v", tc.feature, tc.policyName, policyKeys(doc.NetworkPolicies))
			}
			gotBins := make([]string, 0, len(np.Binaries))
			for _, b := range np.Binaries {
				gotBins = append(gotBins, b.Path)
				if b.Path == "**" {
					t.Errorf("feature %q: binary attribution includes wildcard %q — this is an egress-confinement regression (no capability may use a ** binary)", tc.feature, b.Path)
				}
			}
			if strings.Join(gotBins, ",") != strings.Join(tc.wantBinaries, ",") {
				t.Errorf("feature %q binaries = %v, want %v", tc.feature, gotBins, tc.wantBinaries)
			}
			hosts := endpointHosts(np.Endpoints)
			for _, want := range tc.wantHosts {
				if !contains(hosts, want) {
					t.Errorf("feature %q endpoints missing %q; got %v", tc.feature, want, hosts)
				}
			}
		})
	}
}

// TestLintHostPattern freezes the network-host rule the composer enforces, so a
// bad capability fails at compose time with a clear error instead of crashing
// the live sandbox at startup (the SSH socket never opens). It mirrors
// openshell-sandbox v0.0.72 validate_host_wildcard
// (crates/openshell-supervisor-network/src/l7/mod.rs): literal hosts and
// per-domain wildcards are accepted; match-all, wildcard-outside-first-label,
// recursive-`**`-not-the-whole-first-label, and TLD/single-label wildcards are
// rejected. (Replaces the former permissive-net wildcard guard — `**` egress is
// no longer expressible, by design.)
func TestLintHostPattern(t *testing.T) {
	valid := []string{
		"crates.io", "api.anthropic.com", // literals
		"*.example.com", "**.example.com", "*.sub.example.com", // per-domain wildcards
		"*foo.bar.com", // partial-label wildcard in the first label (openshell allows)
	}
	for _, h := range valid {
		if err := lintHostPattern(h); err != nil {
			t.Errorf("lintHostPattern(%q) = %v, want nil (openshell accepts it)", h, err)
		}
	}
	invalid := []string{
		"*", "**", // match-all
		"*.com", "**.com", // TLD wildcards
		"foo.*.com",        // wildcard outside the first label
		"a**b.example.com", // recursive ** not the whole first label
	}
	for _, h := range invalid {
		if err := lintHostPattern(h); err == nil {
			t.Errorf("lintHostPattern(%q) = nil, want a rejection (openshell rejects it → sandbox would crash)", h)
		}
	}
}

// TestComposePolicy_NoneDeclaredLockedDown freezes the generic-shell contract:
// a config with no features, no capabilities, and no agent is NOT an error — it
// composes a maximally locked-down policy (read-only /workdir, empty
// network_policies = default-deny egress). The composer's baseline is always
// confined, so the empty capability set is the SAFEST box, not a "fully-open"
// sandbox: there is no open empty policy to fail closed against.
func TestComposePolicy_NoneDeclaredLockedDown(t *testing.T) {
	c := &Config{Name: "x", Space: "default"}
	out, err := ComposePolicy(c)
	if err != nil {
		t.Fatalf("no-policy config must compose a locked-down policy, got error: %v", err)
	}
	pol := string(out)
	if !strings.Contains(pol, "network_policies: {}") {
		t.Errorf("empty capability set must yield empty network_policies (default-deny egress); got:\n%s", pol)
	}
	if !strings.Contains(pol, "/workdir") {
		t.Errorf("locked-down policy must still place /workdir (read-only); got:\n%s", pol)
	}
}

// TestPolicyGoldenStableAcrossRuns guards determinism: ComposePolicy must be a
// pure function of its input (map key ordering, slice ordering) so the golden
// comparison is meaningful. Two calls on the same config must be byte-identical.
func TestPolicyGoldenStableAcrossRuns(t *testing.T) {
	for _, tc := range policyGoldenCases {
		a, err := ComposePolicy(tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		b, err := ComposePolicy(tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s: ComposePolicy not deterministic across calls", tc.name)
		}
	}
	// Also assert yaml round-trip stability of the document shape itself.
	var doc policyDoc
	raw, _ := ComposePolicy(&Config{Name: "x", Space: "default", Features: []string{"claude", "github", "npm"}})
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("re-parse composed policy: %v", err)
	}
}

func TestOpenCodeRegistryURLParsing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, features := range [][]string{{"opencode"}, {"npm"}, {"gemini"}, {"pi"}, {"opencode", "npm", "pi"}} {
		doc := roundTripCompose(t, &Config{Name: "coder", Space: "default", Features: features})
		var registryFound bool
		for name, policy := range doc.NetworkPolicies {
			for _, endpoint := range policy.Endpoints {
				if name == "opencode" && endpoint.Host == "registry.npmjs.org" {
					registryFound = true
					if !endpoint.AllowEncodedSlash || endpoint.Protocol != "rest" || endpoint.Enforcement != "enforce" || endpoint.Access != "full" {
						t.Fatalf("OpenCode scoped-package URLs cannot pass: %+v", endpoint)
					}
					continue
				}
				if endpoint.AllowEncodedSlash || endpoint.Protocol != "" || endpoint.Enforcement != "" || endpoint.Access != "" {
					t.Fatalf("unrelated endpoint changed: features=%v policy=%s endpoint=%+v", features, name, endpoint)
				}
			}
		}
		if slices.Contains(features, "opencode") && !registryFound {
			t.Fatal("OpenCode registry grant is missing")
		}
	}
}

// Each declared extra mount is reachable through Landlock with the access its
// bind promises; a writable bind over a $PATH lookup directory is refused like
// any read_write grant there.
func TestPolicyGrantsDeclaredExtraMounts(t *testing.T) {
	compose := func(mounts ...Mount) (policyDoc, error) {
		raw, err := ComposePolicy(&Config{Name: "coder", Space: "default", Features: []string{"python-3.12"}, Mounts: mounts})
		var doc policyDoc
		if err == nil {
			err = yaml.Unmarshal(raw, &doc)
		}
		return doc, err
	}
	doc, err := compose(Mount{Source: "/s/a", Target: "/mnt/x/", ReadOnly: true}, Mount{Source: "/s/b", Target: "/workdir/out"}, Mount{Source: "/s/c", Target: "/mnt/x", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ro, rwPaths := doc.FilesystemPolicy.ReadOnly, doc.FilesystemPolicy.ReadWrite
	if n := strings.Count(strings.Join(ro, "\n")+"\n", "/mnt/x\n"); n != 1 || slices.Contains(rwPaths, "/mnt/x") {
		t.Fatalf("read-only mount: read_only %v, read_write %v", ro, rwPaths)
	}
	// A read-only workdir does not cover a writable bind beneath it.
	if !slices.Contains(rwPaths, "/workdir/out") || !slices.Contains(ro, workdirPath) {
		t.Fatalf("writable mount under a read-only workdir: read_only %v, read_write %v", ro, rwPaths)
	}
	if _, err := compose(Mount{Source: "/s/bin", Target: "/usr/local/bin"}); err == nil {
		t.Fatal("a writable bind over a PATH directory was granted")
	}
	if _, err := compose(Mount{Source: "/s/bin", Target: "/usr/local/bin", ReadOnly: true}); err != nil {
		t.Fatalf("a read-only bind over a PATH directory: %v", err)
	}
}

package agentbox

import (
	"fmt"
	"path"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/features"
	"gopkg.in/yaml.v3"
)

// workdirPath is the in-container workdir root. Its read-only vs
// read-write placement is governed by the fs.workdir.write capability.
const workdirPath = "/workdir"

// ComposePolicy resolves c's declared capability intent into the
// openshell-sandbox policy.yaml by deriving its effective positive-capability
// set and compiling it. The computed policy is an inspect-only artifact (no
// user-editable policy_file escape hatch).
//
// The capability model replaces the old direct Features→network_policies +
// ReadOnly/NoEgress posture
// composition. The compiled YAML is byte-identical to the pre-capability output
// — the testdata/policy_golden contract is what guarantees that. Output shape
// (see assets/sandbox/presets/*.yaml):
//
//	version: 1
//	filesystem_policy: baseline + /workdir (rw iff fs.workdir.write granted)
//	                   + /dev/pts, /dev/tty (rw iff fs.pty.allocate granted)
//	landlock: best_effort
//	process: sandbox / sandbox
//	network_policies: { from effective network capabilities }
//
// A manifest with no features, capabilities, or agent compiles to the safest
// default-deny baseline: empty network_policies and a read-only workdir.
// Exported so inspect, diff, reconciliation, and tests all use the exact policy
// bytes covered by the policy-golden contract.
func ComposePolicy(c *Config) ([]byte, error) {
	if err := refreshChatProjection(c); err != nil {
		return nil, err
	}
	eff, err := EffectiveCapabilities(c)
	if err != nil {
		return nil, err
	}
	return compilePolicy(eff, c.Mounts)
}

// EffectiveCapabilities derives the positive capability set a schema-v1
// Config grants, with origin tracking for policy compilation and read-only
// consumers such as box inspect. Using this one implementation keeps inspect
// and generated policy in agreement. The model in force has a single
// capability boundary — capabilities reach the policy ONLY via features:
//
//   - the box's explicit Capabilities are added directly (OriginExplicit); a
//     writable workdir is the explicit fs.workdir.write capability
//     (create/run add it by default; --read-only omits it), so an empty
//     manifest is locked down by construction;
//   - every feature in the EFFECTIVE FEATURE SET contributes its
//     RequiresCapabilities (OriginFeature). The effective feature set is the
//     box's own Features UNIONed with the selected agent profile's
//     RequiresFeatures — the agent's egress is granted because RequiresFeatures
//     pulls in its CLI feature (e.g. claude → network.anthropic), NOT via any
//     agent-level capability.
func EffectiveCapabilities(c *Config) (*capabilities.EffectiveSet, error) {
	set := capabilities.NewEffectiveSet()
	// Explicit agentbox capabilities. A writable workdir is the explicit
	// fs.workdir.write capability (create/run add it by default; --read-only
	// omits it); an empty manifest is locked down by construction.
	for _, name := range c.Capabilities {
		if err := set.Add(name, capabilities.OriginExplicit); err != nil {
			return nil, err
		}
	}
	// Feature-required capabilities, over the effective feature set (the box's
	// features ∪ the agent profile's required features, deduped).
	for _, name := range effectiveFeatures(c) {
		f, err := features.Lookup(name)
		if err != nil {
			return nil, err
		}
		for _, capName := range f.RequiresCapabilities {
			if capName == capabilities.RetiredPrivilegedCapability {
				return nil, fmt.Errorf("feature %q requires the retired %s capability, and features no longer select a privileged box: remove that requirement from the feature and set privileged: true on the box (--privileged)", name, capName)
			}
			if err := set.Add(capName, capabilities.OriginFeature); err != nil {
				return nil, fmt.Errorf("feature %q requires %w", name, err)
			}
		}
	}
	return set, nil
}

// EffectiveFeaturesStrict returns the box's declared features unioned with the
// selected agent profile's RequiresFeatures, de-duplicated and order-stable
// (box features first, then any profile features not already present).
//
// STRICT: if the box has a real agent whose profile cannot be resolved, it
// returns an error (with the box features collected so far). This is the
// variant the BUILD / APPLY / runtime-image paths MUST use — silently dropping
// the agent's CLI feature (claude/codex) there would produce a runnable
// container with no agent installed. Use the lenient effectiveFeatures only on
// read-only list/show/diff paths, where a missing profile must not break
// policy composition for an already-running box.
func EffectiveFeaturesStrict(c *Config) ([]string, error) {
	seen := make(map[string]struct{}, len(c.Features))
	out := make([]string, 0, len(c.Features))
	add := func(f string) {
		if f == "" {
			return
		}
		if _, dup := seen[f]; dup {
			return
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	for _, f := range c.Features {
		add(f)
	}
	// A generic-shell box (Agent "none"/"") has no agent profile to pull
	// RequiresFeatures from — HasAgent() guards the lookup so "none" never
	// triggers a (failing) agentprofile.Lookup("none").
	if c.HasAgent() {
		p, err := agentprofile.Lookup(c.Agent)
		if err != nil {
			return out, fmt.Errorf("resolve agent %q profile for effective features: %w", c.Agent, err)
		}
		for _, f := range p.RequiresFeatures {
			add(f)
		}
	}
	return out, nil
}

// effectiveFeatures is the LENIENT view: the agent-profile Lookup is non-fatal
// (called from list/show/diff), so a missing or edited profile returns the
// box's own features rather than an error. Build/apply paths use
// EffectiveFeaturesStrict instead. Feature lookup itself stays fatal (in the
// caller's capability resolution).
func effectiveFeatures(c *Config) []string {
	out, _ := EffectiveFeaturesStrict(c)
	return out
}

// compilePolicy lowers an effective capability set into the on-disk policy
// document. Pure over the capability set: workdir read-write placement keys
// off the fs.workdir.write grant, network_policies come from the network
// grants, and the filesystem baseline is the always-on confinement layout.
//
// Each declared extra mount gets the Landlock access its bind promises
// (read_only, or read_write unless ReadOnly), so a --mount outside the
// baseline paths is usable; a mount already covered by an equal or broader
// entry (the account-state slot under /home/sandbox, for example) adds
// nothing, which keeps those boxes' policy bytes unchanged.
func compilePolicy(eff *capabilities.EffectiveSet, mounts []Mount) ([]byte, error) {
	doc := policyDoc{
		Version:          1,
		FilesystemPolicy: baselineFilesystem(),
		Landlock:         landlockBlock{Compatibility: "best_effort"},
		Process:          processBlock{RunAsUser: "sandbox", RunAsGroup: "sandbox"},
		NetworkPolicies:  map[string]networkPolicy{},
	}
	// /workdir placement: read-write when the workdir-write capability is
	// granted, read-only otherwise. Appended last, matching the historical
	// hardening-baseline ordering so the golden output is unchanged.
	if eff.Has(capabilities.WorkdirWriteCapability) {
		doc.FilesystemPolicy.ReadWrite = append(doc.FilesystemPolicy.ReadWrite, workdirPath)
	} else {
		// OpenShell 0.1.6 adds the actual cwd to Landlock's read_write set
		// when include_workdir is true. The read-only bind and explicit
		// /workdir read_only grant must not gain that implicit write grant.
		doc.FilesystemPolicy.IncludeWorkdir = false
		doc.FilesystemPolicy.ReadOnly = append(doc.FilesystemPolicy.ReadOnly, workdirPath)
	}
	for _, capab := range eff.Capabilities() {
		for _, g := range capab.Network {
			for _, e := range g.Endpoints {
				if err := lintHostPattern(e.Host); err != nil {
					return nil, fmt.Errorf("capability %q: %w", capab.Name, err)
				}
			}
			if _, dup := doc.NetworkPolicies[g.PolicyName]; dup {
				return nil, fmt.Errorf("capability %q: network policy name %q collides with another grant", capab.Name, g.PolicyName)
			}
			doc.NetworkPolicies[g.PolicyName] = toNetworkPolicyFromGrant(g)
		}
		for _, g := range capab.Filesystem {
			if g.Path == workdirPath {
				continue // workdir placement handled above via capability presence
			}
			if g.Access == capabilities.ReadWrite {
				doc.FilesystemPolicy.ReadWrite = append(doc.FilesystemPolicy.ReadWrite, g.Path)
			} else {
				doc.FilesystemPolicy.ReadOnly = append(doc.FilesystemPolicy.ReadOnly, g.Path)
			}
		}
	}
	for _, m := range mounts {
		target := path.Clean(m.Target)
		if !path.IsAbs(target) {
			return nil, fmt.Errorf("extra mount target %q is not an absolute path", m.Target)
		}
		covered := policyCovers(doc.FilesystemPolicy.ReadWrite, target)
		if m.ReadOnly {
			covered = covered || policyCovers(doc.FilesystemPolicy.ReadOnly, target)
		}
		if covered {
			continue
		}
		if m.ReadOnly {
			doc.FilesystemPolicy.ReadOnly = append(doc.FilesystemPolicy.ReadOnly, target)
		} else {
			doc.FilesystemPolicy.ReadWrite = append(doc.FilesystemPolicy.ReadWrite, target)
		}
	}
	if err := lintPathOverlap(doc.FilesystemPolicy.ReadWrite); err != nil {
		return nil, err
	}
	if err := lintReadWriteDevSymlink(doc.FilesystemPolicy.ReadWrite); err != nil {
		return nil, err
	}
	return yaml.Marshal(doc)
}

// devSymlinks are the /dev entries an OCI runtime (runc, which nerdctl drives
// in the space VM) creates as SYMLINKS rather than device nodes: setupPtmx
// links /dev/ptmx -> pts/ptmx and setupDevSymlinks links the /proc/self/fd
// aliases (plus /dev/core when /proc/kcore exists).
var devSymlinks = []string{
	"/dev/ptmx",
	"/dev/fd", "/dev/stdin", "/dev/stdout", "/dev/stderr",
	"/dev/core",
}

// lintReadWriteDevSymlink rejects a read_write entry that is a known /dev
// symlink. openshell-sandbox v0.0.72 prepare_read_write_path
// (crates/openshell-supervisor-process/src/process.rs) lstat()s every
// read_write path at startup and ABORTS when one is a symlink ("refusing to
// chown"), so the supervisor never opens its SSH socket and the box never
// becomes ready (upstream NVIDIA/OpenShell#749). /dev/ptmx is the trap that
// matters: granting PTY allocation is done with the /dev/pts directory alone —
// open("/dev/ptmx") resolves through the symlink into that devpts mount, which
// the directory rule covers. Like lintHostPattern, this turns a silent
// sandbox-startup failure into a fail-fast compose error.
//
// Pure function — no filesystem access (the paths live inside the container).
func lintReadWriteDevSymlink(readWrite []string) error {
	for _, rw := range readWrite {
		for _, link := range devSymlinks {
			if rw == link {
				return fmt.Errorf("composed policy has read_write %q, which is a symlink inside the box container: openshell-sandbox refuses symlinked read_write paths and would fail to start; grant the symlink's target directory instead (PTY allocation needs only /dev/pts)", rw)
			}
		}
	}
	return nil
}

// pathDirs is the set of "binary lookup directories" the lint
// flags. Anything on $PATH that the agent might exec — moving any
// of these into read_write defeats TOFU because a malicious agent
// could plant a wrapper at /usr/bin/git (etc.) and have it cached
// as trusted on first invocation.
//
// Keep this list narrow: only directories whose entries are
// resolved by name lookup against $PATH or hard-coded into agent
// binaries. /etc gets its own protection elsewhere.
var pathDirs = []string{
	"/bin", "/sbin",
	"/usr/bin", "/usr/sbin",
	"/usr/local/bin", "/usr/local/sbin",
}

// lintPathOverlap returns an error when any entry in readWrite
// overlaps a known $PATH lookup directory. Overlap is defined as
// equality OR readWrite[i] being a prefix of (or equal to) any
// pathDirs entry — both forms grant write access to a PATH-binary
// location.
//
// Pure function — no filesystem access, no globals beyond pathDirs.
// Tested directly.
//
// Read-write / PATH overlap lint.
func lintPathOverlap(readWrite []string) error {
	for _, rw := range readWrite {
		for _, pd := range pathDirs {
			if rw == pd {
				return fmt.Errorf("composed policy has read_write %q which is a $PATH lookup directory: TOFU would cache any planted binary as trusted on first exec; move it to read_only or split out the writable subpath", rw)
			}
			if isAncestor(rw, pd) {
				return fmt.Errorf("composed policy has read_write %q contains $PATH lookup directory %q: TOFU would cache any planted binary as trusted on first exec; narrow the read_write to a subpath that does not include the PATH dir", rw, pd)
			}
		}
	}
	return nil
}

// lintHostPattern rejects a network endpoint host that openshell-sandbox's L7
// policy validator would reject — which makes the sandbox fail to start (the
// SSH socket never opens) instead of producing a clear error. It mirrors
// upstream validate_host_wildcard (NVIDIA/OpenShell v0.0.72,
// crates/openshell-supervisor-network/src/l7/mod.rs) byte-for-byte: a literal
// host is always fine; a wildcard host must be a per-domain pattern
// (`*.example.com` / `**.example.com`) — never match-all (`*`, `**`), never a
// wildcard outside the first DNS label, never a recursive `**` that is not the
// entire first label, and never a TLD/single-label wildcard (`*.com`). Running
// this at compose time turns a 30s sandbox-startup crash into a fail-fast error
// naming the offending capability.
func lintHostPattern(host string) error {
	if !strings.Contains(host, "*") {
		return nil // literal host — always valid
	}
	if host == "*" || host == "**" {
		return fmt.Errorf("network host %q is a match-all wildcard, which openshell rejects; use a per-domain pattern like *.example.com", host)
	}
	labels := strings.Split(host, ".")
	for _, label := range labels[1:] {
		if strings.Contains(label, "*") {
			return fmt.Errorf("network host %q has a wildcard outside the first DNS label, which openshell rejects; the wildcard may only be the first label (e.g. *.example.com)", host)
		}
	}
	if first := labels[0]; strings.Contains(first, "**") && first != "**" {
		return fmt.Errorf("network host %q has a recursive ** that is not the entire first DNS label, which openshell rejects (e.g. **.example.com)", host)
	}
	if len(labels) <= 2 {
		return fmt.Errorf("network host %q is a TLD/single-label wildcard, which openshell rejects; use a subdomain wildcard like *.example.com", host)
	}
	return nil
}

// isAncestor reports whether parent is an ancestor of (or equal to)
// child in a unix path sense. Returns false when parent == child
// because the equality case is handled separately (better error
// message); reserved for the strict ancestor relation.
// policyCovers reports whether target is one of paths or lies beneath one.
func policyCovers(paths []string, target string) bool {
	for _, p := range paths {
		if p == target || isAncestor(p, target) {
			return true
		}
	}
	return false
}

func isAncestor(parent, child string) bool {
	if parent == child {
		return false
	}
	// "/" is ancestor of everything.
	if parent == "/" {
		return true
	}
	// Normalize "/foo" → "/foo/" then check child starts with it.
	if parent[len(parent)-1] != '/' {
		parent += "/"
	}
	return len(child) >= len(parent) && child[:len(parent)] == parent
}

// baselineFilesystem is the always-on filesystem layout shared by every
// agentbox: the sandbox confinement baseline (a hard invariant, NOT a
// capability). /workdir is deliberately absent here — its read-only vs
// read-write placement is decided by the compiler from the
// fs.workdir.write capability.
//
// Mirrors what the presets in assets/sandbox/presets/ agree on: system paths
// read-only (so the agent can't tamper), /tmp + /run + /dev/null + /dev/zero
// always writable (the tool ecosystem expects them), /home/sandbox always
// writable (every tool's cache + config lives there).
//
// No terminal device is in the baseline: /dev/pts, /dev/ptmx and /dev/tty are
// all denied by Landlock unless the box opts into fs.pty.allocate. The managed
// agent session still gets a terminal because openshell-sandbox allocates that
// PTY before enforcing Landlock; only NEW allocations from inside are denied.
func baselineFilesystem() filesystemPolicy {
	return filesystemPolicy{
		IncludeWorkdir: true,
		ReadOnly: []string{
			"/usr", "/lib", "/lib64", "/bin", "/sbin",
			"/proc",
			"/dev/urandom", "/dev/random",
			"/etc", "/app", "/var/log",
		},
		ReadWrite: []string{
			"/tmp", "/home/sandbox", "/run",
			"/dev/null", "/dev/zero",
		},
	}
}

// toNetworkPolicyFromGrant lowers a capabilities.NetworkGrant into the on-disk
// YAML shape. The two types diverge on field types (capabilities use
// human-friendly slices, the YAML uses tagged structs) so they evolve
// independently.
func toNetworkPolicyFromGrant(g capabilities.NetworkGrant) networkPolicy {
	endpoints := make([]endpointSpec, 0, len(g.Endpoints))
	for _, e := range g.Endpoints {
		endpoint := endpointSpec{Host: e.Host, Ports: e.Ports}
		if e.AllowEncodedSlash {
			// OpenShell's default HTTP parser rejects %2F. The opt-in is
			// honored only on a REST endpoint; preserve the existing host/port
			// grant with full HTTP access and the same binary attribution.
			endpoint.Protocol = "rest"
			endpoint.Enforcement = "enforce"
			endpoint.Access = "full"
			endpoint.AllowEncodedSlash = true
		}
		endpoints = append(endpoints, endpoint)
	}
	binaries := make([]binarySpec, 0, len(g.Binaries))
	for _, b := range g.Binaries {
		binaries = append(binaries, binarySpec{Path: b})
	}
	return networkPolicy{
		Name:      g.PolicyName,
		Endpoints: endpoints,
		Binaries:  binaries,
	}
}

// On-disk policy.yaml shape. Field names + casing match openshell-
// sandbox's rego data expectations and the existing
// assets/sandbox/presets/*.yaml.
type policyDoc struct {
	Version          int                      `yaml:"version"`
	FilesystemPolicy filesystemPolicy         `yaml:"filesystem_policy"`
	Landlock         landlockBlock            `yaml:"landlock"`
	Process          processBlock             `yaml:"process"`
	NetworkPolicies  map[string]networkPolicy `yaml:"network_policies"`
}

type filesystemPolicy struct {
	IncludeWorkdir bool     `yaml:"include_workdir"`
	ReadOnly       []string `yaml:"read_only"`
	ReadWrite      []string `yaml:"read_write"`
}

type landlockBlock struct {
	Compatibility string `yaml:"compatibility"`
}

type processBlock struct {
	RunAsUser  string `yaml:"run_as_user"`
	RunAsGroup string `yaml:"run_as_group"`
}

type networkPolicy struct {
	Name      string         `yaml:"name"`
	Endpoints []endpointSpec `yaml:"endpoints"`
	Binaries  []binarySpec   `yaml:"binaries"`
}

// endpointSpec is the on-disk policy endpoint shape. Ports MUST be a list:
// the bundled rego matches endpoint.ports[_] (assets/sandbox/sandbox-policy.rego),
// so emitting a singular scalar `port` makes every egress grant fail closed.
type endpointSpec struct {
	Host              string `yaml:"host"`
	Ports             []int  `yaml:"ports"`
	Protocol          string `yaml:"protocol,omitempty"`
	Enforcement       string `yaml:"enforcement,omitempty"`
	Access            string `yaml:"access,omitempty"`
	AllowEncodedSlash bool   `yaml:"allow_encoded_slash,omitempty"`
}

type binarySpec struct {
	Path string `yaml:"path"`
}

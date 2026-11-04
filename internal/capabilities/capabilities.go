// Package capabilities is the positive-grant access model from which yoloctl
// compiles OpenShell policy.
//
// A capability is an ADDITIVE positive grant: network egress and/or filesystem
// access. There are no deny fields, no posture, no max-capabilities cap. The
// default is locked down; composition is set union, so the less restrictive
// effective grant wins because no negative grants exist. Hard invariants (VM
// boundary, space mount grants, yoloctl internal paths, credential isolation,
// host path validation, the sandbox baseline) are enforced elsewhere and
// cannot be widened by a capability. A privileged box, which replaces the whole
// OpenShell sandbox, is the box's own privileged setting, not a capability; the
// retired yolo.dev.privileged name (RetiredPrivilegedCapability) resolves to
// nothing and only explains that.
//
// The built-in registry below is the single source of truth for the network
// grants that used to live inline on features (internal/features). Each
// network grant keeps its original openshell `network_policies` map key
// (NetworkGrant.PolicyName) so the compiled policy is byte-identical to the
// pre-capability output — the Phase 1 golden contract
// (internal/agentbox/testdata/policy_golden) is what guarantees this.
package capabilities

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/nativerelease"
	"github.com/leogr/yoloctl/internal/operator"
)

// Access is a filesystem grant mode. Positive only — there is no "deny".
type Access string

const (
	ReadOnly  Access = "read_only"
	ReadWrite Access = "read_write"
)

// Endpoint is one allowed host plus the ports it is reachable on. Host may be
// a glob ("*.anthropic.com", "**"). Ports is a list: the generated policy and
// the bundled OpenShell rego match on endpoint.ports[_] (see
// assets/sandbox/sandbox-policy.rego) — a singular scalar never matches and
// silently fails egress closed.
type Endpoint struct {
	Host  string `yaml:"host"`
	Ports []int  `yaml:"ports"`
	// AllowEncodedSlash permits percent-encoded slashes in HTTP URL paths,
	// needed by scoped npm package names. Other endpoints retain strict parsing.
	AllowEncodedSlash bool `yaml:"allow_encoded_slash,omitempty"`
}

// NetworkGrant is one named egress allowance restricted to specific binaries.
// PolicyName is the openshell `network_policies` map key it compiles to and is
// part of the on-disk policy contract — do not rename casually.
type NetworkGrant struct {
	PolicyName string     `yaml:"policy_name"`
	Endpoints  []Endpoint `yaml:"endpoints"`
	// Binaries are absolute paths permitted to use Endpoints. Per-binary
	// attribution is the anti-exfil property: a grant names exactly which
	// binaries may reach its endpoints — never a match-all wildcard.
	Binaries []string `yaml:"binaries,omitempty"`
}

// FSGrant is one filesystem subtree grant.
type FSGrant struct {
	Path   string `yaml:"path"`
	Access Access `yaml:"access"`
}

// Capability is a named positive grant preset.
type Capability struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Network     []NetworkGrant `yaml:"network,omitempty"`
	Filesystem  []FSGrant      `yaml:"filesystem,omitempty"`
}

// WorkdirWriteCapability is the capability whose presence grants /workdir
// read-write. Its absence leaves the workdir read-only (the positive-grant
// replacement for the old read_only posture flag).
const WorkdirWriteCapability = "fs.workdir.write"

// PTYAllocateCapability is the opt-in capability that lets sandboxed processes
// allocate NEW pseudo-terminals and open their controlling terminal. Without
// it the Landlock allow-list covers neither /dev/pts nor /dev/tty, so
// open("/dev/ptmx"), openpty(3), tmux, script(1), expect, PTY-backed test
// suites (creack/pty, pexpect, node-pty) and /dev/tty prompts (git, ssh) all
// fail with EACCES even though DAC allows them. The managed agent session is
// unaffected either way: openshell-sandbox allocates that PTY as root BEFORE
// it enforces Landlock and the child only inherits the slave descriptors.
const PTYAllocateCapability = "fs.pty.allocate"

// ptyGrants is the exact path set PTYAllocateCapability lowers to. It is
// deliberately narrow and deliberately NOT "/dev":
//
//   - /dev/pts is the box container's PRIVATE devpts instance (runc mounts it
//     with newinstance,ptmxmode=0666,mode=0620,gid=5). A read_write rule on
//     that directory covers the ptmx multiplexer inside it and every slave
//     node the sandbox user allocates. Session PTYs allocated by the root
//     supervisor stay root:tty 0620, so DAC still keeps the sandbox user from
//     opening them by path.
//   - /dev/tty is the caller's OWN controlling terminal; opening it yields a
//     terminal the process could already reach through its inherited stdio.
//
// /dev/ptmx MUST NOT be listed: in a runc container it is a symlink to
// pts/ptmx, and openshell-sandbox (v0.0.72 prepare_read_write_path) refuses to
// start when a read_write path is a symlink. The open(2) of /dev/ptmx resolves
// into /dev/pts, which the directory rule already covers. The composer lints
// this (internal/agentbox lintReadWriteDevSymlink).
var ptyGrants = []FSGrant{
	{Path: "/dev/pts", Access: ReadWrite},
	{Path: "/dev/tty", Access: ReadWrite},
}

// PTYGrants returns a copy of the filesystem grants PTYAllocateCapability (and
// a custom grants.pty.allocate) lowers to.
func PTYGrants() []FSGrant {
	return append([]FSGrant(nil), ptyGrants...)
}

// RetiredPrivilegedCapability is the capability name that selected a
// privileged box before privilege became the box's explicit privileged
// setting. It is reserved: no built-in or custom capability has it, so a
// feature or grant can never select privilege, and a box declaration that
// lists it fails validation (internal/agentbox).
const RetiredPrivilegedCapability = "yolo.dev.privileged"

// ErrRetiredPrivileged explains a reference to RetiredPrivilegedCapability.
var ErrRetiredPrivileged = fmt.Errorf("capability %q is retired: a privileged box is the box setting privileged: true (--privileged), and no capability or feature selects it", RetiredPrivilegedCapability)

// registry holds the built-in capabilities. Network grants mirror the
// pre-capability feature network policies exactly (endpoints + binaries +
// policy name) so the compiled policy is unchanged.
var registry = map[string]Capability{
	operator.Capability: {
		Name:        operator.Capability,
		Description: "Operate every box in this space, including other accounts, stronger capabilities, delegation and destruction",
		Filesystem:  []FSGrant{{Path: operator.HelperMount, Access: ReadOnly}, {Path: operator.Mount, Access: ReadWrite}},
	},
	"network.anthropic": {
		Name:        "network.anthropic",
		Description: "Anthropic API + Claude login hosts, restricted to the claude binary",
		Network: []NetworkGrant{{
			PolicyName: "anthropic",
			Endpoints: []Endpoint{
				{Host: "api.anthropic.com", Ports: []int{443}},
				{Host: "*.anthropic.com", Ports: []int{443}},
				{Host: "claude.com", Ports: []int{443}},
				{Host: "*.claude.com", Ports: []int{443}},
				{Host: "claude.ai", Ports: []int{443}},
				{Host: "*.claude.ai", Ports: []int{443}},
			},
			Binaries: []string{
				"/usr/bin/node",
				"/home/sandbox/.npm-global/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe",
			},
		}},
	},
	"network.openai": {
		Name:        "network.openai",
		Description: "OpenAI API + ChatGPT OAuth hosts, restricted to Node and native Codex",
		Network: []NetworkGrant{{
			PolicyName: "openai",
			Endpoints: []Endpoint{
				{Host: "api.openai.com", Ports: []int{443}},
				{Host: "*.openai.com", Ports: []int{443}},
				{Host: "chatgpt.com", Ports: []int{443}},
				{Host: "*.chatgpt.com", Ports: []int{443}},
			},
			// Native Codex and its detached daemon have no Node ancestor.
			// Bound the daemon wildcard to one version/target directory so
			// @latest updates retain egress without granting the whole cache.
			Binaries: []string{
				"/usr/bin/node",
				"/home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex",
				"/home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-arm64/vendor/aarch64-unknown-linux-musl/bin/codex",
				"/home/sandbox/.codex/packages/app-server-daemon/releases/*-x86_64-unknown-linux-musl/bin/codex",
				"/home/sandbox/.codex/packages/app-server-daemon/releases/*-aarch64-unknown-linux-musl/bin/codex",
			},
		}},
	},
	"network.agy": {
		Name:        "network.agy",
		Description: "Antigravity CLI Google OAuth and model API egress, restricted to its native executable",
		Network: []NetworkGrant{{
			PolicyName: "agy",
			Endpoints: []Endpoint{
				// Native language-server feature configuration (observed in 1.2.17).
				{Host: "antigravity-unleash.goog", Ports: []int{443}},
				{Host: "daily-cloudcode-pa.googleapis.com", Ports: []int{443}},
				{Host: "cloudcode-pa.googleapis.com", Ports: []int{443}},
				{Host: "generativelanguage.googleapis.com", Ports: []int{443}},
				{Host: "oauth2.googleapis.com", Ports: []int{443}},
				// Google userinfo lookup; no general Google API grant to other tools.
				{Host: "www.googleapis.com", Ports: []int{443}},
			},
			Binaries: []string{nativerelease.AgyBinary},
		}},
	},
	"network.gemini": {
		Name:        "network.gemini",
		Description: "Gemini API and Google OAuth/Code Assist egress, restricted to node",
		Network: []NetworkGrant{{
			PolicyName: "gemini",
			Endpoints: []Endpoint{
				{Host: "generativelanguage.googleapis.com", Ports: []int{443}},
				{Host: "cloudcode-pa.googleapis.com", Ports: []int{443}},
				{Host: "oauth2.googleapis.com", Ports: []int{443}},
				// Gemini's Google login fetches /oauth2/v2/userinfo here.
				{Host: "www.googleapis.com", Ports: []int{443}},
			},
			Binaries: []string{"/usr/bin/node"},
		}},
	},
	"network.opencode": {
		Name:        "network.opencode",
		Description: "OpenCode provider APIs, ChatGPT OAuth and npm plugins, restricted to the native executable",
		Network: []NetworkGrant{{
			PolicyName: "opencode",
			Endpoints: []Endpoint{
				{Host: "registry.npmjs.org", Ports: []int{443}, AllowEncodedSlash: true},
				{Host: "opencode.ai", Ports: []int{443}},
				{Host: "api.anthropic.com", Ports: []int{443}},
				{Host: "api.openai.com", Ports: []int{443}},
				{Host: "auth.openai.com", Ports: []int{443}},
				{Host: "chatgpt.com", Ports: []int{443}},
				{Host: "generativelanguage.googleapis.com", Ports: []int{443}},
				{Host: "openrouter.ai", Ports: []int{443}},
			},
			Binaries: []string{"/home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe"},
		}},
	},
	"network.pi": {
		Name:        "network.pi",
		Description: "Pi Anthropic, OpenAI/ChatGPT, Gemini API and OpenRouter egress, restricted to node",
		Network: []NetworkGrant{{
			PolicyName: "pi",
			Endpoints: []Endpoint{
				{Host: "api.anthropic.com", Ports: []int{443}},
				// Anthropic OAuth exchange and refresh, not browser navigation.
				{Host: "platform.claude.com", Ports: []int{443}},
				{Host: "api.openai.com", Ports: []int{443}},
				{Host: "auth.openai.com", Ports: []int{443}},
				// Pi also retains its legacy openai-codex subscription provider.
				{Host: "chatgpt.com", Ports: []int{443}},
				{Host: "generativelanguage.googleapis.com", Ports: []int{443}},
				{Host: "openrouter.ai", Ports: []int{443}},
			},
			Binaries: []string{"/usr/bin/node"},
		}},
	},
	"network.github": {
		Name:        "network.github",
		Description: "GitHub egress restricted to git + gh",
		Network: []NetworkGrant{{
			PolicyName: "github",
			Endpoints: []Endpoint{
				{Host: "github.com", Ports: []int{443}},
				{Host: "*.github.com", Ports: []int{443}},
				{Host: "*.githubusercontent.com", Ports: []int{443}},
			},
			Binaries: []string{"/usr/bin/git", "/usr/bin/gh"},
		}},
	},
	"network.go-modules": {
		Name:        "network.go-modules",
		Description: "Go module proxy + checksum DB, restricted to go",
		Network: []NetworkGrant{{
			PolicyName: "go-modules",
			Endpoints: []Endpoint{
				{Host: "proxy.golang.org", Ports: []int{443}},
				{Host: "sum.golang.org", Ports: []int{443}},
			},
			// OpenShell attributes egress to the resolved executable, so this
			// names the go feature's real binary, never its PATH symlink.
			Binaries: []string{"/usr/local/go/bin/go"},
		}},
	},
	"network.npm": {
		Name:        "network.npm",
		Description: "npm registry egress",
		Network: []NetworkGrant{{
			PolicyName: "npm",
			Endpoints:  []Endpoint{{Host: "registry.npmjs.org", Ports: []int{443}}},
			Binaries: []string{
				"/usr/bin/node", "/usr/bin/npm", "/usr/bin/npx", "/usr/bin/pnpm", "/usr/bin/yarn",
			},
		}},
	},
	"network.pypi": {
		Name:        "network.pypi",
		Description: "PyPI + pythonhosted egress",
		Network: []NetworkGrant{{
			PolicyName: "pypi",
			Endpoints: []Endpoint{
				{Host: "pypi.org", Ports: []int{443}},
				{Host: "*.pypi.org", Ports: []int{443}},
				{Host: "files.pythonhosted.org", Ports: []int{443}},
			},
			Binaries: []string{
				"/usr/bin/python3.13", "/usr/bin/pip", "/usr/bin/pip3", "/usr/bin/python3", "/usr/local/bin/uv",
			},
		}},
	},
	"network.cargo": {
		Name:        "network.cargo",
		Description: "crates.io egress, restricted to cargo",
		Network: []NetworkGrant{{
			PolicyName: "cargo",
			Endpoints: []Endpoint{
				{Host: "crates.io", Ports: []int{443}},
				{Host: "static.crates.io", Ports: []int{443}},
				{Host: "index.crates.io", Ports: []int{443}},
			},
			Binaries: []string{"/usr/bin/cargo"},
		}},
	},
	WorkdirWriteCapability: {
		Name:        WorkdirWriteCapability,
		Description: "Read-write access to the agentbox workdir (/workdir)",
		Filesystem:  []FSGrant{{Path: "/workdir", Access: ReadWrite}},
	},
	PTYAllocateCapability: {
		Name: PTYAllocateCapability,
		Description: "Allocate pseudo-terminals in the box's private /dev/pts (tmux, script, expect, " +
			"PTY-backed test suites) and open the controlling terminal /dev/tty (git/ssh prompts)",
		Filesystem: PTYGrants(),
	},
}

// IsBuiltin reports whether name is one of the embedded built-in capabilities.
// Built-ins are yoloctl-owned and read-only; the catalog commands use this to
// reject editing/destroying a built-in and to reject a custom item that would
// shadow one (same-name overrides of built-ins are not allowed).
func IsBuiltin(name string) bool {
	_, ok := registry[name]
	return ok
}

// Lookup returns the capability with the given name. A custom capability under
// layout.CapabilitiesDir() resolves first, then the built-in registry. Custom
// items may NOT shadow a built-in name (LoadUser errors on a collision), so the
// two namespaces are disjoint and this ordering only matters for the disjoint
// fast path. Returns an error naming the known capabilities when name resolves
// to neither, and ErrRetiredPrivileged for RetiredPrivilegedCapability.
func Lookup(name string) (Capability, error) {
	if name == RetiredPrivilegedCapability {
		return Capability{}, ErrRetiredPrivileged
	}
	user, err := LoadUser()
	if err != nil {
		return Capability{}, fmt.Errorf("load user capabilities: %w", err)
	}
	if c, ok := user[name]; ok {
		return c, nil
	}
	if c, ok := registry[name]; ok {
		return c, nil
	}
	return Capability{}, fmt.Errorf("unknown capability %q (known: %s)", name, strings.Join(Names(), ", "))
}

// BuiltinNames returns the sorted names of the embedded built-in capabilities
// only (no user-defined items). Used by the catalog `ls` command to mark which
// items are built-in.
func BuiltinNames() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Names returns every known capability name (built-in + user), sorted.
func Names() []string {
	user, _ := LoadUser()
	seen := make(map[string]struct{}, len(registry)+len(user))
	out := make([]string, 0, len(registry)+len(user))
	for n := range user {
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for n := range registry {
		if _, dup := seen[n]; dup {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Origin records why a capability is in the effective set. A capability may
// have more than one origin (e.g. both the profile and a feature require it).
type Origin string

const (
	OriginExplicit Origin = "explicit" // declared directly on the agentbox manifest
	OriginFeature  Origin = "feature"  // required by a selected feature (incl. an agent profile's required features)
)

// EffectiveCapability is a resolved capability plus the origins that pulled it
// into the effective set, for origin-aware diff/inspect output.
type EffectiveCapability struct {
	Capability Capability
	Origins    []Origin
}

// EffectiveSet accumulates capabilities by name with origin tracking. The
// composition is a pure positive union: adding the same capability twice (from
// different origins) records both origins but grants it once.
type EffectiveSet struct {
	order   []string
	byName  map[string]Capability
	origins map[string][]Origin
}

func NewEffectiveSet() *EffectiveSet {
	return &EffectiveSet{byName: map[string]Capability{}, origins: map[string][]Origin{}}
}

// Add resolves name and records origin. Idempotent per (name): the grant is
// added once; origins accumulate (deduped).
func (s *EffectiveSet) Add(name string, origin Origin) error {
	c, err := Lookup(name)
	if err != nil {
		return err
	}
	if _, present := s.byName[name]; !present {
		s.order = append(s.order, name)
		s.byName[name] = c
	}
	if slices.Contains(s.origins[name], origin) {
		return nil
	}
	s.origins[name] = append(s.origins[name], origin)
	return nil
}

// Has reports whether the named capability is in the effective set.
func (s *EffectiveSet) Has(name string) bool {
	_, ok := s.byName[name]
	return ok
}

// Capabilities returns the effective capabilities in insertion order.
func (s *EffectiveSet) Capabilities() []Capability {
	out := make([]Capability, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.byName[n])
	}
	return out
}

// Effective returns the effective capabilities with their origins, sorted by
// name for deterministic output.
func (s *EffectiveSet) Effective() []EffectiveCapability {
	names := append([]string(nil), s.order...)
	sort.Strings(names)
	out := make([]EffectiveCapability, 0, len(names))
	for _, n := range names {
		out = append(out, EffectiveCapability{Capability: s.byName[n], Origins: s.origins[n]})
	}
	return out
}

// LoadUser (custom capability discovery + high-level grants→internal
// translation) lives in user.go.

// Validate checks a capability's invariants: a name, and only positive grants.
func (c Capability) Validate() error {
	if err := layout.ValidateName("capability", c.Name); err != nil {
		return err
	}
	for i, g := range c.Network {
		if g.PolicyName == "" {
			return fmt.Errorf("capability %q: network[%d].policy_name is required", c.Name, i)
		}
		if len(g.Endpoints) == 0 {
			return fmt.Errorf("capability %q: network[%d] has no endpoints", c.Name, i)
		}
		for j, e := range g.Endpoints {
			if len(e.Ports) == 0 {
				return fmt.Errorf("capability %q: network[%d].endpoints[%d] (host %q) has no ports; the rego matches endpoint.ports[_] so an endpoint with no ports can never match", c.Name, i, j, e.Host)
			}
		}
	}
	for i, g := range c.Filesystem {
		switch g.Access {
		case ReadOnly, ReadWrite:
		default:
			return fmt.Errorf("capability %q: filesystem[%d].access %q is invalid (capabilities are positive grants: %s|%s, never deny)",
				c.Name, i, g.Access, ReadOnly, ReadWrite)
		}
		if g.Path == "" {
			return fmt.Errorf("capability %q: filesystem[%d].path is required", c.Name, i)
		}
	}
	return nil
}

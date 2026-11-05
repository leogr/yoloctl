// Package agentbox manages the per-agentbox declared desired-state YAML
// at ~/.config/yoloctl/spaces/<space>/boxes/<name>.yaml: a typed struct,
// strict loading, atomic saving, file-scope helpers, and the reconciler that
// applies declared state to live container, policy, and port-forward state.
//
// Config defines the schema; Diff computes drift and Reconcile applies it.
package agentbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/manifestio"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

// SchemaVersion is the on-disk schema version. Every saved file has
// `schema_version: 1`. Open rejects any other version (yoloctl is
// pre-release — there is no prior on-disk format to migrate from).
const SchemaVersion = 1

const (
	fileMode os.FileMode = 0o644
	dirMode  os.FileMode = 0o700
	ext                  = ".yaml"
)

// Config is one agentbox's declared desired state — the editable manifest.
// Source of truth for everything that should survive container restarts:
// image, agent, account name, features, positive-grant capabilities, workdir,
// port forwards, env (non-secret literal values only), resources, extra bind
// mounts. The box does NOT own secret values: supported environment
// credentials come from its account through agent-declared secret_injection
// rules, while native credentials remain agent-owned in the account-state
// leaf. Live state (container labels, systemd unit list, iptables rules, the
// bind-mounted policy.yaml) is reconciled FROM this — not the other way around.
//
// The openshell-sandbox policy is a COMPUTED artifact, not a manifest field:
// it is derived from the effective capability set (explicit +
// feature-required) by the compiler in compose.go. There is no user-editable
// policy_file, posture (read_only/no_egress), or ad-hoc allow list — those
// are all expressed as capabilities now.
type Config struct {
	// path is the absolute path of the YAML file backing this config.
	// Empty for in-memory configs not yet saved.
	path string
	// chat is a native-controller projection refreshed during policy/runtime
	// preparation, never decoded from or written to the declaration.
	chat *managedchat.Projection

	SchemaVersion int    `yaml:"schema_version"`
	Name          string `yaml:"name"`
	Space         string `yaml:"space"`
	// ID is a controller-preserved declaration identity scoped by the native disk
	// ID, independent of names, accounts and replaceable container incarnations.
	// Chat enrollment mints it on demand; ordinary saves and box copy do not.
	ID string `yaml:"id,omitempty"`
	// Agent records which agent profile this agentbox hosts (e.g.
	// "claude", "codex"). Pinned at create time from the CLI's
	// --agent flag and propagated to the yoloctl.agent container
	// label. The lookup key for bulk operations like
	// `yoloctl agent update <profile>`, the destination of the
	// agent-state bind mount, and the scope
	// of agent-specific credential injection. Empty means "generic
	// agentbox" — no agent-specific behavior applies.
	Agent string `yaml:"agent,omitempty"`
	// Account is the plain NAME of the space-owned account this box selects
	// (e.g. "default", "personal"), or the sentinel "none". A real agent
	// REQUIRES a real account (DECISION B) — any real account serves any
	// agent; a generic-shell box (Agent "none"/"") pairs with Account "none"
	// (DECISION A). The box never owns secret values or native state — it
	// only references the account, and binds exactly its own agent's leaf
	// (account-state/<account>/<agent>/).
	Account string `yaml:"account"`
	Image   string `yaml:"image,omitempty"`

	// AgentExtraArgs are persistent, per-box arguments appended to the selected
	// agent profile's command line after profile.DefaultArgs whenever yoloctl
	// starts a managed agent session. They are box-owned runtime preference, not
	// agent adapter metadata. Separate commands launched by box exec do not
	// use them.
	// Non-secret only: argv is visible in process listings.
	AgentExtraArgs []string `yaml:"agent_extra_args,omitempty"`

	// AgentSession selects how yoloctl owns the agent's conversation identity
	// when its profile declares a session block. Nil leaves the policy unset:
	// manual when AgentExtraArgs carry an identity argument, otherwise last.
	AgentSession *AgentSession `yaml:"agent_session,omitempty"`

	// SessionCommand is the persisted argv for an agentless managed session.
	// Empty uses the login shell. Separate commands launched by box exec do not
	// change it. Arguments are non-secret and never shell-expanded.
	SessionCommand []string `yaml:"session_command,omitempty"`

	// Workdir is the explicit working-directory grant. A host workdir
	// (mode "host") is bound at its canonical path mirrored under
	// /workdir (Workdir.ContainerPath); VM-local ephemeral (mode
	// "ephemeral") binds at bare /workdir. Stored with the derived
	// canonical path so workdir grouping and the in-container path are
	// deterministic without consulting another config object.
	Workdir Workdir `yaml:"workdir"`

	Resources      Resources                    `yaml:"resources,omitempty"`
	Features       []string                     `yaml:"features,omitempty"`
	FeatureOptions map[string]map[string]string `yaml:"feature_options,omitempty"`
	FeatureLocks   []ocifeature.Lock            `yaml:"feature_locks,omitempty"`
	// Capabilities are the EXPLICIT positive grants on this agentbox
	// (internal/capabilities). The composer unions them with feature-required
	// capabilities (origin-tracked) to derive the openshell
	// policy. This is the positive-grant model replacing the
	// read_only/no_egress/allow posture fields: a writable workdir is the
	// fs.workdir.write capability (create grants it by default; --read-only
	// omits it), egress is network.* capabilities, and an ad-hoc allowance is
	// a user-defined capability.
	Capabilities []string `yaml:"capabilities,omitempty"`
	// Ports declares the host <-> container TCP/UDP forwards in the
	// Docker-Compose-style shape (target = box/container port, published =
	// host port; host_ip/protocol optional). See the Port type.
	Ports  []Port            `yaml:"ports,omitempty"`
	Env    map[string]EnvVar `yaml:"env,omitempty"`
	Mounts []Mount           `yaml:"mounts,omitempty"`

	// UnconfinedNetwork drops the openshell-sandbox network confinement layer
	// for this box: the sandbox runs with --mode=process, so NO inner netns and
	// NO L7 egress proxy are created and the box's network_policies go unused —
	// the agent inherits the container's ambient VM network with unrestricted
	// outbound egress. EVERY other confinement layer is intact (Landlock
	// filesystem rules, seccomp, drop-to-sandbox-user) and the embedded SSH
	// daemon still serves, so attach/exec are unaffected (sandbox.EntrypointArgs).
	//
	// This is an explicit, loud opt-in (--unconfined-network): it removes the
	// ONLY control over what data leaves the box, so a misaligned or
	// prompt-injected agent could exfiltrate to anywhere reachable from the VM.
	// The VM boundary still contains host escape; egress filtering does not.
	// Surfaced as a warning by box show / box inspect; a recreate-level field
	// (toggling it rebuilds the container). When set, any declared network.*
	// capabilities are moot — there is no proxy to enforce them.
	UnconfinedNetwork bool `yaml:"unconfined_network,omitempty"`

	// Privileged selects a privileged development box: a privileged container
	// served by yoloctl's own endpoint instead of openshell-sandbox, where the
	// sandbox user has sudo. That is space-VM root (PrivilegedAuthority);
	// composed policy is recorded for a later confined launch but not
	// enforced. Only this explicit setting selects it: capabilities and
	// features never do. A recreate-level field that only an explicit box
	// recreate switches (CheckRuntimeModeChange).
	Privileged bool `yaml:"privileged,omitempty"`
}

// AgentSession is a box's conversation-identity policy.
type AgentSession struct {
	// Resume is last (resume the box's conversation on every start), never
	// (a new conversation on every start) or manual (yoloctl assigns and
	// resumes nothing; agent_extra_args own identity).
	Resume string `yaml:"resume,omitempty"`
}

// Conversation resume policies a manifest may declare.
const (
	AgentSessionResumeLast   = "last"
	AgentSessionResumeNever  = "never"
	AgentSessionResumeManual = "manual"
)

// ResumePolicy returns the declared policy, or "" when it is unset.
func (c *Config) ResumePolicy() string {
	if c.AgentSession == nil {
		return ""
	}
	return c.AgentSession.Resume
}

// Resources holds per-agentbox kernel-resource limits forwarded to
// `nerdctl run --cpus / --memory`. Empty/shared memory borrows the VM workload
// pool without a per-box cap. Changing either field requires a
// container recreate; nerdctl HostConfig is create-only.
type Resources struct {
	CPUs   string `yaml:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// Port declares one host <-> container port forward in the
// Docker-Compose-style shape. Target is the box/container
// port and Published is the host port (Docker's host:container order maps to
// published:target). HostIP and Protocol are OPTIONAL in the manifest and are
// omitted when they hold their default (omitempty); the RESOLVED runtime fills
// them via ResolvedHostIP / ResolvedProtocol. Persisting forwards in the YAML
// (rather than only as transient systemd units) lets them survive restarts and
// policy swaps.
type Port struct {
	Target    int    `yaml:"target"`
	Published int    `yaml:"published"`
	HostIP    string `yaml:"host_ip,omitempty"`
	Protocol  string `yaml:"protocol,omitempty"`
}

// DefaultPortHostIP and DefaultPortProtocol are the resolved defaults applied
// when a Port omits HostIP / Protocol. The manifest stores the omitted form;
// box inspect and the nerdctl -p construction use the resolved form.
const (
	DefaultPortHostIP   = "127.0.0.1"
	DefaultPortProtocol = "tcp"
)

// ResolvedHostIP returns the port's host IP, defaulting to 127.0.0.1 when the
// manifest omits HostIP.
func (p Port) ResolvedHostIP() string {
	if p.HostIP == "" {
		return DefaultPortHostIP
	}
	return p.HostIP
}

// ResolvedProtocol returns the port's protocol, defaulting to "tcp" when the
// manifest omits Protocol.
func (p Port) ResolvedProtocol() string {
	if p.Protocol == "" {
		return DefaultPortProtocol
	}
	return p.Protocol
}

// Mount is one extra bind mount beyond the implicit /workdir.
// Use sparingly — extra mounts widen the agentbox's filesystem
// surface and force a container recreate when added or removed.
type Mount struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

// EnvVar holds a LITERAL environment-variable value. `env` is for
// non-secret literal configuration only: a box does NOT
// own secret values. Agent-declared secret_injection rules deliver supported
// account secrets, while native credentials remain agent-owned. The only
// accepted YAML form is a scalar:
//
//	KEY: "literal-value"
//
// The old `{ from_credential: NAME }` reference form is GONE (it pointed
// at the removed global credential store); UnmarshalYAML fails closed on
// it with a migration note.
type EnvVar struct {
	Literal string
}

// UnmarshalYAML accepts only a scalar (literal value). The removed
// `{ from_credential: NAME }` mapping form is rejected with a migration
// note so an old manifest fails CLOSED rather than silently losing its
// env.
func (e *EnvVar) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		e.Literal = node.Value
		return nil
	case yaml.MappingNode:
		return errors.New(
			"env: the `{from_credential: NAME}` reference form is no longer supported — " +
				"`env` is for non-secret literal values only; a box no longer owns secret values. " +
				"Move environment credentials into the box's account (`account secret set`); " +
				"native credentials belong in the account-state leaf. " +
				"See yoloctl account auth --help")
	default:
		return fmt.Errorf("env: expected a string (literal value)")
	}
}

// MarshalYAML emits the literal scalar form. `env` is literal-only.
func (e EnvVar) MarshalYAML() (any, error) {
	return e.Literal, nil
}

// Load reads the agentbox config with the given name from the given
// space's subdir. Returns fs.ErrNotExist if no file is there (caller
// decides whether to create or error).
func Load(space, name string) (*Config, error) {
	path, err := layout.BoxPath(space, name)
	if err != nil {
		return nil, err
	}
	raw, err := manifestio.Read(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	if c.Name != name || c.Space != space {
		return nil, fmt.Errorf("box manifest identity does not match %s/%s", space, name)
	}
	c.path = path
	return c, nil
}

// Open reads a box manifest from an explicit path. Useful for tests.
// It rejects any schema version other than the current one and does not call
// Validate.
func Open(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseManifest(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.path = path
	return c, nil
}

// ParseManifest accepts exactly one strict declaration, without resolving any
// filesystem path. Validation against catalogs and grants is a separate step.
func ParseManifest(raw []byte) (*Config, error) {
	c := &Config{}
	if err := strictyaml.Decode(raw, c); err != nil {
		return nil, err
	}
	if c.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("schema_version %d not supported (this build supports %d)", c.SchemaVersion, SchemaVersion)
	}
	return c, nil
}

// ListNames returns the names of all agentbox config files within
// the given space, sorted. Returns an empty slice (no error) if the
// dir does not exist.
func ListNames(space string) ([]string, error) {
	dir, err := layout.BoxesDir(space) // validates space
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ext) {
			continue
		}
		name = strings.TrimSuffix(name, ext)
		if layout.ValidateName("agentbox", name) != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// AgentboxRef is one agentbox's identity across spaces. Returned by
// ListAllAgentboxes for cross-space views (e.g. `box list
// --all-spaces`).
type AgentboxRef struct {
	Space string
	Name  string
}

// ListAllAgentboxes walks every space bundle under spaces/ and returns one
// AgentboxRef per agentbox YAML found, sorted by (space, name). A bundle
// with no boxes/ subdir contributes nothing. Empty slice + nil error
// if spaces/ doesn't exist yet.
func ListAllAgentboxes() ([]AgentboxRef, error) {
	base, err := layout.SpacesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []AgentboxRef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		space := e.Name()
		if layout.ValidateExistingSpaceName(space) != nil {
			continue
		}
		names, err := ListNames(space)
		if err != nil {
			return nil, fmt.Errorf("list agentboxes in space %s: %w", space, err)
		}
		for _, n := range names {
			refs = append(refs, AgentboxRef{Space: space, Name: n})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Space != refs[j].Space {
			return refs[i].Space < refs[j].Space
		}
		return refs[i].Name < refs[j].Name
	})
	return refs, nil
}

// Path returns the file path backing this config, "" if not yet saved.
func (c *Config) Path() string { return c.path }

// HasAgent reports whether this box hosts a real managed agent. A
// generic-shell box stores Agent "none" (DECISION A) — and a never-set Agent
// "" is treated as no-agent for safety — so both run the built-in
// generic-shell runtime (sleep-supervised; reached via box exec/attach) and
// MUST NOT be looked up as an agent profile. Every site that branches on
// "does this box have an agent?" keys off this, never a bare Agent != "".
func (c *Config) HasAgent() bool { return c.Agent != "" && c.Agent != account.None }

// HasRealAccount reports whether this box selects a real persisted account
// (not the generic-shell "none" sentinel). Mirrors HasAgent for the account
// dimension: strict lifecycle existence, state mounting, and env injection
// all gate on this. A real-account box always has a real agent
// (validateAccount couples them), but checking the Account field directly
// keeps callers honest about which dimension they mean.
func (c *Config) HasRealAccount() bool {
	return c.HasAgent() && c.Account != "" && c.Account != account.None
}

// WorkdirWritable reports whether this agentbox grants the
// fs.workdir.write capability — the positive-grant replacement for the old
// read_only posture flag. A writable workdir binds /workdir read-write at
// the container layer and places it in the policy's read_write set; its
// absence makes the workdir read-only. create/run add this capability by
// default and --read-only omits it. Pure check over the explicit capability
// list (workdir writability is never profile- or feature-inherited), so the
// bind decision in buildMounts and the recreate diff don't need the full
// compose pass.
func (c *Config) WorkdirWritable() bool {
	return slices.Contains(c.Capabilities, capabilities.WorkdirWriteCapability)
}

// validateAccount enforces the account/agent coherence rules (pure — no I/O;
// account EXISTENCE is checked by the CLI, not here):
//
//   - Generic-shell box (no real agent, HasAgent() false): Account must be
//     empty or "none" (DECISION A). A real account name with no real agent is
//     incoherent — reject it.
//   - Real agent (HasAgent() true): Account must NOT be empty or "none"
//     (DECISION B — real agents require real accounts) and must be a valid
//     plain account name. Any real account serves any agent; the agent is a
//     VIEW over the account rather than a segment of its identity.
func (c *Config) validateAccount() error {
	if !c.HasAgent() {
		if c.Account != "" && c.Account != account.None {
			return fmt.Errorf(
				"account %q is set but agent is %q: only a real agent can select a real account; a generic-shell box must use account: none",
				c.Account, c.Agent)
		}
		return nil
	}
	if c.Account == "" || c.Account == account.None {
		return fmt.Errorf(
			"agent %q requires a real account (got %q): real agents require real accounts. Omit --account for %q",
			c.Agent, c.Account, account.Default)
	}
	if err := account.ValidateName(c.Account); err != nil {
		return err
	}
	return nil
}

// Validate checks the config's invariants. Returns nil if valid.
// Validation rules:
//   - Name + Space pass layout.ValidateName
//   - Agent (if set) passes layout.ValidateName
//   - Account/agent coherence (validateAccount): a generic-shell box (no
//     real agent) uses account none; a real agent requires a real account
//     name — any real account serves any agent (account EXISTENCE is
//     checked by the CLI, not here)
//   - workdir.mode is host|ephemeral with an absolute source
//   - Feature names pass layout.ValidateName or parse as OCI references
//   - Capability names pass layout.ValidateName and are not the retired
//     privileged capability (only the privileged setting selects privilege)
//   - Ports have target & published in [1,65535]; protocol (if set) is
//     tcp|udp; host_ip (if set) parses; no duplicate published+protocol+host_ip
//   - Mounts have absolute Source + Target
//   - Env keys are non-empty (values are literal, non-secret by convention)
func (c *Config) Validate() error {
	if err := ValidateChatMountTargets(c); err != nil {
		return err
	}
	if c.ID != "" {
		if err := layout.ValidateIdentity(c.ID); err != nil {
			return err
		}
	}
	if _, err := resources.CPUQuota(c.Resources.CPUs); err != nil {
		return err
	}
	if _, err := resources.BoxMemory(c.Resources.Memory); err != nil {
		return err
	}
	if err := layout.ValidateName("agentbox", c.Name); err != nil {
		return err
	}
	if c.Space == "" {
		return errors.New("space is required")
	}
	if err := layout.ValidateExistingSpaceName(c.Space); err != nil {
		return err
	}
	if c.Agent != "" {
		if err := layout.ValidateName("agent", c.Agent); err != nil {
			return err
		}
	}
	if len(c.AgentExtraArgs) > 0 && !c.HasAgent() {
		return errors.New("agent_extra_args requires a real agent (generic-shell boxes must leave it empty)")
	}
	if c.AgentSession != nil {
		if !c.HasAgent() {
			return errors.New("agent_session requires a real agent (generic-shell boxes must leave it empty)")
		}
		switch c.AgentSession.Resume {
		case AgentSessionResumeLast, AgentSessionResumeNever, AgentSessionResumeManual:
		default:
			return fmt.Errorf("agent_session.resume %q must be last, never or manual", c.AgentSession.Resume)
		}
	}
	if len(c.SessionCommand) > 0 {
		if c.HasAgent() {
			return errors.New("session_command requires agent none")
		}
		if strings.TrimSpace(c.SessionCommand[0]) == "" {
			return errors.New("session_command executable must not be empty")
		}
		for _, arg := range c.SessionCommand {
			if strings.ContainsRune(arg, '\x00') {
				return errors.New("session_command must not contain NUL")
			}
		}
	}
	if err := c.validateAccount(); err != nil {
		return err
	}
	if err := c.validateWorkdir(); err != nil {
		return err
	}
	for _, f := range c.Features {
		if ocifeature.IsReference(f) {
			if _, err := ocifeature.Parse(f); err != nil {
				return err
			}
			continue
		}
		if err := layout.ValidateName("feature", f); err != nil {
			return fmt.Errorf("%w (in agentbox %s)", err, c.Name)
		}
	}
	for _, capName := range c.Capabilities {
		if err := layout.ValidateName("capability", capName); err != nil {
			return fmt.Errorf("%w (in agentbox %s)", err, c.Name)
		}
		if capName == capabilities.RetiredPrivilegedCapability {
			return fmt.Errorf("%w (in agentbox %s)", capabilities.ErrRetiredPrivileged, c.Name)
		}
	}
	// Duplicate detection keys on the fully-resolved published+protocol+host_ip
	// tuple: the same host port on a different protocol or bind address is a
	// distinct listener and must be allowed.
	type publishedKey struct {
		published int
		protocol  string
		hostIP    string
	}
	seenPublished := make(map[publishedKey]struct{}, len(c.Ports))
	for i, p := range c.Ports {
		if !validPort(p.Target) {
			return fmt.Errorf("ports[%d]: target %d out of range (1-65535)", i, p.Target)
		}
		if !validPort(p.Published) {
			return fmt.Errorf("ports[%d]: published %d out of range (1-65535)", i, p.Published)
		}
		if p.Protocol != "" && p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("ports[%d]: protocol %q must be tcp or udp", i, p.Protocol)
		}
		if p.HostIP != "" && net.ParseIP(p.HostIP) == nil {
			return fmt.Errorf("ports[%d]: host_ip %q is not a valid IP address", i, p.HostIP)
		}
		key := publishedKey{published: p.Published, protocol: p.ResolvedProtocol(), hostIP: p.ResolvedHostIP()}
		if _, dup := seenPublished[key]; dup {
			return fmt.Errorf("ports: published port %d (%s %s) declared twice",
				p.Published, key.hostIP, key.protocol)
		}
		seenPublished[key] = struct{}{}
	}
	for i, m := range c.Mounts {
		if m.Source == "" || m.Target == "" {
			return fmt.Errorf("mounts[%d]: source and target are required", i)
		}
		if !filepath.IsAbs(m.Source) {
			return fmt.Errorf("mounts[%d]: source %q must be an absolute path", i, m.Source)
		}
		if !filepath.IsAbs(m.Target) {
			return fmt.Errorf("mounts[%d]: target %q must be an absolute path", i, m.Target)
		}
	}
	for k := range c.Env {
		if k == "" {
			return errors.New("env: empty key")
		}
	}
	return nil
}

// Save writes the config atomically (tmp + rename). Auto-fills
// SchemaVersion if unset, then Validates.
func (c *Config) Save() error {
	if err := c.ValidateImage(); err != nil {
		return err
	}
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.ResolveFeatures(context.Background()); err != nil {
		return err
	}
	path := c.path
	if path == "" {
		resolved, err := layout.BoxPath(c.Space, c.Name)
		if err != nil {
			return err
		}
		path = resolved
	}
	if err := c.preserveIdentity(path); err != nil {
		return err
	}
	if err := ValidateChatDeclaration(c); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := manifestio.Write(path, raw, fileMode); err != nil {
		return err
	}
	c.path = path
	return nil
}

// Delete removes the agentbox YAML file. Returns fs.ErrNotExist if
// the file isn't there (caller decides whether that's an error).
func (c *Config) Delete() error {
	path := c.path
	if path == "" {
		resolved, err := layout.BoxPath(c.Space, c.Name)
		if err != nil {
			return err
		}
		path = resolved
	}
	return manifestio.Remove(path)
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

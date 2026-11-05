package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newAgentboxInspectCmd is `yoloctl box inspect NAME` — the read-only
// DEBUG surface for one agentbox. Where `box show` is the friendly status
// view, `inspect` dumps the resolved inputs, computed artifacts, and
// reconstructed runtime state in one place: the declared manifest, the
// resolved agent profile, the COMPUTED OpenShell policy (which users
// cannot edit in the target model), the live container substrate, and the
// reconcile/drift plan.
//
// Strictly read-only: it never starts a space, mutates a manifest, or
// touches runtime. It degrades gracefully — each section is best-effort and
// reports its own error rather than failing the whole command, so inspect
// stays useful against a stopped space, an orphaned container, or an
// invalid declaration.
//
// Secret hygiene: env VALUES never appear (manifest + runtime env are shown as
// KEYS only). `env` is non-secret literal by convention, but users may use it
// as an escape hatch, so the value is redacted anyway. The computed/active
// policy carries no secrets (hosts, paths, binaries) so it is shown in full.
func newAgentboxInspectCmd() *cobra.Command {
	var (
		spaceName string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "inspect BOX",
		Short: "Read-only debug view: resolved inputs, computed policy, and reconstructed runtime",
		Long: `Prints everything yoloctl knows about one box in a single
read-only view, for debugging:

  - the declared manifest (desired state), with secrets redacted;
  - the resolved agent definition;
  - the COMPUTED openshell-sandbox policy (generated, not user-editable)
    plus its hash;
  - the live container substrate (status, image, labels, mounts, env KEYS,
    active policy hash, port forwards);
  - the reconcile/drift plan between declaration and runtime.

inspect never mutates anything and never starts a space. Use ` + "`box show`" + `
for the friendly status summary; use inspect when you need to see the
generated policy or diagnose why a box is drifted.

  yoloctl box inspect coder
  yoloctl box inspect coder --space prod-untrusted
  yoloctl box inspect coder --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			resolved, err := newBoxStateResolver().ResolveOne(cmd.Context(), spaceName, name, boxstate.FullDiff)
			if err != nil {
				return err
			}
			if resolved.Declaration == boxstate.DeclarationMissing && resolved.Runtime == boxstate.RuntimeMissing {
				return asUserError(fmt.Errorf("box %q not found in space %q", name, spaceName))
			}
			report := buildInspectReport(cmd.Context(), spaceName, name, resolved)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			return writeInspectHuman(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the human-readable view")
	return cmd
}

// ---- report types ----------------------------------------------------------

type inspectReport struct {
	Identity          inspectIdentity       `json:"identity"`
	Manifest          *inspectManifest      `json:"manifest,omitempty"`
	ManifestError     string                `json:"manifest_error,omitempty"`
	Profile           *inspectProfile       `json:"resolved_profile,omitempty"`
	ProfileError      string                `json:"profile_error,omitempty"`
	Capabilities      []inspectEffectiveCap `json:"effective_capabilities,omitempty"`
	CapsError         string                `json:"effective_capabilities_error,omitempty"`
	OperatorAuthority string                `json:"operator_authority,omitempty"`
	// PrivilegedAuthority is set for a box declared privileged; OpenShell
	// policy is then recorded, not enforced.
	PrivilegedAuthority string          `json:"privileged_authority,omitempty"`
	Policy              *inspectPolicy  `json:"computed_policy,omitempty"`
	PolicyError         string          `json:"policy_error,omitempty"`
	Runtime             *inspectRuntime `json:"runtime,omitempty"`
	RuntimeError        string          `json:"runtime_error,omitempty"`
	Drift               inspectDrift    `json:"drift"`
}

// inspectEffectiveCap is one entry in the effective capability set: the
// capability name and the origins that contributed it (explicit on the
// manifest, profile-inherited, feature-inherited). The same origin-tracked set
// the policy compiler consumes, so inspect and the generated policy agree.
type inspectEffectiveCap struct {
	Name    string   `json:"name"`
	Origins []string `json:"origins"`
}

type inspectIdentity struct {
	Space         string `json:"space"`
	Name          string `json:"name"`
	Agent         string `json:"agent,omitempty"`
	State         string `json:"state"`
	Declaration   string `json:"declaration"`
	RuntimeState  string `json:"runtime"`
	SessionState  string `json:"session"`
	DriftState    string `json:"drift_state"`
	SchemaVersion int    `json:"schema_version,omitempty"`
	ConfigPath    string `json:"config_path,omitempty"`
	ManifestHash  string `json:"manifest_hash,omitempty"`
}

type inspectWorkdir struct {
	Mode          string `json:"mode"`
	Source        string `json:"source,omitempty"`
	ContainerPath string `json:"container_path,omitempty"`
}

// inspectPort is the resolved full port form (host_ip + protocol defaults
// folded in) shown by box inspect.
type inspectPort struct {
	HostIP    string `json:"host_ip"`
	Published int    `json:"published"`
	Target    int    `json:"target"`
	Protocol  string `json:"protocol"`
}

type inspectMount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

// inspectEnv carries an env declaration with the VALUE deliberately
// stripped: env is literal (non-secret by convention) but may be used as a
// secret escape hatch, so only the key is shown.
type inspectEnv struct {
	Key string `json:"key"`
}

type inspectManifest struct {
	Image string `json:"image,omitempty"`
	// RuntimeImage is the image the box ACTUALLY runs: the base Image when the
	// effective feature set installs nothing, else the content-addressed
	// feature-composed ref (yoloctl/agentbox-composed:<hash>), build-or-selected
	// on demand. Composed reports which case applies. Empty if it can't be
	// resolved (e.g. a missing agent profile).
	RuntimeImage string         `json:"runtime_image,omitempty"`
	Composed     bool           `json:"composed,omitempty"`
	Workdir      inspectWorkdir `json:"workdir"`
	WorkdirWrite bool           `json:"workdir_writable"`
	// UnconfinedNetwork true means the box dropped the network confinement
	// layer (openshell --mode=process): open egress, no allow-list / L7 proxy.
	// All other layers stay on; declared Capabilities of kind network.* are moot.
	UnconfinedNetwork bool `json:"unconfined_network"`
	// Privileged is the declared privileged setting; PrivilegedAuthority
	// carries its warning.
	Privileged bool `json:"privileged"`
	// AgentExtraArgs are the box-owned args appended after the agent definition's
	// default_args when a managed agent session is started.
	AgentExtraArgs []string `json:"agent_extra_args,omitempty"`
	SessionCommand []string `json:"session_command,omitempty"`
	// AgentState is the account-DERIVED native-state bind (not a manifest
	// field): scope (space|host) + the host source dir + the in-container
	// destination. Resolved from the box's account; absent for a generic-shell
	// box / a profile without a state path.
	AgentState     *inspectAgentState           `json:"agent_state,omitempty"`
	Features       []string                     `json:"features,omitempty"`
	FeatureOptions map[string]map[string]string `json:"feature_options,omitempty"`
	Capabilities   []string                     `json:"capabilities,omitempty"`
	Ports          []inspectPort                `json:"ports,omitempty"`
	Env            []inspectEnv                 `json:"env,omitempty"`
	ExtraMounts    []inspectMount               `json:"extra_mounts,omitempty"`
	CPUs           string                       `json:"cpus,omitempty"`
	Memory         string                       `json:"memory,omitempty"`
}

// inspectAgentState is the account-derived native-state bind shown by
// `box inspect`: the box's account's per-agent leaf. The state path is owned
// by the account and derived (account.StatePath) — never a box field, never
// user-overridable.
type inspectAgentState struct {
	Additional  []inspectAgentState `json:"additional,omitempty"`
	Source      string              `json:"source"`
	Destination string              `json:"destination"`
}

type inspectProfile struct {
	Name                 string                    `json:"name"`
	Image                string                    `json:"image,omitempty"`
	Command              string                    `json:"command,omitempty"`
	DefaultArgs          []string                  `json:"default_args,omitempty"`
	NpmPackage           string                    `json:"npm_package,omitempty"`
	NativeRelease        string                    `json:"native_release,omitempty"`
	StateContainerPath   string                    `json:"state_container_path,omitempty"`
	StateMounts          []agentprofile.StateMount `json:"state_mounts,omitempty"`
	StateArchiveExcludes []string                  `json:"state_archive_excludes,omitempty"`
	LaunchEnv            map[string]string         `json:"launch_env,omitempty"`
	RequiresFeatures     []string                  `json:"requires_features,omitempty"`
	AuthConfigured       bool                      `json:"auth_configured"`
}

type inspectPolicy struct {
	Source string `json:"source"` // always "composed" (no user-editable policy_file)
	SHA256 string `json:"sha256"`
	YAML   string `json:"yaml"`
}

type inspectRuntime struct {
	MemoryLimit         string                    `json:"memory_limit_bytes,omitempty"`
	WorkloadMemory      *container.WorkloadMemory `json:"workload_memory,omitempty"`
	WorkloadMemoryError string                    `json:"workload_memory_error,omitempty"`
	Status              string                    `json:"status,omitempty"`
	Running             bool                      `json:"running"`
	Image               string                    `json:"image,omitempty"`
	Labels              map[string]string         `json:"labels,omitempty"`
	Mounts              []inspectMount            `json:"mounts,omitempty"`
	EnvKeys             []string                  `json:"env_keys,omitempty"`
	ActivePolicySHA256  string                    `json:"active_policy_sha256,omitempty"`
	ActivePolicyMatches *bool                     `json:"active_policy_matches_computed,omitempty"`
	Ports               []inspectPort             `json:"ports,omitempty"`
	// RuntimeMode is the verified launch mode (privileged or confined), empty
	// when the evidence is incomplete or inconsistent (RuntimeModeReason).
	// Launcher and LauncherSHA256 are the privileged endpoint recorded at
	// creation; a later binary replacement does not change the running one.
	RuntimeMode       string `json:"runtime_mode,omitempty"`
	RuntimeModeReason string `json:"runtime_mode_reason,omitempty"`
	Launcher          string `json:"launcher,omitempty"`
	LauncherSHA256    string `json:"launcher_sha256,omitempty"`
}

type inspectDrift struct {
	State    string   `json:"state"`
	Strategy string   `json:"strategy,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Note     string   `json:"note,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}

// ---- report construction ---------------------------------------------------

func buildInspectReport(ctx context.Context, space, name string, b boxstate.Box) inspectReport {
	r := inspectReport{
		Identity: inspectIdentity{
			Space:        space,
			Name:         name,
			State:        string(b.State),
			Declaration:  string(b.Declaration),
			RuntimeState: string(b.Runtime),
			SessionState: string(b.Session.State),
			DriftState:   string(b.Drift),
		},
		Drift: buildInspectDrift(b),
	}
	if b.ConfigError != "" {
		r.ManifestError = b.ConfigError
	}

	if cfg := b.Config; cfg != nil {
		r.Identity.Agent = cfg.Agent
		r.Identity.SchemaVersion = cfg.SchemaVersion
		r.Identity.ConfigPath = cfg.Path()
		if raw, err := yaml.Marshal(cfg); err == nil {
			r.Identity.ManifestHash = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		}
		r.Manifest = buildInspectManifest(cfg)
		r.Profile, r.ProfileError = buildInspectProfile(cfg)
		r.Capabilities, r.CapsError = buildInspectEffectiveCaps(cfg)
		if enabled, err := agentbox.OperatorEnabled(cfg); err == nil && enabled {
			r.OperatorAuthority = operatorAuthorityNotice
		}
		if cfg.Privileged {
			r.PrivilegedAuthority = agentbox.PrivilegedAuthority
		}
		r.Policy, r.PolicyError = buildInspectPolicy(cfg)
	}

	// Runtime substrate: present whenever a container exists (declared or
	// orphaned). All probes are best-effort; a failure annotates
	// RuntimeError but never aborts inspect.
	if b.Container != nil {
		rt, rtErr := buildInspectRuntime(ctx, space, name, r.Policy)
		r.Runtime = rt
		if rtErr != "" {
			r.RuntimeError = rtErr
		}
	}
	return r
}

func buildInspectDrift(b boxstate.Box) inspectDrift {
	d := inspectDrift{State: string(b.Drift)}
	for _, reason := range b.Reasons {
		if reason.Message != "" {
			d.Reasons = append(d.Reasons, fmt.Sprintf("%s: %s", reason.Code, reason.Message))
		} else {
			d.Reasons = append(d.Reasons, string(reason.Code))
		}
	}
	if p := b.DiffPlan; p != nil {
		d.Note = p.PolicyNote
		switch {
		case p.NeedsCreate:
			d.Strategy, d.Reason = "create", p.CreateReason
		case p.NeedsRecreate:
			d.Strategy, d.Reason = "recreate", p.RecreateReason
		case p.NeedsRestart:
			d.Strategy, d.Reason = "restart", p.RestartReason
		case len(p.PortForwardsToAdd) > 0 || len(p.PortForwardsToRemove) > 0:
			d.Strategy = "live (port forwards)"
		default:
			d.Strategy = "none (in sync)"
		}
	}
	return d
}

func buildInspectManifest(cfg *agentbox.Config) *inspectManifest {
	m := &inspectManifest{
		Image:             cfg.Image,
		WorkdirWrite:      cfg.WorkdirWritable(),
		UnconfinedNetwork: cfg.UnconfinedNetwork,
		Privileged:        cfg.Privileged,
		AgentExtraArgs:    cfg.AgentExtraArgs,
		SessionCommand:    cfg.SessionCommand,
		Features:          cfg.Features,
		FeatureOptions:    cfg.FeatureOptions,
		Capabilities:      cfg.Capabilities,
		CPUs:              cfg.Resources.CPUs,
		Memory:            cfg.Resources.Memory,
		Workdir: inspectWorkdir{
			Mode:          cfg.Workdir.Mode,
			Source:        cfg.Workdir.MountSource(),
			ContainerPath: cfg.Workdir.ContainerPath(),
		},
	}
	// Resolved runtime image (best-effort): the base composed with the effective
	// features. A missing agent profile just omits it — Image is still shown.
	if ref, composed, err := agentbox.ResolvedImage(cfg); err == nil {
		m.RuntimeImage = ref
		m.Composed = composed
	}
	// Account-derived native-state bind (best-effort: a missing profile /
	// invalid account just omits the block — `box show` is the friendly
	// surface, this is the debug view).
	if cfg.HasAgent() {
		if profile, err := agentprofile.Lookup(cfg.Agent); err == nil {
			if st, err := resolveAgentStateMount(profile, cfg); err == nil && st.Enabled {
				m.AgentState = &inspectAgentState{
					Source:      st.Source,
					Destination: st.Destination,
				}
				for _, extra := range st.Additional {
					m.AgentState.Additional = append(m.AgentState.Additional, inspectAgentState{Source: extra.Source, Destination: extra.Destination})
				}
			}
		}
	}
	for _, p := range cfg.Ports {
		m.Ports = append(m.Ports, inspectPort{
			HostIP:    p.ResolvedHostIP(),
			Published: p.Published,
			Target:    p.Target,
			Protocol:  p.ResolvedProtocol(),
		})
	}
	for _, em := range cfg.Mounts {
		m.ExtraMounts = append(m.ExtraMounts, inspectMount{Source: em.Source, Destination: em.Target, ReadOnly: em.ReadOnly})
	}
	for _, k := range sortedEnvKeys(cfg.Env) {
		m.Env = append(m.Env, inspectEnv{Key: k})
	}
	return m
}

func buildInspectProfile(cfg *agentbox.Config) (*inspectProfile, string) {
	if !cfg.HasAgent() {
		return nil, ""
	}
	p, err := agentprofile.Lookup(cfg.Agent)
	if err != nil {
		return nil, err.Error()
	}
	out := &inspectProfile{
		Name:                 p.Name,
		Image:                p.Image,
		Command:              p.Command,
		DefaultArgs:          p.DefaultArgs,
		NpmPackage:           p.NpmPackage,
		NativeRelease:        p.NativeRelease,
		StateContainerPath:   p.StateContainerPath,
		StateMounts:          p.StateMounts,
		StateArchiveExcludes: p.StateArchiveExcludes,
		LaunchEnv:            p.LaunchEnv,
		RequiresFeatures:     p.RequiresFeatures,
	}
	if p.Auth != nil {
		out.AuthConfigured = true
	}
	return out, ""
}

func buildInspectPolicy(cfg *agentbox.Config) (*inspectPolicy, string) {
	raw, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		return nil, err.Error()
	}
	// Policy is always a computed artifact now (no user-editable policy_file).
	return &inspectPolicy{
		Source: "composed",
		SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)),
		YAML:   string(raw),
	}, ""
}

// buildInspectEffectiveCaps resolves the manifest's effective capability set
// with origins (explicit / profile / feature) — the positive-grant view the
// compiler turns into the openshell policy. Returns the error string when the
// set can't be resolved (e.g. an unknown feature).
func buildInspectEffectiveCaps(cfg *agentbox.Config) ([]inspectEffectiveCap, string) {
	eff, err := agentbox.EffectiveCapabilities(cfg)
	if err != nil {
		return nil, err.Error()
	}
	var out []inspectEffectiveCap
	for _, e := range eff.Effective() {
		origins := make([]string, 0, len(e.Origins))
		for _, o := range e.Origins {
			origins = append(origins, string(o))
		}
		out = append(out, inspectEffectiveCap{Name: e.Capability.Name, Origins: origins})
	}
	return out, ""
}

func buildInspectRuntime(ctx context.Context, space, name string, computed *inspectPolicy) (*inspectRuntime, string) {
	client := container.New(space)
	d, err := client.Inspect(ctx, name)
	if err != nil {
		return nil, fmt.Sprintf("inspect container: %v", err)
	}
	evidence := agentbox.ClassifyRuntime(d)
	rt := &inspectRuntime{
		MemoryLimit:       d.Memory,
		Status:            d.Status,
		Running:           d.Running,
		Image:             d.Image,
		EnvKeys:           envKeysOnly(d.Env),
		Labels:            filterUserLabels(d.Labels),
		RuntimeMode:       string(evidence.Mode),
		RuntimeModeReason: evidence.Reason,
		Launcher:          evidence.Launcher,
		LauncherSHA256:    d.Labels[agentbox.LabelLauncherSHA256],
	}
	if memory, err := client.WorkloadMemory(ctx); err == nil {
		rt.WorkloadMemory = memory
	} else {
		rt.WorkloadMemoryError = "VM workload memory counters unavailable"
	}
	for _, m := range d.Mounts {
		rt.Mounts = append(rt.Mounts, inspectMount{Source: m.Source, Destination: m.Destination, ReadOnly: m.ReadOnly})
	}
	// Use the same selected-generation policy as reconciliation. A native
	// failure stays unavailable; its legacy mirror may describe an old policy.
	if active, perr := (agentbox.LiveInspector{Client: client}).ReadRuntimePolicy(ctx, d); perr == nil {
		rt.ActivePolicySHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256(active))
		if computed != nil {
			matches := rt.ActivePolicySHA256 == computed.SHA256
			rt.ActivePolicyMatches = &matches
		}
	}
	// Active port forwards (best-effort), in the resolved full form. The socat
	// units carry only published+target, so host_ip/protocol resolve to their
	// defaults (127.0.0.1/tcp).
	if specs, perr := port.List(ctx, client, name); perr == nil {
		for _, s := range specs {
			p := agentbox.Port{Published: s.HostPort, Target: s.ContainerPort}
			rt.Ports = append(rt.Ports, inspectPort{
				HostIP:    p.ResolvedHostIP(),
				Published: p.Published,
				Target:    p.Target,
				Protocol:  p.ResolvedProtocol(),
			})
		}
		sort.Slice(rt.Ports, func(i, j int) bool { return rt.Ports[i].Published < rt.Ports[j].Published })
	}
	return rt, ""
}

// ---- helpers ----------------------------------------------------------------

func sortedEnvKeys(env map[string]agentbox.EnvVar) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// envKeysOnly extracts the KEY from each "KEY=VALUE" runtime env entry,
// dropping every value. Sorted + deduped. This is the only form runtime env
// is ever exposed in (values may be injected secrets).
func envKeysOnly(env []string) []string {
	seen := make(map[string]struct{}, len(env))
	keys := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func filterUserLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if isInternalLabel(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// ---- human rendering --------------------------------------------------------

func writeInspectHuman(w io.Writer, r inspectReport) error {
	id := r.Identity
	_, _ = fmt.Fprintf(w, "Box:      %s   (space: %s)\n", id.Name, id.Space)
	_, _ = fmt.Fprintf(w, "State:    %s   [declaration=%s runtime=%s session=%s drift=%s]\n",
		id.State, id.Declaration, id.RuntimeState, id.SessionState, id.DriftState)
	if id.Agent != "" {
		_, _ = fmt.Fprintf(w, "Agent:    %s\n", id.Agent)
	}
	if id.ConfigPath != "" {
		_, _ = fmt.Fprintf(w, "Manifest: %s (schema v%d)\n", id.ConfigPath, id.SchemaVersion)
	}
	if id.ManifestHash != "" {
		_, _ = fmt.Fprintf(w, "Hash:     %s\n", id.ManifestHash)
	}

	if r.ManifestError != "" {
		_, _ = fmt.Fprintf(w, "\nManifest: ERROR: %s\n", r.ManifestError)
	}
	if m := r.Manifest; m != nil {
		writeInspectManifest(w, m)
	}

	if r.ProfileError != "" {
		_, _ = fmt.Fprintf(w, "\nResolved agent: ERROR: %s\n", r.ProfileError)
	} else if p := r.Profile; p != nil {
		writeInspectProfile(w, p)
	}

	if r.CapsError != "" {
		_, _ = fmt.Fprintf(w, "\nEffective capabilities: ERROR: %s\n", r.CapsError)
	} else if len(r.Capabilities) > 0 {
		_, _ = fmt.Fprintln(w, "\nEffective capabilities (origin: explicit | feature)")
		for _, c := range r.Capabilities {
			_, _ = fmt.Fprintf(w, "  %-22s %s\n", c.Name, strings.Join(c.Origins, ", "))
		}
	}

	if r.OperatorAuthority != "" {
		_, _ = fmt.Fprintf(w, "\nBox operator: %s\n", r.OperatorAuthority)
	}
	if r.PrivilegedAuthority != "" {
		_, _ = fmt.Fprintf(w, "\nPrivileged box: %s\n", r.PrivilegedAuthority)
	}
	_, _ = fmt.Fprintln(w, "\nComputed OpenShell policy")
	if r.PrivilegedAuthority != "" {
		_, _ = fmt.Fprintln(w, "  (recorded for a later confined recreate; not enforced while privileged)")
	}
	switch {
	case r.PolicyError != "":
		_, _ = fmt.Fprintf(w, "  ERROR: %s\n", r.PolicyError)
	case r.Policy != nil:
		_, _ = fmt.Fprintf(w, "  Source: %s\n", r.Policy.Source)
		_, _ = fmt.Fprintf(w, "  SHA256: %s\n", r.Policy.SHA256)
		_, _ = fmt.Fprintln(w, "  ---")
		for line := range strings.SplitSeq(strings.TrimRight(r.Policy.YAML, "\n"), "\n") {
			_, _ = fmt.Fprintf(w, "  %s\n", line)
		}
	default:
		_, _ = fmt.Fprintln(w, "  (not available)")
	}

	_, _ = fmt.Fprintln(w, "\nRuntime container")
	switch {
	case r.RuntimeError != "":
		_, _ = fmt.Fprintf(w, "  ERROR: %s\n", r.RuntimeError)
	case r.Runtime != nil:
		writeInspectRuntime(w, r.Runtime)
	default:
		_, _ = fmt.Fprintln(w, "  (no container materialized)")
	}

	_, _ = fmt.Fprintln(w, "\nDrift")
	_, _ = fmt.Fprintf(w, "  State:    %s\n", r.Drift.State)
	if r.Drift.Strategy != "" {
		_, _ = fmt.Fprintf(w, "  Strategy: %s\n", r.Drift.Strategy)
	}
	if r.Drift.Reason != "" {
		_, _ = fmt.Fprintf(w, "  Reason:   %s\n", r.Drift.Reason)
	}
	if r.Drift.Note != "" {
		_, _ = fmt.Fprintf(w, "  Note:     %s\n", r.Drift.Note)
	}
	for _, reason := range r.Drift.Reasons {
		_, _ = fmt.Fprintf(w, "  - %s\n", reason)
	}
	return nil
}

func writeInspectManifest(w io.Writer, m *inspectManifest) {
	_, _ = fmt.Fprintln(w, "\nManifest (declared desired state)")
	if m.Image != "" {
		_, _ = fmt.Fprintf(w, "  Image:        %s  (base)\n", m.Image)
	}
	if m.Composed {
		_, _ = fmt.Fprintf(w, "  Runtime image: %s  (base + composed features, built on demand)\n", m.RuntimeImage)
	}
	wd := m.Workdir.Mode
	if m.Workdir.Source != "" {
		wd = fmt.Sprintf("%s  %s -> %s", m.Workdir.Mode, m.Workdir.Source, m.Workdir.ContainerPath)
	}
	_, _ = fmt.Fprintf(w, "  Workdir:      %s\n", wd)
	_, _ = fmt.Fprintf(w, "  Workdir rw:   %t\n", m.WorkdirWrite)
	if m.AgentState != nil {
		_, _ = fmt.Fprintf(w, "  Account state: %s -> %s\n",
			m.AgentState.Source, m.AgentState.Destination)
		for _, extra := range m.AgentState.Additional {
			_, _ = fmt.Fprintf(w, "  Account state: %s -> %s\n", extra.Source, extra.Destination)
		}
	}
	if len(m.SessionCommand) > 0 {
		_, _ = fmt.Fprintf(w, "  Session command: %q\n", m.SessionCommand)
	}
	if len(m.AgentExtraArgs) > 0 {
		_, _ = fmt.Fprintf(w, "  Agent extra args: %s\n", strings.Join(m.AgentExtraArgs, " "))
	}
	if len(m.Features) > 0 {
		_, _ = fmt.Fprintf(w, "  Features:     %s\n", strings.Join(m.Features, ", "))
	}
	if len(m.Capabilities) > 0 {
		_, _ = fmt.Fprintf(w, "  Capabilities: %s\n", strings.Join(m.Capabilities, ", "))
	}
	for _, p := range m.Ports {
		_, _ = fmt.Fprintf(w, "  Port:         %s:%d -> %d/%s\n", p.HostIP, p.Published, p.Target, p.Protocol)
	}
	if m.CPUs != "" || m.Memory != "" {
		_, _ = fmt.Fprintf(w, "  Resources:    cpus=%s memory=%s\n", dash(m.CPUs), dash(m.Memory))
	}
	for _, em := range m.ExtraMounts {
		mode := "rw"
		if em.ReadOnly {
			mode = "ro"
		}
		_, _ = fmt.Fprintf(w, "  Extra mount:  %s -> %s (%s)\n", em.Source, em.Destination, mode)
	}
	for _, e := range m.Env {
		_, _ = fmt.Fprintf(w, "  Env:          %s = (redacted)\n", e.Key)
	}
}

func writeInspectProfile(w io.Writer, p *inspectProfile) {
	_, _ = fmt.Fprintf(w, "\nResolved agent: %s\n", p.Name)
	cmdLine := p.Command
	if len(p.DefaultArgs) > 0 {
		cmdLine = p.Command + " " + strings.Join(p.DefaultArgs, " ")
	}
	if cmdLine != "" {
		_, _ = fmt.Fprintf(w, "  Command:      %s\n", cmdLine)
	}
	if p.Image != "" {
		_, _ = fmt.Fprintf(w, "  Image:        %s\n", p.Image)
	}
	if len(p.RequiresFeatures) > 0 {
		_, _ = fmt.Fprintf(w, "  Req features: %s\n", strings.Join(p.RequiresFeatures, ", "))
	}
	if p.NativeRelease != "" {
		_, _ = fmt.Fprintf(w, "Native release: %s\n", p.NativeRelease)
	}
	if p.NpmPackage != "" {
		_, _ = fmt.Fprintf(w, "  Npm package:  %s\n", p.NpmPackage)
	}
	if p.StateContainerPath != "" {
		_, _ = fmt.Fprintf(w, "  State path:   %s\n", p.StateContainerPath)
		for _, mount := range p.StateMounts {
			_, _ = fmt.Fprintf(w, "  State view:   %s -> %s\n", mount.Source, mount.Destination)
		}
	}
	for _, k := range sortedStringKeys(p.LaunchEnv) {
		_, _ = fmt.Fprintf(w, "  Launch env:   %s=%s\n", k, p.LaunchEnv[k])
	}
	if p.AuthConfigured {
		_, _ = fmt.Fprintf(w, "  Auth:         configured\n")
	}
}

func writeInspectRuntime(w io.Writer, rt *inspectRuntime) {
	_, _ = fmt.Fprintf(w, "  Status:       %s\n", rt.Status)
	if rt.MemoryLimit != "" {
		limit := rt.MemoryLimit + " bytes"
		if rt.MemoryLimit == "0" {
			limit = "shared (no per-box cap)"
		}
		_, _ = fmt.Fprintf(w, "  Memory limit: %s\n", limit)
	}
	if m := rt.WorkloadMemory; m != nil {
		_, _ = fmt.Fprintf(w, "  VM workload pool (all boxes and builds): %.0f/%.0f MiB, peak %.0f MiB; OOM kills since pool creation: %d\n", float64(m.Current)/(1<<20), float64(m.Max)/(1<<20), float64(m.Peak)/(1<<20), m.Events["oom_kill"])
	} else if rt.WorkloadMemoryError != "" {
		_, _ = fmt.Fprintf(w, "  Memory:       %s\n", rt.WorkloadMemoryError)
	}
	if rt.Image != "" {
		_, _ = fmt.Fprintf(w, "  Image:        %s\n", rt.Image)
	}
	switch {
	case rt.RuntimeMode != "":
		_, _ = fmt.Fprintf(w, "  Launch mode:  %s\n", rt.RuntimeMode)
	case rt.RuntimeModeReason != "":
		_, _ = fmt.Fprintf(w, "  Launch mode:  unknown (%s)\n", rt.RuntimeModeReason)
	}
	if rt.Launcher != "" {
		_, _ = fmt.Fprintf(w, "  Launcher:     %s  (binary sha256:%s at creation)\n", rt.Launcher, dash(rt.LauncherSHA256))
	}
	if rt.ActivePolicySHA256 != "" {
		match := ""
		if rt.ActivePolicyMatches != nil {
			if *rt.ActivePolicyMatches {
				match = "  (matches computed)"
			} else {
				match = "  (DIFFERS from computed)"
			}
		}
		_, _ = fmt.Fprintf(w, "  Active policy:%s%s\n", " "+rt.ActivePolicySHA256, match)
	}
	for _, p := range rt.Ports {
		_, _ = fmt.Fprintf(w, "  Port:         %s:%d -> %d/%s\n", p.HostIP, p.Published, p.Target, p.Protocol)
	}
	if len(rt.EnvKeys) > 0 {
		_, _ = fmt.Fprintf(w, "  Env keys:     %s\n", strings.Join(rt.EnvKeys, ", "))
	}
	if len(rt.Mounts) > 0 {
		_, _ = fmt.Fprintln(w, "  Mounts:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "    SOURCE\tDESTINATION\tMODE")
		for _, m := range rt.Mounts {
			mode := "rw"
			if m.ReadOnly {
				mode = "ro"
			}
			_, _ = fmt.Fprintf(tw, "    %s\t%s\t%s\n", m.Source, m.Destination, mode)
		}
		_ = tw.Flush()
	}
	if len(rt.Labels) > 0 {
		_, _ = fmt.Fprintln(w, "  Labels:")
		for _, k := range sortedStringKeys(rt.Labels) {
			_, _ = fmt.Fprintf(w, "    %s=%s\n", k, rt.Labels[k])
		}
	}
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

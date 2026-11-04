package agentbox

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/resources"
)

// ActualState describes the agentbox's observed state in its space:
// container properties + the currently bind-mounted policy content +
// the active set of port forwards. Used as input to Diff. Obtained
// from a StateInspector; Diff itself does no I/O.
type ActualState struct {
	// Exists reports whether a container labeled
	// yoloctl.agentbox=<Config.Name> exists in the target space.
	// When false, every other field is the zero value and Diff
	// returns NeedsCreate without consulting them.
	Exists bool

	// Container-level properties pulled from `nerdctl inspect`.
	Image             string
	CPUs              string
	Memory            string
	WorkloadPoolKnown bool
	WorkloadPool      bool
	ReadOnlyWD        bool // whether the /workdir bind is read-only

	// WorkdirSource is the source path of the workdir bind mount.
	// A change (e.g. an edited --workdir) forces a recreate. Empty when
	// the inspector didn't populate it (Diff then skips the comparison).
	WorkdirSource string

	// WorkdirDest is the in-container destination of the workdir bind
	// mount. A host workdir mirrors its canonical path under /workdir
	// (Workdir.ContainerPath), so a moved workdir or an ephemeral↔host
	// switch changes the destination too; Diff recreates on a change.
	// Empty when the inspector didn't populate it.
	WorkdirDest string

	// UnconfinedNetwork is the live box's network confinement posture, read
	// back from the LabelNetworkUnconfined container label (absent → false).
	// A confined↔unconfined toggle changes openshell-sandbox's --mode, which
	// is create-only, so Diff recreates on a change. Safe to compare
	// unconditionally: the label is absent (false) on every box that predates
	// the feature, matching the default Config.UnconfinedNetwork=false.
	UnconfinedNetwork bool
	// Operator endpoints are create-time mounts. Grant changes require their
	// addition/removal; the controller also revokes authorization immediately.
	OperatorEnabled bool
	// Runtime is the inspected launch-mode evidence (ClassifyRuntime). The
	// zero value proves neither mode: a desired privileged box needs positive
	// evidence, and a confined one drifts only on a privileged signal.
	Runtime RuntimeEvidence
	// ObservationEnabled reports the agent observation bind. Containers
	// created before it show observation as unsupported; it is never a
	// recreate reason.
	ObservationEnabled bool

	// Mounts are the non-canonical bind mounts (everything past the
	// fixed infra mounts + /workdir), including the agent-state mount.
	Mounts []Mount
	// ChatMounts are controller-derived leaves, kept separate from manifest
	// mounts so drift cannot silently turn into a host mount declaration.
	ChatMounts []Mount

	// Env is the container's full environment (KEY=VALUE), including
	// image-baked entries. Diff compares only the yoloctl-managed keys
	// (those declared in Config.Env); nil when not inspected.
	Env []string

	// ManagedEnvKeys are the env keys yoloctl set at create time, read
	// back from the LabelEnvKeys container label. Diff uses it to detect a
	// REMOVED managed key (present here, absent from Config.Env → recreate).
	// nil when the label is absent (container created before the label
	// existed, or not inspected) — Diff then skips removal detection and
	// falls back to comparing declared keys only.
	ManagedEnvKeys []string

	// Labels are the container's labels; Diff checks the yoloctl.agent
	// label. nil when not inspected.
	Labels map[string]string

	// Account is the plain account NAME the live container recorded at
	// create time (the yoloctl.account label). Empty for a generic-shell
	// container or when not inspected. Compared against the desired
	// Config.Account to detect an account RE-POINT (a recreate reason) —
	// account CONTENT changes are never diffed. Non-secret —
	// a name, never a value.
	Account string

	// PolicyContent is the raw bytes of the policy YAML currently
	// bind-mounted into the agentbox (at
	// /etc/openshell/policy.yaml). Diff uses byte-equality against
	// the desired policy; whitespace-only changes will trigger a
	// restart, which is harmless and easier to reason about than
	// semantic-equality.
	PolicyContent []byte

	// PortForwards enumerates active yoloctl-port-* systemd units
	// targeting this agentbox, in the resolved Port shape.
	PortForwards []Port
}

// StateInspector reads the actual state of an agentbox. Implementations:
//   - LiveInspector: queries the VM via internal/container
//   - internal/port + reads the bind-mounted policy.yaml.
//   - test fakes (this commit's *_test.go).
type StateInspector interface {
	Inspect(ctx context.Context, c *Config) (ActualState, error)
}

// DiffPlan describes the work needed to bring actual state in line
// with the declared Config. Exactly one of NeedsCreate /
// NeedsRecreate / NeedsRestart is true, OR all three are false (the
// live-update-only path). PortForwardsToAdd / ToRemove are always
// computed: when a Create / Recreate / Restart happens, they reflect
// the full set the Applier should end up with; when only live updates
// are needed, they reflect the symmetric diff.
type DiffPlan struct {
	// ImageRecreateReason independently records recipe-image drift, even when a
	// stronger recreation reason (for example launch mode) is shown first.
	ImageRecreateReason string

	NeedsCreate   bool
	NeedsRecreate bool
	NeedsRestart  bool

	// Human-readable reasons for the chosen strategy. Useful for
	// dry-run output, Reconcile logging, and test assertions.
	// Empty when no work needed at that level.
	CreateReason   string
	RecreateReason string
	RestartReason  string
	// PolicyNote explains a composed-policy change that needs no restart
	// because the proved privileged runtime does not enforce OpenShell policy.
	PolicyNote string

	PortForwardsToAdd    []Port
	PortForwardsToRemove []Port
}

// HasChanges reports whether the plan represents any work.
func (p DiffPlan) HasChanges() bool {
	return p.NeedsCreate || p.NeedsRecreate || p.NeedsRestart ||
		len(p.PortForwardsToAdd) > 0 || len(p.PortForwardsToRemove) > 0
}

// Diff computes the work needed to align actual with c's declared
// state. Pure function: no I/O, no side effects.
//
// desiredImage is the runtime image the box SHOULD run — the feature-composed
// ref (or the base when nothing composes), which the caller computes with
// ResolvedImage(c) before invoking Diff (keeping Diff pure). It is compared
// against the live container's actual image so a --features edit that moves the
// composed ref is detected as a recreate; the raw manifest c.Image (the base)
// is deliberately NOT used here, because the container runs the resolved ref.
//
// desiredPolicy is the policy YAML bytes that should be bind-mounted
// into the agentbox. The caller (Reconcile) computes this by composing
// the effective capability set (ComposePolicy) before invoking Diff.
//
// Account CONTENT is deliberately NOT a diff input: editing an account
// never produces a reconcile action or synchronizes native state. Only an
// account RE-POINT (the desired Config.Account vs the name the container
// recorded, see recreateReason) is diffed.
//
// Strategy selection is most-restrictive-wins:
//
//	Create > Recreate > Restart > Live
//
// When a stronger strategy applies, PortForwardsToAdd captures every
// declared forward (the Applier re-applies the full set after the
// container settles) and PortForwardsToRemove captures every actual
// forward (the strategy will have torn them down; keeping the field
// populated lets Reconcile and downstream callers act uniformly).
func Diff(c *Config, actual ActualState, desiredImage string, desiredPolicy []byte) DiffPlan {
	if !actual.Exists {
		return DiffPlan{
			NeedsCreate:       true,
			CreateReason:      "container does not exist",
			PortForwardsToAdd: append([]Port(nil), c.Ports...),
		}
	}

	if reason := recreateReason(c, actual, desiredImage); reason != "" {
		return DiffPlan{
			NeedsRecreate:        true,
			ImageRecreateReason:  imageRecreateReason(actual, desiredImage),
			RecreateReason:       reason,
			PortForwardsToAdd:    append([]Port(nil), c.Ports...),
			PortForwardsToRemove: append([]Port(nil), actual.PortForwards...),
		}
	}

	note := ""
	if !bytes.Equal(desiredPolicy, actual.PolicyContent) {
		if !provedPrivilegedRuntime(c, actual) {
			return DiffPlan{
				NeedsRestart:         true,
				RestartReason:        "policy content changed",
				PortForwardsToAdd:    append([]Port(nil), c.Ports...),
				PortForwardsToRemove: append([]Port(nil), actual.PortForwards...),
			}
		}
		// The staged bytes are refreshed by the next create/recreate, which
		// also covers an explicit return to a confined launch.
		note = "composed policy changed; not enforced while privileged (no restart)"
	}

	add, remove := portForwardDiff(c.Ports, actual.PortForwards)
	return DiffPlan{
		PolicyNote:           note,
		PortForwardsToAdd:    add,
		PortForwardsToRemove: remove,
	}
}

// provedPrivilegedRuntime reports whether both the declaration and the
// inspected runtime are the current privileged launch. Unknown evidence never
// qualifies.
func provedPrivilegedRuntime(c *Config, a ActualState) bool {
	return c.Privileged && a.Runtime.Mode == RuntimeModePrivileged && a.Runtime.Protocol == boxendpoint.ProtocolVersion
}

// runtimeModeReason is the create-only launch-mode comparison. Every reason
// requires an explicit box recreate; reconcile, set and start never switch
// modes on their own (see RuntimeModeChange).
func runtimeModeReason(c *Config, a ActualState) string {
	want := c.Privileged
	switch {
	case want && a.Runtime.Mode != RuntimeModePrivileged:
		actual := string(a.Runtime.Mode)
		if actual == "" {
			actual = "unknown"
			if a.Runtime.Reason != "" {
				actual += " (" + a.Runtime.Reason + ")"
			}
		}
		return fmt.Sprintf("runtime mode: %s -> privileged", actual)
	case want && a.Runtime.Protocol != boxendpoint.ProtocolVersion:
		// Agreeing older evidence is still privileged; it needs the current protocol.
		return fmt.Sprintf("privileged launcher protocol: %q -> %q", a.Runtime.Protocol, boxendpoint.ProtocolVersion)
	case !want && a.Runtime.AnyPrivileged:
		return "runtime mode: privileged -> confined"
	}
	return ""
}

// recreateReason returns a non-empty string explaining why a recreate
// is required, or "" when no recreate-only field differs. Order
// reflects relative debug value — the first mismatch is the one
// surfaced.
//
// Compared fields: image, cpus, memory, workdir read-only state (derived
// from the fs.workdir.write capability), the workdir bind source AND
// in-container destination, extra mounts, yoloctl-managed env, the agent
// label, and the account label (a re-point) — every create-only nerdctl
// property. Each new-field comparison is guarded on the inspector having
// populated the actual side, so a fake inspector that reports only the
// legacy fields opts out cleanly.
//
// Resource values are normalized before comparison. Empty actual values mean
// unavailable, while "0" is an observed unlimited value. Empty desired memory
// and "shared" both remove the per-box cap, preserving the aggregate budget.
func recreateReason(c *Config, a ActualState, desiredImage string) string {
	if reason := chatBindingReason(c, a); reason != "" {
		return reason
	}
	if reason := chatServerRuntimeReason(c, a); reason != "" {
		return reason
	}
	// Launch mode first: it explains the image change that comes with it.
	if reason := runtimeModeReason(c, a); reason != "" {
		return reason
	}
	if reason := imageRecreateReason(a, desiredImage); reason != "" {
		return reason
	}
	wantCPU, _ := resources.CPUQuota(c.Resources.CPUs)
	actualCPU, _ := resources.CPUQuota(a.CPUs)
	if a.CPUs != "" && wantCPU != actualCPU {
		return fmt.Sprintf("cpus: %s -> %s", a.CPUs, c.Resources.CPUs)
	}
	wantMemory, _ := resources.BoxMemory(c.Resources.Memory)
	actualMemory, _ := resources.BoxMemory(a.Memory)
	if a.Memory != "" && wantMemory != actualMemory {
		return fmt.Sprintf("memory: %s -> %s", a.Memory, c.Resources.Memory)
	}
	if a.WorkloadPoolKnown && !a.WorkloadPool {
		return "join the VM workload memory pool"
	}
	if wantRO := !c.WorkdirWritable(); wantRO != a.ReadOnlyWD {
		return fmt.Sprintf("workdir read-only: %v -> %v", a.ReadOnlyWD, wantRO)
	}
	// Network confinement posture (openshell --mode): toggling drops/adds the
	// netns + L7 proxy, which is create-only. Compared unconditionally — the
	// actual side is false both when confined and when the label predates the
	// feature, matching the c=false default, so this never churns an existing
	// confined box.
	if c.UnconfinedNetwork != a.UnconfinedNetwork {
		return fmt.Sprintf("network unconfined: %v -> %v", a.UnconfinedNetwork, c.UnconfinedNetwork)
	}

	if enabled, err := OperatorEnabled(c); err == nil && enabled != a.OperatorEnabled {
		return fmt.Sprintf("box operator: %v -> %v", a.OperatorEnabled, enabled)
	}
	// Workdir source: an edited --workdir / ephemeral↔host switch must
	// rebind the workdir. Guarded so an inspector that didn't report the
	// source (older fakes) skips the check.
	if want := c.Workdir.MountSource(); want != "" && a.WorkdirSource != "" && want != a.WorkdirSource {
		return fmt.Sprintf("workdir source: %s -> %s", a.WorkdirSource, want)
	}
	// Workdir container destination: a moved host workdir mirrors to a
	// different /workdir/<path>, so the destination is an
	// independent recreate trigger. Same actual-populated guard.
	if want := c.Workdir.ContainerPath(); want != "" && a.WorkdirDest != "" && want != a.WorkdirDest {
		return fmt.Sprintf("workdir path: %s -> %s", a.WorkdirDest, want)
	}
	if reason := extraMountsRecreateReason(c.Mounts, a.Mounts); reason != "" {
		return reason
	}
	if reason := envRecreateReason(c.Env, a.Env, a.ManagedEnvKeys); reason != "" {
		return reason
	}
	if c.Agent != "" && a.Labels != nil && a.Labels[LabelAgent] != c.Agent {
		return fmt.Sprintf("agent: %s -> %s", a.Labels[LabelAgent], c.Agent)
	}
	// Account RE-POINT: the box was pointed at a different account since the
	// container was created (desired Config.Account vs the recorded
	// yoloctl.account label). An EXPLICIT recreate reason — not inferred from
	// mount changes — so it also covers injection-only agents with no state
	// leaf, whose env (OPENSHELL_USER_ENVIRONMENT) is create-time-only.
	// Account CONTENT changes never appear here. Guarded on the label being
	// present so an uninspected actual side opts out cleanly.
	if c.HasRealAccount() && a.Account != "" && a.Account != c.Account {
		return fmt.Sprintf("account: %s -> %s", a.Account, c.Account)
	}
	return ""
}

// extraMountsRecreateReason reports a non-empty reason when the declared
// extra mounts (including the agent-state mount) differ from the live
// container's by target, source, or read-only flag. nil-vs-nil
// (no extra mounts on either side) compares equal.
func extraMountsRecreateReason(desired, actual []Mount) string {
	if len(desired) != len(actual) {
		return fmt.Sprintf("extra mounts: %d -> %d", len(actual), len(desired))
	}
	byTarget := make(map[string]Mount, len(actual))
	for _, m := range actual {
		byTarget[m.Target] = m
	}
	for _, m := range desired {
		got, ok := byTarget[m.Target]
		if !ok || got.Source != m.Source || got.ReadOnly != m.ReadOnly {
			return fmt.Sprintf("extra mount %s changed", m.Target)
		}
	}
	return ""
}

// envRecreateReason reports a non-empty reason when the yoloctl-managed
// env set changed in a way that requires a recreate: a key added, a
// literal value changed, or — when managedKeys is known — a key removed.
//
// It deliberately ignores image-baked env (only managed keys are
// considered) and credential-reference values (resolved at create time;
// a changed credential needs an explicit recreate, so literal-only here).
//
//   - managedKeys != nil (the LabelEnvKeys label was present): the set of
//     keys yoloctl managed last time is known, so added / removed are
//     decided by comparing key sets, and a removed key forces a recreate.
//   - managedKeys == nil (legacy container without the label, or actual
//     not inspected): fall back to the conservative declared-keys-only
//     check — no removal detection.
//
// Pure: no I/O. Iteration over the key sets is sorted for a deterministic
// reason string.
func envRecreateReason(desired map[string]EnvVar, actualEnv []string, managedKeys []string) string {
	byKey := make(map[string]string, len(actualEnv))
	for _, e := range actualEnv {
		if i := strings.IndexByte(e, '='); i > 0 {
			byKey[e[:i]] = e[i+1:]
		}
	}
	if managedKeys != nil {
		desiredSet := make(map[string]bool, len(desired))
		for k := range desired {
			desiredSet[k] = true
		}
		// Removed: a key yoloctl managed last time is no longer declared.
		// managedKeys is stored sorted, so the first hit is deterministic.
		for _, k := range managedKeys {
			if !desiredSet[k] {
				return fmt.Sprintf("env %s removed", k)
			}
		}
		managedSet := make(map[string]bool, len(managedKeys))
		for _, k := range managedKeys {
			managedSet[k] = true
		}
		for _, k := range sortedEnvKeys(desired) {
			if !managedSet[k] {
				return fmt.Sprintf("env %s added", k)
			}
			if v := desired[k]; v.Literal != "" {
				if val, ok := byKey[k]; !ok || val != v.Literal {
					return fmt.Sprintf("env %s changed", k)
				}
			}
		}
		return ""
	}
	// Legacy fallback: no managed-keys label to compare against.
	if len(desired) == 0 || actualEnv == nil {
		return ""
	}
	for _, k := range sortedEnvKeys(desired) {
		v := desired[k]
		val, ok := byKey[k]
		if !ok {
			return fmt.Sprintf("env %s added", k)
		}
		if v.Literal != "" && val != v.Literal {
			return fmt.Sprintf("env %s changed", k)
		}
	}
	return ""
}

// sortedEnvKeys returns the keys of an env map in sorted order, so
// envRecreateReason's "added"/"changed" reason is deterministic.
func sortedEnvKeys(env map[string]EnvVar) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// normalizeImage canonicalizes a Docker/OCI image reference so two
// equivalent forms compare equal. Delegates to the upstream
// distribution/reference parser, which handles the full set of
// implicit-registry / implicit-namespace / digest / tag rules:
//
//	ubuntu                       → docker.io/library/ubuntu:latest
//	ubuntu:24.04                 → docker.io/library/ubuntu:24.04
//	yoloctl/agentbox:tag         → docker.io/yoloctl/agentbox:tag
//	docker.io/library/ubuntu     → docker.io/library/ubuntu:latest
//	ghcr.io/leogr/foo:latest     → ghcr.io/leogr/foo:latest
//	registry:5000/foo            → registry:5000/foo:latest
//	localhost:5000/foo           → localhost:5000/foo:latest
//	<digest-ref>                 → preserved verbatim
//
// Falls back to the input string when the reference isn't parseable;
// the caller (recreateReason) then compares the literal values.
// Empty strings pass through unchanged so the "actual unknown"
// short-circuit in recreateReason keeps working.
func normalizeImage(s string) string {
	if s == "" {
		return ""
	}
	ref, err := reference.ParseAnyReference(s)
	if err != nil {
		return s
	}
	if named, ok := ref.(reference.Named); ok {
		return reference.TagNameOnly(named).String()
	}
	return ref.String()
}

// portForwardDiff returns the symmetric difference between desired
// and actual. Identity key is the resolved published+protocol+host_ip tuple
// (Config.Validate enforces uniqueness within a single Config; the actual side
// comes from systemd unit names which encode published-port-by-construction).
// A forward whose target port changed for the same published port
// appears in both toAdd and toRemove, which is correct: live update
// is remove-then-add, and the Applier handles the order.
//
// The actual side is reconstructed from systemd unit names, which carry only
// published+target — so its HostIP/Protocol are always the resolved defaults.
// Comparison therefore folds defaults in via the resolver so a desired port
// that only restates the default host_ip/protocol doesn't churn against an
// actual default-valued forward.
func portForwardDiff(desired, actual []Port) (toAdd, toRemove []Port) {
	type key struct {
		published int
		protocol  string
		hostIP    string
	}
	keyOf := func(p Port) key {
		return key{published: p.Published, protocol: p.ResolvedProtocol(), hostIP: p.ResolvedHostIP()}
	}
	desiredByKey := make(map[key]Port, len(desired))
	for _, p := range desired {
		desiredByKey[keyOf(p)] = p
	}
	actualByKey := make(map[key]Port, len(actual))
	for _, p := range actual {
		actualByKey[keyOf(p)] = p
	}
	for _, p := range desired {
		actualP, ok := actualByKey[keyOf(p)]
		if !ok || actualP.Target != p.Target {
			toAdd = append(toAdd, p)
		}
	}
	for _, p := range actual {
		desiredP, ok := desiredByKey[keyOf(p)]
		if !ok || desiredP.Target != p.Target {
			toRemove = append(toRemove, p)
		}
	}
	return toAdd, toRemove
}

func imageRecreateReason(actual ActualState, desiredImage string) string {
	if desiredImage != "" && normalizeImage(desiredImage) != normalizeImage(actual.Image) {
		return fmt.Sprintf("image: %s -> %s", actual.Image, desiredImage)
	}
	return ""
}

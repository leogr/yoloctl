package agentbox

import (
	"fmt"
	"strings"

	"github.com/leogr/yoloctl/internal/account"
)

// This file adds the container-label codec the live-reconstruction layer
// relies on.
// There is NO private live-object YAML database: the live agentbox is
// reconstructed from container labels + runtime APIs + VM files. Labels carry
// only compact identity and reconciliation signals, never large payloads or
// secret values.
//
// The label keys defined here extend the identity labels already in live.go
// (LabelAgentbox / LabelSpace / LabelAgent / LabelEnvKeys). Phase 10 wires
// EncodeLabels into container create/recreate and DecodeLabels into the
// LiveLoader.

const (
	// LabelKind marks a container as a yoloctl-managed agentbox. Lets the
	// live loader distinguish agentboxes from any other container without
	// inferring from the image or name.
	LabelKind = "yoloctl.kind"
	// KindAgentbox is the LabelKind value for agentbox containers.
	KindAgentbox = "agentbox"

	// LabelAccount records the plain NAME of the account this box selects
	// (e.g. "default") — the box's recorded identity. A generic-shell box (no
	// real account) carries no account label at all. The reconcile diff
	// compares it against the desired Config.Account to catch an account
	// RE-POINT (a recreate reason); account CONTENT changes are never labeled
	// or diffed. NEVER a secret value or a value-derived hash.
	LabelAccount = "yoloctl.account"

	// LabelNetworkUnconfined marks a box created with the network confinement
	// layer dropped (Config.UnconfinedNetwork → openshell --mode=process). The
	// value is "true" when set; the label is OMITTED entirely for the normal
	// confined box (so it stays absent on every existing box). Lets the
	// reconciler detect a confined↔unconfined toggle as recreate drift and box
	// inspect surface the egress posture, without inspecting the live argv.
	LabelNetworkUnconfined = "yoloctl.network_unconfined"

	// LabelRuntimeMode records the launch mode selected at creation. It is
	// "privileged" for a privileged box (Config.Privileged) and omitted
	// otherwise. It is a record, not evidence: ClassifyRuntime also requires
	// the runtime's privileged setting, PID 1 argv and endpoint bind.
	LabelRuntimeMode = "yoloctl.runtime_mode"
	// LabelLauncher records the privileged endpoint's protocol identity
	// (boxendpoint.Launcher) at creation; a different value is drift.
	LabelLauncher = "yoloctl.launcher"
	// LabelLauncherSHA256 records the endpoint binary verified at creation.
	// A later binary replacement does not replace the running PID 1; this is
	// shown by inspect and is never a drift signal.
	LabelLauncherSHA256 = "yoloctl.launcher_sha256"
)

// MaxLabelValueBytes is the per-label key+value ceiling. containerd keeps
// labels small; large data belongs in the runtime substrate or is recomputed.
// EncodeLabels rejects anything over this so an oversize value fails loudly at
// create time rather than being silently truncated by the runtime.
const MaxLabelValueBytes = 4096

// LabelSet is the typed view of an agentbox's identity/reconciliation labels. It is
// the bridge between the manifest (encode) and the reconstructed live
// object (decode). Every field is non-secret.
type LabelSet struct {
	Space    string
	Agentbox string
	Agent    string
	// EnvKeys is the sorted list of yoloctl-managed env var keys (keys only).
	EnvKeys []string
	// Account is the plain account NAME this box selects, or empty/"none" for
	// a generic-shell box. The non-secret identity signal — a name, never a
	// secret value.
	Account string
	// UnconfinedNetwork records whether the box dropped the network confinement
	// layer (openshell --mode=process). Round-trips via LabelNetworkUnconfined
	// (stamped only when true; absent → false).
	UnconfinedNetwork bool
	// Privileged, Launcher and LauncherSHA256 record a privileged launch.
	// Config.Labels never sets them: the applier adds them from the
	// effective capabilities and the verified endpoint binary.
	Privileged     bool
	Launcher       string
	LauncherSHA256 string
}

// Labels builds the compact identity/reconciliation label set for this
// manifest, ready for EncodeLabels at container create/recreate time.
// Account content never appears in labels because content changes have no
// reconcile signal.
func (c *Config) Labels() LabelSet {
	return LabelSet{
		Space:             c.Space,
		Agentbox:          c.Name,
		Agent:             c.Agent,
		EnvKeys:           managedEnvKeys(c.Env),
		Account:           c.Account,
		UnconfinedNetwork: c.UnconfinedNetwork,
	}
}

// EncodeLabels renders a LabelSet into the container label map, always setting
// LabelKind=agentbox. Empty fields are omitted. Returns an error if any single
// label's key+value exceeds MaxLabelValueBytes (containerd's small-label
// expectation) so the caller fails before nerdctl run.
func EncodeLabels(ls LabelSet) (map[string]string, error) {
	m := map[string]string{
		LabelKind: KindAgentbox,
	}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	put(LabelSpace, ls.Space)
	put(LabelAgentbox, ls.Agentbox)
	put(LabelAgent, ls.Agent)
	put(LabelEnvKeys, strings.Join(ls.EnvKeys, ","))
	// The account NAME is the box's recorded identity: stamped only for
	// a real account, compared by the reconcile diff against the desired
	// Config.Account to catch a re-point. A generic-shell box (Account "" or
	// "none") carries no account label. Never a secret value; account content
	// is never labeled.
	if ls.Account != "" && ls.Account != account.None {
		m[LabelAccount] = ls.Account
	}
	// Network confinement posture: stamp only the unconfined case so the label
	// stays absent on every normal (confined) box, matching the omit-default
	// convention above.
	if ls.UnconfinedNetwork {
		m[LabelNetworkUnconfined] = "true"
	}
	if ls.Privileged {
		m[LabelRuntimeMode] = string(RuntimeModePrivileged)
	}
	put(LabelLauncher, ls.Launcher)
	put(LabelLauncherSHA256, ls.LauncherSHA256)

	for k, v := range m {
		if len(k)+len(v) > MaxLabelValueBytes {
			return nil, fmt.Errorf("label %q exceeds %d bytes (key+value=%d); labels must stay compact (store identity signals, not payloads)",
				k, MaxLabelValueBytes, len(k)+len(v))
		}
	}
	return m, nil
}

// DecodeLabels reconstructs a LabelSet from a container label map. Unknown
// labels are ignored. The inverse of EncodeLabels for the round-trippable
// fields.
func DecodeLabels(m map[string]string) LabelSet {
	ls := LabelSet{
		Space:             m[LabelSpace],
		Agentbox:          m[LabelAgentbox],
		Agent:             m[LabelAgent],
		Account:           m[LabelAccount],
		UnconfinedNetwork: m[LabelNetworkUnconfined] == "true",
		Privileged:        m[LabelRuntimeMode] == string(RuntimeModePrivileged),
		Launcher:          m[LabelLauncher],
		LauncherSHA256:    m[LabelLauncherSHA256],
	}
	if v := m[LabelEnvKeys]; v != "" {
		ls.EnvKeys = strings.Split(v, ",")
	}
	return ls
}

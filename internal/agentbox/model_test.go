package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
)

func featureConfig() *Config {
	return &Config{
		SchemaVersion: 1,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Features:      []string{"claude", "github"},
		Env: map[string]EnvVar{
			"FOO":       {Literal: "bar"},
			"LOG_LEVEL": {Literal: "info"},
		},
	}
}

func TestEncodeDecodeLabelsRoundTrip(t *testing.T) {
	in := LabelSet{
		Space:    "default",
		Agentbox: "coder",
		Agent:    "claude",
		EnvKeys:  []string{"API_KEY", "FOO"},
		Account:  "personal",
	}
	m, err := EncodeLabels(in)
	if err != nil {
		t.Fatal(err)
	}
	if m[LabelKind] != KindAgentbox {
		t.Errorf("kind label = %q, want %q", m[LabelKind], KindAgentbox)
	}
	// The plain account name lands in its own label (non-secret identity;
	// account CONTENT is never labeled).
	if m[LabelAccount] != "personal" {
		t.Errorf("account label = %q, want %q", m[LabelAccount], "personal")
	}
	out := DecodeLabels(m)
	if out.Space != in.Space || out.Agentbox != in.Agentbox || out.Agent != in.Agent {
		t.Errorf("scalar fields did not round-trip: %+v", out)
	}
	if strings.Join(out.EnvKeys, ",") != "API_KEY,FOO" {
		t.Errorf("env keys did not round-trip: %v", out.EnvKeys)
	}
	if out.Account != "personal" {
		t.Errorf("account name did not round-trip: account=%q", out.Account)
	}
}

// TestEncodeDecodeLabelsNetworkUnconfined: the network confinement posture
// round-trips through LabelNetworkUnconfined — stamped only when true, OMITTED
// when false, and an absent label decodes to false. The reconcile recreate-diff
// keys off this, so a box created before the feature (no label) must read as
// confined, never spuriously unconfined.
func TestEncodeDecodeLabelsNetworkUnconfined(t *testing.T) {
	// Unconfined: label present, decodes back to true.
	m, err := EncodeLabels(LabelSet{Agentbox: "open", UnconfinedNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if m[LabelNetworkUnconfined] != "true" {
		t.Errorf("network_unconfined label = %q, want %q", m[LabelNetworkUnconfined], "true")
	}
	if !DecodeLabels(m).UnconfinedNetwork {
		t.Error("UnconfinedNetwork did not round-trip true")
	}

	// Confined (default): label omitted; both the encoded map and an empty map
	// decode to false.
	m2, err := EncodeLabels(LabelSet{Agentbox: "closed", UnconfinedNetwork: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := m2[LabelNetworkUnconfined]; present {
		t.Errorf("confined box must not carry the network_unconfined label, got %v", m2)
	}
	if DecodeLabels(m2).UnconfinedNetwork {
		t.Error("omitted network_unconfined label must decode to false")
	}
	if DecodeLabels(map[string]string{}).UnconfinedNetwork {
		t.Error("empty label map must decode UnconfinedNetwork=false (pre-feature box)")
	}
}

// TestEncodeLabelsGenericShellNoAccountLabels: a generic-shell box (Account
// "none", the no-account sentinel) leaks no account label — there is no
// identity to record. "" (never-set) behaves the same.
func TestEncodeLabelsGenericShellNoAccountLabels(t *testing.T) {
	for _, acct := range []string{account.None, ""} {
		m, err := EncodeLabels(LabelSet{Agentbox: "shell", Account: acct})
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := m[LabelAccount]; ok {
			t.Errorf("account=%q: account label present (=%q) for a generic-shell box", acct, v)
		}
		// And decode tolerates the absence → empty name.
		out := DecodeLabels(m)
		if out.Account != "" {
			t.Errorf("account=%q: decoded a generic-shell box as account=%q", acct, out.Account)
		}
	}
}

// TestEncodeLabelsNoSecretValues is the targeted guard for the account security
// posture: a box selecting a real account with secrets must reach the
// container labels with only the account NAME — no label key or value may
// carry a secret value (or a hash derived from it).
func TestEncodeLabelsNoSecretValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const secretVal = "sk-SECRETVALUE"

	acct := account.New("default", "personal")
	acct.SetSecret("ANTHROPIC_API_KEY", secretVal)
	if err := acct.Save(); err != nil {
		t.Fatalf("save account: %v", err)
	}

	// Build the labels for a box selecting this account through the real
	// Config.Labels path.
	box := &Config{
		SchemaVersion: 1,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       "personal",
		Features:      []string{"claude"},
	}
	ls := box.Labels()
	if ls.Account != "personal" {
		t.Errorf("Labels account = %q, want %q", ls.Account, "personal")
	}

	m, err := EncodeLabels(ls)
	if err != nil {
		t.Fatal(err)
	}
	if m[LabelAccount] != "personal" {
		t.Errorf("account label = %q, want %q", m[LabelAccount], "personal")
	}
	// The crux: no label KEY or VALUE anywhere contains the secret value (and
	// since no value is a hash of it either, asserting the substring is absent
	// from every key+value covers both).
	for k, v := range m {
		if strings.Contains(k, secretVal) {
			t.Errorf("label KEY %q contains the secret value substring %q", k, secretVal)
		}
		if strings.Contains(v, secretVal) {
			t.Errorf("label %q VALUE %q contains the secret value substring %q", k, v, secretVal)
		}
	}
}

func TestEncodeLabelsRejectsOversize(t *testing.T) {
	big := strings.Repeat("x", MaxLabelValueBytes)
	_, err := EncodeLabels(LabelSet{Agentbox: "x", EnvKeys: []string{big}})
	if err == nil {
		t.Fatal("expected oversize label error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q does not explain the size limit", err.Error())
	}
}

func TestManifestComposesPolicyAndLabels(t *testing.T) {
	raw, err := ComposePolicy(featureConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || !strings.Contains(string(raw), "network_policies") {
		t.Errorf("policy not composed from the manifest")
	}
	// Labels() carries identity + managed env keys.
	ls := featureConfig().Labels()
	if ls.Agentbox != "coder" || ls.Agent != "claude" {
		t.Errorf("Labels() = %+v", ls)
	}
	if strings.Join(ls.EnvKeys, ",") != "FOO,LOG_LEVEL" {
		t.Errorf("Labels env keys = %v", ls.EnvKeys)
	}
}

func TestComposeNoPolicyLockedDown(t *testing.T) {
	// A manifest with no features, no capabilities, and no agent is a valid
	// generic-shell box: it composes to a maximally locked-down policy
	// (read-only /workdir, empty network_policies — default-deny egress), NOT
	// an error. The empty capability set is the safest box, not a misconfig.
	raw, err := ComposePolicy(&Config{SchemaVersion: 1, Name: "x", Space: "default"})
	if err != nil {
		t.Fatalf("no-policy manifest must compose to a locked-down policy, got error: %v", err)
	}
	pol := string(raw)
	if !strings.Contains(pol, "network_policies: {}") {
		t.Errorf("no-capability box must compile to empty network_policies (default-deny egress); got:\n%s", pol)
	}
	if !strings.Contains(pol, "/workdir") {
		t.Errorf("locked-down box must still place /workdir (read-only); got:\n%s", pol)
	}
}

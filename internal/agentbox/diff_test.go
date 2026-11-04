package agentbox

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/capabilities"
)

// pfSlice returns a comparison-friendly sorted copy. Port is
// fundamentally a set, but Diff returns slices; sorting before
// reflect.DeepEqual avoids order-dependent test brittleness.
func pfSlice(in []Port) []Port {
	out := append([]Port(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Published < out[j].Published })
	return out
}

func TestDiff_ContainerMissing_NeedsCreate(t *testing.T) {
	c := &Config{
		Name:  "coder",
		Space: "default",
		Image: "img:1",
		Ports: []Port{{Target: 3000, Published: 3000}, {Target: 8080, Published: 8080}},
	}
	plan := Diff(c, ActualState{Exists: false}, c.Image, []byte("policy"))
	if !plan.NeedsCreate {
		t.Fatalf("expected NeedsCreate, plan=%+v", plan)
	}
	if plan.NeedsRecreate || plan.NeedsRestart {
		t.Errorf("multiple strategies set: %+v", plan)
	}
	if plan.CreateReason == "" {
		t.Errorf("CreateReason empty")
	}
	if !reflect.DeepEqual(pfSlice(plan.PortForwardsToAdd), pfSlice(c.Ports)) {
		t.Errorf("PortForwardsToAdd = %+v, want full desired set", plan.PortForwardsToAdd)
	}
	if len(plan.PortForwardsToRemove) != 0 {
		t.Errorf("PortForwardsToRemove should be empty on Create, got %+v", plan.PortForwardsToRemove)
	}
}

func TestDiff_RecreateTriggers(t *testing.T) {
	base := func() (*Config, ActualState) {
		c := &Config{
			Name:      "coder",
			Space:     "default",
			Image:     "img:2",
			Resources: Resources{CPUs: "2", Memory: "2g"},
			// Writable workdir (fs.workdir.write granted), matching the
			// actual ReadOnlyWD:false below — so the base is in sync and each
			// case isolates a single recreate trigger.
			Capabilities: []string{capabilities.WorkdirWriteCapability},
		}
		a := ActualState{
			Exists:        true,
			Image:         "img:2",
			CPUs:          "2",
			Memory:        "2g",
			ReadOnlyWD:    false,
			PolicyContent: []byte("policy"),
		}
		return c, a
	}
	tests := []struct {
		name   string
		mutate func(*Config, *ActualState)
		want   string // substring in RecreateReason
	}{
		{"image", func(c *Config, _ *ActualState) { c.Image = "img:3" }, "image"},
		{"cpus", func(c *Config, _ *ActualState) { c.Resources.CPUs = "4" }, "cpus"},
		{"memory", func(c *Config, _ *ActualState) { c.Resources.Memory = "4g" }, "memory"},
		{"remove memory limit", func(c *Config, _ *ActualState) { c.Resources.Memory = "shared" }, "memory"},
		{"clear memory limit", func(c *Config, _ *ActualState) { c.Resources.Memory = "" }, "memory"},
		{"remove CPU limit", func(c *Config, _ *ActualState) { c.Resources.CPUs = "" }, "cpus"},
		{"workload pool", func(_ *Config, a *ActualState) { a.WorkloadPoolKnown = true }, "workload memory pool"},
		{"workdir read-only flip", func(c *Config, _ *ActualState) { c.Capabilities = nil }, "read-only"},
		{"workdir read-only flip (actual was ro)", func(c *Config, a *ActualState) { a.ReadOnlyWD = true }, "read-only"},
		// Network confinement toggle: openshell --mode is create-only, so a
		// confined↔unconfined flip in either direction must recreate.
		{"network unconfined on", func(c *Config, _ *ActualState) { c.UnconfinedNetwork = true }, "network unconfined"},
		{"network unconfined off (actual was unconfined)", func(_ *Config, a *ActualState) { a.UnconfinedNetwork = true }, "network unconfined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, a := base()
			tt.mutate(c, &a)
			plan := Diff(c, a, c.Image, []byte("policy"))
			if !plan.NeedsRecreate {
				t.Fatalf("expected NeedsRecreate, plan=%+v", plan)
			}
			if !strings.Contains(plan.RecreateReason, tt.want) {
				t.Errorf("RecreateReason = %q, want substring %q", plan.RecreateReason, tt.want)
			}
		})
	}
}

func TestResourceUnitsAndSharedMemoryConverge(t *testing.T) {
	for _, tc := range []struct{ desired, actual string }{{"2g", "2147483648"}, {"2048m", "2g"}, {"shared", "0"}, {"", "0"}} {
		c := &Config{Resources: Resources{CPUs: "2.0", Memory: tc.desired}}
		a := ActualState{CPUs: "2", Memory: tc.actual, ReadOnlyWD: true, WorkloadPoolKnown: true, WorkloadPool: true}
		if reason := recreateReason(c, a, ""); reason != "" {
			t.Fatalf("equivalent resources churn: %s", reason)
		}
	}
}

// TestDiff_RecreateTriggers_V2Fields covers the workdir source, extra
// mounts, managed env, and agent-label comparisons added so an edited
// agentbox YAML can no longer look "in sync" while the live container
// keeps stale config.
func TestDiff_RecreateTriggers_V2Fields(t *testing.T) {
	base := func() (*Config, ActualState) {
		c := &Config{
			Name:         "coder",
			Space:        "default",
			Image:        "img:2",
			Agent:        "claude",
			Workdir:      Workdir{Mode: WorkdirModeHost, Source: "/h/repo", Canonical: "/h/repo"},
			Capabilities: []string{capabilities.WorkdirWriteCapability}, // writable workdir
			// TOKEN has an empty literal: a managed key whose value is set
			// elsewhere (image-baked / runtime), so Diff tracks its presence but
			// never compares its value.
			Env:    map[string]EnvVar{"DEBUG": {Literal: "1"}, "TOKEN": {Literal: ""}},
			Mounts: []Mount{{Source: "/h/state", Target: "/home/sandbox/.claude"}},
		}
		a := ActualState{
			Exists:        true,
			Image:         "img:2",
			PolicyContent: []byte("policy"),
			WorkdirSource: "/h/repo",
			WorkdirDest:   "/workdir/h/repo", // mirrored container path
			// image-baked env (PATH) + the managed entries.
			Env:            []string{"PATH=/usr/bin", "DEBUG=1", "TOKEN=ghp_xxx"},
			ManagedEnvKeys: []string{"DEBUG", "TOKEN"},
			Mounts:         []Mount{{Source: "/h/state", Target: "/home/sandbox/.claude"}},
			Labels:         map[string]string{LabelAgent: "claude"},
		}
		return c, a
	}
	// Sanity: an in-sync base is neither recreate nor restart.
	if c, a := base(); Diff(c, a, c.Image, []byte("policy")).HasChanges() {
		t.Fatalf("base should be fully in sync")
	}

	tests := []struct {
		name   string
		mutate func(*Config, *ActualState)
		want   string
	}{
		{"workdir source", func(c *Config, _ *ActualState) { c.Workdir.Canonical = "/h/other" }, "workdir source"},
		// Container destination changes independently of the source only on
		// the cutover (old bare /workdir bind vs the new mirrored
		// path); prove it forces a recreate on its own.
		{"workdir container path", func(_ *Config, a *ActualState) { a.WorkdirDest = "/workdir" }, "workdir path"},
		{"extra mount source", func(c *Config, _ *ActualState) { c.Mounts[0].Source = "/h/state2" }, "extra mount"},
		{"extra mount added", func(c *Config, _ *ActualState) {
			c.Mounts = append(c.Mounts, Mount{Source: "/x", Target: "/y"})
		}, "extra mounts"},
		{"env added", func(c *Config, _ *ActualState) { c.Env["NEW"] = EnvVar{Literal: "v"} }, "env NEW"},
		{"env literal changed", func(c *Config, _ *ActualState) { c.Env["DEBUG"] = EnvVar{Literal: "2"} }, "env DEBUG"},
		{"env removed", func(c *Config, _ *ActualState) { delete(c.Env, "TOKEN") }, "env TOKEN removed"},
		{"agent label", func(c *Config, _ *ActualState) { c.Agent = "codex" }, "agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, a := base()
			tt.mutate(c, &a)
			plan := Diff(c, a, c.Image, []byte("policy"))
			if !plan.NeedsRecreate {
				t.Fatalf("expected NeedsRecreate, plan=%+v", plan)
			}
			if !strings.Contains(plan.RecreateReason, tt.want) {
				t.Errorf("RecreateReason = %q, want substring %q", plan.RecreateReason, tt.want)
			}
		})
	}

	// An empty-literal managed key's container value differing does NOT force a
	// recreate (only its presence is tracked; the value is set elsewhere).
	c, a := base()
	a.Env = []string{"PATH=/usr/bin", "DEBUG=1", "TOKEN=different"}
	if Diff(c, a, c.Image, []byte("policy")).NeedsRecreate {
		t.Error("empty-literal managed key value change should not trigger recreate")
	}

	// Unpopulated actual fields (older/partial inspector) must not
	// false-positive a recreate. ManagedEnvKeys nil = no env-keys label =
	// the conservative legacy path (no removal detection).
	c, a = base()
	a.WorkdirSource, a.WorkdirDest, a.Env, a.Labels, a.ManagedEnvKeys = "", "", nil, nil, nil
	c.Workdir.Canonical = "/h/changed"
	c.Agent = "codex"
	if Diff(c, a, c.Image, []byte("policy")).NeedsRecreate {
		t.Error("unpopulated actual fields should not trigger recreate")
	}
}

// TestEnvRecreateReason_RemovalDetection covers finding 4 directly: a key
// yoloctl managed last time (recorded in the LabelEnvKeys label, surfaced
// as ManagedEnvKeys) but no longer declared forces a recreate — while the
// legacy path (no label → nil managedKeys) deliberately does not detect
// removals.
func TestEnvRecreateReason_RemovalDetection(t *testing.T) {
	desired := map[string]EnvVar{"DEBUG": {Literal: "1"}}
	// OLD was managed before but is gone from desired now.
	actualEnv := []string{"DEBUG=1", "OLD=x"}

	if got := envRecreateReason(desired, actualEnv, []string{"DEBUG", "OLD"}); !strings.Contains(got, "OLD removed") {
		t.Errorf("with managed keys, a removed key should recreate; got %q", got)
	}
	if got := envRecreateReason(desired, actualEnv, nil); got != "" {
		t.Errorf("without the managed-keys label, removal must NOT be detected; got %q", got)
	}
	// A managed set that's fully in sync yields no reason.
	if got := envRecreateReason(desired, []string{"DEBUG=1"}, []string{"DEBUG"}); got != "" {
		t.Errorf("in-sync managed env should be empty; got %q", got)
	}
}

func TestDiff_PolicyChange_NeedsRestart(t *testing.T) {
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:2",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Ports:        []Port{{Target: 3000, Published: 3000}},
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:2",
		PolicyContent: []byte("old-policy"),
		PortForwards:  []Port{{Target: 3000, Published: 3000}},
	}
	plan := Diff(c, a, c.Image, []byte("new-policy"))
	if !plan.NeedsRestart {
		t.Fatalf("expected NeedsRestart, plan=%+v", plan)
	}
	if plan.NeedsCreate || plan.NeedsRecreate {
		t.Errorf("multiple strategies set: %+v", plan)
	}
	if plan.RestartReason == "" {
		t.Errorf("RestartReason empty")
	}
	// Restart re-applies the full forward set: every desired is in
	// Add, every actual is in Remove (so the Applier can rebuild
	// cleanly).
	if !reflect.DeepEqual(pfSlice(plan.PortForwardsToAdd), pfSlice(c.Ports)) {
		t.Errorf("PortForwardsToAdd on restart = %+v, want full desired", plan.PortForwardsToAdd)
	}
	if !reflect.DeepEqual(pfSlice(plan.PortForwardsToRemove), pfSlice(a.PortForwards)) {
		t.Errorf("PortForwardsToRemove on restart = %+v, want full actual", plan.PortForwardsToRemove)
	}
}

func TestDiff_AllMatch_NoChanges(t *testing.T) {
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:2",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Resources:    Resources{CPUs: "2", Memory: "2g"},
		Ports:        []Port{{Target: 3000, Published: 3000}, {Target: 8080, Published: 8080}},
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:2",
		CPUs:          "2",
		Memory:        "2g",
		PolicyContent: []byte("policy"),
		PortForwards:  []Port{{Target: 3000, Published: 3000}, {Target: 8080, Published: 8080}},
	}
	plan := Diff(c, a, c.Image, []byte("policy"))
	if plan.HasChanges() {
		t.Errorf("expected no changes, plan=%+v", plan)
	}
}

func TestDiff_LiveOnly_PortForwardChanges(t *testing.T) {
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:2",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Ports:        []Port{{Target: 3000, Published: 3000}, {Target: 9090, Published: 9090}}, // add 9090
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:2",
		PolicyContent: []byte("policy"),
		PortForwards:  []Port{{Target: 3000, Published: 3000}, {Target: 8080, Published: 8080}}, // remove 8080
	}
	plan := Diff(c, a, c.Image, []byte("policy"))
	if plan.NeedsCreate || plan.NeedsRecreate || plan.NeedsRestart {
		t.Fatalf("expected live-update only, plan=%+v", plan)
	}
	wantAdd := []Port{{Target: 9090, Published: 9090}}
	wantRemove := []Port{{Target: 8080, Published: 8080}}
	if !reflect.DeepEqual(pfSlice(plan.PortForwardsToAdd), wantAdd) {
		t.Errorf("PortForwardsToAdd = %+v, want %+v", plan.PortForwardsToAdd, wantAdd)
	}
	if !reflect.DeepEqual(pfSlice(plan.PortForwardsToRemove), wantRemove) {
		t.Errorf("PortForwardsToRemove = %+v, want %+v", plan.PortForwardsToRemove, wantRemove)
	}
}

func TestDiff_DefaultHostIPProtocolDoNotChurn(t *testing.T) {
	// A desired port that explicitly restates the DEFAULT host_ip/protocol
	// must not churn against an actual forward reconstructed from a socat unit
	// (which carries only published+target, i.e. default host_ip/protocol).
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:2",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Ports:        []Port{{Target: 3000, Published: 3000, HostIP: "127.0.0.1", Protocol: "tcp"}},
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:2",
		PolicyContent: []byte("policy"),
		PortForwards:  []Port{{Target: 3000, Published: 3000}}, // defaults omitted
	}
	plan := Diff(c, a, c.Image, []byte("policy"))
	if plan.HasChanges() {
		t.Errorf("explicit-default host_ip/protocol should fold to no change, plan=%+v", plan)
	}
}

func TestDiff_SamePublishedPortDifferentTargetPort(t *testing.T) {
	// published 3000 -> target 3000 becomes published 3000 -> target 4000:
	// same published port, new target port. Symmetric diff treats this as a
	// remove (old mapping) + add (new mapping). Applier will sequence
	// remove-then-add.
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:2",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Ports:        []Port{{Target: 4000, Published: 3000}},
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:2",
		PolicyContent: []byte("policy"),
		PortForwards:  []Port{{Target: 3000, Published: 3000}},
	}
	plan := Diff(c, a, c.Image, []byte("policy"))
	if plan.NeedsCreate || plan.NeedsRecreate || plan.NeedsRestart {
		t.Fatalf("expected live-update only, plan=%+v", plan)
	}
	if len(plan.PortForwardsToAdd) != 1 || plan.PortForwardsToAdd[0] != (Port{Target: 4000, Published: 3000}) {
		t.Errorf("PortForwardsToAdd = %+v, want [{target 4000 published 3000}]", plan.PortForwardsToAdd)
	}
	if len(plan.PortForwardsToRemove) != 1 || plan.PortForwardsToRemove[0] != (Port{Target: 3000, Published: 3000}) {
		t.Errorf("PortForwardsToRemove = %+v, want [{target 3000 published 3000}]", plan.PortForwardsToRemove)
	}
}

func TestNormalizeImage(t *testing.T) {
	// distribution/reference adds an implicit `:latest` to untagged
	// names so `ubuntu` and `ubuntu:latest` (and nerdctl's
	// `docker.io/library/ubuntu:latest` inspect output) all
	// canonicalize together.
	cases := []struct {
		in, want string
	}{
		{"ubuntu", "docker.io/library/ubuntu:latest"},
		{"ubuntu:24.04", "docker.io/library/ubuntu:24.04"},
		{"yoloctl/agentbox:ubuntu-24.04", "docker.io/yoloctl/agentbox:ubuntu-24.04"},
		{"docker.io/yoloctl/agentbox:ubuntu-24.04", "docker.io/yoloctl/agentbox:ubuntu-24.04"},
		{"docker.io/library/ubuntu", "docker.io/library/ubuntu:latest"},
		{"ghcr.io/leogr/yoloctl:latest", "ghcr.io/leogr/yoloctl:latest"},
		{"registry.example.com:5000/myimage:v1", "registry.example.com:5000/myimage:v1"},
		{"localhost/myimage:v1", "localhost/myimage:v1"},
		{"localhost:5000/myimage:v1", "localhost:5000/myimage:v1"},
		{"", ""},
		// Malformed: not parseable, falls back to the literal.
		{"not_a_valid_image::", "not_a_valid_image::"},
	}
	for _, c := range cases {
		if got := normalizeImage(c.in); got != c.want {
			t.Errorf("normalizeImage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDiff_NormalizedImagesMatch(t *testing.T) {
	// Regression: previously a Config with `image: yoloctl/agentbox:tag`
	// would trigger NeedsRecreate against an actual state of
	// `docker.io/yoloctl/agentbox:tag` (which is what nerdctl inspect
	// reports). Now normalizeImage canonicalizes both sides.
	c := &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "yoloctl/agentbox:ubuntu-24.04",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
	}
	a := ActualState{
		Exists:        true,
		Image:         "docker.io/yoloctl/agentbox:ubuntu-24.04",
		PolicyContent: []byte("policy"),
	}
	plan := Diff(c, a, c.Image, []byte("policy"))
	if plan.HasChanges() {
		t.Errorf("expected no changes after image normalization, plan=%+v", plan)
	}
}

// TestDiff_AccountContentChangesNoAction freezes the account-content contract:
// account CONTENT is not a Diff input at all — an in-sync
// box stays in sync no matter how its account was edited. Native state is
// never synchronized; create-time environment injection changes only on an
// explicit recreate, never through reconcile.
func TestDiff_AccountContentChangesNoAction(t *testing.T) {
	c := &Config{
		Name:         "claude-box",
		Space:        "default",
		Image:        "img:2",
		Agent:        "claude",
		Account:      "default",
		Capabilities: []string{capabilities.WorkdirWriteCapability},
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:2",
		PolicyContent: []byte("policy"),
		Account:       "default",
	}
	// There is no account-content parameter to vary: the signature itself
	// guarantees content changes cannot produce a plan. The in-sync case must
	// report no changes.
	if plan := Diff(c, a, c.Image, []byte("policy")); plan.HasChanges() {
		t.Errorf("in-sync real-account box must have no changes, plan=%+v", plan)
	}
}

// TestDiff_AccountRepointRecreates covers the account re-point rule: pointing a
// BOX at a different account (desired Config.Account vs the recorded
// yoloctl.account label) is an EXPLICIT recreate reason — not inferred from
// mount changes, so it also covers injection-only agents with no state leaf.
func TestDiff_AccountRepointRecreates(t *testing.T) {
	base := func() (*Config, ActualState) {
		c := &Config{
			Name:         "claude-box",
			Space:        "default",
			Image:        "img:2",
			Agent:        "claude",
			Account:      "default",
			Capabilities: []string{capabilities.WorkdirWriteCapability},
		}
		a := ActualState{
			Exists:        true,
			Image:         "img:2",
			PolicyContent: []byte("policy"),
			Account:       "default",
		}
		return c, a
	}

	// Sanity: matching account names are in sync.
	if c, a := base(); Diff(c, a, c.Image, []byte("policy")).HasChanges() {
		t.Fatalf("matching account should be in sync")
	}

	// Re-point: the box now selects "personal" but the container recorded
	// "default" → recreate with the explicit reason.
	c, a := base()
	c.Account = "personal"
	plan := Diff(c, a, c.Image, []byte("policy"))
	if !plan.NeedsRecreate {
		t.Fatalf("expected NeedsRecreate on account re-point, plan=%+v", plan)
	}
	if !strings.Contains(plan.RecreateReason, "account: default -> personal") {
		t.Errorf("RecreateReason = %q, want the re-point reason", plan.RecreateReason)
	}

	// An uninspected/absent account label opts out (no false recreate).
	c, a = base()
	c.Account = "personal"
	a.Account = ""
	if Diff(c, a, c.Image, []byte("policy")).NeedsRecreate {
		t.Error("missing recorded account label must not trigger a re-point recreate")
	}

	// A generic-shell box never compares accounts, regardless of a
	// (defensive) recorded label.
	c, a = base()
	c.Agent, c.Account = account.None, account.None
	a.Account = "default"
	if Diff(c, a, c.Image, []byte("policy")).NeedsRecreate {
		t.Error("generic-shell box must not trigger a re-point recreate")
	}
}

func TestDiff_StrategyPriority(t *testing.T) {
	// When multiple triggers would fire (recreate AND restart),
	// the stronger one wins. Verify recreate beats restart when
	// both would apply.
	c := &Config{
		Name:  "coder",
		Space: "default",
		Image: "img:NEW",
	}
	a := ActualState{
		Exists:        true,
		Image:         "img:OLD",
		PolicyContent: []byte("old-policy"),
	}
	plan := Diff(c, a, c.Image, []byte("new-policy")) // both image and policy differ
	if !plan.NeedsRecreate {
		t.Fatalf("expected NeedsRecreate (stronger than restart), plan=%+v", plan)
	}
	if plan.NeedsRestart {
		t.Errorf("NeedsRestart should be false when NeedsRecreate is true")
	}
}

func TestDiffImageDriftRemainsAvailableBesideAnotherRecreateReason(t *testing.T) {
	cfg := &Config{Name: "worker", Space: "work", Privileged: true}
	actual := ActualState{Exists: true, Image: "local/test:old"}
	plan := Diff(cfg, actual, "local/test:new", nil)
	if !plan.NeedsRecreate || plan.ImageRecreateReason != "image: local/test:old -> local/test:new" || plan.RecreateReason == plan.ImageRecreateReason {
		t.Fatalf("lost independent image drift: %+v", plan)
	}
	actual.Image = "docker.io/library/alpine:latest"
	plan = Diff(&Config{}, actual, "alpine", nil)
	if plan.ImageRecreateReason != "" {
		t.Fatalf("image normalization changed: %+v", plan)
	}
}

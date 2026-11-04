package agentbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/sandbox"
	"gopkg.in/yaml.v3"
)

func privilegedConfig() *Config {
	c := validConfig()
	c.Privileged = true
	return c
}

func writeUserFeature(t *testing.T, name string, requires ...string) {
	t.Helper()
	dir, err := layout.FeaturesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "name: " + name + "\ndescription: test\nrequires_capabilities: [" + strings.Join(requires, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Only the box setting selects privilege. The retired capability, granted
// explicitly or required by a feature, resolves to nothing and explains the
// setting instead.
func TestPrivilegedComesOnlyFromTheBoxSetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	recipe, err := RuntimeFeatures(privilegedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if recipe[len(recipe)-1] != features.PrivilegedRuntime || slices.Index(recipe, features.PrivilegedRuntime) != len(recipe)-1 {
		t.Fatalf("installer not appended exactly once, last: %q", recipe)
	}
	ordinary := validConfig()
	strict, err := EffectiveFeaturesStrict(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := RuntimeFeatures(ordinary); err != nil || !slices.Equal(got, strict) {
		t.Fatalf("ordinary recipe changed: %q vs %q (%v)", got, strict, err)
	}
	writeUserFeature(t, "kernel-dev", capabilities.RetiredPrivilegedCapability)
	explicit := validConfig()
	explicit.Capabilities = append(explicit.Capabilities, capabilities.RetiredPrivilegedCapability)
	viaFeature := validConfig()
	viaFeature.Features = append(viaFeature.Features, "kernel-dev")
	for name, c := range map[string]*Config{"explicit": explicit, "feature": viaFeature} {
		_, err := ComposePolicy(c)
		if err == nil || !strings.Contains(err.Error(), "privileged: true") || !strings.Contains(err.Error(), "--privileged") {
			t.Errorf("%s: retired privilege = %v", name, err)
		}
		if recipe, err := RuntimeFeatures(c); slices.Contains(recipe, features.PrivilegedRuntime) {
			t.Errorf("%s: recipe = %q, %v", name, recipe, err)
		}
	}
	if _, err := ComposePolicy(explicit); !errors.Is(err, capabilities.ErrRetiredPrivileged) {
		t.Fatalf("explicit retired grant = %v", err)
	}
}

// A stored declaration that still grants the retired capability is read as
// written, with or without the setting, and refused before any save, policy or
// reconcile; nothing converts or rewrites it, and the grant never selects
// privilege.
func TestRetiredPrivilegedDeclarationIsRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	legacy := validConfig()
	legacy.SchemaVersion = SchemaVersion
	legacy.Capabilities = []string{capabilities.WorkdirWriteCapability, capabilities.RetiredPrivilegedCapability, capabilities.PTYAllocateCapability}
	raw, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path, err := layout.BoxPath(legacy.Space, legacy.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		extra      string
		privileged bool
	}{
		"grant only":             {"", false},
		"with privileged: true":  {"privileged: true\n", true},
		"with privileged: false": {"privileged: false\n", false},
	} {
		declared := append(slices.Clone(raw), tc.extra...)
		if err := os.WriteFile(path, declared, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(legacy.Space, legacy.Name)
		if err != nil || got.Privileged != tc.privileged || !slices.Equal(got.Capabilities, legacy.Capabilities) {
			t.Fatalf("%s: not read as written: %+v, %v", name, got, err)
		}
		if recipe, err := RuntimeFeatures(got); err != nil || slices.Contains(recipe, features.PrivilegedRuntime) != tc.privileged {
			t.Errorf("%s: recipe = %q, %v", name, recipe, err)
		}
		if err := got.Validate(); !errors.Is(err, capabilities.ErrRetiredPrivileged) {
			t.Errorf("%s: validate = %v", name, err)
		}
		if err := got.Save(); !errors.Is(err, capabilities.ErrRetiredPrivileged) {
			t.Errorf("%s: save = %v", name, err)
		}
		if _, err := ComposePolicy(got); !errors.Is(err, capabilities.ErrRetiredPrivileged) {
			t.Errorf("%s: policy = %v", name, err)
		}
		inspector, applier := &fakeInspector{}, &fakeApplier{}
		if _, err := Reconcile(t.Context(), got, inspector, applier); !errors.Is(err, capabilities.ErrRetiredPrivileged) || inspector.calls != 0 || len(applier.createCalls)+len(applier.recreateCalls)+len(applier.restartCalls) != 0 {
			t.Errorf("%s: reconcile = %v after %d inspections, applier %+v", name, err, inspector.calls, applier)
		}
		if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, declared) {
			t.Fatalf("%s: the declaration was rewritten: %v", name, err)
		}
	}
}

// The explicit setting round-trips both ways: true is stored and read back,
// false is the default and stays absent, and an explicit false parses.
func TestPrivilegedSettingRoundTrips(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, privileged := range []bool{true, false} {
		c := validConfig()
		c.Privileged = privileged
		if err := c.Save(); err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(c.Path())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(saved), "privileged: true") != privileged || strings.Contains(string(saved), "privileged: false") {
			t.Fatalf("privileged=%v saved:\n%s", privileged, saved)
		}
		got, err := Load(c.Space, c.Name)
		if err != nil || got.Privileged != privileged || !slices.Equal(got.Capabilities, c.Capabilities) {
			t.Fatalf("privileged=%v reloaded = %+v, %v", privileged, got, err)
		}
	}
	for text, want := range map[string]bool{"privileged: true\n": true, "privileged: false\n": false} {
		c, err := ParseManifest([]byte("schema_version: " + strconv.Itoa(SchemaVersion) + "\nname: x\nspace: default\naccount: none\n" + text))
		if err != nil || c.Privileged != want {
			t.Fatalf("%q parsed = %+v, %v", text, c, err)
		}
	}
}

// The internal installer feeds nothing back into capability resolution and
// cannot be declared or listed.
func TestPrivilegedInstallerIsInternalAndCapabilityFree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := features.Lookup(features.PrivilegedRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.RequiresCapabilities) != 0 || f.Install == nil || !strings.Contains(f.Install.Dockerfile, features.PrivilegedSudoers) || !strings.Contains(f.Install.Dockerfile, "chmod 0440") {
		t.Fatalf("installer = %+v", f)
	}
	if slices.Contains(features.Names(), features.PrivilegedRuntime) || slices.Contains(features.BuiltinNames(), features.PrivilegedRuntime) {
		t.Fatal("internal installer is listed")
	}
	declared := validConfig()
	declared.Features = []string{features.PrivilegedRuntime}
	if err := declared.Validate(); err == nil {
		t.Fatal("internal installer accepted in a declaration")
	}
	for _, needle := range []string{"linux-modules", "linux-headers", "modprobe"} {
		if strings.Contains(f.Install.Dockerfile, needle) {
			t.Fatalf("installer fetches kernel material: %s", needle)
		}
	}
}

func TestPrivilegedChangesOnlyItsOwnImageRecipe(t *testing.T) {
	ordinary, privileged := validConfig(), privilegedConfig()
	a, _, err := ResolvedImage(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	b, composed, err := ResolvedImage(privileged)
	if err != nil {
		t.Fatal(err)
	}
	again, _, _ := ResolvedImage(privilegedConfig())
	if a == b || !composed || b != again {
		t.Fatalf("recipes: ordinary %s privileged %s (%v) again %s", a, b, composed, again)
	}
	// A generic box with no other installs still gets a composed image.
	bare := &Config{Name: "x", Space: "default", Image: "img:1", Workdir: NewEphemeralWorkdir("x"), Privileged: true}
	if ref, composed, err := ResolvedImage(bare); err != nil || !composed || ref == "img:1" {
		t.Fatalf("bare privileged image = %s, %v, %v", ref, composed, err)
	}
}

// The other capabilities keep their meaning: the composed policy and the
// policy golden for an ordinary box are unchanged; a privileged box records
// the same policy for a later confined recreate.
func TestPrivilegedPolicyIsRecordedNotWidened(t *testing.T) {
	ordinary, privileged := mustCompose(t, validConfig()), mustCompose(t, privilegedConfig())
	if string(ordinary) != string(privileged) {
		t.Fatalf("privileged capability changed the compiled policy:\n%s\nvs\n%s", ordinary, privileged)
	}
}

func privilegedDetails(c *Config) *container.Details {
	opts, _ := buildRunOptions(c, "img", &EndpointBinary{SHA256: "abc"})
	yes := true
	d := &container.Details{Name: c.Name, Labels: opts.Labels, Command: opts.Command, Privileged: &yes}
	for _, m := range opts.Mounts {
		d.Mounts = append(d.Mounts, container.DetailMount(m))
	}
	return d
}

func confinedDetails(c *Config) *container.Details {
	opts, _ := buildRunOptions(c, "img", nil)
	no := false
	d := &container.Details{Name: c.Name, Labels: opts.Labels, Command: opts.Command, Privileged: &no}
	for _, m := range opts.Mounts {
		d.Mounts = append(d.Mounts, container.DetailMount(m))
	}
	return d
}

func TestPrivilegedRunOptions(t *testing.T) {
	c := privilegedConfig()
	opts, err := buildRunOptions(c, "img", &EndpointBinary{SHA256: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{sandbox.EntrypointShimPath}, boxendpoint.Args(c.Workdir.ContainerPath(), sandbox.SSHSocketPath)...)
	if !opts.Privileged || !slices.Equal(opts.Command, want) {
		t.Fatalf("privileged=%v command=%q", opts.Privileged, opts.Command)
	}
	if opts.Labels[LabelRuntimeMode] != "privileged" || opts.Labels[LabelLauncher] != boxendpoint.Launcher || opts.Labels[LabelLauncherSHA256] != "abc" {
		t.Fatalf("labels = %v", opts.Labels)
	}
	if !slices.Contains(opts.Mounts, container.Mount{Source: boxendpoint.BinaryDir, Destination: boxendpoint.BinaryDir, ReadOnly: true}) {
		t.Fatalf("endpoint directory bind missing: %+v", opts.Mounts)
	}
	ordinary, err := buildRunOptions(c, "img", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only the retired interpreter binds disappear; policy data, socket,
	// workdir and the actual privileged endpoint remain unchanged.
	wantMounts := []container.Mount{
		{Source: sandbox.AgentboxPolicyHostPath(c.Name), Destination: sandbox.PolicyContainerYAMLPath, ReadOnly: true},
		{Source: sandbox.SocketDirHost(c.Name), Destination: sandbox.SocketDirContainer},
		{Source: c.Workdir.MountSource(), Destination: c.Workdir.ContainerPath(), ReadOnly: !c.WorkdirWritable()},
		{Source: boxendpoint.BinaryDir, Destination: boxendpoint.BinaryDir, ReadOnly: true},
	}
	if !slices.Equal(opts.Mounts, wantMounts) || !slices.Equal(opts.Env, ordinary.Env) || opts.User != ordinary.User || !slices.Equal(opts.Sysctl, ordinary.Sysctl) {
		t.Fatalf("privileged launch changed ordinary options:\n%+v\n%+v", opts, ordinary)
	}
	if ordinary.Privileged || ordinary.Labels[LabelRuntimeMode] != "" || ordinary.Labels[LabelLauncher] != "" {
		t.Fatalf("ordinary options carry privilege: %+v", ordinary)
	}
}

func TestClassifyRuntimeRequiresAgreeingEvidence(t *testing.T) {
	c := privilegedConfig()
	if ev := ClassifyRuntime(privilegedDetails(c)); ev.Mode != RuntimeModePrivileged || ev.Launcher != boxendpoint.Launcher || !ev.AnyPrivileged {
		t.Fatalf("privileged = %+v", ev)
	}
	if ev := ClassifyRuntime(confinedDetails(validConfig())); ev.Mode != RuntimeModeConfined || ev.AnyPrivileged {
		t.Fatalf("confined = %+v", ev)
	}
	no := false
	for name, mutate := range map[string]func(*container.Details){
		"runtime not privileged": func(d *container.Details) { d.Privileged = &no },
		"privilege unreported":   func(d *container.Details) { d.Privileged = nil },
		"label only missing":     func(d *container.Details) { delete(d.Labels, LabelRuntimeMode) },
		"openshell command":      func(d *container.Details) { d.Command = confinedDetails(c).Command },
		"no endpoint bind":       func(d *container.Details) { d.Mounts = confinedDetails(c).Mounts },
		"writable endpoint bind": func(d *container.Details) {
			for i := range d.Mounts {
				if d.Mounts[i].Destination == boxendpoint.BinaryDir {
					d.Mounts[i].ReadOnly = false
				}
			}
		},
		"foreign bind source": func(d *container.Details) {
			for i := range d.Mounts {
				if d.Mounts[i].Destination == boxendpoint.BinaryDir {
					d.Mounts[i].Source = "/tmp/fake"
				}
			}
		},
	} {
		d := privilegedDetails(c)
		mutate(d)
		if ev := ClassifyRuntime(d); ev.Mode == RuntimeModePrivileged {
			t.Errorf("%s: classified privileged: %+v", name, ev)
		} else if ev.Mode == RuntimeModeConfined {
			t.Errorf("%s: partial privilege classified confined: %+v", name, ev)
		}
	}
	// A confined runtime with any privileged signal is not proven confined.
	yes := true
	for name, mutate := range map[string]func(*container.Details){
		"recorded privileged": func(d *container.Details) { d.Privileged = &yes },
		"stale mode label":    func(d *container.Details) { d.Labels[LabelRuntimeMode] = "privileged" },
		"launcher label":      func(d *container.Details) { d.Labels[LabelLauncher] = boxendpoint.Launcher },
	} {
		d := confinedDetails(validConfig())
		mutate(d)
		if ev := ClassifyRuntime(d); ev.Mode != RuntimeModeUnknown || !ev.AnyPrivileged {
			t.Errorf("%s: %+v", name, ev)
		}
	}
	if ev := ClassifyRuntime(nil); ev.Mode != RuntimeModeUnknown || ev.Reason == "" {
		t.Fatalf("nil = %+v", ev)
	}
}

func provedPrivilegedActual(t *testing.T, c *Config) ActualState {
	t.Helper()
	return ActualState{
		Exists:        true,
		Image:         mustResolveImage(t, c),
		ReadOnlyWD:    !c.WorkdirWritable(),
		PolicyContent: mustCompose(t, c),
		Runtime:       ClassifyRuntime(privilegedDetails(c)),
	}
}

func TestPrivilegedModeChangesAreCreateOnlyDrift(t *testing.T) {
	c := privilegedConfig()
	confined := provedPrivilegedActual(t, c)
	confined.Runtime = ClassifyRuntime(confinedDetails(validConfig()))
	for name, tc := range map[string]struct {
		desired *Config
		actual  ActualState
		reason  string
	}{
		"enable":           {c, confined, "runtime mode: confined -> privileged"},
		"unknown evidence": {c, ActualState{Exists: true, Image: mustResolveImage(t, c), PolicyContent: mustCompose(t, c)}, "runtime mode: unknown"},
		"old launcher": {c, func() ActualState {
			a := provedPrivilegedActual(t, c)
			a.Runtime = ClassifyRuntime(withProtocol(privilegedDetails(c), "0", boxendpoint.LauncherFor("0")))
			return a
		}(), "privileged launcher protocol"},
		"disable": {validConfig(), func() ActualState {
			a := provedPrivilegedActual(t, c)
			a.Image = mustResolveImage(t, validConfig())
			return a
		}(), "runtime mode: privileged -> confined"},
	} {
		plan := Diff(tc.desired, tc.actual, mustResolveImage(t, tc.desired), mustCompose(t, tc.desired))
		if !plan.NeedsRecreate || !strings.HasPrefix(plan.RecreateReason, tc.reason) {
			t.Errorf("%s: plan = %+v", name, plan)
		}
		if err := checkStartPlan(tc.desired, plan); err == nil {
			t.Errorf("%s: start would replace the container", name)
		}
	}
	// An ordinary box with an ordinary inspection is unchanged.
	ordinary := validConfig()
	actual := ActualState{Exists: true, Image: mustResolveImage(t, ordinary), PolicyContent: mustCompose(t, ordinary)}
	if plan := Diff(ordinary, actual, mustResolveImage(t, ordinary), mustCompose(t, ordinary)); plan.HasChanges() {
		t.Fatalf("ordinary box drifted: %+v", plan)
	}
}

// Policy bytes are not enforced in a proved privileged runtime, so a
// policy-only change never restarts it; everything else still does.
func TestPolicyOnlyChangeDoesNotRestartPrivilegedBox(t *testing.T) {
	old := privilegedConfig()
	actual := provedPrivilegedActual(t, old)
	desired := privilegedConfig()
	desired.Features = append(desired.Features, "github") // policy-only: no install
	if string(mustCompose(t, desired)) == string(actual.PolicyContent) {
		t.Fatal("fixture did not change the policy")
	}
	inspector := &fakeInspector{state: actual}
	applier := &fakeApplier{}
	result, err := Reconcile(t.Context(), desired, inspector, applier)
	if err != nil || result.HasChanges() || len(applier.restartCalls)+len(applier.recreateCalls) != 0 {
		t.Fatalf("result=%+v err=%v applier=%+v", result, err, applier)
	}
	plan := Diff(desired, actual, mustResolveImage(t, desired), mustCompose(t, desired))
	if !strings.Contains(plan.PolicyNote, "not enforced while privileged") {
		t.Fatalf("plan note = %q", plan.PolicyNote)
	}
	// Unknown evidence never earns the exemption.
	unknown := actual
	unknown.Runtime = RuntimeEvidence{}
	if plan := Diff(desired, unknown, mustResolveImage(t, desired), mustCompose(t, desired)); !plan.NeedsRecreate {
		t.Fatalf("unknown runtime plan = %+v", plan)
	}
	// An ordinary box with the same change still restarts.
	ordinary := validConfig()
	ordinaryActual := ActualState{Exists: true, Image: mustResolveImage(t, ordinary), PolicyContent: []byte("old")}
	if plan := Diff(ordinary, ordinaryActual, mustResolveImage(t, ordinary), mustCompose(t, ordinary)); !plan.NeedsRestart {
		t.Fatalf("ordinary plan = %+v", plan)
	}
	// Create-only changes on a privileged box still recreate.
	moved := privilegedConfig()
	moved.Mounts = []Mount{{Source: "/host/x", Target: "/workdir/x"}}
	if plan := Diff(moved, actual, mustResolveImage(t, moved), mustCompose(t, moved)); !plan.NeedsRecreate {
		t.Fatalf("mount change plan = %+v", plan)
	}
}

func TestRuntimeModeChangeNeedsExplicitRecreate(t *testing.T) {
	ctx := context.Background()
	privileged, ordinary := privilegedConfig(), validConfig()
	if err := CheckRuntimeModeChange(ctx, privileged, confinedDetails(ordinary)); err == nil || !strings.Contains(err.Error(), "box recreate") {
		t.Fatalf("enable = %v", err)
	}
	if err := CheckRuntimeModeChange(ctx, ordinary, privilegedDetails(privileged)); err == nil {
		t.Fatal("disable allowed without explicit recreate")
	}
	if err := CheckRuntimeModeChange(ctx, privileged, privilegedDetails(privileged)); err != nil {
		t.Fatalf("privileged repair refused: %v", err)
	}
	if err := CheckRuntimeModeChange(ctx, ordinary, confinedDetails(ordinary)); err != nil {
		t.Fatalf("ordinary recreate refused: %v", err)
	}
	explicit := WithRuntimeModeChange(ctx)
	if err := CheckRuntimeModeChange(explicit, privileged, confinedDetails(ordinary)); err != nil {
		t.Fatalf("explicit enable refused: %v", err)
	}
	if err := CheckRuntimeModeChange(explicit, ordinary, privilegedDetails(privileged)); err != nil {
		t.Fatalf("explicit disable refused: %v", err)
	}
}

func privilegedFixtureInspector(t *testing.T, c *Config, d *container.Details) LiveInspector {
	t.Helper()
	inspector := fixtureLiveInspector(t, c, d.Command, nil)
	var mounts []map[string]any
	for _, m := range d.Mounts {
		mounts = append(mounts, map[string]any{"Source": m.Source, "Destination": m.Destination, "RW": !m.ReadOnly})
	}
	labels := map[string]string{resources.WorkloadLabel: resources.WorkloadVersion}
	for k, v := range d.Labels {
		labels[k] = v
	}
	raw, err := json.Marshal([]any{map[string]any{
		"Name": c.Name, "Path": d.Command[0], "Args": d.Command[1:],
		"Config":     map[string]any{"Image": mustResolveImage(t, c), "Labels": labels},
		"Mounts":     mounts,
		"HostConfig": map[string]any{"Memory": 0, "Privileged": d.Privileged != nil && *d.Privileged},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("WORKDIR_TEST_FIXTURE"), "inspect.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return inspector
}

// The live inspector reads the endpoint launch, identifies its workdir and
// treats the endpoint bind as infrastructure, so an in-sync privileged box
// converges with no action.
func TestLiveInspectorConvergesPrivilegedRuntime(t *testing.T) {
	c := privilegedConfig()
	inspector := privilegedFixtureInspector(t, c, privilegedDetails(c))
	state, err := inspector.Inspect(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if state.Runtime.Mode != RuntimeModePrivileged || state.WorkdirDest != c.Workdir.ContainerPath() || len(state.Mounts) != 0 {
		t.Fatalf("state = %+v", state)
	}
	applier := &fakeApplier{}
	result, err := Reconcile(t.Context(), c, inspector, applier)
	if err != nil || result.HasChanges() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// A launch recorded for another protocol is drift, not uncertainty.
	old := privilegedDetails(c)
	for i, arg := range old.Command {
		if arg == "--protocol" {
			old.Command[i+1] = "0"
		}
	}
	if _, err := liveWorkdirMount(old); err != nil {
		t.Fatalf("old protocol workdir: %v", err)
	}
}

// A replaced endpoint binary does not replace a running PID 1: only the
// recorded protocol is compared, never the binary hash recorded at creation.
func TestLauncherBinaryReplacementIsNotDrift(t *testing.T) {
	c := privilegedConfig()
	d := privilegedDetails(c)
	d.Labels[LabelLauncherSHA256] = "a-different-build"
	actual := provedPrivilegedActual(t, c)
	actual.Runtime = ClassifyRuntime(d)
	if plan := Diff(c, actual, mustResolveImage(t, c), mustCompose(t, c)); plan.HasChanges() {
		t.Fatalf("binary replacement drifted: %+v", plan)
	}
}

// The network declaration is kept as written; the privileged launch simply has
// no inner namespace, and both settings stay consistent across inspection.
func TestPrivilegedCoexistsWithUnconfinedNetwork(t *testing.T) {
	c := privilegedConfig()
	c.UnconfinedNetwork = true
	opts, err := buildRunOptions(c, "img", &EndpointBinary{SHA256: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Labels[LabelNetworkUnconfined] != "true" || !opts.Privileged || slices.Contains(opts.Command, "--mode") {
		t.Fatalf("options = %+v", opts)
	}
	d := privilegedDetails(c)
	actual := provedPrivilegedActual(t, c)
	actual.UnconfinedNetwork = DecodeLabels(d.Labels).UnconfinedNetwork
	actual.Runtime = ClassifyRuntime(d)
	if plan := Diff(c, actual, mustResolveImage(t, c), mustCompose(t, c)); plan.HasChanges() {
		t.Fatalf("plan = %+v", plan)
	}
	if spec := portSpec(c, Port{Published: 8080, Target: 80}); !spec.Direct {
		t.Fatalf("port spec = %+v", spec)
	}
	if spec := portSpec(validConfig(), Port{Published: 8080, Target: 80}); spec.Direct {
		t.Fatalf("ordinary port spec = %+v", spec)
	}
}

// withProtocol rewrites a privileged launch's recorded argv protocol and its
// launcher label.
func withProtocol(d *container.Details, protocol, launcher string) *container.Details {
	d.Command = slices.Clone(d.Command)
	for i, arg := range d.Command {
		if arg == "--protocol" && i+1 < len(d.Command) {
			d.Command[i+1] = protocol
		}
	}
	if launcher == "" {
		delete(d.Labels, LabelLauncher)
	} else {
		d.Labels[LabelLauncher] = launcher
	}
	return d
}

// The recorded argv protocol must agree with the launcher label. Agreeing
// older evidence stays privileged and needs recreation; anything inconsistent
// is unknown but keeps its privileged signal. Neither earns the current
// runtime's policy-only exemption, and start refuses both.
func TestRuntimeProtocolMustAgreeWithLauncher(t *testing.T) {
	c := privilegedConfig()
	changed := privilegedConfig()
	changed.Features = append(changed.Features, "github") // policy-only change
	malformed := privilegedDetails(c)
	malformed.Command = append(slices.Clone(malformed.Command), "--unknown", "x")
	for name, tc := range map[string]struct {
		details  *container.Details
		mode     RuntimeMode
		protocol string
		reason   string
	}{
		"current argv and label":    {privilegedDetails(c), RuntimeModePrivileged, boxendpoint.ProtocolVersion, ""},
		"newer argv, current label": {withProtocol(privilegedDetails(c), "2", boxendpoint.Launcher), RuntimeModeUnknown, "2", "runtime mode: unknown"},
		"current argv, other label": {withProtocol(privilegedDetails(c), boxendpoint.ProtocolVersion, boxendpoint.LauncherFor("0")), RuntimeModeUnknown, boxendpoint.ProtocolVersion, "runtime mode: unknown"},
		"agreeing older protocol":   {withProtocol(privilegedDetails(c), "0", boxendpoint.LauncherFor("0")), RuntimeModePrivileged, "0", "privileged launcher protocol"},
		"missing launcher label":    {withProtocol(privilegedDetails(c), boxendpoint.ProtocolVersion, ""), RuntimeModeUnknown, boxendpoint.ProtocolVersion, "runtime mode: unknown"},
		"malformed launch":          {malformed, RuntimeModeUnknown, "", "runtime mode: unknown"},
	} {
		ev := ClassifyRuntime(tc.details)
		if ev.Mode != tc.mode || ev.Protocol != tc.protocol || !ev.AnyPrivileged {
			t.Errorf("%s: evidence = %+v", name, ev)
			continue
		}
		actual := provedPrivilegedActual(t, c)
		actual.Runtime = ev
		for _, desired := range []*Config{c, changed} {
			plan := Diff(desired, actual, mustResolveImage(t, desired), mustCompose(t, desired))
			if tc.reason == "" {
				if desired == c && plan.HasChanges() || desired == changed && (plan.HasChanges() || plan.PolicyNote == "") {
					t.Errorf("%s: current runtime plan = %+v", name, plan)
				}
				continue
			}
			if !plan.NeedsRecreate || !strings.HasPrefix(plan.RecreateReason, tc.reason) || plan.PolicyNote != "" {
				t.Errorf("%s: plan = %+v", name, plan)
			}
			if err := checkStartPlan(desired, plan); err == nil {
				t.Errorf("%s: start would preserve the runtime", name)
			}
		}
		// Every case is privileged for authority and mode-change purposes.
		if err := CheckRuntimeModeChange(context.Background(), validConfig(), tc.details); err == nil {
			t.Errorf("%s: an ordinary recreate over it is not a mode change", name)
		}
	}
	// The workdir of an inconsistent launch is still identified for drift.
	if _, err := liveWorkdirMount(withProtocol(privilegedDetails(c), "2", boxendpoint.Launcher)); err != nil {
		t.Fatal(err)
	}
}

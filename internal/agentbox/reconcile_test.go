package agentbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
)

// fakeInspector returns a pre-canned ActualState (or error).
type fakeInspector struct {
	state ActualState
	err   error
	calls int
}

func (f *fakeInspector) Inspect(_ context.Context, _ *Config) (ActualState, error) {
	f.calls++
	return f.state, f.err
}

// fakeApplier records every call and returns a per-method canned
// error (zero = success). For Create/Recreate/Restart we record the
// policy bytes too so tests can assert the right content reached
// the Applier.
type fakeApplier struct {
	createCalls   []callInvocation
	recreateCalls []callInvocation
	restartCalls  []callInvocation
	addCalls      []portCall
	removeCalls   []portCall

	createErr, recreateErr, restartErr, addErr, removeErr error
}

type callInvocation struct {
	cfg    *Config
	policy []byte
}

type portCall struct {
	cfg *Config
	pf  Port
}

func (f *fakeApplier) Create(_ context.Context, c *Config, p []byte) error {
	f.createCalls = append(f.createCalls, callInvocation{c, append([]byte(nil), p...)})
	return f.createErr
}
func (f *fakeApplier) Recreate(_ context.Context, c *Config, p []byte) error {
	f.recreateCalls = append(f.recreateCalls, callInvocation{c, append([]byte(nil), p...)})
	return f.recreateErr
}
func (f *fakeApplier) Restart(_ context.Context, c *Config, p []byte) error {
	f.restartCalls = append(f.restartCalls, callInvocation{c, append([]byte(nil), p...)})
	return f.restartErr
}
func (f *fakeApplier) AddPortForward(_ context.Context, c *Config, pf Port) error {
	f.addCalls = append(f.addCalls, portCall{c, pf})
	return f.addErr
}
func (f *fakeApplier) RemovePortForward(_ context.Context, c *Config, pf Port) error {
	f.removeCalls = append(f.removeCalls, portCall{c, pf})
	return f.removeErr
}

// validConfig returns a minimal valid Config whose openshell policy is
// COMPOSED from features (deterministic). Tests compose the expected policy
// bytes via mustCompose rather than hardcoding a string — there is no
// user-supplied policy_file anymore.
func validConfig() *Config {
	return &Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:1",
		Workdir:      NewEphemeralWorkdir("coder"),
		Features:     []string{"claude"},
		Capabilities: []string{capabilities.WorkdirWriteCapability},
	}
}

// mustCompose returns ComposePolicy(c) or fails the test. The bytes are what
// Reconcile feeds the Applier, so tests assert against this rather than a
// literal.
func mustCompose(t *testing.T, c *Config) []byte {
	t.Helper()
	p, err := ComposePolicy(c)
	if err != nil {
		t.Fatalf("ComposePolicy: %v", err)
	}
	return p
}

// mustResolveImage returns the runtime image a container for c actually runs
// (the feature-composed ref, or the base). An in-sync fake inspector must
// report THIS as actual.Image — exactly what LiveApplier.Create runs — so the
// image axis of the diff is neutral and the test isolates the other dimensions.
func mustResolveImage(t *testing.T, c *Config) string {
	t.Helper()
	ref, _, err := ResolvedImage(c)
	if err != nil {
		t.Fatalf("ResolvedImage: %v", err)
	}
	return ref
}

func TestReconcile_CreatesWhenContainerMissing(t *testing.T) {
	c := validConfig()
	c.Ports = []Port{{Target: 3000, Published: 3000}}

	insp := &fakeInspector{state: ActualState{Exists: false}}
	app := &fakeApplier{}
	res, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.ContainerCreated {
		t.Errorf("ContainerCreated = false, want true")
	}
	if len(app.createCalls) != 1 {
		t.Fatalf("Create called %d times, want 1", len(app.createCalls))
	}
	if want := mustCompose(t, c); string(app.createCalls[0].policy) != string(want) {
		t.Errorf("Create got policy %q, want composed %q", app.createCalls[0].policy, want)
	}
	if !reflect.DeepEqual(res.PortForwardsAdded, c.Ports) {
		t.Errorf("PortForwardsAdded = %+v, want %+v", res.PortForwardsAdded, c.Ports)
	}
	// Strategy is atomic — no live add/remove calls when Create fired.
	if len(app.addCalls) != 0 || len(app.removeCalls) != 0 {
		t.Errorf("live port calls during Create: add=%d remove=%d", len(app.addCalls), len(app.removeCalls))
	}
}

func TestReconcile_RecreatesOnImageChange(t *testing.T) {
	c := validConfig()
	c.Image = "img:NEW"

	insp := &fakeInspector{state: ActualState{
		Exists:        true,
		Image:         "img:OLD",
		PolicyContent: mustCompose(t, c),
	}}
	app := &fakeApplier{}
	res, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.ContainerRecreated {
		t.Errorf("ContainerRecreated = false, want true")
	}
	if len(app.recreateCalls) != 1 {
		t.Fatalf("Recreate called %d times, want 1", len(app.recreateCalls))
	}
}

func TestReconcile_RestartsOnPolicyChange(t *testing.T) {
	c := validConfig()

	insp := &fakeInspector{state: ActualState{
		Exists:        true,
		Image:         mustResolveImage(t, c),
		PolicyContent: []byte("policy: OLD\n"), // differs from the composed policy
	}}
	app := &fakeApplier{}
	res, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.ContainerRestarted {
		t.Errorf("ContainerRestarted = false, want true; res=%+v", res)
	}
	if len(app.restartCalls) != 1 {
		t.Fatalf("Restart called %d times, want 1", len(app.restartCalls))
	}
	if want := mustCompose(t, c); string(app.restartCalls[0].policy) != string(want) {
		t.Errorf("Restart got policy %q, want composed %q", app.restartCalls[0].policy, want)
	}
}

func TestReconcile_LivePortForwardOnly(t *testing.T) {
	c := validConfig()
	c.Ports = []Port{{Target: 3000, Published: 3000}, {Target: 9090, Published: 9090}}

	insp := &fakeInspector{state: ActualState{
		Exists:        true,
		Image:         mustResolveImage(t, c),
		PolicyContent: mustCompose(t, c),
		PortForwards:  []Port{{Target: 3000, Published: 3000}, {Target: 8080, Published: 8080}},
	}}
	app := &fakeApplier{}
	res, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.ContainerCreated || res.ContainerRecreated || res.ContainerRestarted {
		t.Errorf("unexpected container action: %+v", res)
	}
	// Should have one Add (9090) and one Remove (8080).
	if len(app.addCalls) != 1 || app.addCalls[0].pf != (Port{Target: 9090, Published: 9090}) {
		t.Errorf("addCalls = %+v, want [{9090 9090}]", app.addCalls)
	}
	if len(app.removeCalls) != 1 || app.removeCalls[0].pf != (Port{Target: 8080, Published: 8080}) {
		t.Errorf("removeCalls = %+v, want [{8080 8080}]", app.removeCalls)
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	c := validConfig()
	c.Ports = []Port{{Target: 3000, Published: 3000}}

	insp := &fakeInspector{state: ActualState{
		Exists:        true,
		Image:         mustResolveImage(t, c),
		PolicyContent: mustCompose(t, c),
		PortForwards:  []Port{{Target: 3000, Published: 3000}},
	}}
	app := &fakeApplier{}
	res, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.HasChanges() {
		t.Errorf("expected no changes on idempotent reconcile, res=%+v", res)
	}
	if len(app.createCalls)+len(app.recreateCalls)+len(app.restartCalls)+len(app.addCalls)+len(app.removeCalls) != 0 {
		t.Errorf("Applier called when no changes needed")
	}
}

func TestReconcile_ValidationFailsBeforeInspect(t *testing.T) {
	// Empty Name fails Validate; Inspector should never be called.
	c := &Config{Space: "default"} // missing Name
	insp := &fakeInspector{}
	app := &fakeApplier{}
	_, err := Reconcile(context.Background(), c, insp, app)
	if err == nil {
		t.Fatal("Reconcile: expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "validate config") {
		t.Errorf("error %q does not mention validation", err.Error())
	}
	if insp.calls != 0 {
		t.Errorf("Inspector called %d times despite validation failure", insp.calls)
	}
}

func TestReconcile_InspectorErrorPropagates(t *testing.T) {
	c := validConfig()
	insp := &fakeInspector{err: errors.New("vm unreachable")}
	app := &fakeApplier{}
	_, err := Reconcile(context.Background(), c, insp, app)
	if err == nil {
		t.Fatal("expected inspect error, got nil")
	}
	if !strings.Contains(err.Error(), "vm unreachable") {
		t.Errorf("error %q does not wrap inspector error", err.Error())
	}
	if len(app.createCalls)+len(app.recreateCalls)+len(app.restartCalls) != 0 {
		t.Errorf("Applier called despite inspect failure")
	}
}

func TestReconcile_FeaturesModeComposes(t *testing.T) {
	// Features-as-policy: a Config in features mode composes
	// a policy YAML at reconcile time and proceeds. With Exists:
	// false the Diff picks NeedsCreate; Reconcile dispatches to
	// Applier.Create with the composed policy bytes. Verify the
	// policy bytes are non-empty (a smoke check; the detailed
	// compose contract lives in compose_test.go).
	c := &Config{
		Name:     "coder",
		Space:    "default",
		Image:    "img:1",
		Workdir:  NewEphemeralWorkdir("coder"),
		Features: []string{"claude"},
	}
	insp := &fakeInspector{state: ActualState{Exists: false}}
	app := &fakeApplier{}
	res, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile features mode: %v", err)
	}
	if !res.ContainerCreated {
		t.Errorf("expected ContainerCreated, res=%+v", res)
	}
	if len(app.createCalls) != 1 {
		t.Fatalf("Create called %d times, want 1", len(app.createCalls))
	}
	if len(app.createCalls[0].policy) == 0 {
		t.Errorf("Create got empty policy bytes; composer should produce content")
	}
}

func TestReconcile_NoPolicyLockedDown(t *testing.T) {
	// No capabilities/features/agent declared is NOT an error: the box is a
	// generic shell that composes a maximally locked-down policy (default-deny),
	// and Reconcile creates it like any other box.
	c := &Config{Name: "coder", Space: "default", Image: "img:1"}
	insp := &fakeInspector{state: ActualState{Exists: false}}
	app := &fakeApplier{}
	if _, err := Reconcile(context.Background(), c, insp, app); err != nil {
		t.Fatalf("no-policy box must reconcile to a locked-down create, got error: %v", err)
	}
	if len(app.createCalls) != 1 {
		t.Fatalf("Create called %d times, want 1 (a locked-down shell box is still created)", len(app.createCalls))
	}
	if len(app.createCalls[0].policy) == 0 {
		t.Error("Create got empty policy bytes; the locked-down composer should still produce content")
	}
}

func TestReconcile_ApplierErrorPropagates(t *testing.T) {
	c := validConfig()
	insp := &fakeInspector{state: ActualState{Exists: false}}
	app := &fakeApplier{createErr: errors.New("boom")}
	_, err := Reconcile(context.Background(), c, insp, app)
	if err == nil {
		t.Fatal("expected applier error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not wrap applier error", err.Error())
	}
}

func TestReconcile_LiveOnly_RemoveBeforeAdd(t *testing.T) {
	// Remove ordering matters for "same published port, new target
	// port" — the old listener must be torn down before the new one
	// tries to bind. Reconcile should call RemovePortForward before
	// AddPortForward when both apply to the same published port.
	c := validConfig()
	c.Ports = []Port{{Target: 4000, Published: 3000}}

	insp := &fakeInspector{state: ActualState{
		Exists:        true,
		Image:         mustResolveImage(t, c),
		PolicyContent: mustCompose(t, c),
		PortForwards:  []Port{{Target: 3000, Published: 3000}},
	}}
	app := &fakeApplier{}
	_, err := Reconcile(context.Background(), c, insp, app)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(app.removeCalls) != 1 || len(app.addCalls) != 1 {
		t.Fatalf("expected 1 remove + 1 add, got remove=%d add=%d", len(app.removeCalls), len(app.addCalls))
	}
	// fakeApplier records calls in execution order. The remove must
	// have happened before the add — verifying by relative call
	// index via the slices' lengths captured at each step.
	// Re-run with an interleaving tracker for stricter assertion:
	type marker struct {
		op string
		pf Port
	}
	var trace []marker
	insp2 := &fakeInspector{state: insp.state}
	app2 := &recordingApplier{
		fakeApplier: fakeApplier{},
		onAdd:       func(pf Port) { trace = append(trace, marker{"add", pf}) },
		onRemove:    func(pf Port) { trace = append(trace, marker{"remove", pf}) },
	}
	if _, err := Reconcile(context.Background(), c, insp2, app2); err != nil {
		t.Fatalf("Reconcile retry: %v", err)
	}
	if len(trace) != 2 || trace[0].op != "remove" || trace[1].op != "add" {
		t.Errorf("expected remove-then-add ordering, got %+v", trace)
	}
}

// recordingApplier extends fakeApplier with callbacks that fire in
// execution order, letting TestReconcile_LiveOnly_RemoveBeforeAdd
// assert remove-before-add sequencing.
type recordingApplier struct {
	fakeApplier
	onAdd    func(Port)
	onRemove func(Port)
}

func (r *recordingApplier) AddPortForward(ctx context.Context, c *Config, pf Port) error {
	if r.onAdd != nil {
		r.onAdd(pf)
	}
	return r.fakeApplier.AddPortForward(ctx, c, pf)
}
func (r *recordingApplier) RemovePortForward(ctx context.Context, c *Config, pf Port) error {
	if r.onRemove != nil {
		r.onRemove(pf)
	}
	return r.fakeApplier.RemovePortForward(ctx, c, pf)
}

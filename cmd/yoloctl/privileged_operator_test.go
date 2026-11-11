package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/spf13/cobra"
)

var testOperator = controller.Principal{Box: "lead", Incarnation: "0123456789abcdef0123456789abcdef"}

func privilegedTestConfig(name string) *agentbox.Config {
	return &agentbox.Config{
		Name: name, Space: "work", Account: "none", Agent: "none", Image: "img:1",
		Workdir:      agentbox.NewEphemeralWorkdir(name),
		Capabilities: []string{capabilities.WorkdirWriteCapability},
		Privileged:   true,
	}
}

func writeTestFeature(t *testing.T, name string, requires ...string) {
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

func detailsFrom(opts container.RunOptions, privileged *bool) *container.Details {
	d := &container.Details{Name: opts.Name, Labels: opts.Labels, Command: opts.Command, Privileged: privileged}
	for _, m := range opts.Mounts {
		d.Mounts = append(d.Mounts, container.DetailMount(m))
	}
	return d
}

// privilegedTestRuntime is an inspected privileged launch of dev whose argv
// and launcher label name protocol.
func privilegedTestRuntime(id, protocol, label string, running bool) *container.Details {
	yes := true
	d := detailsFrom(container.RunOptions{
		Name:    "dev",
		Labels:  map[string]string{agentbox.LabelRuntimeMode: "privileged", agentbox.LabelLauncher: boxendpoint.LauncherFor(label)},
		Command: sandbox.PrivilegedEntrypointArgs("/workdir"),
		Mounts:  []container.Mount{{Source: boxendpoint.BinaryDir, Destination: boxendpoint.BinaryDir, ReadOnly: true}},
	}, &yes)
	for i, arg := range d.Command {
		if arg == "--protocol" {
			d.Command[i+1] = protocol
		}
	}
	d.ID, d.Running = id, running
	return d
}

func confinedTestRuntime(id string, running bool) *container.Details {
	no := false
	d := detailsFrom(container.RunOptions{
		Name:    "dev",
		Labels:  map[string]string{agentbox.LabelAgentbox: "dev"},
		Command: sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity"),
	}, &no)
	d.ID, d.Running = id, running
	return d
}

// A launch counts as privileged by the declared setting alone, never by a
// capability or feature, and a start or restart also by the existing
// container, so a changed declaration cannot start a stale privileged
// container. Uncertain evidence counts.
func TestPrivilegedLaunchReason(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeTestFeature(t, "kernel-dev", capabilities.RetiredPrivilegedCapability)
	yes := true
	ordinary := privilegedTestConfig("dev")
	ordinary.Privileged = false
	featureDerived := privilegedTestConfig("dev")
	featureDerived.Privileged = false
	featureDerived.Features = []string{"kernel-dev"}
	unresolvable := privilegedTestConfig("dev")
	unresolvable.Privileged = false
	unresolvable.Capabilities = []string{"missing.capability"}
	confined := confinedTestRuntime("c1", false)
	privileged := privilegedTestRuntime("p1", boxendpoint.ProtocolVersion, boxendpoint.ProtocolVersion, false)
	relabeled := detailsFrom(container.RunOptions{Name: "dev", Labels: map[string]string{}, Command: confined.Command}, &yes)
	unreported := detailsFrom(container.RunOptions{Name: "dev", Labels: map[string]string{}, Command: confined.Command}, nil)
	for name, tc := range map[string]struct {
		cfg        *agentbox.Config
		launch     privilegedLaunch
		details    *container.Details
		inspectErr error
		privileged bool
	}{
		"ordinary create":                {ordinary, launchCreate, nil, nil, false},
		"explicit create":                {privilegedTestConfig("dev"), launchCreate, nil, nil, true},
		"legacy feature create":          {featureDerived, launchCreate, nil, nil, false},
		"unresolvable create":            {unresolvable, launchCreate, nil, nil, false},
		"declared start, confined":       {privilegedTestConfig("dev"), launchStart, confined, nil, true},
		"ordinary start, confined":       {ordinary, launchStart, confined, nil, false},
		"ordinary start, no container":   {ordinary, launchStart, nil, nil, false},
		"stale privileged start":         {ordinary, launchStart, privileged, nil, true},
		"stale privileged restart":       {ordinary, launchRestart, privileged, nil, true},
		"privileged to confined":         {ordinary, launchRecreate, privileged, nil, false},
		"relabeled start":                {ordinary, launchStart, relabeled, nil, true},
		"unreported start":               {ordinary, launchStart, unreported, nil, true},
		"uninspectable restart":          {ordinary, launchRestart, nil, errors.New("inspect failed"), true},
		"legacy feature recreate":        {featureDerived, launchRecreate, confined, nil, false},
		"legacy feature start, stale":    {featureDerived, launchStart, privileged, nil, true},
		"unresolvable recreate, runtime": {unresolvable, launchRecreate, privileged, nil, false},
	} {
		if got := privilegedLaunchReason(tc.cfg, tc.launch, tc.details, tc.inspectErr) != ""; got != tc.privileged {
			t.Errorf("%s: privileged = %v, want %v", name, got, tc.privileged)
		}
	}
}

// Operators launch nothing privileged themselves. Only a restart or recreate
// of a running, current privileged launch, or the start leg bound to the
// container an explicit restart stopped, continues a host-started runtime.
// Host launches proceed with the operator-access warning.
func TestOperatorPrivilegedLaunchBoundary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var warnings bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &warnings))
	declared := privilegedTestConfig("dev")
	ordinary := privilegedTestConfig("dev")
	ordinary.Privileged = false
	current := boxendpoint.ProtocolVersion
	running := privilegedTestRuntime("p1", current, current, true)
	stopped := privilegedTestRuntime("p1", current, current, false)
	older := privilegedTestRuntime("p1", "0", "0", true)
	olderStopped := privilegedTestRuntime("p1", "0", "0", false)
	mismatched := privilegedTestRuntime("p1", "2", current, true)
	operator := withControllerPrincipal(t.Context(), testOperator)
	restartOf := func(space, box, id string) context.Context {
		return context.WithValue(operator, privilegedRestartKey{}, privilegedRestart{space: space, box: box, id: id})
	}
	for name, tc := range map[string]struct {
		ctx        context.Context
		cfg        *agentbox.Config
		launch     privilegedLaunch
		details    *container.Details
		inspectErr error
		allowed    bool
	}{
		"create":                           {operator, declared, launchCreate, nil, nil, false},
		"create beside a running one":      {operator, declared, launchCreate, running, nil, false},
		"ordinary create":                  {operator, ordinary, launchCreate, nil, nil, true},
		"recreate running":                 {operator, declared, launchRecreate, running, nil, true},
		"recreate stopped":                 {operator, declared, launchRecreate, stopped, nil, false},
		"recreate without observation":     {operator, declared, launchRecreate, nil, nil, false},
		"confined to privileged recreate":  {operator, declared, launchRecreate, confinedTestRuntime("c1", true), nil, false},
		"recreate older protocol":          {operator, declared, launchRecreate, older, nil, false},
		"recreate mismatched protocol":     {operator, declared, launchRecreate, mismatched, nil, false},
		"privileged to confined recreate":  {operator, ordinary, launchRecreate, running, nil, true},
		"restart running":                  {operator, declared, launchRestart, running, nil, true},
		"restart stopped":                  {operator, declared, launchRestart, stopped, nil, false},
		"restart stale, stopped":           {operator, ordinary, launchRestart, stopped, nil, false},
		"restart uninspectable":            {operator, declared, launchRestart, nil, errors.New("inspect failed"), false},
		"start stopped":                    {operator, declared, launchStart, stopped, nil, false},
		"start stale under new decl":       {operator, ordinary, launchStart, stopped, nil, false},
		"start own restart leg":            {restartOf("work", "dev", "p1"), declared, launchStart, stopped, nil, true},
		"start replaced container":         {restartOf("work", "dev", "p0"), declared, launchStart, stopped, nil, false},
		"start another box's restart leg":  {restartOf("work", "other", "p1"), declared, launchStart, stopped, nil, false},
		"start another space's restart":    {restartOf("lab", "dev", "p1"), declared, launchStart, stopped, nil, false},
		"start restart leg, older runtime": {restartOf("work", "dev", "p1"), declared, launchStart, olderStopped, nil, false},
		"start ordinary confined":          {operator, ordinary, launchStart, confinedTestRuntime("c1", false), nil, true},
	} {
		err := guardPrivilegedLaunch(tc.ctx, tc.cfg, tc.launch, tc.details, tc.inspectErr)
		if (err == nil) != tc.allowed {
			t.Errorf("%s: err = %v, allowed %v", name, err, tc.allowed)
			continue
		}
		var refused *privilegedLaunchError
		if err != nil && (!errors.As(err, &refused) || !IsUserError(err) || !strings.Contains(err.Error(), "only the host starts it (yoloctl box start dev --space work)")) {
			t.Errorf("%s: refusal is not actionable: %v", name, err)
		}
		// The host launches every one of them, warning about operator access.
		warnings.Reset()
		if err := guardPrivilegedLaunch(context.Background(), tc.cfg, tc.launch, tc.details, tc.inspectErr); err != nil {
			t.Errorf("%s: host launch refused: %v", name, err)
		}
		privileged := privilegedLaunchReason(tc.cfg, tc.launch, tc.details, tc.inspectErr) != ""
		if got := strings.Contains(warnings.String(), agentbox.PrivilegedOperatorAccess); got != privileged {
			t.Errorf("%s: host warning = %q, privileged %v", name, warnings.String(), privileged)
		}
	}
	if !strings.Contains(agentbox.PrivilegedOperatorAccess, "every yolo.space.operator box") || !strings.Contains(agentbox.PrivilegedOperatorAccess, "only the host can start it") {
		t.Fatalf("operator access warning = %q", agentbox.PrivilegedOperatorAccess)
	}
}

// Every lifecycle seam refuses before cleanup, build, stop or removal, and a
// managed restart/recreate of the running box proceeds as one operation.
func TestManagedApplierAuthorizesOperatorLaunchBeforeTeardown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	current := boxendpoint.ProtocolVersion
	operator := withControllerPrincipal(t.Context(), testOperator)
	for _, tc := range []struct {
		name    string
		details *container.Details
		allowed map[string]bool
	}{
		{"stopped", privilegedTestRuntime("p1", current, current, false), map[string]bool{}},
		{"running", privilegedTestRuntime("p1", current, current, true), map[string]bool{"recreate": true, "restart": true}},
	} {
		for _, launch := range []string{"create", "recreate", "restart"} {
			var calls []string
			inner := &fakeManagedInner{}
			a := managedApplier{
				inner:           inner,
				inspect:         func(context.Context, string) (*container.Details, error) { return tc.details, nil },
				prepareRecreate: func(context.Context) error { calls = append(calls, "prepare"); return nil },
				cleanup: func(context.Context, *agentbox.Config) (bool, error) {
					calls = append(calls, "cleanup")
					return true, nil
				},
			}
			cfg := privilegedTestConfig("dev")
			var err error
			switch launch {
			case "create":
				err = a.Create(operator, cfg, nil)
			case "recreate":
				err = a.Recreate(operator, cfg, nil)
			case "restart":
				err = a.Restart(operator, cfg, nil)
			}
			calls = append(calls, inner.calls...)
			if tc.allowed[launch] {
				if err != nil || !slices.Contains(calls, launch) || !slices.Contains(calls, "cleanup") {
					t.Errorf("%s %s: err=%v calls=%v", tc.name, launch, err, calls)
				}
			} else if err == nil || len(calls) != 0 {
				t.Errorf("%s %s: refusal had effects: err=%v calls=%v", tc.name, launch, err, calls)
			}
		}
	}
}

// The explicit restart authorizes its start leg before stopping anything, and
// only for the running privileged container it observed.
func TestAuthorizePrivilegedRestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	current := boxendpoint.ProtocolVersion
	if err := privilegedTestConfig("dev").Save(); err != nil {
		t.Fatal(err)
	}
	ordinary := privilegedTestConfig("plain")
	ordinary.Privileged = false
	if err := ordinary.Save(); err != nil {
		t.Fatal(err)
	}
	operator := withControllerPrincipal(t.Context(), testOperator)
	runtime := func(d *container.Details, listErr error) *operatorChatRuntimeFixture {
		f := &operatorChatRuntimeFixture{details: d, listErr: listErr}
		if d != nil {
			f.containers = []container.Container{{Name: d.Name}}
		}
		return f
	}
	ctx, err := authorizePrivilegedRestart(operator, runtime(privilegedTestRuntime("p1", current, current, true), nil), "work", "dev")
	if err != nil {
		t.Fatal(err)
	}
	cfg := privilegedTestConfig("dev")
	stopped := privilegedTestRuntime("p1", current, current, false)
	if err := guardPrivilegedLaunch(ctx, cfg, launchStart, stopped, nil); err != nil {
		t.Fatalf("restart start leg refused: %v", err)
	}
	if err := guardPrivilegedLaunch(operator, cfg, launchStart, stopped, nil); err == nil {
		t.Fatal("a later request started the stopped privileged box")
	}
	for name, f := range map[string]*operatorChatRuntimeFixture{
		"stopped":        runtime(stopped, nil),
		"older protocol": runtime(privilegedTestRuntime("p1", "0", "0", true), nil),
		"absent":         runtime(nil, nil),
		"inventory":      runtime(nil, errors.New("inventory failed")),
	} {
		if _, err := authorizePrivilegedRestart(operator, f, "work", "dev"); err == nil || !strings.Contains(err.Error(), "only the host starts it") {
			t.Errorf("%s: restart authorized: %v", name, err)
		}
	}
	// Confined boxes and the host need no continuation.
	if got, err := authorizePrivilegedRestart(operator, runtime(confinedTestRuntime("c1", true), nil), "work", "plain"); err != nil || got != operator {
		t.Fatalf("ordinary restart: %v", err)
	}
	if got, err := authorizePrivilegedRestart(t.Context(), runtime(stopped, nil), "work", "dev"); err != nil || got != t.Context() {
		t.Fatalf("host restart: %v", err)
	}
}

// Operators may declare a privileged box; an ordinary create or copy that
// would launch it is refused before anything is saved, naming the
// manifest-only alternative, and a refusal after a save says it was saved.
// The retired capability, explicit or required by a feature, is refused for
// everyone with guidance toward the setting.
func TestOperatorPrivilegedDeclarationsAndCreate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeTestFeature(t, "kernel-dev", capabilities.RetiredPrivilegedCapability)
	operatorCtx := withControllerPrincipal(context.Background(), testOperator)
	explicit := privilegedTestConfig("dev")
	if err := requireResolvableManifest(operatorCtx, explicit); err != nil {
		t.Fatalf("operator declaration refused: %v", err)
	}
	err := guardOperatorPrivilegedCreate(operatorCtx, explicit)
	if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "--manifest-only") || !strings.Contains(err.Error(), "nothing was saved") {
		t.Errorf("operator create = %v", err)
	}
	if err := guardOperatorPrivilegedCreate(context.Background(), explicit); err != nil {
		t.Errorf("host create refused: %v", err)
	}
	retiredGrant := privilegedTestConfig("dev")
	retiredGrant.Privileged = false
	retiredGrant.Capabilities = append(retiredGrant.Capabilities, capabilities.RetiredPrivilegedCapability)
	viaFeature := privilegedTestConfig("dev")
	viaFeature.Privileged = false
	viaFeature.Features = []string{"kernel-dev"}
	for name, cfg := range map[string]*agentbox.Config{"retired grant": retiredGrant, "feature": viaFeature} {
		for _, ctx := range []context.Context{operatorCtx, context.Background()} {
			err := requireResolvableManifest(ctx, cfg)
			if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "privileged: true") || !strings.Contains(err.Error(), "--privileged") {
				t.Errorf("%s: retired privilege accepted or unexplained: %v", name, err)
			}
		}
	}
	ordinary := privilegedTestConfig("dev")
	ordinary.Privileged = false
	if err := guardOperatorPrivilegedCreate(operatorCtx, ordinary); err != nil {
		t.Fatalf("ordinary operator create refused: %v", err)
	}
	refused := guardPrivilegedLaunch(operatorCtx, explicit, launchCreate, nil, nil)
	saved := savedDeclarationError(explicit, fmt.Errorf("create container: %w", refused))
	if !strings.Contains(saved.Error(), "box dev declaration is saved but not applied") || !IsUserError(saved) || !errors.As(saved, new(*privilegedLaunchError)) {
		t.Fatalf("saved refusal = %v", saved)
	}
	other := errors.New("port in use")
	if savedDeclarationError(explicit, other) != other || savedDeclarationError(explicit, nil) != nil {
		t.Fatal("unrelated reconcile errors were rewritten")
	}
}

// The shared start seam (box start, attach/use revival, chat) re-reads the
// container, so an operator request that saw the box running cannot start it
// after it stopped; the refusal precedes every VM-side preparation.
func TestStartExistingContainerRefusesOperatorPrivilegedStart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	saveSpaceForTest(t, "work", t.TempDir())
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("START_TEST_FIXTURE", bin)
	d := privilegedTestRuntime("p1", boxendpoint.ProtocolVersion, boxendpoint.ProtocolVersion, false)
	raw, err := json.Marshal([]any{map[string]any{
		"Id": d.ID, "Name": d.Name, "Path": d.Command[0], "Args": d.Command[1:],
		"State":      map[string]any{"Status": "exited", "Running": false},
		"Config":     map[string]any{"Labels": d.Labels},
		"Mounts":     []any{map[string]any{"Source": boxendpoint.BinaryDir, "Destination": boxendpoint.BinaryDir, "RW": false}},
		"HostConfig": map[string]any{"Privileged": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "inspect.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$START_TEST_FIXTURE/calls\"\ncase \"$*\" in\n  *'nerdctl inspect'*) cat \"$START_TEST_FIXTURE/inspect.json\" ;;\n  *) exit 42 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	operator := withControllerPrincipal(t.Context(), testOperator)
	err = startExistingAgentboxContainer(operator, container.New("work"), privilegedTestConfig("dev"))
	if !errors.As(err, new(*privilegedLaunchError)) {
		t.Fatalf("operator start = %v", err)
	}
	calls, readErr := os.ReadFile(filepath.Join(bin, "calls"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if !strings.Contains(line, "nerdctl inspect") {
			t.Fatalf("refused start touched the VM: %s", calls)
		}
	}
}

// An operator's box and attach commands, and its box edit apply, recheck the
// live grant after the native lock is held and before any effect: a request
// that waited behind a revocation or replacement runs nothing and saves nothing.
func TestOperatorRequestsRevalidateUnderLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var held []bool
	deny := false
	previous := revalidateOperatorFn
	revalidateOperatorFn = func(ctx context.Context) error {
		held = append(held, spaceOperationHeld(ctx))
		if deny {
			return errors.New("grant revoked")
		}
		return nil
	}
	t.Cleanup(func() { revalidateOperatorFn = previous })
	ran := 0
	run := func(ctx context.Context, args ...string) error {
		root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
		box := &cobra.Command{Use: "box"}
		for _, use := range []string{"exec", "copy", "show"} {
			c := &cobra.Command{Use: use, RunE: func(*cobra.Command, []string) error { ran++; return nil }}
			c.Flags().String("space", "work", "")
			box.AddCommand(c)
		}
		root.AddCommand(box)
		registerSpaceOperations(root)
		root.SetArgs(args)
		return root.ExecuteContext(ctx)
	}
	operator := withControllerPrincipal(t.Context(), testOperator)
	for _, args := range [][]string{{"box", "exec", "dev", "--space", "work", "--", "id"}, {"box", "copy", "source", "copy", "--space", "work"}, {"box", "show", "dev", "--space", "work"}} {
		if err := run(operator, args...); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(held, []bool{true, true}) || ran != 3 {
		t.Fatalf("revalidations under lock = %v, ran = %d", held, ran)
	}
	deny, held, ran = true, nil, 0
	if err := run(operator, "box", "exec", "dev", "--space", "work", "--", "id"); err == nil || ran != 0 {
		t.Fatalf("revoked operator ran: %v ran=%d", err, ran)
	}
	if err := run(t.Context(), "box", "copy", "source", "copy", "--space", "work"); err != nil || ran != 1 || len(held) != 1 {
		t.Fatalf("host command was revalidated: %v ran=%d held=%v", err, ran, held)
	}

	cfg := privilegedTestConfig("dev")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(editDocument{Revision: "stale", Data: []byte("name: dev\n")})
	if err != nil {
		t.Fatal(err)
	}
	held = nil
	handled, err := runControllerDocument(operator, controller.Request{Method: "box.edit.apply", Space: "work", Args: []string{"dev"}, Payload: payload}, "")
	if !handled || err == nil || !strings.Contains(err.Error(), "grant revoked") || len(held) != 1 {
		t.Fatalf("revoked edit apply: handled=%v err=%v checks=%d", handled, err, len(held))
	}
	if after, err := os.ReadFile(cfg.Path()); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("revoked edit changed the declaration: %v", err)
	}
}

func TestPrivilegedShowAndInspectState(t *testing.T) {
	yes := true
	d := detailsFrom(container.RunOptions{
		Name:    "dev",
		Labels:  map[string]string{agentbox.LabelAgentbox: "dev", agentbox.LabelSpace: "work", agentbox.LabelRuntimeMode: "privileged", agentbox.LabelLauncher: boxendpoint.Launcher, agentbox.LabelLauncherSHA256: "abc"},
		Command: sandbox.PrivilegedEntrypointArgs("/workdir"),
		Mounts:  []container.Mount{{Source: boxendpoint.BinaryDir, Destination: boxendpoint.BinaryDir, ReadOnly: true}},
	}, &yes)
	meta := agentboxShowMetadata{Privileged: true, Runtime: agentbox.ClassifyRuntime(d)}
	var text bytes.Buffer
	if err := writeAgentboxShow(&text, d, "work", landlockState{Summary: "available", KernelSupported: true, ABI: 10}, agentStateMount{}, meta, sessionStatus{State: "running"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PRIVILEGED", "space-VM root", boxendpoint.Launcher, "Landlock: not applied"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("show lacks %q:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), "Landlock: available") {
		t.Errorf("show claims Landlock for a privileged box:\n%s", text.String())
	}
	var raw bytes.Buffer
	if err := writeAgentboxShowJSON(&raw, d, "work", landlockState{Summary: "available", KernelSupported: true, ABI: 10}, agentStateMount{}, meta, sessionStatus{State: "running"}, nil); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Privileged  bool   `json:"privileged"`
		RuntimeMode string `json:"runtime_mode"`
		Landlock    struct {
			Summary string `json:"summary"`
		} `json:"landlock"`
	}
	if err := json.Unmarshal(raw.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Privileged || doc.RuntimeMode != "privileged" || doc.Landlock.Summary != "not-applied" {
		t.Fatalf("show JSON = %s", raw.String())
	}
	// A declared but not yet recreated box says so instead of claiming the mode.
	pending := agentboxShowMetadata{Privileged: true, Runtime: agentbox.RuntimeEvidence{Mode: agentbox.RuntimeModeConfined}}
	if got := privilegedNotice(pending.Runtime); !strings.Contains(got, "explicit box recreate") {
		t.Fatalf("pending notice = %q", got)
	}
	// Inspect reports the declared setting beside its authority warning.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	manifest, err := json.Marshal(buildInspectManifest(privilegedTestConfig("dev")))
	if err != nil || !strings.Contains(string(manifest), `"privileged":true`) {
		t.Fatalf("inspect manifest = %s (%v)", manifest, err)
	}
}

func TestEndpointBinaryIsTheInstalledVMBinary(t *testing.T) {
	if boxendpoint.Binary != session.VMBinaryPath {
		t.Fatalf("endpoint binary %s is not the installed VM binary %s", boxendpoint.Binary, session.VMBinaryPath)
	}
}

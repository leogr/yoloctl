package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

func TestSpaceUpgradeCommandScopeAndCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "alpha", "/tmp/alpha")
	saveSpaceForTest(t, "beta", "/tmp/beta")

	if cmd := findByPath(newRootCmd(), "space", "upgrade"); cmd != nil {
		t.Fatal("space upgrade registered on the worker command tree")
	}
	cmd := findByPath(newClientRootCmd(), "space", "upgrade")
	if cmd == nil || cmd.ValidArgsFunction == nil {
		t.Fatal("space upgrade or its positional completion is missing from the host command tree")
	}
	for _, args := range [][]string{nil, {"alpha"}} {
		if err := cmd.Args(cmd, args); err != nil {
			t.Fatalf("space upgrade %v: %v", args, err)
		}
	}
	if err := cmd.Args(cmd, []string{"alpha", "beta"}); err == nil {
		t.Fatal("space upgrade accepted two positional names")
	}
	got, directive := cmd.ValidArgsFunction(cmd, nil, "")
	if !slices.Equal(got, []string{"alpha", "beta"}) || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("space upgrade completion = %v, %v", got, directive)
	}
	got, directive = cmd.ValidArgsFunction(cmd, []string{"alpha"}, "")
	if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("space upgrade completion after first argument = %v, %v", got, directive)
	}
}

func upgradeTestBox(name string, runtime boxstate.RuntimeState, build boxstate.SessionBuild) boxstate.Box {
	b := boxstate.Box{
		Name: name, Space: "test", Declaration: boxstate.DeclarationDeclared, Runtime: runtime,
		Config:  &agentbox.Config{Name: name, Space: "test"},
		Session: boxstate.SessionInfo{State: boxstate.SessionNone},
	}
	if runtime != boxstate.RuntimeMissing {
		b.Container = &container.Container{ID: strings.Repeat("a", 64)}
	}
	if build != "" {
		b.Session = boxstate.SessionInfo{State: boxstate.SessionRunning, Build: build, Holder: "77@start"}
	}
	return b
}

func upgradeTestDrift(b boxstate.Box, drift boxstate.DriftState) boxDriftView {
	d := boxDriftView{Name: b.Name, Drift: drift}
	if b.Container != nil {
		d.Upgrade = &upgradeTarget{
			ManifestHash: "hash", Mode: agentbox.RuntimeModeConfined, Run: container.Run{ID: b.Container.ID},
			Running: b.Runtime == boxstate.RuntimeRunning,
		}
	}
	return d
}

func TestSpaceUpgradeBoxActions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime boxstate.RuntimeState
		build   boxstate.SessionBuild
		drift   boxstate.DriftState
		legacy  bool
		action  spaceUpgradeAction
		confirm bool
	}{
		{"stale", boxstate.RuntimeRunning, boxstate.SessionBuildOther, boxstate.DriftInSync, false, upgradeRestart, false},
		{"current", boxstate.RuntimeRunning, boxstate.SessionBuildCurrent, boxstate.DriftInSync, false, upgradeKeep, false},
		{"stopped", boxstate.RuntimeStopped, "", boxstate.DriftNeedsRestart, false, upgradeKeep, false},
		{"missing", boxstate.RuntimeMissing, "", boxstate.DriftNeedsCreate, false, upgradeKeep, false},
		{"stopped-recreate", boxstate.RuntimeStopped, "", boxstate.DriftNeedsRecreate, false, upgradeRecreate, true},
		{"stale-recreate", boxstate.RuntimeRunning, boxstate.SessionBuildOther, boxstate.DriftNeedsRecreate, false, upgradeRecreate, true},
		{"policy", boxstate.RuntimeRunning, boxstate.SessionBuildCurrent, boxstate.DriftNeedsRestart, false, upgradeRestart, true},
		{"ports", boxstate.RuntimeRunning, boxstate.SessionBuildCurrent, boxstate.DriftLiveOnly, false, upgradeManual, false},
		{"stale-with-ports", boxstate.RuntimeRunning, boxstate.SessionBuildOther, boxstate.DriftLiveOnly, false, upgradeRestart, false},
		{"legacy-with-stale-holder", boxstate.RuntimeRunning, boxstate.SessionBuildOther, boxstate.DriftNeedsRecreate, true, upgradeRestart, false},
		{"stopped-legacy", boxstate.RuntimeStopped, "", boxstate.DriftInSync, true, upgradeRecreate, true},
		{"unknown", boxstate.RuntimeRunning, boxstate.SessionBuildUnknown, boxstate.DriftInSync, false, upgradeManual, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := upgradeTestBox("box", tc.runtime, tc.build)
			d := upgradeTestDrift(b, tc.drift)
			d.LegacyRuntime = tc.legacy
			r := classifySpaceUpgradeBox("test", b, d, managedchat.Status{}, nil)
			if r.action != tc.action || r.confirm != tc.confirm {
				t.Fatalf("plan = %s (confirm=%v), want %s (confirm=%v)", r.action, r.confirm, tc.action, tc.confirm)
			}
		})
	}
}

func TestSpaceUpgradeUnavailableBatch(t *testing.T) {
	boxes := make([]boxstate.Box, maxDriftBoxes+2)
	for i := range boxes {
		boxes[i] = upgradeTestBox(fmt.Sprintf("box-%02d", i), boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	}
	inventory := func(context.Context) ([]boxstate.Box, error) { return boxes, nil }
	var batchSizes []int
	drift := func(_ context.Context, names []string) ([]boxDriftView, error) {
		batchSizes = append(batchSizes, len(names))
		if len(names) == maxDriftBoxes {
			return nil, context.DeadlineExceeded
		}
		return []boxDriftView{upgradeTestDrift(boxes[maxDriftBoxes], boxstate.DriftInSync), upgradeTestDrift(boxes[maxDriftBoxes+1], boxstate.DriftInSync)}, nil
	}
	ops := spaceUpgradeOps{sessionRestartOffer: sessionRestartOffer{inventory: inventory, drift: drift}, room: upgradeNoRoom}
	rows, err := readSpaceUpgradePlan(t.Context(), "test", ops)
	if err != nil || !slices.Equal(batchSizes, []int{maxDriftBoxes, 2}) || len(rows) != len(boxes) {
		t.Fatalf("plan with failed group = %d rows, %v, batches %v", len(rows), err, batchSizes)
	}
	for i, row := range rows {
		if i < maxDriftBoxes && (row.action != upgradeManual || !strings.Contains(row.reason, "deadline exceeded")) ||
			i >= maxDriftBoxes && row.action != upgradeRestart {
			t.Fatalf("row %d = %+v", i, row)
		}
	}
}

func upgradeNoRoom(context.Context) (managedchat.Status, error) {
	return managedchat.Status{}, nil
}

func TestSpaceUpgradeDecisionsThenEffects(t *testing.T) {
	a := upgradeTestBox("alpha", boxstate.RuntimeRunning, boxstate.SessionBuildCurrent)
	b := upgradeTestBox("beta", boxstate.RuntimeStopped, "")
	c := upgradeTestBox("third", boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	boxes := []boxstate.Box{a, b, c}
	for _, tc := range []struct {
		name, answers              string
		interactive, changed, lost bool
		want                       []string
	}{
		{"all-decisions-first", "y\ny\n", true, false, false, []string{"restart alpha", "recreate beta", "restart third"}},
		{"decline-and-accept", "no\ny\n", true, false, false, []string{"recreate beta", "restart third"}},
		{"eof", "y\n", true, false, false, []string{"restart alpha", "restart third"}},
		{"script", "y\ny\n", false, false, false, []string{"restart third"}},
		{"changed-plan", "y\ny\n", true, true, false, []string{"restart alpha", "restart third"}},
		{"lost-reply", "y\ny\n", true, false, true, []string{"restart alpha", "recreate beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prompts bytes.Buffer
			var calls []string
			var reads int
			lost := errors.New("lost controller reply")
			ops := spaceUpgradeOps{
				sessionRestartOffer: sessionRestartOffer{
					interactive: tc.interactive, in: strings.NewReader(tc.answers), errw: &prompts,
					inventory: func(context.Context) ([]boxstate.Box, error) {
						reads++
						return boxes, nil
					},
					drift: func(_ context.Context, names []string) ([]boxDriftView, error) {
						var ds []boxDriftView
						for _, name := range names {
							switch name {
							case a.Name:
								ds = append(ds, upgradeTestDrift(a, boxstate.DriftNeedsRestart))
							case b.Name:
								d := upgradeTestDrift(b, boxstate.DriftNeedsRecreate)
								if tc.changed && reads > 1 {
									d.Upgrade.ManifestHash = "edited"
								}
								ds = append(ds, d)
							case c.Name:
								ds = append(ds, upgradeTestDrift(c, boxstate.DriftInSync))
							}
						}
						return ds, nil
					},
					restart: func(_ context.Context, name, holder string, _ bool) error {
						if tc.interactive && !strings.Contains(prompts.String(), "recreate box beta? [y/N]") {
							t.Fatal("restart ran before the last decision")
						}
						if name == c.Name && holder != c.Session.Holder || name == a.Name && holder != "" {
							t.Fatalf("restart %s got holder %q", name, holder)
						}
						calls = append(calls, "restart "+name)
						return nil
					},
				},
				room: upgradeNoRoom,
				recreate: func(_ context.Context, row spaceUpgradeRow) error {
					calls = append(calls, "recreate "+row.box.Name)
					if tc.lost {
						return lost
					}
					return nil
				},
			}
			err := runSpaceUpgrade(t.Context(), "test", ops)
			if !slices.Equal(calls, tc.want) {
				t.Fatalf("calls = %v, want %v", calls, tc.want)
			}
			if tc.lost && !errors.Is(err, lost) {
				t.Fatalf("lost reply returned %v", err)
			}
			if tc.name == "all-decisions-first" && err != nil {
				t.Fatal(err)
			}
			if tc.name != "all-decisions-first" && err == nil {
				t.Fatal("outstanding work returned success")
			}
		})
	}
}

func TestSpaceUpgradeRecreateTargetRejectsReplacement(t *testing.T) {
	unprivileged := false
	details := &container.Details{
		ID: strings.Repeat("a", 64), Running: true, StartedAt: "start", PID: 50,
		Privileged: &unprivileged, Command: []string{sandbox.BinaryDirContainerPath + "/openshell-sandbox"},
	}
	target := upgradeTarget{Run: container.RunOf(details), Mode: agentbox.RuntimeModeConfined, Running: true}
	ctx := context.WithValue(t.Context(), upgradeRecreateKey{}, target)
	if err := checkUpgradeRecreateTarget(ctx, details); err != nil {
		t.Fatal(err)
	}
	replaced := *details
	replaced.StartedAt = "next"
	for _, got := range []*container.Details{&replaced, nil} {
		if err := checkUpgradeRecreateTarget(ctx, got); err == nil {
			t.Fatal("different or missing run accepted")
		}
	}
}

func TestSpaceUpgradeRestoresSelectedChatLastAndRestartsWorkload(t *testing.T) {
	roomID, serverID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	server := upgradeTestBox("actual-server", boxstate.RuntimeRunning, boxstate.SessionBuildCurrent)
	server.Config.ID = serverID
	server.Config.SessionCommand = agentbox.ChatServerCommand()
	workload := upgradeTestBox("workload", boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	workload.Config.SessionCommand = []string{"schedule"}
	plain := upgradeTestBox("chat", boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	boxes := []boxstate.Box{server, workload, plain}
	drift := map[string]boxDriftView{
		server.Name:   upgradeTestDrift(server, boxstate.DriftNeedsRecreate),
		workload.Name: upgradeTestDrift(workload, boxstate.DriftInSync),
		plain.Name:    upgradeTestDrift(plain, boxstate.DriftInSync),
	}
	var calls []string
	room := &managedchat.Room{ID: roomID, Server: managedchat.Box{Name: server.Name, ID: serverID}}
	ops := spaceUpgradeOps{
		sessionRestartOffer: sessionRestartOffer{
			interactive: true, in: strings.NewReader("y\n"), errw: io.Discard,
			inventory: func(context.Context) ([]boxstate.Box, error) { return boxes, nil },
			drift: func(_ context.Context, names []string) ([]boxDriftView, error) {
				var view []boxDriftView
				for _, name := range names {
					view = append(view, drift[name])
				}
				return view, nil
			},
			restart: func(_ context.Context, name, holder string, startWorkload bool) error {
				if startWorkload != (name == workload.Name) || holder != "77@start" {
					t.Fatalf("wrong restart: %s %s %v", name, holder, startWorkload)
				}
				calls = append(calls, "restart "+name)
				return nil
			},
		},
		room: func(context.Context) (managedchat.Status, error) {
			return managedchat.Status{Room: room, ServerReady: true}, nil
		},
		recreate: func(_ context.Context, row spaceUpgradeRow) error {
			if row.kind != chatServerSession || row.room != roomID {
				t.Fatal("wrong room binding")
			}
			calls = append(calls, "recreate "+row.box.Name)
			return nil
		},
		ensure: func(_ context.Context, got string) error {
			if got != roomID {
				t.Fatalf("restore bound to %s", got)
			}
			calls = append(calls, "ensure")
			return nil
		},
	}
	if err := runSpaceUpgrade(t.Context(), "test", ops); err != nil {
		t.Fatal(err)
	}
	want := []string{"restart chat", "restart workload", "recreate actual-server", "ensure"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSpaceUpgradeRefusesOperator(t *testing.T) {
	stubOperatorClient(t, &controller.OperatorClient{Space: "dev"})
	root := newClientRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"space", "upgrade", "dev"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("operator space upgrade returned %v", err)
	}
}

type upgradeDeploymentProbe struct {
	controllerDeploymentVM
	require func(string) error
}

func (p upgradeDeploymentProbe) RequireAccountDisk(_ context.Context, space, _ string) error {
	return p.require(space)
}

func TestSpaceUpgradeSelectsThisBuildWithoutStartFlags(t *testing.T) {
	shortYoloctlHome(t)
	boot := imageUpdateFixture(t)
	boot.VM.Backend, boot.VM.Image = "lima", ""
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	stubOperatorClient(t, nil)
	oldDeploy, oldCheck := newDeploymentVM, checkCommandVMCompatibility
	t.Cleanup(func() {
		newDeploymentVM, checkCommandVMCompatibility = oldDeploy, oldCheck
	})
	var checks int
	checkCommandVMCompatibility = func(_ context.Context, got *spaceconfig.Bootstrap) error {
		checks++
		if got.Name != boot.Name {
			t.Fatalf("checked space %s, want %s", got.Name, boot.Name)
		}
		return nil
	}
	probe := errors.New("stop before the fixture touches any disk or VM")
	newDeploymentVM = func() controllerDeploymentVM {
		return upgradeDeploymentProbe{require: func(got string) error {
			if got != boot.Name {
				t.Fatalf("selected space %s, want %s", got, boot.Name)
			}
			return probe
		}}
	}
	t.Setenv("YOLOCTL_SPACE", boot.Name)
	root := newClientRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"space", "upgrade"})
	if err := root.ExecuteContext(t.Context()); !errors.Is(err, probe) || checks != 1 {
		t.Fatalf("implicit upgrade target did not attempt controller selection: %v, compatibility checks %d", err, checks)
	}
}

func TestSpaceUpgradeRepeatsEffectsAtNamedPrompt(t *testing.T) {
	b := upgradeTestBox("sandbox", boxstate.RuntimeStopped, "")
	b.Config.Privileged = true
	d := upgradeTestDrift(b, boxstate.DriftNeedsRecreate)
	r := spaceUpgradeRow{box: b, drift: d, action: upgradeRecreate, confirm: true}
	var out bytes.Buffer
	yes, err := confirmSpaceUpgrade(t.Context(), bufio.NewReader(strings.NewReader("y\n")), &out, r)
	if err != nil || !yes {
		t.Fatalf("affirmative answer: %v, %v", yes, err)
	}
	for _, want := range []string{
		"Container-only files are lost", "Workdir, account state and separate cache are kept",
		"The replacement container will start", "space-VM root",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt lacks %q: %s", want, out.String())
		}
	}
	if !strings.HasSuffix(out.String(), "recreate box sandbox? [y/N]: ") {
		t.Fatalf("wrong prompt: %s", out.String())
	}
	b.Config.Privileged = false
	r.kind = chatServerSession
	out.Reset()
	if _, err := confirmSpaceUpgrade(t.Context(), bufio.NewReader(strings.NewReader("n\n")), &out, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Connected clients must reconnect and re-arm") ||
		strings.Contains(out.String(), "space-VM root") {
		t.Fatalf("room prompt has the wrong warnings: %s", out.String())
	}
}

func TestSpaceUpgradeDeclinedLauncherReplacementRestartsOnlyWhenEligible(t *testing.T) {
	box := upgradeTestBox("older", boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	for _, tc := range []struct {
		name, answer       string
		interactive, drift bool
		want               string
	}{
		{"declined", "n\n", true, false, "restart"},
		{"nonterminal", "", false, false, "restart"},
		{"eof", "", true, false, "restart"},
		{"accepted", "y\n", true, false, "recreate"},
		{"declined-recreate-drift", "n\n", true, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drift := upgradeTestDrift(box, boxstate.DriftInSync)
			drift.Reasons = []boxstate.Reason{{Code: boxstate.ReasonLauncherRecreate, Message: "created before the session launcher"}}
			if tc.drift {
				drift.Drift = boxstate.DriftNeedsRecreate
			}
			var calls []string
			ops := spaceUpgradeOps{
				sessionRestartOffer: sessionRestartOffer{
					interactive: tc.interactive, in: strings.NewReader(tc.answer), errw: io.Discard,
					inventory: func(context.Context) ([]boxstate.Box, error) { return []boxstate.Box{box}, nil },
					drift:     func(context.Context, []string) ([]boxDriftView, error) { return []boxDriftView{drift}, nil },
					restart: func(_ context.Context, name, holder string, _ bool) error {
						if name != box.Name || holder != box.Session.Holder {
							t.Fatalf("fallback restart is not bound: %q, %q", name, holder)
						}
						calls = append(calls, "restart")
						return nil
					},
				},
				room: upgradeNoRoom,
				recreate: func(context.Context, spaceUpgradeRow) error {
					calls = append(calls, "recreate")
					return nil
				},
			}
			err := runSpaceUpgrade(t.Context(), "test", ops)
			if got := strings.Join(calls, ","); got != tc.want || (err == nil) != (tc.want == "recreate") {
				t.Fatalf("actions %v, error %v, want %s with a nonzero result only when work remains", calls, err, tc.want)
			}
		})
	}
}

func TestSpaceUpgradeFailureKeepsOutstandingAndRecoveryHints(t *testing.T) {
	outstanding := upgradeTestBox("declined", boxstate.RuntimeStopped, "")
	failing := upgradeTestBox("worker", boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	failing.Config.SessionCommand = []string{"true"}
	later := upgradeTestBox("z-last", boxstate.RuntimeRunning, boxstate.SessionBuildOther)
	boxes := []boxstate.Box{outstanding, failing, later}
	var logs bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &logs))
	lost := errors.New("lost restart reply")
	ops := spaceUpgradeOps{
		sessionRestartOffer: sessionRestartOffer{
			interactive: true, in: strings.NewReader("n\n"), errw: io.Discard,
			inventory: func(context.Context) ([]boxstate.Box, error) { return boxes, nil },
			drift: func(_ context.Context, names []string) ([]boxDriftView, error) {
				drift := make([]boxDriftView, 0, len(names))
				for _, b := range boxes {
					if !slices.Contains(names, b.Name) {
						continue
					}
					state := boxstate.DriftInSync
					if b.Name == outstanding.Name {
						state = boxstate.DriftNeedsRecreate
					}
					drift = append(drift, upgradeTestDrift(b, state))
				}
				return drift, nil
			},
			restart: func(_ context.Context, name, _ string, restore bool) error {
				if name != failing.Name || !restore {
					t.Fatalf("unexpected restart %s, restore %v", name, restore)
				}
				return lost
			},
		},
		room: upgradeNoRoom,
	}
	if err := runSpaceUpgrade(t.Context(), "test", ops); !errors.Is(err, lost) {
		t.Fatalf("lost reply returned %v", err)
	}
	for _, want := range []string{"Outstanding box actions", "yoloctl box recreate declined --space test",
		"yoloctl box attach worker --space test", "Box actions not attempted", "z-last"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q in output:\n%s", want, logs.String())
		}
	}
	logs.Reset()
	reportSpaceUpgradeRecovery("test", spaceUpgradeRow{box: failing, kind: chatServerSession, restore: true}, true)
	if got := logs.String(); !strings.Contains(got, "yoloctl box recreate worker --space test") ||
		!strings.Contains(got, "yoloctl chat ensure --space test") {
		t.Fatalf("missing pre-native server recovery:\n%s", got)
	}
}

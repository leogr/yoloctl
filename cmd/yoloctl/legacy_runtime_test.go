package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/termio"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/spf13/cobra"
)

const legacyRemedy = "Box coder predates the native runtime and cannot be started or restarted. It is stopped. Recreate it to continue: yoloctl box recreate coder --space default."

// legacyStartFixture is the existing-start fixture (a pre-native confined
// container) with the production native preparation, so the actual route
// classification decides. running starts it as a running container; the
// shared stop's container leg stops it unless stop is given.
func legacyStartFixture(t *testing.T, running bool, stop func() error) (calls string, stops *[]string) {
	return legacyStartFixtureWithDrift(t, "none", running, stop)
}

// legacyStartFixtureWithDrift is legacyStartFixture whose declaration also
// requires recreation when drift is "before start" (image img:old -> img:desired).
func legacyStartFixtureWithDrift(t *testing.T, drift string, running bool, stop func() error) (calls string, stops *[]string) {
	t.Helper()
	calls = setupExistingStartFixture(t, drift)
	prepareNativeExistingStart, legacyRuntimeFirst = actualPrepareNativeExistingStart, actualLegacyRuntimeFirst
	dir := filepath.Dir(calls)
	if running {
		if err := os.WriteFile(filepath.Join(dir, "started"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		// Listed as running too while it runs.
		script, err := os.ReadFile(filepath.Join(dir, "limactl"))
		if err != nil {
			t.Fatal(err)
		}
		listed := strings.Replace(string(script), `printf '{"Names":"coder","Status":"Exited (0)"}\n'`, `if [ -f "$START_TEST_FIXTURE/started" ]; then printf '{"Names":"coder","Status":"Up"}\n'; else printf '{"Names":"coder","Status":"Exited (0)"}\n'; fi`, 1)
		if listed == string(script) {
			t.Fatal("fixture listing not found")
		}
		if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(listed), 0o700); err != nil {
			t.Fatal(err)
		}
		withCompletionResolver(t, []container.Container{{Name: "coder", Image: "img:desired", Status: "Up"}})
	}
	stops = recordStartFixtureStops(t)
	lifecycleStopAgentboxContainer = func(context.Context, *container.Client, string) error {
		*stops = append(*stops, "container")
		if stop != nil {
			return stop()
		}
		return os.Remove(filepath.Join(dir, "started"))
	}
	return calls, stops
}

func readCalls(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// No legacy policy write, engine start, retired-binary read, socket setup or
// native launch: the refusal is the only outcome of a new run.
func assertNoLegacyRun(t *testing.T, calls string) {
	t.Helper()
	for _, forbidden := range []string{"nerdctl start", "nerdctl run", "nerdctl rm", "sudo tee", "binary=/opt/openshell/bin/openshell-sandbox", "sudo mkdir -p /run"} {
		if strings.Contains(calls, forbidden) {
			t.Fatalf("a pre-native box ran %q:\n%s", forbidden, calls)
		}
	}
}

// Regression 3: a running pre-native box's restart (its policy unchanged)
// stops the exact run through the shared session-aware stop, then refuses with
// the recreation remedy. Regression 4: a stopped one's start is refused before
// any effect and nothing is stopped.
func TestPreNativeBoxIsStoppedAndRefusedNeverStarted(t *testing.T) {
	for _, tc := range []struct {
		operation string
		running   bool
		stops     []string
	}{
		{"restart", true, []string{"holder", "ports", "container", "session", "netns"}},
		{"start", false, nil},
		{"restart", false, nil},
		{"start", true, nil}, // already running: a no-op, nothing stopped
	} {
		t.Run(tc.operation+map[bool]string{true: "/running", false: "/stopped"}[tc.running], func(t *testing.T) {
			callsPath, stops := legacyStartFixture(t, tc.running, nil)
			cmd := newAgentboxStartCmd()
			if tc.operation == "restart" {
				cmd = newAgentboxRestartCmd()
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{"coder", "--space", "default", "--no-attach", "--no-autoupdate"})
			err := cmd.Execute()
			var legacy *agentbox.LegacyRuntimeError
			if tc.operation == "start" && tc.running {
				if err != nil {
					t.Fatalf("a running pre-native box's start is a no-op: %v", err)
				}
			} else if err == nil || err.Error() != legacyRemedy || !errors.As(err, &legacy) || legacy.Box != "coder" {
				t.Fatalf("got %v, want the stopped remedy", err)
			}
			assertNoLegacyRun(t, readCalls(t, callsPath))
			if !slices.Equal(*stops, tc.stops) {
				t.Fatalf("stops %v, want %v", *stops, tc.stops)
			}
		})
	}
}

// Regression 3: a stop that fails, or that a read cannot confirm, is reported
// as such: never "It is stopped", so no recreation is offered after it.
func TestPreNativeRefusalNeverClaimsAnUnconfirmedStop(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func() error
		want string
	}{
		{"failed stop", func() error { return errors.New("engine refused") }, "Stopping it failed"},
		{"stop left it running", func() error { return nil }, "It is still running after its stop."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callsPath, _ := legacyStartFixture(t, true, tc.stop)
			cmd := newAgentboxRestartCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"coder", "--space", "default", "--no-attach", "--no-autoupdate"})
			err := cmd.Execute()
			var legacy *agentbox.LegacyRuntimeError
			if err == nil || !errors.As(err, &legacy) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "It is stopped") || strings.Contains(err.Error(), "Recreate it to continue") {
				t.Fatalf("got %v", err)
			}
			assertNoLegacyRun(t, readCalls(t, callsPath))
		})
	}
}

// Only the typed legacy refusal turns into a stop: any other preflight error
// is returned unchanged and nothing is read or stopped.
func TestOtherRefusalsAreNotTurnedIntoStops(t *testing.T) {
	callsPath, stops := legacyStartFixture(t, true, nil)
	cause := errors.New("native policy qualification failed")
	got := refuseLegacyRun(t.Context(), container.New("default"), "default", "coder", io.Discard, cause, legacyPolicyApply)
	if got != cause || len(*stops) != 0 || readCalls(t, callsPath) != "" {
		t.Fatal("a non-legacy refusal stopped or read the box", got, *stops)
	}
}

// Regression 3, policy apply: the saved policy stays saved; a running
// pre-native box ends its session (recorded), is stopped and refused, and its
// policy is never written or launched.
func TestPolicyApplyStopsAndRefusesAPreNativeBox(t *testing.T) {
	callsPath, stops := legacyStartFixture(t, true, nil)
	cfg, err := agentbox.Load("default", "coder")
	if err != nil {
		t.Fatal(err)
	}
	client := container.NewWithOutput(cfg.Space, io.Discard, io.Discard)
	var ended []string
	a := managedApplier{
		inner:  agentbox.LiveApplier{Client: client},
		client: client,
		notice: io.Discard,
		cleanup: func(context.Context, *agentbox.Config) (bool, error) {
			*stops = append(*stops, "session-end")
			return true, nil
		},
		onSessionEnded: func(detail string) error { ended = append(ended, detail); return nil },
	}
	err = a.Restart(t.Context(), cfg, []byte("saved B"))
	want := legacyRemedy + " The saved policy was not reverted."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	if !slices.Equal(*stops, []string{"session-end", "holder", "ports", "container", "session", "netns"}) || !slices.Equal(ended, []string{sessionEndedByLegacyRefusal}) {
		t.Fatal("session end/stop order", *stops, ended)
	}
	assertNoLegacyRun(t, readCalls(t, callsPath))
}

// Regression 5: the confirmation is asked only of a person, only after a read
// shows the box pre-native and stopped. Default No, EOF and every other case
// keep the refusal and send nothing; a yes sends exactly one ordinary
// recreate and never the original request again.
func TestLegacyRecreateOfferAsksOnlyAfterAVerifiedStop(t *testing.T) {
	previous := recreateOfferInteractive
	t.Cleanup(func() { recreateOfferInteractive = previous })
	recreateOfferInteractive = func(*cobra.Command) bool { return true }
	stopped := `{"identity":{"runtime":"stopped","legacy_runtime":true}}`
	running := `{"identity":{"runtime":"running","legacy_runtime":true}}`
	native := `{"identity":{"runtime":"stopped"}}`
	// The known outcome as the client reads it from the worker's typed record.
	refusal := &controller.ExitError{Status: 1, Message: legacyRemedy, Remedy: remedyLegacyRecreate}
	prompt := "Recreation deletes files stored only in the container. Workdir, declared cache and account state are preserved. Recreate api now? [y/N]: "
	for _, tc := range []struct {
		name, report, answer string
		refusal              error
		asked, recreate      bool
	}{
		{"yes", stopped, "y\n", refusal, true, true},
		{"default no", stopped, "\n", refusal, true, false},
		{"no", stopped, "n\n", refusal, true, false},
		{"EOF", stopped, "", refusal, true, false},
		{"unconfirmed stop", running, "y\n", refusal, false, false},
		{"native box", native, "y\n", refusal, false, false},
		{"unreadable", "not json", "y\n", refusal, false, false},
		{"no refusal", stopped, "y\n", nil, false, false},
		// F3: present state never stands in for the operation's outcome. Each
		// of these asks nothing although a read shows the box legacy/stopped.
		{"text without typed remedy", stopped, "y\n", errors.New(legacyRemedy), false, false},
		{"uncertain result", stopped, "y\n", &controller.ExitError{Status: 2, Message: legacyRemedy, State: "uncertain", Remedy: remedyLegacyRecreate}, false, false},
		{"lost reply", stopped, "y\n", &controller.OutcomeUnknown{ID: "r", Cause: errors.New("reply lost")}, false, false},
		{"unrelated error", stopped, "y\n", &controller.ExitError{Status: 1, Message: "image build failed"}, false, false},
		{"failed stop", stopped, "y\n", &controller.ExitError{Status: 1, Message: "Box api predates the native runtime and cannot be started or restarted. Stopping it failed: engine refused."}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []offerCall
			cmd, errw := offerCmd(tc.answer)
			err := offerLegacyRecreate(cmd, offerTarget(tc.report, &calls), "api", true, tc.refusal)
			if asked := strings.Contains(errw.String(), "[y/N]"); asked != tc.asked || asked && errw.String() != prompt {
				t.Fatalf("prompt %q", errw.String())
			}
			var methods []string
			for _, c := range calls {
				methods = append(methods, c.method)
			}
			switch {
			case tc.recreate:
				if err != nil || strings.Join(methods, ",") != "box.inspect,box.recreate" || !flagTrue(calls[1].flags, "force") || !flagTrue(calls[1].flags, "no-attach") || !flagTrue(calls[1].flags, "no-autoupdate") || !flagTrue(calls[1].flags, legacyRemedyFlag) {
					t.Fatalf("err %v calls %+v", err, calls)
				}
			case tc.refusal == nil:
				if err != nil || len(calls) != 0 {
					t.Fatalf("err %v calls %+v", err, calls)
				}
			default:
				// Without the typed known refusal nothing is even read.
				if err != tc.refusal || slices.Contains(methods, "box.recreate") || tc.refusal != error(refusal) && len(calls) != 0 {
					t.Fatalf("err %v calls %+v", err, calls)
				}
			}
		})
	}
	recreateOfferInteractive = func(*cobra.Command) bool { return false }
	var calls []offerCall
	cmd, errw := offerCmd("y\n")
	if err := offerLegacyRecreate(cmd, offerTarget(stopped, &calls), "api", false, refusal); err != refusal || len(calls) != 0 || errw.Len() != 0 {
		t.Fatal("non-interactive caller inspected, prompted or recreated", calls, errw.String(), err)
	}
}

// Through the terminal path: a pre-native box gets no pre-dispatch offer; the
// refused request is followed by one fresh read and the confirmation. A failed
// recreate is not retried, and the original request is never sent again. A
// policy apply is offered only when a read before it showed the box running.
func TestLegacyRecreateOfferThroughTheTerminalPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("YOLOCTL_TUI", "1")
	t.Setenv(termio.ViewFDEnv, "")
	previous := recreateOfferInteractive
	t.Cleanup(func() { recreateOfferInteractive = previous })
	recreateOfferInteractive = func(*cobra.Command) bool { return true }
	legacy := func(state string) string {
		return `{"identity":{"runtime":"` + state + `","legacy_runtime":true},"drift":{"strategy":"recreate","reason":"image: a -> b"},"manifest":{"session_command":["svc"]}}`
	}
	for _, tc := range []struct {
		name, method, answer string
		flags                map[string][]string
		before               string
		failRecreate, asked  bool
		want                 []string
		result               error // the original request's result; nil: the typed known refusal
	}{
		{"start yes", "box.start", "y\n", map[string][]string{}, "stopped", false, true, []string{"box.inspect", "box.start", "box.inspect", "box.recreate"}, nil},
		{"start --no-attach no", "box.start", "n\n", map[string][]string{"no-attach": {"true"}}, "stopped", false, true, []string{"box.start", "box.inspect"}, nil},
		{"restart yes, failed recreate", "box.restart", "y\n", map[string][]string{}, "running", true, true, []string{"box.inspect", "box.restart", "box.inspect", "box.recreate"}, nil},
		{"attach no", "box.attach", "n\n", map[string][]string{}, "stopped", false, true, []string{"box.inspect", "box.attach", "box.inspect"}, nil},
		{"set on a running box", "box.set", "y\n", map[string][]string{}, "running", false, true, []string{"box.inspect", "box.set", "box.inspect", "box.recreate"}, nil},
		{"set on a stopped box", "box.set", "y\n", map[string][]string{}, "stopped", false, false, []string{"box.inspect", "box.set"}, nil},
		// F3 controls: a later read shows the box legacy/stopped, but the
		// request's own outcome is not the typed known refusal: nothing is
		// read again, asked or recreated, and the result is unchanged.
		{"lost reply", "box.start", "y\n", map[string][]string{}, "stopped", false, false, []string{"box.inspect", "box.start"}, &controller.OutcomeUnknown{ID: "r", Cause: errors.New("reply lost")}},
		{"unrelated error", "box.start", "y\n", map[string][]string{}, "stopped", false, false, []string{"box.inspect", "box.start"}, &controller.ExitError{Status: 1, Message: "image build failed"}},
		{"failed stop", "box.restart", "y\n", map[string][]string{}, "running", false, false, []string{"box.inspect", "box.restart"}, &controller.ExitError{Status: 1, Message: "Box api predates the native runtime and cannot be started or restarted. Stopping it failed: engine refused."}},
		{"uncertain", "box.restart", "y\n", map[string][]string{}, "running", false, false, []string{"box.inspect", "box.restart"}, &controller.ExitError{Status: 2, State: "uncertain", Remedy: remedyLegacyRecreate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []offerCall
			sent := false
			target := controllerTarget{Space: "work", Operator: true, Call: func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
				calls = append(calls, offerCall{req.Method, req.Flags})
				switch req.Method {
				case "box.inspect":
					state := tc.before
					if sent {
						state = "stopped"
					}
					_, _ = io.WriteString(streams.Out, legacy(state))
				case "box.recreate":
					if tc.failRecreate {
						return errors.New("recreate failed")
					}
				default:
					sent = true
					if tc.result != nil {
						return tc.result
					}
					return &controller.ExitError{Status: 1, Message: legacyRemedy, Remedy: remedyLegacyRecreate}
				}
				return nil
			}}
			cmd := newAgentboxAttachCmd()
			cmd.SetContext(context.Background())
			var errw bytes.Buffer
			cmd.SetIn(strings.NewReader(tc.answer))
			cmd.SetOut(io.Discard)
			cmd.SetErr(&errw)
			req := controller.Request{ID: "r", Method: tc.method, Space: "work", Args: []string{"api"}, Flags: tc.flags, Dash: -1}
			err := callControllerTerminal(cmd, target, req)
			var methods []string
			for _, c := range calls {
				methods = append(methods, c.method)
			}
			if strings.Join(methods, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("calls %v, want %v", methods, tc.want)
			}
			recreated := slices.Contains(methods, "box.recreate")
			if strings.Count(errw.String(), "[y/N]") != map[bool]int{true: 1}[tc.asked] || strings.Contains(errw.String(), "requires recreation") {
				t.Fatalf("prompt %q", errw.String())
			}
			if recreated && !tc.failRecreate && err != nil || !recreated && err == nil || tc.failRecreate && (err == nil || err.Error() != "recreate failed") {
				t.Fatalf("err %v", err)
			}
			if tc.result != nil && !errors.Is(err, tc.result) {
				t.Fatalf("the original result was replaced: %v", err)
			}
			if recreated && !flagTrue(calls[len(calls)-1].flags, legacyRemedyFlag) {
				t.Fatalf("the accepted recreate lacks its remedy marker: %+v", calls[len(calls)-1].flags)
			}
		})
	}
}

// Regression 6: --restart-sessions never sets a pre-native box aside as "left
// running": its restart is attempted (stopping it with the remedy), the other
// boxes still restart, nothing is recreated and the run fails naming the
// command. Unrelated recreation drift keeps being set aside.
func TestRestartSessionsNeverSetsAPreNativeBoxAside(t *testing.T) {
	o, log, errw := workloadOfferFixture(t, false)
	o.drift = func(_ context.Context, names []string) ([]boxDriftView, error) {
		view := make([]boxDriftView, 0, len(names))
		for _, name := range names {
			v := boxDriftView{Name: name, Drift: boxstate.DriftInSync}
			switch name {
			case "chat", "svc":
				v.LegacyRuntime = true
				v.Drift = boxstate.DriftNeedsRecreate
				v.Reasons = []boxstate.Reason{{Code: boxstate.ReasonLauncherRecreate, Message: "old runtime"}}
			}
			view = append(view, v)
		}
		return view, nil
	}
	o.restart = func(_ context.Context, name, _ string, workload bool) error {
		*log = append(*log, "restart "+name)
		if name == "chat" || name == "svc" {
			return errors.New(strings.ReplaceAll(legacyRemedy, "coder", name))
		}
		return nil
	}
	err := offerSessionRestarts(context.Background(), "work", *o, true)
	if !slices.Equal(*log, []string{"restart alpha", "restart chat", "restart svc"}) {
		t.Fatalf("restarts %v", *log)
	}
	out := errw.String()
	if err == nil || !strings.Contains(err.Error(), "chat, svc predate the native runtime and cannot be restarted; nothing was recreated. Recreate to continue: yoloctl box recreate chat --space work; yoloctl box recreate svc --space work") ||
		strings.Contains(out, "left running") || slices.Contains(*log, "chat ensure") ||
		!strings.Contains(out, "The chat room may be down. Recover it after recreating the box:\n  yoloctl box recreate chat --space work\n  yoloctl chat ensure --space work") ||
		!strings.Contains(out, "The managed workload of svc may be down. Recover it after recreating the box:\n  yoloctl box recreate svc --space work\n  yoloctl box attach svc --space work") {
		t.Fatalf("err %v\n%s", err, out)
	}

	// Unrelated recreation drift: still set aside, never restarted.
	o, log, errw = workloadOfferFixture(t, false)
	o.drift = func(_ context.Context, names []string) ([]boxDriftView, error) {
		view, _ := inSync(context.Background(), names)
		for i := range view {
			if view[i].Name == "svc" {
				view[i].Drift = boxstate.DriftNeedsRecreate
				view[i].Reasons = []boxstate.Reason{{Code: boxstate.ReasonDriftNeedsRecreate, Message: "image: a -> b"}}
			}
		}
		return view, nil
	}
	if err := offerSessionRestarts(context.Background(), "work", *o, true); err != nil || slices.Contains(*log, "restart svc workload=true") || !strings.Contains(errw.String(), "so it is left running") {
		t.Fatalf("err %v log %v\n%s", err, *log, errw)
	}
}

// Regression 4, session revival: a stopped pre-native box's attach is refused
// by the shared native preparation before the tracked record, the tombstone or
// any launch, with the remedy.
func TestPreNativeReviveIsRefusedBeforeSessionEffects(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	cfg.SchemaVersion = agentbox.SchemaVersion
	cfg.Image = defaultAgentboxImage
	cfg.Workdir = agentbox.NewEphemeralWorkdir(cfg.Name)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit(cfg.Name, 7)
	record, _ := layout.BoxSessionPath(cfg.Space, cfg.Name)
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	tomb := f.tombstone[cfg.Name]
	previous := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previous })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: cfg.Space, Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: cfg.Name, Status: "Exited (0)"}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionNone}
			},
			InspectActual: func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
				return agentbox.ActualState{Exists: true, Image: "fixture", PolicyContent: []byte("p"), ReadOnlyWD: !cfg.WorkdirWritable(), Account: cfg.Account, Runtime: agentbox.RuntimeEvidence{Mode: agentbox.RuntimeModeConfined}}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) { return []byte("p"), nil },
			ResolvedImage: func(*agentbox.Config) (string, bool, error) { return "fixture", true, nil },
		}
	}
	old := prepareNativeExistingStart
	t.Cleanup(func() { prepareNativeExistingStart = old })
	prepareNativeExistingStart = func(context.Context, *container.Client, *agentbox.Config, bool) (nativePreparedStart, bool, error) {
		return nil, false, &agentbox.LegacyRuntimeError{Space: cfg.Space, Box: cfg.Name}
	}
	_, err = ensureAgentSession(held(t.Context()), container.New(cfg.Space), cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{})
	after, readErr := os.ReadFile(record)
	var legacy *agentbox.LegacyRuntimeError
	if !errors.As(err, &legacy) || !strings.Contains(err.Error(), "predates the native runtime and cannot be started or restarted") || readErr != nil || !bytes.Equal(before, after) || f.tombstone[cfg.Name] != tomb || len(f.starts) != 1 {
		t.Fatal("revival of a pre-native box was not refused before session effects", err, readErr, len(f.starts))
	}
}

// A start-type refusal (start, revive, chat server) never stops: one that
// meets the box running says so and stops nothing.
func TestStartRefusalNeverStopsARunningPreNativeBox(t *testing.T) {
	_, stops := legacyStartFixture(t, true, nil)
	refused := &agentbox.LegacyRuntimeError{Space: "default", Box: "coder"}
	err := refuseLegacyRun(t.Context(), container.New("default"), "default", "coder", io.Discard, refused, legacyStart)
	want := "Box coder predates the native runtime and cannot be started or restarted. It is running. Recreate it to continue: yoloctl box recreate coder --space default."
	var legacy *agentbox.LegacyRuntimeError
	if err == nil || err.Error() != want || !errors.As(err, &legacy) || len(*stops) != 0 {
		t.Fatal(err, *stops)
	}
}

// F1: a pre-native box whose declaration also requires recreation (a saved
// image change) still takes its own remedy: a running one's restart stops its
// exact run instead of returning the generic drift refusal, and a stopped
// one's start is refused without a stop. The native drift preservation is
// covered by TestBoxStartAndRestartPreserveExistingContainer.
func TestPreNativeRemedyPrecedesRecreateDrift(t *testing.T) {
	for _, tc := range []struct {
		operation string
		running   bool
		stops     []string
	}{
		{"restart", true, []string{"holder", "ports", "container", "session", "netns"}},
		{"start", false, nil},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			callsPath, stops := legacyStartFixtureWithDrift(t, "before start", tc.running, nil)
			cmd := newAgentboxStartCmd()
			if tc.operation == "restart" {
				cmd = newAgentboxRestartCmd()
			}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"coder", "--space", "default", "--no-attach", "--no-autoupdate"})
			err := cmd.Execute()
			var legacy *agentbox.LegacyRuntimeError
			if err == nil || err.Error() != legacyRemedy || !errors.As(err, &legacy) || strings.Contains(err.Error(), "requires recreation") {
				t.Fatalf("got %v, want the pre-native remedy", err)
			}
			if !slices.Equal(*stops, tc.stops) {
				t.Fatalf("stops %v, want %v", *stops, tc.stops)
			}
			assertNoLegacyRun(t, readCalls(t, callsPath))
		})
	}
}

// F2: a recreation accepted from the pre-native remedy that fails after its
// replacement started (session start or autoupdate) is rolled back by box
// start's shared result policy, stopping only the exact run it started: a
// same-name container that is not that run, or a run without a recorded
// identity, is never stopped, and the failure says so. Nothing relaunches a
// previous runtime or policy: the rollback only stops.
func TestRemedyRecreateFailureStopsOnlyItsExactReplacement(t *testing.T) {
	failure := errors.New("autoupdate coder: npm failed")
	attempt := func(id string) func(context.Context) error {
		return func(ctx context.Context) error {
			container.EngineCallsFrom(ctx).MarkStart()
			if id != "" {
				container.ExpectedRunsFrom(ctx).Expect("coder", container.Run{ID: id, StartedAt: "2026-11-16T10:00:00Z", PID: 7})
			}
			return failure
		}
	}
	for _, tc := range []struct {
		name     string
		started  string // the run this recreate recorded starting
		replaced bool   // later reads show another same-name container
		stopped  []string
		message  string
	}{
		{"its replacement", firstID, false, []string{firstID}, "start failed: autoupdate coder: npm failed"},
		{"another same-name container", firstID, true, nil, "is now another container than the replacement this recreate started, so it was not stopped"},
		{"unknown identity", "", false, nil, "the identity of the replacement this recreate started is unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStop{present: true, running: true, replaced: tc.replaced}
			if tc.replaced {
				f.reads = 1 // every read after the attempt shows the other container
			}
			ctx := container.WithExpectedRuns(t.Context())
			err := runStartForResult(ctx, "coder", exactRunStopSteps(f.steps(), "coder"), attempt(tc.started))
			if err == nil || !strings.Contains(err.Error(), tc.message) || !slices.Equal(f.stopped, tc.stopped) {
				t.Fatalf("err %v stopped %v", err, f.stopped)
			}
		})
	}
	// A successful remedy recreate whose replacement runs stops nothing.
	f := &fakeStop{present: true, running: true, after: true}
	f.reads = 1
	ctx := container.WithExpectedRuns(t.Context())
	ok := func(ctx context.Context) error {
		container.EngineCallsFrom(ctx).MarkStart()
		container.ExpectedRunsFrom(ctx).Expect("coder", container.Run{ID: firstID, StartedAt: "2026-11-16T10:00:00Z"})
		return nil
	}
	if err := runStartForResult(ctx, "coder", exactRunStopSteps(f.steps(), "coder"), ok); err != nil || len(f.stopped) != 0 {
		t.Fatalf("success: %v stopped %v", err, f.stopped)
	}
}

// The remedy marker exists only on box recreate, hidden, and is what the host
// sends after an accepted offer; an ordinary recreate keeps its behavior.
func TestRemedyRecreateMarkerIsHiddenOnRecreateOnly(t *testing.T) {
	flag := newAgentboxRecreateCmd().Flags().Lookup(legacyRemedyFlag)
	if flag == nil || !flag.Hidden || flag.DefValue != "false" {
		t.Fatalf("recreate marker: %+v", flag)
	}
	for _, cmd := range []*cobra.Command{newAgentboxStartCmd(), newAgentboxRestartCmd(), newAgentboxAttachCmd()} {
		if cmd.Flags().Lookup(legacyRemedyFlag) != nil {
			t.Fatalf("%s carries the recreate marker", cmd.Name())
		}
	}
}

// The worker types the remedy only for a known pre-native refusal whose box a
// read showed stopped; an unconfirmed stop, another error or an uncertain
// result carries none.
func TestOutcomeRemedyOnlyForAConfirmedStoppedRefusal(t *testing.T) {
	legacy := &agentbox.LegacyRuntimeError{Space: "default", Box: "coder"}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"stopped", &legacyRuntimeRefusal{msg: legacyRemedy, legacy: legacy, stopped: true}, true},
		{"wrapped stopped", fmt.Errorf("reconcile coder: %w", &legacyRuntimeRefusal{msg: legacyRemedy, legacy: legacy, stopped: true}), true},
		{"failed stop", &legacyRuntimeRefusal{msg: "Stopping it failed", legacy: legacy}, false},
		{"bare typed error", legacy, false},
		{"other", errors.New(legacyRemedy), false},
		{"nil", nil, false},
	} {
		attrs := outcomeRemedy(tc.err)
		if got := len(attrs) == 1 && attrs[0].Key == prettylog.KeyRemedy && attrs[0].Value.String() == remedyLegacyRecreate; got != tc.want || !tc.want && len(attrs) != 0 {
			t.Fatalf("%s: %v", tc.name, attrs)
		}
	}
}

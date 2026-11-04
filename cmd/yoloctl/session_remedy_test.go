package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/termio"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

const imageDrift = "image: docker.io/yoloctl/agentbox-composed:152e5241c2bf → yoloctl/agentbox-composed:990ade22f540"

func recreationDrift() boxstate.Box {
	return boxstate.Box{Drift: boxstate.DriftNeedsRecreate, Reasons: []boxstate.Reason{{Code: boxstate.ReasonDriftNeedsRecreate, Message: imageDrift}}}
}

// stubStaleSessionResolution answers the drift resolution behind the
// guidance, one batch for exactly the attached box, and counts its calls.
func stubStaleSessionResolution(t *testing.T, b boxstate.Box, err error) *int {
	t.Helper()
	calls := 0
	previous := resolveStaleSessionsFn
	resolveStaleSessionsFn = func(_ context.Context, space string, names []string) ([]boxstate.Box, error) {
		calls++
		if space != "default-fc" || !slices.Equal(names, []string{"astra"}) {
			t.Fatalf("resolved %s/%v", space, names)
		}
		if err != nil {
			return nil, err
		}
		b.Name = "astra"
		return []boxstate.Box{b}, nil
	}
	t.Cleanup(func() { resolveStaleSessionsFn = previous })
	return &calls
}

// The holder of another build replaces this build's attach with its own
// fixed restart advice. Through both the direct attach and the controller's
// relayed view, the error names the remedy a full drift resolution proves:
// recreation for recreation drift, never a restart a box would refuse.
func TestAttachToAnotherBuildsSessionNamesTheProvedRemedy(t *testing.T) {
	stubProtectedChatServer(t, false, nil)
	t.Setenv("YOLOCTL_TUI", "1")
	previous, previousNative := dialSessionVMFn, nativeSessionTransportFn
	nativeSessionTransportFn = func() bool { return true }
	t.Cleanup(func() { dialSessionVMFn = previous; nativeSessionTransportFn = previousNative })
	dialSessionVMFn = func(context.Context, string, string) (io.ReadWriteCloser, error) {
		holder, client := net.Pipe()
		t.Cleanup(func() { _ = holder.Close() })
		go func() {
			// A well-formed HELLO of another protocol: an older build.
			_, _ = holder.Write([]byte{'h', 0, 1, 0})
			_, _ = io.Copy(io.Discard, holder)
		}()
		return client, nil
	}
	check := func(t *testing.T, err error) {
		t.Helper()
		msg := fmt.Sprint(err)
		if !errors.Is(err, session.ErrBuildMismatch) || !strings.Contains(msg, "also requires recreation ("+imageDrift+")") ||
			!strings.Contains(msg, "yoloctl box recreate astra --space default-fc --no-attach") || strings.Contains(msg, "restart its box") {
			t.Fatalf("attach error: %v", err)
		}
	}
	t.Run("direct", func(t *testing.T) {
		calls := stubStaleSessionResolution(t, recreationDrift(), nil)
		check(t, attachToSession(t.Context(), "default-fc", "astra"))
		if *calls != 1 {
			t.Fatalf("%d resolutions", *calls)
		}
	})
	t.Run("relayed view", func(t *testing.T) {
		stubStaleSessionResolution(t, recreationDrift(), nil)
		// The inherited view channel is a bidirectional socket.
		view, tui, err := termio.ViewPair()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = view.Close(); _ = tui.Close() })
		go func() { _, _ = io.Copy(io.Discard, tui) }()
		check(t, attachToSession(context.WithValue(t.Context(), controllerViewKey{}, view), "default-fc", "astra"))
	})
}

func TestStaleSessionGuidanceStatesOnlyWhatDriftProves(t *testing.T) {
	mismatch := fmt.Errorf("session build/core mismatch; quit the old agent session and attach again with the selected build, or explicitly restart its box: %w", session.ErrBuildMismatch)
	agentBox := func(b boxstate.Box) boxstate.Box {
		b.Config = &agentbox.Config{Name: "astra", Agent: "codex", Account: "work"}
		b.Conversation = &agentsession.Status{Provider: "codex", Policy: "tracked", Tracked: true, ID: "019a"}
		return b
	}
	for _, c := range []struct {
		name    string
		box     boxstate.Box
		err     error
		want    []string
		without []string
	}{
		{"recreation", agentBox(recreationDrift()), nil, []string{
			"cannot attach to, and box \"astra\" also requires recreation (" + imageDrift + "), which a restart refuses",
			"inspect with yoloctl box inspect astra --space default-fc, then explicitly use yoloctl box recreate astra --space default-fc --no-attach and attach again",
			"the workdir and the account's native state are kept, other files in the container are lost",
			"the next attach requests conversation 019a again",
		}, []string{"box restart"}},
		{"restart only", agentBox(boxstate.Box{Drift: boxstate.DriftNeedsRestart}), nil, []string{
			"restart its box on this build with yoloctl box restart astra --space default-fc --no-attach, then attach again",
			"ends the agent's current process; the next attach requests conversation 019a again",
		}, []string{"recreat"}},
		{"in sync agentless", boxstate.Box{Drift: boxstate.DriftInSync, Config: &agentbox.Config{Name: "astra", Agent: "none", Account: "none"}}, nil, []string{
			"yoloctl box restart astra --space default-fc --no-attach",
		}, []string{"conversation", "recreat"}},
		{"resolution failed", boxstate.Box{}, errors.New("controller unavailable"), []string{
			"whether box \"astra\" needs a restart or a recreation could not be determined (controller unavailable): inspect it with yoloctl box inspect astra --space default-fc",
		}, []string{"box restart", "box recreate"}},
		{"drift error", boxstate.Box{Drift: boxstate.DriftError, DriftError: "inspect actual state: timeout"}, nil, []string{
			"could not be determined (inspect actual state: timeout)",
		}, []string{"box restart", "box recreate"}},
		{"drift unknown", boxstate.Box{Drift: boxstate.DriftUnknown}, nil, []string{"could not be determined (its drift is unknown)"}, []string{"box restart", "box recreate"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubStaleSessionResolution(t, c.box, c.err)
			err := staleSessionGuidance(t.Context(), "default-fc", "astra", mismatch)
			if !errors.Is(err, session.ErrBuildMismatch) || IsUserError(err) {
				t.Fatalf("the guidance keeps the mismatch class: %v", err)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %q", want, err)
				}
			}
			for _, not := range append(c.without, "quit the old agent session") {
				if strings.Contains(err.Error(), not) {
					t.Errorf("unexpected %q in %q", not, err)
				}
			}
		})
	}
	t.Run("other outcomes pass through", func(t *testing.T) {
		calls := stubStaleSessionResolution(t, recreationDrift(), nil)
		for _, err := range []error{nil, context.Canceled, session.ErrSessionClosed, &session.ExitError{Status: 3}, errors.New("session protocol mismatch")} {
			if got := staleSessionGuidance(t.Context(), "default-fc", "astra", err); got != err {
				t.Errorf("%v became %v", err, got)
			}
		}
		if *calls != 0 {
			t.Fatal("only a mismatch resolves drift")
		}
	})
}

// box.drift is a narrow host read: a bounded list of distinct named boxes,
// resolved in one batch, each with its drift or the batch's error, and no
// operator access.
func TestBoxDriftViewIsBoundedAndHostOnly(t *testing.T) {
	previous := resolveStaleSessionsFn
	t.Cleanup(func() { resolveStaleSessionsFn = previous })
	var asked [][]string
	var batchErr error
	resolveStaleSessionsFn = func(_ context.Context, space string, names []string) ([]boxstate.Box, error) {
		asked = append(asked, append([]string{space}, names...))
		if batchErr != nil {
			return nil, batchErr
		}
		astra := recreationDrift()
		astra.Name = "astra"
		return []boxstate.Box{astra, {Name: "broken", Drift: boxstate.DriftError, DriftError: "inspect failed"}}, nil
	}
	view := func(t *testing.T) []boxDriftView {
		t.Helper()
		var out bytes.Buffer
		handled, err := runControllerView(t.Context(), controller.Request{Method: boxDriftMethod, Space: "work", Args: []string{"astra", "broken"}}, &out)
		if !handled || err != nil {
			t.Fatal(handled, err)
		}
		var v []boxDriftView
		if err := decodeBoundedJSON(&out, &v, 1<<20); err != nil {
			t.Fatal(err)
		}
		return v
	}
	got := view(t)
	if len(asked) != 1 || !slices.Equal(asked[0], []string{"work", "astra", "broken"}) || len(got) != 2 {
		t.Fatalf("one batch for both: asked %v, view %+v", asked, got)
	}
	if r, reason := got[0].remedy(); r != remedyRecreate || reason != imageDrift {
		t.Fatalf("astra: %v %q", r, reason)
	}
	if r, reason := got[1].remedy(); r != remedyUnknown || reason != "inspect failed" {
		t.Fatalf("broken: %v %q", r, reason)
	}
	batchErr = errors.New("space runtime unavailable")
	for _, v := range view(t) {
		if r, reason := v.remedy(); r != remedyUnknown || reason != "space runtime unavailable" {
			t.Fatalf("a failed batch is every box's error: %+v", v)
		}
	}
	for _, names := range [][]string{nil, slices.Repeat([]string{"a"}, maxDriftBoxes+1), {"../x"}, {"a", "a"}} {
		asked = nil
		if _, err := runControllerView(t.Context(), controller.Request{Method: boxDriftMethod, Space: "work", Args: names}, io.Discard); err == nil || len(asked) != 0 {
			t.Errorf("%d names %v accepted", len(names), names)
		}
	}
	request := controller.Request{ID: strings.Repeat("r", 20), Method: boxDriftMethod, Space: "work", Args: []string{"astra"}, Dash: -1}
	if err := controller.Authorize(controller.Principal{}, request, "work", controllerOperations); err != nil {
		t.Fatal("host admin refused:", err)
	}
	if err := controller.Authorize(controller.Principal{Box: "lead", Incarnation: "live"}, request, "work", controllerOperations); err == nil {
		t.Fatal("an operator box may read box.drift")
	}
	if controllerOperations[boxDriftMethod].Mutates {
		t.Fatal("box.drift is a read")
	}
}

// After an update, a box whose session runs the previous build and whose
// declaration also needs recreation is left running with its explicit
// recreation remedy: neither the prompt nor --restart-sessions restarts or
// recreates it. Other boxes keep the ordinary restart offer.
func TestSessionRestartOfferLeavesRecreationsRunning(t *testing.T) {
	driftOf := map[string]boxDriftView{
		"alpha": {Name: "alpha", Drift: boxstate.DriftNeedsRecreate, Reasons: recreationDrift().Reasons},
		"gamma": {Name: "gamma", Drift: boxstate.DriftInSync},
	}
	fixture := func(t *testing.T, interactive bool, answer string) (*sessionRestartOffer, *[]string, *bytes.Buffer) {
		o, restarted, errw := sessionOfferFixture(interactive, answer)
		o.drift = func(_ context.Context, names []string) ([]boxDriftView, error) {
			if !slices.Equal(names, []string{"alpha", "gamma"}) {
				t.Fatalf("drift of %v", names)
			}
			return []boxDriftView{driftOf["alpha"], driftOf["gamma"]}, nil
		}
		return o, restarted, errw
	}
	recreation := []string{
		"Box alpha runs an agent session of the previous build and also requires recreation (" + imageDrift + "), which a restart refuses, so it is left running.",
		"the workdir and the account's native state are kept",
		"  yoloctl box inspect alpha --space work\n  yoloctl box recreate alpha --space work --no-attach\n",
		"alpha: its conversation is not known here",
	}
	t.Run("script", func(t *testing.T) {
		o, restarted, errw := fixture(t, false, "")
		if err := offerSessionRestarts(t.Context(), "work", *o, false); err != nil {
			t.Fatal(err)
		}
		out := errw.String()
		for _, want := range append(recreation, "this build cannot attach to: gamma.", "  yoloctl box restart gamma --space work --no-attach\n") {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
		if len(*restarted) != 0 || strings.Contains(out, "box restart alpha") {
			t.Fatalf("restarted %v or offered alpha a restart:\n%s", *restarted, out)
		}
	})
	t.Run("prompt", func(t *testing.T) {
		o, restarted, errw := fixture(t, true, "y\n")
		if err := offerSessionRestarts(t.Context(), "work", *o, false); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(*restarted, []string{"gamma 13@g"}) || !strings.Contains(errw.String(), "Restart 1 box now? [y/N]: ") {
			t.Fatalf("restarted %v:\n%s", *restarted, errw)
		}
	})
	t.Run("restart-sessions", func(t *testing.T) {
		o, restarted, errw := fixture(t, false, "")
		if err := offerSessionRestarts(t.Context(), "work", *o, true); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(*restarted, []string{"gamma 13@g"}) {
			t.Fatalf("--restart-sessions restarted %v:\n%s", *restarted, errw)
		}
	})
	t.Run("only recreations", func(t *testing.T) {
		o, restarted, errw := fixture(t, true, "y\n")
		driftOf["gamma"] = driftOf["alpha"]
		t.Cleanup(func() { driftOf["gamma"] = boxDriftView{Name: "gamma", Drift: boxstate.DriftInSync} })
		o.drift = func(context.Context, []string) ([]boxDriftView, error) {
			return []boxDriftView{driftOf["alpha"], {Name: "gamma", Drift: boxstate.DriftNeedsRecreate}}, nil
		}
		if err := offerSessionRestarts(t.Context(), "work", *o, true); err != nil {
			t.Fatal(err)
		}
		if len(*restarted) != 0 || strings.Contains(errw.String(), "[y/N]") || !strings.Contains(errw.String(), "also requires recreation (drift that only a new container applies)") {
			t.Fatalf("restarted %v:\n%s", *restarted, errw)
		}
	})
	t.Run("drift unavailable", func(t *testing.T) {
		o, restarted, errw := fixture(t, false, "")
		o.drift = func(context.Context, []string) ([]boxDriftView, error) { return nil, errors.New("controller busy") }
		if err := offerSessionRestarts(t.Context(), "work", *o, true); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(*restarted, []string{"alpha 11@a", "gamma 13@g"}) || !strings.Contains(errw.String(), "could not check whether alpha, gamma also require recreation (controller busy); a restart refuses a box that does") {
			t.Fatalf("restarted %v:\n%s", *restarted, errw)
		}
	})
	t.Run("one unknown", func(t *testing.T) {
		o, restarted, errw := fixture(t, false, "")
		o.drift = func(context.Context, []string) ([]boxDriftView, error) {
			return []boxDriftView{{Name: "alpha", Error: "inspect failed"}}, nil
		}
		if err := offerSessionRestarts(t.Context(), "work", *o, true); err != nil {
			t.Fatal(err)
		}
		out := errw.String()
		if !slices.Equal(*restarted, []string{"alpha 11@a", "gamma 13@g"}) || !strings.Contains(out, "Could not check whether box alpha also requires recreation (inspect failed)") ||
			!strings.Contains(out, "Could not check whether box gamma also requires recreation (not reported)") {
			t.Fatalf("restarted %v:\n%s", *restarted, out)
		}
	})
	t.Run("managed workloads", func(t *testing.T) {
		o, log, errw := workloadOfferFixture(false)
		o.drift = func(_ context.Context, names []string) ([]boxDriftView, error) {
			view := []boxDriftView{}
			for _, name := range names {
				view = append(view, boxDriftView{Name: name, Drift: boxstate.DriftNeedsRecreate})
			}
			return view, nil
		}
		if err := offerSessionRestarts(t.Context(), "work", *o, true); err != nil {
			t.Fatal(err)
		}
		out := errw.String()
		for _, want := range []string{
			"  yoloctl box recreate chat --space work --no-attach\n  yoloctl chat ensure --space work   # then start the chat room again on this build\n",
			"  yoloctl box recreate svc --space work --no-attach\n  yoloctl box attach svc --space work   # then start its managed workload again, and detach\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
		if len(*log) != 0 {
			t.Fatalf("nothing is restarted or ensured: %v", *log)
		}
	})
}

// The real batch path lists the space once and does per-box work only for the
// named boxes, all under one overall bound: an inspection that never answers
// ends at that bound for the whole batch, as each box's error.
func TestBoxDriftBatchHasOneInventoryAndOneBound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	workdir := t.TempDir()
	saveSpaceForTest(t, "work", workdir)
	names := []string{"alpha", "beta", "gamma", "delta"}
	for _, name := range names {
		cfg := &agentbox.Config{Name: name, Space: "work", Agent: "none", Account: "none", Image: "img",
			Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: workdir, Canonical: workdir}}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
	}
	previousResolver, previousBound := newBoxStateResolver, staleSessionInspectTimeout
	t.Cleanup(func() { newBoxStateResolver, staleSessionInspectTimeout = previousResolver, previousBound })
	staleSessionInspectTimeout = 100 * time.Millisecond
	var mu sync.Mutex
	work := map[string][]string{}
	note := func(kind, name string) {
		mu.Lock()
		defer mu.Unlock()
		work[kind] = append(work[kind], name)
	}
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				note("list", "work")
				out := []container.Container{{Name: "orphan", Status: "Up"}}
				for _, name := range names {
					out = append(out, container.Container{Name: name, Status: "Up"})
				}
				return out, nil
			},
			ProbeSession: func(_ context.Context, _ string, name string, _ bool) boxstate.SessionInfo {
				note("probe", name)
				return boxstate.SessionInfo{State: boxstate.SessionRunning}
			},
			InspectActual: func(ctx context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
				note("inspect", cfg.Name)
				<-ctx.Done()
				return agentbox.ActualState{}, ctx.Err()
			},
			ComposePolicy: func(cfg *agentbox.Config) ([]byte, error) { note("policy", cfg.Name); return nil, nil },
			ResolvedImage: func(cfg *agentbox.Config) (string, bool, error) { note("image", cfg.Name); return "img", false, nil },
		}
	}
	start := time.Now()
	view, err := collectBoxDrift(t.Context(), "work", []string{"gamma", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the batch outlived its bound: %v", elapsed)
	}
	if !slices.Equal(work["list"], []string{"work"}) || len(work["probe"]) != 2 || len(work["inspect"]) != 2 || len(work["policy"]) != 0 || len(work["image"]) != 0 {
		t.Fatalf("per-box work for unrelated boxes or a second inventory: %v", work)
	}
	for _, kind := range []string{"probe", "inspect"} {
		got := slices.Sorted(slices.Values(work[kind]))
		if !slices.Equal(got, []string{"alpha", "gamma"}) {
			t.Fatalf("%s ran for %v", kind, got)
		}
	}
	if len(view) != 2 || view[0].Name != "gamma" || view[1].Name != "alpha" {
		t.Fatalf("results follow the request: %+v", view)
	}
	for _, v := range view {
		if r, reason := v.remedy(); r != remedyUnknown || !strings.Contains(reason, "deadline exceeded") {
			t.Fatalf("%s: %v %q", v.Name, r, reason)
		}
	}
}

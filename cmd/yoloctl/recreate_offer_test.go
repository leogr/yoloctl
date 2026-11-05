package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// fakeDriftResolver reports one box whose container runs image actual while
// its declaration resolves to desired.
func fakeDriftResolver(t *testing.T, cfg *agentbox.Config, status, actual, desired string) {
	t.Helper()
	previous := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previous })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: cfg.Space, Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: cfg.Name, Status: status}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionNone}
			},
			InspectActual: func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
				return agentbox.ActualState{Exists: true, Image: actual, PolicyContent: []byte("p"), ReadOnlyWD: !cfg.WorkdirWritable(), Account: cfg.Account, Runtime: agentbox.RuntimeEvidence{Mode: agentbox.RuntimeModeConfined}}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) { return []byte("p"), nil },
			ResolvedImage: func(*agentbox.Config) (string, bool, error) { return desired, true, nil },
		}
	}
}

func savedNoneBox(t *testing.T, name string) *agentbox.Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("YOLOCTL_HOME", "")
	cfg := &agentbox.Config{Name: name, ID: agentbox.NewIdentity(), Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(name)}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A stopped box with recreate-only drift is refused with the start/restart
// text before any image work: the warm state sees no image request.
func TestStoppedAttachRefusesRecreateDriftBeforeImageWork(t *testing.T) {
	cfg := savedNoneBox(t, "api")
	fakeDriftResolver(t, cfg, "Exited (0)", "yoloctl/agentbox-composed:old", "yoloctl/agentbox-composed:new")
	previousHolder := holderUnitActiveFn
	t.Cleanup(func() { holderUnitActiveFn = previousHolder })
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return false }
	var imageCalls []string
	state := &imageWarmState{enabled: true, entries: map[string]warmImage{}, call: func(_ context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		imageCalls = append(imageCalls, r.Action)
		return controller.ImageReply{}, errors.New("image work must not start")
	}}
	ctx := context.WithValue(held(t.Context()), warmKey{}, state)
	_, err := ensureAgentSession(ctx, container.New(cfg.Space), cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{})
	want := agentbox.StartRecreateError(cfg, "image: yoloctl/agentbox-composed:old -> yoloctl/agentbox-composed:new").Error()
	if err == nil || err.Error() != want || len(imageCalls) != 0 {
		t.Fatalf("got %v (image calls %v), want %q", err, imageCalls, want)
	}
}

// Unreadable evidence and in-sync boxes decide nothing early.
func TestEarlyRecreateRefusalNeedsAffirmativeDrift(t *testing.T) {
	cfg := savedNoneBox(t, "api")
	fakeDriftResolver(t, cfg, "Exited (0)", "same", "same")
	if err := refuseRecreateBeforeImageWork(t.Context(), cfg.Space, cfg); err != nil {
		t.Fatal("in-sync box refused", err)
	}
	previous := newBoxStateResolver
	newBoxStateResolver = func() boxstate.Resolver {
		r := previous()
		r.ListSpaces = func(context.Context) ([]vmhost.Instance, error) { return nil, errors.New("unreachable") }
		return r
	}
	if err := refuseRecreateBeforeImageWork(t.Context(), cfg.Space, cfg); err != nil {
		t.Fatal("unreadable evidence refused", err)
	}
}

type offerCall struct {
	method string
	flags  map[string][]string
}

func offerTarget(report string, calls *[]offerCall) controllerTarget {
	return controllerTarget{Space: "work", Call: func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
		*calls = append(*calls, offerCall{req.Method, req.Flags})
		if req.Method == "box.inspect" {
			_, _ = io.WriteString(streams.Out, report)
		}
		return nil
	}}
}

func offerCmd(answer string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var errw bytes.Buffer
	cmd.SetIn(strings.NewReader(answer))
	cmd.SetErr(&errw)
	cmd.SetOut(io.Discard)
	return cmd, &errw
}

func TestRecreateOfferAsksOnceAndRecreatesOnlyOnYes(t *testing.T) {
	previous := recreateOfferInteractive
	t.Cleanup(func() { recreateOfferInteractive = previous })
	recreateOfferInteractive = func(*cobra.Command) bool { return true }
	stopped := `{"identity":{"runtime":"stopped"},"drift":{"strategy":"recreate","reason":"image: a -> b"}}`
	running := `{"identity":{"runtime":"running"},"drift":{"strategy":"recreate","reason":"image: a -> b"}}`
	restart := `{"identity":{"runtime":"running"},"drift":{"strategy":"restart","reason":"tmpdir"}}`
	for _, tc := range []struct {
		name, report, answer string
		kind                 recreateOfferKind
		noAutoupdate         bool
		prompt               string
		recreate             bool
	}{
		{"stopped attach yes", stopped, "y\n", offerAttach, true, "box api requires recreation (image: a -> b)", true},
		{"stopped attach no", stopped, "n\n", offerAttach, false, "Recreate it now?", false},
		{"running restart yes", running, "y\n", offerRestart, false, "its running session ends", true},
		{"running start", running, "y\n", offerStart, false, "", false},
		{"running attach", running, "y\n", offerAttach, false, "", false},
		{"chat server restart drift", restart, "y\n", offerChatServer, false, "restarts the room for every connected chat client", true},
		{"restart drift on a box", restart, "y\n", offerRestart, false, "", false},
		{"unreadable inspect", "not json", "y\n", offerAttach, false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []offerCall
			cmd, errw := offerCmd(tc.answer)
			if err := offerRecreate(cmd, offerTarget(tc.report, &calls), "api", tc.kind, tc.noAutoupdate); err != nil {
				t.Fatal(err)
			}
			if tc.prompt == "" && strings.Contains(errw.String(), "[y/N]") || tc.prompt != "" && (!strings.Contains(errw.String(), tc.prompt) || strings.Count(errw.String(), "[y/N]") != 1) {
				t.Fatalf("prompt %q", errw.String())
			}
			recreated := len(calls) == 2 && calls[1].method == "box.recreate"
			if recreated != tc.recreate || len(calls) > 2 {
				t.Fatalf("calls %+v", calls)
			}
			if recreated && (!flagTrue(calls[1].flags, "force") || !flagTrue(calls[1].flags, "no-attach") || flagTrue(calls[1].flags, "no-autoupdate") != tc.noAutoupdate) {
				t.Fatalf("recreate flags %+v", calls[1].flags)
			}
		})
	}
	recreateOfferInteractive = func(*cobra.Command) bool { return false }
	var calls []offerCall
	cmd, errw := offerCmd("y\n")
	if err := offerRecreate(cmd, offerTarget(stopped, &calls), "api", offerAttach, false); err != nil || len(calls) != 0 || errw.Len() != 0 {
		t.Fatal("non-terminal caller inspected or prompted", calls, errw.String(), err)
	}
}

func TestBoxRecreateOfferSelection(t *testing.T) {
	for _, tc := range []struct {
		req  controller.Request
		want bool
	}{
		{controller.Request{Method: "box.start", Args: []string{"api"}}, true},
		{controller.Request{Method: "box.restart", Args: []string{"api"}, Flags: map[string][]string{"new-session": {"true"}}}, true},
		{controller.Request{Method: "box.attach", Args: []string{"api"}}, true},
		{controller.Request{Method: "box.start", Args: []string{"api"}, Flags: map[string][]string{"no-attach": {"true"}}}, false},
		{controller.Request{Method: "box.attach", Args: []string{"api"}, Flags: map[string][]string{"chat-member-id": {"m"}}}, false},
		{controller.Request{Method: "box.exec", Args: []string{"api"}}, false},
	} {
		if _, _, ok := boxRecreateOffer(tc.req); ok != tc.want {
			t.Errorf("%s %v: offer %v, want %v", tc.req.Method, tc.req.Flags, ok, tc.want)
		}
	}
}

// A running container without a holder refuses a fresh session on image drift
// before its effect gate opens (the gate begins effects).
func TestRunningWithoutHolderRefusesImageDriftBeforeEffects(t *testing.T) {
	cfg := savedNoneBox(t, "api")
	fakeDriftResolver(t, cfg, "Up 1 minute", "yoloctl/agentbox-composed:old", "yoloctl/agentbox-composed:new")
	previousHolder := holderUnitActiveFn
	t.Cleanup(func() { holderUnitActiveFn = previousHolder })
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return false }
	gates := 0
	ctx := context.WithValue(held(t.Context()), warmGateKey{}, func() error { gates++; return nil })
	_, err := ensureAgentSession(ctx, container.New(cfg.Space), cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{})
	if err == nil || !strings.Contains(err.Error(), "needs recreation before a new agent session") || gates != 0 {
		t.Fatalf("err %v, effect gate opened %d times", err, gates)
	}
}

// The chat server's own rule (restart or recreate drift) refuses before its
// image warm, with the existing server text.
func TestChatServerDriftRefusedBeforeWarm(t *testing.T) {
	s, i := roomRuntimeFixture(t)
	r, err := s.Ensure("", "person", i)
	if err != nil {
		t.Fatal(err)
	}
	i.Declarations[r.Server.Name] = r.Server.ID
	cfg := &agentbox.Config{Name: r.Server.Name, ID: r.Server.ID, Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage,
		Workdir: agentbox.NewEphemeralWorkdir(r.Server.Name), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
	previous := managedServerPlanFn
	t.Cleanup(func() { managedServerPlanFn = previous })
	managedServerPlanFn = func(context.Context, *container.Client, *agentbox.Config) (agentbox.DiffPlan, []byte, error) {
		return agentbox.DiffPlan{NeedsRestart: true, RestartReason: "lacks its temporary directory"}, nil, nil
	}
	var imageCalls []string
	state := &imageWarmState{enabled: true, entries: map[string]warmImage{}, call: func(_ context.Context, req controller.ImageRequest) (controller.ImageReply, error) {
		imageCalls = append(imageCalls, req.Action)
		return controller.ImageReply{}, errors.New("image work must not start")
	}}
	ctx := context.WithValue(t.Context(), warmKey{}, state)
	err = warmChatServer(ctx, "work", s, chatRuntimeRequest{}, []boxstate.Box{{Name: cfg.Name, Config: cfg}}, i)
	want := managedServerPlanError(agentbox.DiffPlan{NeedsRestart: true, RestartReason: "lacks its temporary directory"}, cfg.Name, "work").Error()
	if err == nil || err.Error() != want || len(imageCalls) != 0 {
		t.Fatalf("got %v (image calls %v), want %q", err, imageCalls, want)
	}
}

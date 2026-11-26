package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/port"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmservice"
)

type preparedStartFixture struct {
	check, start func() error
	// startCtx, when set, replaces start and sees the call's context (its
	// engine-call record).
	startCtx func(context.Context) error
	closed   int
}

func (s *preparedStartFixture) Check(context.Context, *agentbox.Config) error {
	if s.check != nil {
		return s.check()
	}
	return nil
}
func (s *preparedStartFixture) Start(ctx context.Context, _ *agentbox.Config) error {
	if s.startCtx != nil {
		return s.startCtx(ctx)
	}
	return s.start()
}
func (s *preparedStartFixture) Close() error { s.closed++; return nil }

// Only the native checkpoint is substituted; inventory/stop/session teardown
// use the real Client + Lima argv adapters against this finite executable.
func nativeStartCallerFixture(t *testing.T, failedStop bool) (*agentbox.Config, *container.Client, string) {
	t.Helper()
	calls := setupExistingStartFixture(t, "no drift")
	cfg, err := agentbox.Load("default", "coder")
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("START_TEST_FIXTURE")
	spec := port.Spec{Agentbox: cfg.Name, HostPort: 9000, ContainerPort: 7000}
	unit, err := port.OpenShellUnitName(spec)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"inventory": unit + " loaded failed auto-restart retained\n", "stop-exit": map[bool]string{true: "23", false: "0"}[failedStop]} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$START_TEST_FIXTURE/calls"
case "$*" in
 *'nerdctl inspect'*) cat "$START_TEST_FIXTURE/same.json";;
 *'systemctl list-units'*) cat "$START_TEST_FIXTURE/inventory";;
 *'systemctl stop '*) code=$(cat "$START_TEST_FIXTURE/stop-exit"); if [ "$code" = 0 ]; then : > "$START_TEST_FIXTURE/inventory"; fi; exit "$code";;
 *'sudo rm -rf '*) exit 0;;
 *) exit 42;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return cfg, container.NewWithOutput(cfg.Space, io.Discard, io.Discard), calls
}
func TestNativeStartSharedCallerWithdrawsRetainedTransportsBeforeSessionEffects(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ACK", true: "lost-ACK"}[failed], func(t *testing.T) {
			cfg, client, log := nativeStartCallerFixture(t, failed)
			old := prepareNativeExistingStart
			t.Cleanup(func() { prepareNativeExistingStart = old })
			prepared := &preparedStartFixture{}
			launches := 0
			prepared.start = func() error {
				launches++
				raw, _ := os.ReadFile(log)
				if strings.Count(string(raw), "systemctl stop ") != 1 || !strings.Contains(string(raw), "sudo rm -rf ") {
					t.Fatal("shared native start preceded ACK/session cleanup", string(raw))
				}
				return nil
			}
			prepareNativeExistingStart = func(ctx context.Context, _ *container.Client, _ *agentbox.Config, observed bool) (nativePreparedStart, bool, error) {
				if !observed {
					t.Fatal("revive selected explicit desired-forward route")
				}
				known, err := port.Inventory(ctx, client, cfg.Name)
				if err != nil || len(known) != 1 || known[0].Spec.ContainerPort != 7000 {
					t.Fatal("revive lost original observed forwarding before withdrawal", known, err)
				}
				return prepared, true, nil
			}
			err := startExistingAgentboxContainer(t.Context(), client, cfg)
			raw, _ := os.ReadFile(log)
			if failed {
				var uncertain *agentbox.NativeEffectsUncertain
				if err == nil || errors.As(err, &uncertain) || launches != 0 || strings.Contains(string(raw), "sudo rm -rf ") || strings.Count(string(raw), "systemctl stop ") != 1 || strings.Contains(string(raw), "is-active") {
					t.Fatal("shared caller suppressed failed forward ACK", err, launches, string(raw))
				}
			} else if err != nil || launches != 1 || strings.Contains(string(raw), "nerdctl start") || strings.Contains(string(raw), "sudo mkdir") {
				t.Fatal("native shared caller bypassed fresh launcher", err, launches, string(raw))
			}
			if prepared.closed != 1 {
				t.Fatal("caller lost private checkpoint", prepared.closed)
			}
		})
	}
}

// Real ensureAgentSession and real on-disk tracked record. Refusal must happen
// before LoadLocked's possible migration write and before tombstone consumption.
func TestNativeStartReviveQualificationPrecedesTrackedRecordAndTombstone(t *testing.T) {
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
	// A premature LoadLocked would retire this stale account scope and Save.
	cfg.Account = "next"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
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
				return agentbox.ActualState{Exists: true, Image: "fixture", PolicyContent: []byte("recorded A"), ReadOnlyWD: !cfg.WorkdirWritable(), Account: cfg.Account, Runtime: agentbox.RuntimeEvidence{Mode: agentbox.RuntimeModeConfined}}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) { return []byte("desired B"), nil },
			ResolvedImage: func(*agentbox.Config) (string, bool, error) { return "fixture", true, nil },
		}
	}
	old := prepareNativeExistingStart
	t.Cleanup(func() { prepareNativeExistingStart = old })
	cause := errors.New("recorded-policy qualification refused")
	calls := 0
	prepareNativeExistingStart = func(context.Context, *container.Client, *agentbox.Config, bool) (nativePreparedStart, bool, error) {
		calls++
		return nil, true, cause
	}
	_, err = ensureAgentSession(held(t.Context()), container.New(cfg.Space), cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{})
	after, readErr := os.ReadFile(record)
	if !errors.Is(err, cause) || calls != 1 || readErr != nil || !bytes.Equal(before, after) || f.tombstone[cfg.Name] != tomb || len(f.starts) != 1 {
		t.Fatal("revive qualification followed tracked/tombstone/launch effects", err, calls, readErr, len(f.starts))
	}
}

type startCallerInspector struct {
	state agentbox.ActualState
	calls int
}

func (i *startCallerInspector) Inspect(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
	i.calls++
	return i.state, nil
}

type startCallerApplier struct {
	restart    func(context.Context, *agentbox.Config, []byte) error
	unexpected int
}

func (a *startCallerApplier) Create(context.Context, *agentbox.Config, []byte) error {
	a.unexpected++
	return errors.New("unexpected create")
}
func (a *startCallerApplier) Recreate(context.Context, *agentbox.Config, []byte) error {
	a.unexpected++
	return errors.New("unexpected recreate")
}
func (a *startCallerApplier) Restart(ctx context.Context, c *agentbox.Config, p []byte) error {
	return a.restart(ctx, c, p)
}
func (a *startCallerApplier) AddPortForward(context.Context, *agentbox.Config, agentbox.Port) error {
	a.unexpected++
	return errors.New("unexpected forward")
}
func (a *startCallerApplier) RemovePortForward(context.Context, *agentbox.Config, agentbox.Port) error {
	a.unexpected++
	return errors.New("unexpected unforward")
}

// Actual explicit caller core + shared start + ReconcileForStart. The native
// launch is synthetic: the prepared start launches the saved policy B, so the
// desired reconcile that follows restarts nothing (no A-then-B second leg).
// This fixture proves caller order and typed result propagation, not root or
// engine behavior.
func TestNativeStartExplicitCallerLaunchesSavedPolicyThenReconcilesWithoutRestart(t *testing.T) {
	for _, failure := range []string{"uncertain-start", "success"} {
		t.Run(failure, func(t *testing.T) {
			cfg, client, _ := nativeStartCallerFixture(t, false)
			policy, err := agentbox.ComposePolicy(cfg)
			if err != nil {
				t.Fatal(err)
			}
			image, _, err := agentbox.ResolvedImage(cfg)
			if err != nil {
				t.Fatal(err)
			}
			inspector := &startCallerInspector{state: agentbox.ActualState{Exists: true, Image: image, PolicyContent: append([]byte("# A\n"), policy...), ReadOnlyWD: !cfg.WorkdirWritable(), WorkloadPoolKnown: true, WorkloadPool: true}}
			cause := errors.New("lost start reply")
			starts, restarts := 0, 0
			prepared := &preparedStartFixture{start: func() error {
				starts++
				if inspector.calls != 0 {
					t.Fatal("reconcile preceded the prepared start")
				}
				if failure == "uncertain-start" {
					return &agentbox.NativeEffectsUncertain{Cause: cause}
				}
				inspector.state.PolicyContent = bytes.Clone(policy) // B is what runs now
				return nil
			}}
			applier := &startCallerApplier{restart: func(context.Context, *agentbox.Config, []byte) error {
				restarts++
				return errors.New("unexpected policy restart after a saved-policy start")
			}}
			err = startAndReconcileExistingAgentbox(t.Context(), client, cfg, prepared, inspector, applier)
			if starts != 1 || restarts != 0 || applier.unexpected != 0 {
				t.Fatal("explicit caller repeated effects or restarted the policy", starts, restarts, applier.unexpected)
			}
			if failure == "uncertain-start" {
				if inspector.calls != 0 || !workerEffectsUncertain(err) || !errors.Is(err, cause) {
					t.Fatal("uncertain start reached reconcile or lost its uncertainty", err, inspector.calls)
				}
				return
			}
			if err != nil || inspector.calls == 0 {
				t.Fatal("explicit caller skipped its desired reconciliation", err, inspector.calls)
			}
		})
	}
}

// An attach to an already stopped native box: the qualification inventory
// completes, the forward-withdrawal inventory is never answered and no client
// remains to cancel it (attach's owned setup). Its fail-safe ends the read,
// nothing is started, and the attach returns, so the caller's space lock is
// released for the next operation.
func TestNativeAttachToStoppedBoxEndsSilentWithdrawalInventoryAtFailSafe(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	cfg.SchemaVersion = agentbox.SchemaVersion
	cfg.Image = defaultAgentboxImage
	cfg.Workdir = agentbox.NewEphemeralWorkdir(cfg.Name)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("LIMA_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ATTACH_TEST_FIXTURE", bin)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$ATTACH_TEST_FIXTURE/calls"
case "$*" in
 *'systemctl list-units'*)
  if [ -e "$ATTACH_TEST_FIXTURE/qualified" ]; then sleep 30 & echo $! > "$ATTACH_TEST_FIXTURE/silent"; wait; fi
  : > "$ATTACH_TEST_FIXTURE/qualified";;
 *) exit 42;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if raw, err := os.ReadFile(filepath.Join(bin, "silent")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	previousResolver := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previousResolver })
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
				return agentbox.ActualState{Exists: true, Image: "fixture", PolicyContent: []byte("policy"), ReadOnlyWD: !cfg.WorkdirWritable(), Account: cfg.Account, Runtime: agentbox.RuntimeEvidence{Mode: agentbox.RuntimeModeConfined}}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) { return []byte("policy"), nil },
			ResolvedImage: func(*agentbox.Config) (string, bool, error) { return "fixture", true, nil },
		}
	}
	previousFailSafe := vmservice.InventoryFailSafe
	vmservice.InventoryFailSafe = 300 * time.Millisecond
	t.Cleanup(func() { vmservice.InventoryFailSafe = previousFailSafe })
	previousPrepare := prepareNativeExistingStart
	t.Cleanup(func() { prepareNativeExistingStart = previousPrepare })
	client := container.New(cfg.Space)
	prepared := &preparedStartFixture{}
	launches := 0
	prepared.start = func() error { launches++; return nil }
	prepareNativeExistingStart = func(ctx context.Context, _ *container.Client, _ *agentbox.Config, observed bool) (nativePreparedStart, bool, error) {
		// The qualification inventory is a separate, answered read.
		if _, err := port.Inventory(ctx, client, cfg.Name); err != nil {
			t.Fatal("qualification inventory failed", err)
		}
		return prepared, true, nil
	}
	starts := len(f.starts)
	start := time.Now()
	_, err := ensureAgentSession(held(t.Context()), client, cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) || launches != 0 || len(f.starts) != starts {
		t.Fatal("silent withdrawal inventory did not end the attach before any start", err, launches, len(f.starts)-starts)
	}
	if elapsed < vmservice.InventoryFailSafe || elapsed > 5*time.Second {
		t.Fatal("withdrawal inventory followed something other than its fail-safe", elapsed)
	}
	raw, _ := os.ReadFile(filepath.Join(bin, "calls"))
	if strings.Count(string(raw), "systemctl list-units") != 2 || strings.Contains(string(raw), "nerdctl start") || strings.Contains(string(raw), "systemd-run") {
		t.Fatal("attach reached the engine or a session after the silent inventory", string(raw))
	}
}

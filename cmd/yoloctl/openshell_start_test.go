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
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmservice"
	"github.com/spf13/cobra"
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

// The real attach wrapper owns the space lock. The fixture substitutes
// the native checkpoint and VM command. It withholds either the first
// qualification read or the separate withdrawal read, in call order.
func TestNativeAttachToStoppedBoxEndsSilentInventoryAtFailSafe(t *testing.T) {
	for _, tc := range []struct {
		name      string
		blockRead int
	}{
		{"qualification", 1},
		{"withdrawal", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testStoppedNativeAttachInventory(t, tc.blockRead)
		})
	}
}

func testStoppedNativeAttachInventory(t *testing.T, blockRead int) {
	t.Helper()
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
	t.Setenv("ATTACH_TEST_BLOCK_READ", strconv.Itoa(blockRead))
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$ATTACH_TEST_FIXTURE/calls"
case "$*" in
 *'systemctl list-units'*'--no-pager --full'*)
  printf '%s\n' "$*" >> "$ATTACH_TEST_FIXTURE/inventory-reads"
  if [ "$(wc -l < "$ATTACH_TEST_FIXTURE/inventory-reads")" -eq "$ATTACH_TEST_BLOCK_READ" ]; then
   : > "$ATTACH_TEST_FIXTURE/silent"
   exec sleep 10
  fi;;
 *) exit 42;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
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
	vmservice.InventoryFailSafe = 600 * time.Millisecond
	t.Cleanup(func() { vmservice.InventoryFailSafe = previousFailSafe })
	previousPrepare := prepareNativeExistingStart
	t.Cleanup(func() { prepareNativeExistingStart = previousPrepare })
	client := container.New(cfg.Space)
	prepared := &preparedStartFixture{}
	launches := 0
	prepared.start = func() error { launches++; return nil }
	prepareNativeExistingStart = func(ctx context.Context, _ *container.Client, _ *agentbox.Config, observed bool) (nativePreparedStart, bool, error) {
		if !observed {
			t.Fatal("attach did not request previously observed forwards")
		}
		if _, err := port.InventoryBounded(ctx, client, ""); err != nil {
			return nil, true, err
		}
		return prepared, true, nil
	}
	// The attach runs through the real command wrapper, which takes the
	// space-operation lock and releases it when the command returns. While a read is
	// withheld, another operation finds the lock busy; after
	// the command returns, the same lock is free again.
	previousSync := syncNativeChatRolesFn
	t.Cleanup(func() { syncNativeChatRolesFn = previousSync })
	syncNativeChatRolesFn = func(context.Context, string) error { return nil }
	busy := make(chan error, 1)
	observeCtx, stopObserving := context.WithCancel(t.Context())
	defer stopObserving()
	go func() {
		for observeCtx.Err() == nil {
			if _, err := os.Stat(filepath.Join(bin, "silent")); err == nil {
				release, err := spacelock.TryAcquire(t.Context(), cfg.Space, true)
				if release != nil {
					release()
				}
				busy <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	starts := len(f.starts)
	var err error
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	boxCmd := &cobra.Command{Use: "box"}
	attach := &cobra.Command{Use: "attach", RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := ensureAgentSession(cmd.Context(), client, cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{})
		return err
	}}
	attach.Flags().String("space", cfg.Space, "")
	attach.Flags().Bool(newSessionFlag, false, "")
	boxCmd.AddCommand(attach)
	root.AddCommand(boxCmd)
	registerSpaceOperations(root)
	caller, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx := context.WithValue(agentbox.WithOwnedStartInventory(caller), controllerRequestKey{}, "request-0123456789abcdef")
	ctx = context.WithValue(ctx, controllerOwnedSetupKey{}, true)
	ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { return nil })
	ctx = context.WithValue(ctx, controllerReleaseKey{}, func() error { return nil })
	root.SetArgs([]string{"box", "attach", cfg.Name, "--space", cfg.Space})
	start := time.Now()
	err = root.ExecuteContext(ctx)
	elapsed := time.Since(start)
	select {
	case busyErr := <-busy:
		if !errors.Is(busyErr, spacelock.ErrBusy) {
			t.Fatal("the withheld read did not hold the space lock", busyErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the inventory read was never withheld")
	}
	again, againErr := spacelock.TryAcquire(t.Context(), cfg.Space, true)
	if againErr != nil {
		t.Fatal("the attach's space lock was not released on return", againErr)
	}
	again()
	if !errors.Is(err, context.DeadlineExceeded) || caller.Err() != nil || launches != 0 || len(f.starts) != starts {
		t.Fatal("silent inventory did not end the attach before any start", err, launches, len(f.starts)-starts)
	}
	if elapsed < vmservice.InventoryFailSafe {
		t.Fatal("inventory returned before its fail-safe", elapsed)
	}
	raw, err := os.ReadFile(filepath.Join(bin, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "--no-pager --full") != blockRead || strings.Contains(string(raw), "nerdctl start") ||
		strings.Contains(string(raw), "systemctl stop") || strings.Contains(string(raw), "systemd-run") {
		t.Fatal("attach crossed the withheld inventory boundary", string(raw))
	}
}

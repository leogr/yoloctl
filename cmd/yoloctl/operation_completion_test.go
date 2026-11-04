package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/vmservice"
	"github.com/spf13/cobra"
)

// completionRecorder is the worker's private evidence channel in tests.
type completionRecorder struct {
	events *[]string
	locked func() bool
	status []int
}

func TestWrapperWithholdsPortStopUncertaintyAndPreservesContainer(t *testing.T) {
	dir := t.TempDir()
	script, err := os.ReadFile("../../internal/port/testdata/limactl-forward-fixture.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limactl"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	const unit = "yoloctl-openshell-port-web-8080-80.service"
	if err := os.WriteFile(filepath.Join(dir, "units"), []byte(unit+" loaded failed failed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_FORWARD_DIR", dir)
	t.Setenv("YOLOCTL_TEST_FORWARD_STOP_EXIT", "23")
	calls := installStopAgentboxRuntimeHooks(t, nil)
	lifecycleUnforwardAgentbox = port.UnforwardAgentbox
	events, status, err := runCompletionWrapper(t, func(cmd *cobra.Command) error {
		return stopAgentboxRuntime(cmd.Context(), container.New("work"), "work", "web", io.Discard)
	}, nil, false)
	var exit *exec.ExitError
	// A failed forward stop reply is a known, plain failure: the operation
	// completes with that error (old path), never an uncertain outcome.
	if !slices.Equal(events, []string{"write", "role-sync", "complete"}) || !slices.Equal(status, []int{2}) || workerEffectsUncertain(err) || !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("native port failure became uncertain or lost its selected-adapter result", events, status, err)
	}
	if !slices.Equal(*calls, []string{"stop-holder"}) {
		t.Fatal("container/session cleanup continued after unresolved forward stop", *calls)
	}
	commands, err := os.ReadFile(filepath.Join(dir, "commands.log"))
	if err != nil || strings.Count(string(commands), "systemctl stop "+unit+"\n") != 1 || strings.Contains(string(commands), "is-active") || strings.Contains(string(commands), "nerdctl") {
		t.Fatal("actual port path retried or substituted state", string(commands), err)
	}
}

func TestWrapperWithholdsFailedStopAckThroughActualLimaAdapter(t *testing.T) {
	for _, state := range []string{"inactive", "failed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			script, err := os.ReadFile("../../internal/vmservice/testdata/limactl-stop-fixture.sh")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "limactl"), script, 0o755); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "commands.log")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("YOLOCTL_TEST_SERVICE_LOG", log)
			t.Setenv("YOLOCTL_TEST_SERVICE_STATE", state)
			t.Setenv("YOLOCTL_TEST_STOP_EXIT", "23")
			events, status, err := runCompletionWrapper(t, func(cmd *cobra.Command) error {
				stopErr := vmservice.StopAcknowledged(cmd.Context(), container.New("work"), "yoloctl-openshell-port-web-8080-80.service")
				if stopErr != nil {
					return &agentbox.NativeEffectsUncertain{Cause: stopErr}
				}
				return errors.New("readiness failed after known cleanup")
			}, nil, false)
			var exit *exec.ExitError
			if !slices.Equal(events, []string{"write", "role-sync"}) || len(status) != 0 || !workerEffectsUncertain(err) || !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("failed stop completed or lost its actual result: %v %v %v", events, status, err)
			}
			commands, readErr := os.ReadFile(log)
			if readErr != nil || strings.Count(string(commands), "systemctl stop ") != 1 || strings.Contains(string(commands), "is-active") {
				t.Fatal("actual adapter substituted a state observation", string(commands), readErr)
			}
		})
	}
}

func (r *completionRecorder) ExpandEffects(context.Context, []controller.Resource) error {
	*r.events = append(*r.events, "expand")
	return nil
}

func (r *completionRecorder) RecordDocument(context.Context, controller.DocumentIntent) error {
	*r.events = append(*r.events, "proof")
	return nil
}

func (r *completionRecorder) CompleteEffects(_ context.Context, status int) error {
	if !r.locked() {
		*r.events = append(*r.events, "complete-unlocked")
	}
	*r.events = append(*r.events, "complete")
	r.status = append(r.status, status)
	return nil
}

// runCompletionWrapper executes a recording `box start` through the real
// space-operation wrapper inside a worker context that claims completion.
func runCompletionWrapper(t *testing.T, run func(*cobra.Command) error, syncErr error, cancel bool) ([]string, []int, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousSync := syncNativeChatRolesFn
	t.Cleanup(func() { syncNativeChatRolesFn = previousSync })
	var events []string
	locked := func() bool {
		probe, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer stop()
		unlock, err := spacelock.Acquire(probe, "work", true)
		if err == nil {
			unlock()
		}
		return err != nil
	}
	syncNativeChatRolesFn = func(context.Context, string) error {
		events = append(events, "role-sync")
		return syncErr
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	box := &cobra.Command{Use: "box"}
	start := &cobra.Command{Use: "start", RunE: func(cmd *cobra.Command, _ []string) error {
		events = append(events, "write")
		if cancel {
			stop()
		}
		return run(cmd)
	}}
	start.Flags().String("space", "work", "")
	box.AddCommand(start)
	root.AddCommand(box)
	registerSpaceOperations(root)
	recorder := &completionRecorder{events: &events, locked: locked}
	ctx = context.WithValue(ctx, controllerRequestKey{}, "request-0123456789abcdef")
	ctx = controller.WithEffectRecorder(ctx, recorder)
	ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { return nil })
	completion := &workerCompletion{}
	ctx = context.WithValue(ctx, workerCompletionKey{}, completion)
	root.SetArgs([]string{"box", "start", "web", "--space", "work"})
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errors.New("panicked")
			}
		}()
		err = root.ExecuteContext(ctx)
	}()
	if !completion.owned {
		t.Fatal("the wrapper did not claim completion from the worker")
	}
	if locked() {
		t.Fatal("the wrapper kept its native lock")
	}
	return events, recorder.status, err
}

func TestWrapperRecordsCompletionAfterRoleSyncBeforeUnlock(t *testing.T) {
	ordinary := asUserError(errors.New("invalid box declaration"))
	spoof := errors.New((&agentbox.NativeEffectsUncertain{Cause: errors.New("unit start reply lost")}).Error())
	for _, tc := range []struct {
		name   string
		run    func(*cobra.Command) error
		status []int
	}{
		{"success", func(*cobra.Command) error { return nil }, []int{exitOK}},
		// A normal error is a known failed result, never relabeled success.
		{"ordinary error", func(*cobra.Command) error { return ordinary }, []int{exitUser}},
		// Only a trusted Go type can mark uncertainty; application text cannot.
		{"native uncertainty text is ordinary", func(*cobra.Command) error { return spoof }, []int{classifyError(spoof)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, status, _ := runCompletionWrapper(t, tc.run, nil, false)
			if !slices.Equal(events, []string{"write", "role-sync", "complete"}) || !slices.Equal(status, tc.status) {
				t.Fatalf("events %v status %v", events, status)
			}
		})
	}
}

func TestWrapperWithholdsCompletionWithoutCleanOutcome(t *testing.T) {
	syncFailed := errors.New("role publication failed")
	for _, tc := range []struct {
		name    string
		run     func(*cobra.Command) error
		syncErr error
		cancel  bool
		events  []string
	}{
		{"failed role sync", func(*cobra.Command) error { return nil }, syncFailed, false, []string{"write", "role-sync"}},
		{"uncertain effects", func(*cobra.Command) error {
			return &controller.EffectsUncertain{Cause: errors.New("lost reply")}
		}, nil, false, []string{"write", "role-sync"}},
		{"uncertain native runtime effects", func(*cobra.Command) error {
			return &agentbox.NativeEffectsUncertain{Cause: errors.New("unit start reply lost")}
		}, nil, false, []string{"write", "role-sync"}},
		{"uncertain native generation selection", func(*cobra.Command) error {
			return &openshell.NativeEffectsUncertain{Cause: errors.New("private generation publication result lost")}
		}, nil, false, []string{"write", "role-sync"}},

		{"uncertain native port stop", func(*cobra.Command) error {
			return &port.NativeEffectsUncertain{Cause: errors.New("unit stop reply lost")}
		}, nil, false, []string{"write", "role-sync"}},
		{"canceled", func(*cobra.Command) error { return context.Canceled }, nil, true, []string{"write", "role-sync"}},
		{"panic", func(*cobra.Command) error { panic("after an effect") }, nil, false, []string{"write", "role-sync"}},
		// An owned setup release ends the guard before the outcome exists;
		// its durable setup checkpoint is the evidence instead.
		{"setup release", func(cmd *cobra.Command) error { return releaseSpaceOperation(cmd.Context()) }, nil, false, []string{"write", "role-sync"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, status, _ := runCompletionWrapper(t, tc.run, tc.syncErr, tc.cancel)
			if !slices.Equal(events, tc.events) || len(status) != 0 {
				t.Fatalf("events %v status %v", events, status)
			}
		})
	}
}

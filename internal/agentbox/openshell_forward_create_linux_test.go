package agentbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
)

func TestNativeForwardCreationDoesNotReplayUnknownStartOrHideUnprovedCleanup(t *testing.T) {
	startFailure := errors.New("start reply lost")
	readyFailure := errors.New("owned listener failed")
	cleanupFailure := errors.New("stop reply lost")
	for _, tc := range []struct {
		name                   string
		start, ready, cleanup  error
		want                   []string
		uncertain, cancelReady bool
	}{
		{name: "success", want: []string{"start", "ready"}},
		{name: "unknown start", start: startFailure, uncertain: true, want: []string{"start"}},
		{name: "failed startup cleaned", ready: readyFailure, want: []string{"start", "ready", "cleanup"}},
		{name: "unproved cleanup", ready: readyFailure, cleanup: cleanupFailure, uncertain: true, want: []string{"start", "ready", "cleanup"}},
		{name: "canceled startup still cleans", ready: context.Canceled, cancelReady: true, want: []string{"start", "ready", "cleanup"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls []string
			err := completeForwardCreation(ctx,
				func(context.Context) error { calls = append(calls, "start"); return tc.start },
				func(context.Context) error {
					calls = append(calls, "ready")
					if tc.cancelReady {
						cancel()
					}
					return tc.ready
				},
				func(cleanup context.Context) error {
					calls = append(calls, "cleanup")
					if _, bounded := cleanup.Deadline(); cleanup.Err() != nil || bounded {
						t.Fatal("owned cleanup inherited cancellation or a shared deadline")
					}
					return tc.cleanup
				})
			var uncertain *NativeEffectsUncertain
			if !slices.Equal(calls, tc.want) || errors.As(err, &uncertain) != tc.uncertain {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
			for _, cause := range []error{tc.start, tc.ready, tc.cleanup} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatal("lost actual failure cause", cause, err)
				}
			}
			if tc.start == nil && tc.ready == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeForwardFailedStopAckRetainsActualStatusViaLimaAdapter(t *testing.T) {
	for _, state := range []string{"inactive", "failed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			_, spec, binding := nativeForwardFixture(t)
			directory := forwardStatusTestDirectory(t)
			uid, gid := os.Geteuid(), os.Getegid()
			raw, err := publishForwardStatus(t.Context(), directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid)
			if err != nil {
				t.Fatal(err)
			}
			log := nativeForwardLimaStopFixture(t, state, "23")
			store := forwardStatusStore{directory: directory, uid: uid, gid: gid}
			err = completeForwardCreation(t.Context(), func(context.Context) error { return nil },
				func(context.Context) error { return errors.New("readiness failure") },
				func(ctx context.Context) error {
					return stopNativeForwardAndRemoveStatus(ctx, container.New("fixture"), spec, binding, store)
				})
			var uncertain *NativeEffectsUncertain
			var exit *exec.ExitError
			if !errors.As(err, &uncertain) || !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatal("actual adapter lost its failed acknowledgement/uncertainty", err)
			}
			kept, err := os.ReadFile(filepath.Join(directory, binding.Attempt+".json"))
			if err != nil || !bytes.Equal(kept, raw) {
				t.Fatal("failed stop deleted/replaced owned status", err)
			}
			commands, err := os.ReadFile(log)
			if err != nil || strings.Count(string(commands), "systemctl stop ") != 1 || strings.Contains(string(commands), "is-active") {
				t.Fatal("actual stop retried or state was substituted", string(commands), err)
			}
		})
	}
}

func TestNativeForwardAcknowledgedLimaStopRemovesOnlyOwnedStatus(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	directory := forwardStatusTestDirectory(t)
	uid, gid := os.Geteuid(), os.Getegid()
	if _, err := publishForwardStatus(t.Context(), directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid); err != nil {
		t.Fatal(err)
	}
	nativeForwardLimaStopFixture(t, "unknown", "0")
	store := forwardStatusStore{directory: directory, uid: uid, gid: gid}
	if err := stopNativeForwardAndRemoveStatus(t.Context(), container.New("fixture"), spec, binding, store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, binding.Attempt+".json")); !os.IsNotExist(err) {
		t.Fatal("acknowledged stop retained its owned status", err)
	}
}

func nativeForwardLimaStopFixture(t *testing.T, state, exit string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	script, err := os.ReadFile("../vmservice/testdata/limactl-stop-fixture.sh")
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
	t.Setenv("YOLOCTL_TEST_STOP_EXIT", exit)
	return log
}

func TestNativeForwardCreatorRequiresRootContextAndAllKnownPortExclusion(t *testing.T) {
	if err := StartNativeOpenShellForward(t.Context(), nil, port.Spec{}, nil); err == nil {
		t.Fatal("unconfigured creator reached an infrastructure effect")
	}
	spec := port.Spec{Agentbox: "fixture", HostPort: 8080, ContainerPort: 80}
	for _, native := range []bool{false, true} {
		known := []port.ForwardService{{Spec: port.Spec{Agentbox: "other", HostPort: 8080, ContainerPort: 99}, Unit: "fictional", OpenShell: native}}
		if err := noNativeForwardConflict(known, spec); !errors.Is(err, port.ErrPortInUse) {
			t.Fatal("another transport's retained port was ignored", native, err)
		}
	}
	if err := noNativeForwardConflict(nil, spec); err != nil {
		t.Fatal(err)
	}
}

func TestNativeForwardReadySnapshotRefusesSameContainerRestart(t *testing.T) {
	selected, _, binding := nativeForwardFixture(t)
	if !sameForwardRuntime(selected, selected, binding) {
		t.Fatal("matching selected process rejected")
	}
	changed := selected
	changed.PID++
	if sameForwardRuntime(changed, selected, binding) {
		t.Fatal("same container/generation with another PID adopted")
	}
	changed = selected
	changed.StartedAt = changed.StartedAt.Add(1)
	if sameForwardRuntime(changed, selected, binding) {
		t.Fatal("reused PID with another container start adopted")
	}
}

// failSafeDeadline reports whether ctx is live and ends no sooner than atLeast.
func failSafeDeadline(ctx context.Context, atLeast time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return ok && ctx.Err() == nil && time.Until(deadline) >= atLeast
}

// The Start acknowledgement and each independent cleanup wait own their
// fail-safe. An earlier caller deadline still ends the acknowledgement.
func TestNativeForwardCreationStepsUseFailSafe(t *testing.T) {
	var started, cleaned bool
	err := completeForwardCreation(t.Context(),
		func(start context.Context) error { started = failSafeDeadline(start, time.Minute); return nil },
		func(context.Context) error { return errors.New("owned listener failed") },
		func(cleanup context.Context) error {
			return runRuntimeStep(cleanup, func(step context.Context) error {
				cleaned = failSafeDeadline(step, time.Minute)
				return nil
			})
		})
	if err == nil || !started || !cleaned {
		t.Fatal("forward creation steps lost their default fail-safe", started, cleaned, err)
	}

	caller, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	callerDeadline, _ := caller.Deadline()
	_ = completeForwardCreation(caller,
		func(start context.Context) error {
			if deadline, _ := start.Deadline(); !deadline.Equal(callerDeadline) {
				t.Fatal("earlier caller deadline lost", deadline)
			}
			return errors.New("refused")
		},
		func(context.Context) error { return nil },
		func(context.Context) error { return nil })

	shortRuntimeStepFailSafe(t, 100*time.Millisecond)
	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	var uncertain *NativeEffectsUncertain
	err = completeForwardCreation(t.Context(), hang, func(context.Context) error { return nil }, func(context.Context) error { return nil })
	if !errors.As(err, &uncertain) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "forward service creation was not acknowledged") {
		t.Fatal("unanswered start lost its uncertainty", err)
	}
	err = completeForwardCreation(t.Context(), func(context.Context) error { return nil },
		func(context.Context) error { return errors.New("owned listener failed") },
		func(cleanup context.Context) error { return runRuntimeStep(cleanup, hang) })
	if !errors.As(err, &uncertain) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "owned forward cleanup remains unproved") {
		t.Fatal("unanswered cleanup lost its uncertainty", err)
	}
}

func TestNativeForwardCreationUsesSeparateSetupAndCleanupAllowances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		delay := func(ctx context.Context) error {
			select {
			case <-time.After(80 * time.Second):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		ctx := t.Context()
		spec := port.Spec{Agentbox: "fixture", HostPort: 8000, ContainerPort: 80}
		startedAt := time.Now()
		_, err := prepareNativeForwardStart(ctx, spec, func(check context.Context) (OpenShellRuntime, error) {
			return OpenShellRuntime{Running: true, PID: 42, StartedAt: startedAt}, delay(check)
		}, func(context.Context) error { return nil }, func(check context.Context) ([]port.ForwardService, error) {
			return nil, delay(check)
		})
		if err != nil || time.Since(startedAt) != 160*time.Second {
			t.Fatalf("forward inventory shared the runtime-read allowance: %v at %v", err, time.Since(startedAt))
		}
		startedAt = time.Now()
		readiness := errors.New("listener failed")
		err = completeForwardCreation(ctx, func(context.Context) error { return nil },
			func(context.Context) error { return readiness },
			func(cleanup context.Context) error {
				return completeNativeForwardCleanup(cleanup, delay, func(stop context.Context) error {
					return runRuntimeStep(stop, delay)
				})
			})
		if err != readiness || time.Since(startedAt) != 160*time.Second {
			t.Fatalf("forward Stop shared the ownership-wait allowance: %v at %v", err, time.Since(startedAt))
		}
	})
}

// The listener wait is one wait with the fail-safe; its tries stay. Its
// first check sees the wait's context.
func TestNativeForwardReadyWaitUsesFailSafe(t *testing.T) {
	stop := errors.New("fixture stop")
	wait := func(ctx context.Context, verify func(context.Context) error) error {
		return waitNativeForwardReady(ctx, nil, OpenShellRuntime{}, port.Spec{}, port.ForwardBinding{}, verify)
	}
	bounded := false
	if err := wait(t.Context(), func(ctx context.Context) error { bounded = failSafeDeadline(ctx, time.Minute); return stop }); !errors.Is(err, stop) || !bounded {
		t.Fatal("listener wait lost its default fail-safe", err)
	}
	caller, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	callerDeadline, _ := caller.Deadline()
	if err := wait(caller, func(ctx context.Context) error {
		if deadline, _ := ctx.Deadline(); !deadline.Equal(callerDeadline) {
			t.Fatal("earlier caller deadline lost", deadline)
		}
		return stop
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	shortRuntimeStepFailSafe(t, 100*time.Millisecond)
	if err := wait(t.Context(), func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unanswered listener wait did not end with its fail-safe", err)
	}
}

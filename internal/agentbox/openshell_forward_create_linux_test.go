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
					if _, bounded := cleanup.Deadline(); cleanup.Err() != nil || !bounded {
						t.Fatal("owned cleanup inherited cancellation or lost its bound")
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

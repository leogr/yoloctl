package agentbox

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

func TestStopInspectedRuntimeSelectsActualRouteAndRefusesMixedEvidence(t *testing.T) {
	for _, route := range []string{"native", "legacy", "privileged", "partial-native"} {
		t.Run(route, func(t *testing.T) {
			details := openShellDetailsFixture(t)
			switch route {
			case "legacy":
				details = confinedDetails(&Config{Name: "fixture"})
			case "privileged":
				details = privilegedDetails(&Config{Name: "fixture", Privileged: true})
			case "partial-native":
				details.Command = []string{"/wrong"}
			}
			nativeCalls, legacyCalls := 0, 0
			err := stopInspectedRuntime(t.Context(), details, func(context.Context) error { nativeCalls++; return nil }, func(context.Context) error { legacyCalls++; return nil })
			if route == "partial-native" {
				if err == nil || nativeCalls+legacyCalls != 0 {
					t.Fatal("mixed runtime reached a fallback stop", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (nativeCalls == 1) != (route == "native") || (legacyCalls == 1) == (route == "native") {
				t.Fatal("wrong stop route", nativeCalls, legacyCalls)
			}
		})
	}
}
func TestNativeStopPreservesActualEngineAcknowledgementAndNeverReadsAfterFailure(t *testing.T) {
	for _, status := range []int{0, 23} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			directory := t.TempDir()
			log := filepath.Join(directory, "commands.log")
			endpoint := filepath.Join(directory, "owned-endpoint")
			if err := os.WriteFile(endpoint, []byte("retained"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YOLOCTL_STOP_ACK_LOG", log)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$YOLOCTL_STOP_ACK_LOG\"\nexit " + strconv.Itoa(status) + "\n"
			if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			client := container.NewWithOutput("fixture", io.Discard, io.Discard)
			id := strings.Repeat("a", 64)
			checks := 0
			err := completeNativeRuntimeStop(t.Context(), func(ctx context.Context) error { return client.Stop(ctx, id) }, func(ctx context.Context) error {
				checks++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("post-stop proof unbounded")
				}
				return os.Remove(endpoint)
			})
			if status == 0 {
				if err != nil || checks != 1 {
					t.Fatal("positive stop did not prove its owned cleanup", err, checks)
				}
			} else {
				var uncertain *NativeEffectsUncertain
				var exit *exec.ExitError
				if !errors.As(err, &uncertain) || !errors.As(err, &exit) || exit.ExitCode() != status || checks != 0 {
					t.Fatal("failed ACK lost or followed by proof/cleanup", err, checks)
				}
				if raw, err := os.ReadFile(endpoint); err != nil || string(raw) != "retained" {
					t.Fatal("unknown stop deleted endpoint", err)
				}
			}
			commands, err := os.ReadFile(log)
			if err != nil || string(commands) != "shell yolo-fixture -- sudo nerdctl stop --time 30 "+id+"\n" {
				t.Fatal("stop did not retain exact CID/one ACK", string(commands), err)
			}
		})
	}
}
func TestNativeStopPostAckFailureRetainsUncertaintyAndContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), nativeStopContextKey{}, "admitted"))
	defer cancel()
	failure := errors.New("inactive generation or endpoint could not be verified")
	err := completeNativeRuntimeStop(ctx, func(context.Context) error { cancel(); return nil }, func(check context.Context) error {
		if check.Err() != nil || check.Value(nativeStopContextKey{}) != "admitted" {
			t.Fatal("proof lost admission or inherited canceled client")
		}
		return failure
	})
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || !errors.Is(err, failure) {
		t.Fatal("unproved acknowledged stop reported known success", err)
	}
	calls := 0
	if err := completeNativeRuntimeStop(ctx, func(context.Context) error { calls++; return nil }, func(context.Context) error { calls++; return nil }); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled admission reached a mutation", err, calls)
	}
}

type nativeStopContextKey struct{}

func TestNativeRestartRefusesActualCallerBeforePolicyOrRuntimeEffects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	directory := t.TempDir()
	log := filepath.Join(directory, "commands.log")
	t.Setenv("YOLOCTL_RESTART_ROUTE_LOG", log)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The selected inspect response has new-runtime signals. Even partial new
	// metadata stays on the native route and never enters legacy rollback.
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_RESTART_ROUTE_LOG"
case "$*" in
 *"sudo nerdctl ps -a --format json --filter label=yoloctl.agentbox=coder")
   if [ "$YOLOCTL_RESTART_ABSENT" != 1 ]; then printf '%s\n' '{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Names":"coder"}'; fi ;;
 *"sudo nerdctl inspect coder") printf '%s\n' '[{"Id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Name":"/coder","Config":{"Labels":{"yoloctl.openshell.protocol":"runtime-v1"}},"State":{"Status":"running","Running":true,"Pid":42}}]' ;;
 *) exit 42 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	a := LiveApplier{Client: container.NewWithOutput("fixture", io.Discard, io.Discard)}
	err := a.Restart(t.Context(), &Config{Name: "coder"}, []byte("new policy"))
	// Partial native metadata keeps the native route (with a notice); its own
	// qualification then refuses here, before any policy or runtime effect.
	if err == nil || strings.Contains(err.Error(), "legacy") {
		t.Fatal("native route entered legacy restart", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || string(commands) != "shell yolo-fixture -- sudo nerdctl ps -a --format json --filter label=yoloctl.agentbox=coder\nshell yolo-fixture -- sudo nerdctl inspect coder\n" {
		t.Fatal("native refusal followed policy/runtime effects", string(commands), err)
	}
}

func TestNativeRestartPreflightKeepsAbsentDeclaredBoxPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	directory := t.TempDir()
	log := filepath.Join(directory, "commands.log")
	t.Setenv("YOLOCTL_RESTART_ABSENT_LOG", log)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_RESTART_ABSENT_LOG"
case "$*" in *"sudo nerdctl ps -a --format json --filter label=yoloctl.agentbox=coder") exit 0;; *) exit 42;; esac
`
	if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	a := LiveApplier{Client: container.NewWithOutput("fixture", io.Discard, io.Discard)}
	if err := a.PreflightRestart(t.Context(), &Config{Name: "coder"}); err != nil {
		t.Fatal("known absent declaration path blocked", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || string(commands) != "shell yolo-fixture -- sudo nerdctl ps -a --format json --filter label=yoloctl.agentbox=coder\n" {
		t.Fatal("absent preflight started another operation", string(commands), err)
	}
}

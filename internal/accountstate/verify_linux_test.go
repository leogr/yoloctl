package accountstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/execpipe"
)

// Ordinary selected-command project fixtures; no VM, sandbox or storage probe.
// Each fixture owns its process group and settles all retained pipe holders.
func accountVerificationCommand(t *testing.T, ctx context.Context, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	return cmd
}

// Exercises the ACTUAL verify branch and adapter command selection. Replacing
// only the selected command never configures a fake NativeCtx or disk/VM.
func selectedAccountVerification(t *testing.T, ctx context.Context, script string) (Result, error) {
	t.Helper()
	original := accountStateCommand
	accountStateCommand = func(selected context.Context, space string, args ...string) *exec.Cmd {
		if space != "fixture" || len(args) != 4 || args[0] != "sudo" || args[1] != "python3" || args[2] != "-c" || args[3] != guestScript {
			t.Fatal("verification changed selected adapter/command")
		}
		// The verifier runs under its caller's context: a direct host call
		// (space recreate) has its person present. The controller bounds its
		// own calls end to end; only the post-exit drain has a bound here.
		deadline, ok := selected.Deadline()
		want, wantOK := ctx.Deadline()
		if ok != wantOK || !deadline.Equal(want) {
			t.Fatal("actual verify branch changed its caller's deadline")
		}
		return accountVerificationCommand(t, selected, script)
	}
	defer func() { accountStateCommand = original }()
	return ExecuteForDisk(ctx, "fixture", strings.Repeat("a", 32), "fixture-uuid", 1000, 1000, Request{Action: "verify"})
}

func TestAccountVerificationOriginalReplyAndCommandFailure(t *testing.T) {
	for _, mode := range []string{"valid", "exit", "partial"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			script := `printf '{"uuid": "original-uuid"}\n'`
			if mode == "exit" {
				script += `;printf original-verifier-error >&2;exit 7`
			}
			if mode == "partial" {
				script = `printf '{"uuid":'`
			}
			got, err := selectedAccountVerification(t, ctx, script)
			if mode == "valid" {
				if err != nil || got.UUID != "original-uuid" {
					t.Fatal(got, err)
				}
			} else {
				if err == nil || got.UUID != "" {
					t.Fatal("failure returned partial identity", got, err)
				}
			}
			if mode == "exit" {
				var status *exec.ExitError
				if !errors.As(err, &status) || status.ExitCode() != 7 || !strings.Contains(err.Error(), "original-verifier-error") {
					t.Fatal("original status lost", err)
				}
			}
		})
	}
}

// leaderExited calls then after the fixture's leader has written its pid to
// mark as its last act and that process is gone.
func leaderExited(mark string, then func()) {
	go func() {
		for {
			raw, err := os.ReadFile(mark)
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && convErr == nil && pid > 0 {
				if syscall.Kill(pid, 0) == syscall.ESRCH {
					then()
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// A descendant that keeps stdout or stderr open after the verifier exits holds
// the drain only until the caller's cancellation or deadline, or the drain
// fail-safe when the caller has none, and the reply already read is never
// returned as an identity.
func TestAccountVerificationHeldPipeAfterExitGivesNoIdentity(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, end := range []string{"cancel", "deadline", "drain"} {
			t.Run(stream+"/"+end, func(t *testing.T) {
				mark := filepath.Join(t.TempDir(), "leader")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				switch end {
				case "cancel":
					leaderExited(mark, cancel)
				case "deadline":
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 500*time.Millisecond)
					defer stop()
				case "drain":
					previous := accountVerificationDrainFailSafe
					accountVerificationDrainFailSafe = 300 * time.Millisecond
					t.Cleanup(func() { accountVerificationDrainFailSafe = previous })
				}
				holder := `(exec 2>/dev/null;sleep 30) &`
				if stream == "stderr" {
					holder = `(exec >/dev/null;sleep 30) &`
				}
				script := holder + ` printf '{"uuid":"original-uuid"}\n'; echo $$ > ` + strconv.Quote(mark)
				start := time.Now()
				got, err := selectedAccountVerification(t, ctx, script)
				want := map[string]error{"cancel": context.Canceled, "deadline": context.DeadlineExceeded, "drain": execpipe.ErrOutputHeld}[end]
				if !errors.Is(err, want) || got.UUID != "" || time.Since(start) > 3*time.Second {
					t.Fatal("held pipe outlived its bound or returned identity", got, err, time.Since(start))
				}
			})
		}
	}
}

// The drain fail-safe starts when the verifier exits: a healthy verifier that
// runs longer than it still supplies the complete identity.
func TestAccountVerificationSlowValidReplyOutlivesDrainBound(t *testing.T) {
	previous := accountVerificationDrainFailSafe
	accountVerificationDrainFailSafe = 100 * time.Millisecond
	t.Cleanup(func() { accountVerificationDrainFailSafe = previous })
	got, err := selectedAccountVerification(t, t.Context(), `sleep 0.5; printf '{"uuid":"original-uuid"}\n'`)
	if err != nil || got.UUID != "original-uuid" {
		t.Fatal("slow valid verification lost its identity", got, err)
	}
}
func TestAccountVerificationCancellationRetainsOriginalCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	boom := errors.New("original verification canceled")
	script := `printf '{"uuid":"partial"}';sleep 30`
	done := make(chan struct{})
	go func() { defer close(done); time.Sleep(30 * time.Millisecond); cancel(boom) }()
	defer func() { <-done }()
	start := time.Now()
	got, err := selectedAccountVerification(t, ctx, script)
	if got.UUID != "" || !errors.Is(err, context.Canceled) || !errors.Is(err, boom) || time.Since(start) > 3*time.Second {
		t.Fatal(got, err)
	}
}
func TestAccountVerificationBoundsCaptureBeforeGrowth(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			script := `head -c 70000 /dev/zero`
			if stream == "stderr" {
				script += ` >&2`
			}
			got, err := selectedAccountVerification(t, ctx, script)
			if got.UUID != "" || !errors.Is(err, errAccountVerificationOverflow) {
				t.Fatal(got, err)
			}
		})
	}
	w := accountVerificationCapture{limit: 3}
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := w.Write([]byte("x")); n != 0 || !errors.Is(err, errAccountVerificationOverflow) || w.Len() != 3 || !w.overflow {
		t.Fatal(n, err, w.Len())
	}
}
func TestAccountVerificationStrictClosedFieldsAndNoPrivateErrors(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"UUID":"value"}`, `{"uuid":"value","UUID":"value"}`, `{"UUID":"value","uuid":"value"}`, `{"uuid":"value","uuid":"value"}`, `{"uuid":null}`, `{"uuid":""}`, `{"uuid":"value","leaves":[]}`, `{"uuid":"value"} {}`, `{"uuid":17}`, `{"uuid":"` + strings.Repeat("x", 129) + `"}`} {
		got, err := decodeAccountVerification([]byte(raw))
		if err == nil || got.UUID != "" {
			t.Fatal("ambiguous verification became identity", got, err)
		}
	}
}

func TestAccountVerificationValidUUIDCannotHideOversizedStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	got, err := selectedAccountVerification(t, ctx, `head -c 70000 /dev/zero >&2;printf '{"uuid":"original-uuid"}\n'`)
	if got.UUID != "" || !errors.Is(err, errAccountVerificationOverflow) {
		t.Fatal("oversized stderr became successful storage identity", got, err)
	}
}

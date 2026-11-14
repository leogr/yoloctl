package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"gopkg.in/yaml.v3"
)

const expectedRunID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// otherRuns are what `inspect coder` can answer instead of the expected run:
// a replacement answering on the same name, the same container in a new run.
var otherRuns = map[string]string{
	"replacement": `"Id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","State":{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-11-14T10:00:00.1Z"}`,
	"new run":     `"Id":"` + expectedRunID + `","State":{"Status":"running","Running":true,"Pid":77,"StartedAt":"2026-11-14T10:05:00.2Z"}`,
}

// expectingRun is a fake engine whose box coder now runs as state, and a
// context whose operation expects the run it started before.
func expectingRun(t *testing.T, state string) (context.Context, *container.Client, func() string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EXPECTED_RUN_LOG", filepath.Join(dir, "log"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$EXPECTED_RUN_LOG\"\ncase \"$*\" in\n *'sudo nerdctl inspect coder') printf '%s\\n' '[{\"Name\":\"coder\",\"Config\":{\"Labels\":{}}," + state + "}]' ;;\n *) exit 97 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := container.WithExpectedRuns(t.Context())
	container.ExpectedRunsFrom(ctx).Expect("coder", container.Run{ID: expectedRunID, StartedAt: "2026-11-14T10:00:00.1Z", PID: 42})
	return ctx, container.NewWithOutput("work", io.Discard, io.Discard), func() string {
		raw, _ := os.ReadFile(filepath.Join(dir, "log"))
		return string(raw)
	}
}

func stoppedDuring(err error) bool {
	var stopped *container.StoppedDuringOperation
	return errors.As(err, &stopped) && strings.Contains(err.Error(), "box coder stopped during the operation")
}

// Row 3: the agent update reads and installs only into the run the
// operation started; another run ends it before any exec.
func TestAgentUpdateGoesOnlyIntoTheExpectedRun(t *testing.T) {
	profile, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Skip("no claude profile in this catalog:", err)
	}
	for label, state := range otherRuns {
		ctx, c, log := expectingRun(t, state)
		err := updateAgentInRun(ctx, c, "coder", profile, "9.9.9", nil)
		if !stoppedDuring(err) || strings.Contains(log(), "exec") {
			t.Fatalf("%s: %v\n%s", label, err, log())
		}
	}
}

// Row 12: no holder is started on another run.
func TestSessionHolderStartsOnlyOnTheExpectedRun(t *testing.T) {
	previous := startHolderUnitFn
	t.Cleanup(func() { startHolderUnitFn = previous })
	for label, state := range otherRuns {
		started := false
		startHolderUnitFn = func(context.Context, *container.Client, session.UnitSpec) error { started = true; return nil }
		ctx, c, log := expectingRun(t, state)
		if err := startSessionHolder(ctx, c, "coder", session.UnitSpec{Agentbox: "coder"}); !stoppedDuring(err) || started {
			t.Fatalf("%s: %v started %v\n%s", label, err, started, log())
		}
	}
}

// Rows 16 and 18: each chat exec dials the name-based socket only for the
// expected run.
func TestChatExecDialsOnlyTheExpectedRun(t *testing.T) {
	for label, state := range otherRuns {
		ctx, _, log := expectingRun(t, state)
		err := runManagedChatExec(ctx, &agentbox.Config{Name: "coder", Space: "work"}, []string{"/usr/local/bin/aircd", "features"}, nil, io.Discard, io.Discard)
		if !stoppedDuring(err) {
			t.Fatalf("%s: %v\n%s", label, err, log())
		}
	}
}

// Control: the expected run itself passes; the holder start proceeds.
func TestSessionHolderStartsOnTheSameRun(t *testing.T) {
	previous := startHolderUnitFn
	t.Cleanup(func() { startHolderUnitFn = previous })
	started := false
	startHolderUnitFn = func(context.Context, *container.Client, session.UnitSpec) error {
		started = true
		return errors.New("stop here")
	}
	ctx, c, _ := expectingRun(t, `"Id":"`+expectedRunID+`","State":{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-11-14T10:00:00.1Z"}`)
	if err := startSessionHolder(ctx, c, "coder", session.UnitSpec{Agentbox: "coder"}); stoppedDuring(err) || !started {
		t.Fatalf("same run refused: %v", err)
	}
}

// Row 12/13 at the full ensureAgentSession entry: an operation that already
// expects a run of the box and now resolves it stopped ends plainly, before
// any container start; without an expectation the first start is allowed.
func TestEnsureSessionDoesNotStartAnExpectedRunAgain(t *testing.T) {
	f := newIdentityFixture(t)
	_ = f
	cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: "coder", Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("coder")}
	manifest, _ := layout.BoxPath("work", "coder")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	previousResolver, previousBinary := newBoxStateResolver, ensureVMBinaryFn
	t.Cleanup(func() { newBoxStateResolver, ensureVMBinaryFn = previousResolver, previousBinary })
	ensureVMBinaryFn = func(context.Context, *container.Client) error { return nil }
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: "coder", Status: "Exited (0)"}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionNone}
			},
		}
	}
	ctx := container.WithExpectedRuns(held(t.Context()))
	container.ExpectedRunsFrom(ctx).Expect("coder", container.Run{ID: expectedRunID, StartedAt: "2026-11-14T10:00:00.1Z", PID: 42})
	_, err = ensureAgentSession(ctx, container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{})
	if !stoppedDuring(err) || !strings.Contains(err.Error(), "starting it again") {
		t.Fatalf("an expected run was started again: %v", err)
	} // Control: a first start (no expectation) passes this guard and goes on
	// to the session's own checks.
	_, err = ensureAgentSession(container.WithExpectedRuns(held(t.Context())), container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{})
	if stoppedDuring(err) {
		t.Fatalf("a first start was refused: %v", err)
	}
}

// Directive 56: a running privileged box whose endpoint predates the main-session request is
// restarted (one info line) only when one complete process read shows
// nothing besides its PID 1; otherwise, or when that read fails, the warning
// only and no restart. A box that needs no restart is left alone.
func TestPrivilegedMainSessionRestartOnlyWhenIdle(t *testing.T) {
	previousLauncher, previousCount, previousRestart := boxLauncherFn, otherContainerProcessesFn, restartForMainSessionFn
	t.Cleanup(func() {
		boxLauncherFn, otherContainerProcessesFn, restartForMainSessionFn = previousLauncher, previousCount, previousRestart
	})
	for _, tc := range []struct {
		name     string
		needs    bool
		others   int
		countErr error
		restart  bool
		want     string
	}{
		{"idle", true, 0, nil, true, "box coder was restarted to run this yoloctl version"},
		{"an exec runs", true, 1, nil, false, "restart it (box restart coder) when nothing runs in it"},
		{"unreadable inventory", true, 0, errors.New("permission denied"), false, "restart it (box restart coder) when nothing runs in it"},
		{"no restart needed", false, 0, nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, _ := expectingRun(t, `"Id":"`+expectedRunID+`","State":{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-11-14T10:00:00.1Z"}`)
			var out strings.Builder
			t.Cleanup(bindCLITestLogger(t, &out))
			boxLauncherFn = func(context.Context, *container.Client, string, *container.Details) (agentbox.LauncherSupport, error) {
				return agentbox.LauncherSupport{NeedsRestart: tc.needs}, nil
			}
			otherContainerProcessesFn = func(int) (int, error) { return tc.others, tc.countErr }
			restarts := 0
			restartForMainSessionFn = func(context.Context, *container.Client, string, string, io.Writer) error { restarts++; return nil }
			restarted, err := restartPrivilegedForMainSession(t.Context(), c, "work", "coder", io.Discard)
			if err != nil || restarted != tc.restart || restarts != map[bool]int{true: 1}[tc.restart] ||
				tc.want != "" && !strings.Contains(out.String(), tc.want) || tc.want == "" && out.Len() != 0 {
				t.Fatalf("restarted %v (%d) err %v\n%s", restarted, restarts, err, out.String())
			}
		})
	}
}

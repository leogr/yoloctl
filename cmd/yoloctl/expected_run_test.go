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
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
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

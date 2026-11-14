package container

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	runT = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func details(id, started string, pid int, running bool) *Details {
	return &Details{ID: id, Name: "coder", StartedAt: started, PID: pid, Running: running}
}

// The run is the ID and its own marker: a replacement under the same name
// and the same container in a new run both fail the check.
func TestExpectedRunCheck(t *testing.T) {
	runs := ExpectedRunsFrom(WithExpectedRuns(t.Context()))
	if err := runs.Check("coder", nil, "x"); err != nil {
		t.Fatal("a box without an expectation was checked", err)
	}
	runs.Expect("coder", RunOf(details(runT, "2026-11-14T10:00:00.1Z", 42, true)))
	for label, c := range map[string]struct {
		d  *Details
		ok bool
	}{
		"same run":         {details(runT, "2026-11-14T10:00:00.1Z", 42, true), true},
		"absent":           {nil, false},
		"stopped":          {details(runT, "2026-11-14T10:00:00.1Z", 0, false), false},
		"replacement":      {details(runC, "2026-11-14T10:00:00.1Z", 42, true), false},
		"same ID, new run": {details(runT, "2026-11-14T10:05:00.2Z", 77, true), false},
	} {
		err := runs.Check("coder", c.d, "the agent update")
		var stopped *StoppedDuringOperation
		if c.ok != (err == nil) || err != nil && (!errors.As(err, &stopped) || !strings.Contains(err.Error(), "box coder stopped during the operation") || !strings.Contains(err.Error(), "before the agent update")) {
			t.Fatalf("%s: %v", label, err)
		}
	}
	// After the operation's own stop nothing is expected until it starts one.
	runs.stopped(runT)
	if err := runs.Check("coder", details(runT, "2026-11-14T10:00:00.1Z", 42, true), "x"); err == nil {
		t.Fatal("a run expected after the operation's own stop")
	}
	// A read never overrides what the operation started or stopped itself.
	runs.ExpectIfUnset("coder", RunOf(details(runC, "t", 1, true)))
	if err := runs.Check("coder", details(runC, "t", 1, true), "x"); err == nil {
		t.Fatal("a read replaced the operation's own stop")
	}
}

// expectedRunEngine is a fake engine: `inspect coder` and `inspect <T>`
// answer from files the test rewrites; start and stop succeed; all logged.
func expectedRunEngine(t *testing.T) (*Client, func(string, string), string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EXPECTED_RUN_DIR", dir)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$EXPECTED_RUN_DIR/log"
case "$*" in
 *"sudo nerdctl inspect coder") cat "$EXPECTED_RUN_DIR/by-name" ;;
 *"sudo nerdctl inspect ` + runT + `") cat "$EXPECTED_RUN_DIR/by-id" ;;
 *"sudo nerdctl start ` + runT + `") exit 0 ;;
 *"sudo nerdctl stop --time 30 ` + runT + `") exit 0 ;;
 *) exit 97 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	set := func(file, id string) {
		started := "2026-11-14T10:00:00.1Z"
		if strings.HasSuffix(id, "+new-run") {
			id, started = strings.TrimSuffix(id, "+new-run"), "2026-11-14T10:05:00.2Z"
		}
		raw := `[{"Id":"` + id + `","Name":"coder","Config":{"Labels":{}},"State":{"Status":"running","Running":true,"Pid":42,"StartedAt":"` + started + `"}}]`
		if err := os.WriteFile(filepath.Join(dir, file), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return NewWithOutput("fixture", io.Discard, io.Discard), set, filepath.Join(dir, "log")
}

// The client records the run it started (read back by its ID) and its own
// stop; CheckExpectedRun then refuses a replacement answering on the same
// name and the same container in a new run.
func TestClientRecordsItsOwnStartsAndStops(t *testing.T) {
	c, set, _ := expectedRunEngine(t)
	set("by-id", runT)
	ctx := WithExpectedRuns(t.Context())
	if err := c.Start(ctx, runT); err != nil {
		t.Fatal(err)
	}
	for label, now := range map[string]string{"replacement": runC, "new run": runT + "+new-run"} {
		set("by-name", now)
		if _, err := c.CheckExpectedRun(ctx, "coder", "the session start"); err == nil {
			t.Fatalf("%s accepted", label)
		}
	}
	set("by-name", runT)
	if d, err := c.CheckExpectedRun(ctx, "coder", "the session start"); err != nil || d == nil || d.ID != runT {
		t.Fatalf("own run refused: %v", err)
	}
	if err := c.Stop(ctx, runT); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckExpectedRun(ctx, "coder", "x"); err == nil {
		t.Fatal("a run expected after the operation's own stop")
	}
	// Without a record nothing is read or checked.
	if d, err := c.CheckExpectedRun(t.Context(), "coder", "x"); d != nil || err != nil {
		t.Fatal("checked without an expectation")
	}
}

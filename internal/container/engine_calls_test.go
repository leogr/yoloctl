package container

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The client marks an engine start or stop right before invoking the
// engine, whatever its reply; a stop counts only after a start.
func TestClientMarksEngineStartsAndStopsAtItsBoundary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	directory := t.TempDir()
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte("#!/bin/sh\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewWithOutput("fixture", io.Discard, io.Discard)
	id := strings.Repeat("a", 64)
	calls := &EngineCalls{}
	ctx := WithEngineCalls(t.Context(), calls)
	_ = c.StopContainerID(ctx, id, false) // a previous run's stop: not after a start
	if calls.Started() || calls.StoppedAfterStart() {
		t.Fatal("a stop before any start was recorded")
	}
	_ = c.Start(ctx, id) // the engine fails: the start was still invoked
	if !calls.Started() || calls.StoppedAfterStart() {
		t.Fatal("a failed engine start was not recorded")
	}
	_ = c.Stop(ctx, id)
	if !calls.StoppedAfterStart() {
		t.Fatal("a stop after the start was not recorded")
	}
	// Without a record nothing is marked and nothing fails.
	_ = c.Start(context.Background(), id)
	if EngineCallsFrom(context.Background()) != nil {
		t.Fatal("record without context")
	}
}

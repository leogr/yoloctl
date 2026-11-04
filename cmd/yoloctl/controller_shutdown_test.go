package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
)

func TestSpaceShutdownOverlapsContainerGraceAndReapsOnce(t *testing.T) {
	installStopAgentboxRuntimeHooks(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	calls := map[string][]string{}
	record := func(box, step string) {
		mu.Lock()
		defer mu.Unlock()
		calls[box] = append(calls[box], step)
	}
	lifecycleStopHolderUnit = func(_ context.Context, _ *container.Client, name string) error {
		record(name, "holder")
		return nil
	}
	lifecycleUnforwardAgentbox = func(_ context.Context, _ *container.Client, name string) error {
		record(name, "ports")
		return nil
	}
	ready := make(chan struct{})
	started := 0
	stopErr := errors.New("container stop failed")
	lifecycleStopAgentboxContainer = func(ctx context.Context, _ *container.Client, name string) error {
		record(name, "container")
		mu.Lock()
		started++
		if started == 8 {
			close(ready)
		}
		mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ready:
		}
		if name == "live-0" {
			return stopErr
		}
		return nil
	}
	lifecycleCleanupStoppedSessionState = func(_ context.Context, _ *container.Client, name string) error {
		record(name, "state")
		return nil
	}
	var reapCount int
	lifecycleReapOrphanedNetns = func(context.Context, string, io.Writer) error {
		mu.Lock()
		defer mu.Unlock()
		reapCount++
		if len(calls) != 10 {
			t.Error("reaped before all box cleanup finished")
		}
		return nil
	}
	var boxes []boxstate.Box
	for i := range 8 {
		boxes = append(boxes, boxstate.Box{Name: fmt.Sprintf("live-%d", i), Container: &container.Container{Status: "Up 1 minute"}})
	}
	boxes = append(boxes, boxstate.Box{Name: "missing"}, boxstate.Box{Name: "stopped", Container: &container.Container{Status: "Exited (0)"}})
	var notices bytes.Buffer // exercises synchronized concurrent diagnostics
	err := stopSpaceBoxes(ctx, "work", boxes, &notices)
	if ctx.Err() != nil || !errors.Is(err, stopErr) || !strings.Contains(err.Error(), "live-0") {
		t.Fatalf("sequential or lost stop result: %v (context: %v)", err, ctx.Err())
	}
	if reapCount != 1 {
		t.Fatalf("reaps = %d", reapCount)
	}
	for _, box := range boxes {
		want := []string{"holder", "ports"}
		if strings.HasPrefix(box.Name, "live-") {
			want = append(want, "container")
		}
		if box.Name != "live-0" {
			want = append(want, "state")
		}
		if !slices.Equal(calls[box.Name], want) {
			t.Errorf("%s: %v, want %v", box.Name, calls[box.Name], want)
		}
	}
}

func TestDestroyManifestOnlyBoxDoesNotRemoveMissingContainer(t *testing.T) {
	if runtime.GOOS != "linux" {
		// Destroy runs in the Linux VM controller: it removes the box's agent
		// observation tmpfs, which host builds never manage.
		t.Skip("box destroy is controller-only; agent observation roots exist only on Linux")
	}
	fakeLima(t)
	createTestSpace(t, "work")
	mutations := filepath.Join(t.TempDir(), "mutations")
	t.Setenv("FAKE_LIMA_BOX_MUTATIONS", mutations)
	cfg := &agentbox.Config{Name: "declared", Space: "work", Agent: "none", Account: "none", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("declared")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceCommand(t, newAgentboxDestroyCmd(), "declared", "--space", "work", "--force"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentbox.Load("work", "declared"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest not deleted: %v", err)
	}
	raw, err := os.ReadFile(mutations)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "nerdctl rm") || strings.Contains(line, "nerdctl stop") {
			t.Fatalf("destroy called container teardown for a missing container: %s", line)
		}
	}
}

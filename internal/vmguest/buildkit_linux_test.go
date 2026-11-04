//go:build linux

package vmguest

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestBuilderExitRetainsVMAndRestartsOnNextRequest(t *testing.T) {
	var b lazyBuilder
	var cmds []*exec.Cmd
	start := func() (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "30")
		if err := startOwned(cmd); err != nil {
			return nil, err
		}
		cmds = append(cmds, cmd)
		return cmd, nil
	}
	t.Cleanup(func() {
		for _, cmd := range cmds {
			_ = cmd.Process.Kill()
		}
		if b.process != nil {
			<-b.process.done
		}
	})
	probe := func(context.Context) error { return nil }
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := b.ensure(t.Context(), start, probe); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(cmds) != 1 {
		t.Fatal("concurrent starts", len(cmds))
	}
	old := b.process
	if err := cmds[0].Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.done:
	case <-time.After(time.Second):
		t.Fatal("builder not reaped")
	}
	if len(cmds) != 1 {
		t.Fatal("builder auto restarted")
	}
	if err := b.ensure(t.Context(), start, probe); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 || b.process == old {
		t.Fatal("next request did not restart")
	}
}

func TestBuilderReadinessFailureIsBoundedAndRetryable(t *testing.T) {
	var b lazyBuilder
	cmd := exec.Command("sleep", "30")
	start := func() (*exec.Cmd, error) { return cmd, startOwned(cmd) }
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			<-b.process.done
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	err := b.ensure(ctx, start, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	err = b.ensure(t.Context(), func() (*exec.Cmd, error) { t.Fatal("duplicate builder after readiness timeout"); return nil, nil }, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}

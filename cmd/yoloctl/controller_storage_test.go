package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
)

func TestStorageSchedulerWakeAndCancellationLogging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		defer bindCLITestLogger(t, io.Discard)()
		var out bytes.Buffer
		log := logging.NewFileLogger(&out)
		defer func() { _ = log.Close() }()
		server := &controller.Server{}
		var runs atomic.Int32
		stop := scheduleStorageGC(t.Context(), server, func(ctx context.Context) error {
			current := runs.Add(1)
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != 5*time.Minute {
				t.Fatal("idle cleanup needs its own five-minute budget")
			}
			if current == 1 {
				return context.Canceled // foreground cancellation is quiet
			}
			if current == 2 {
				return context.DeadlineExceeded // its own budget is observable
			}
			return nil
		}, log.Slog())
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if runs.Load() != 1 || out.Len() != 0 {
			t.Fatalf("startup: runs=%d log=%q", runs.Load(), out.String())
		}
		// Previously these could cancel a sweep but never rearm it. Bursts
		// should still collapse into one scan rather than one per request.
		for _, method := range []string{"box.exec", "box.stop", "account.secret.set", "image.prune"} {
			server.MutationFinished(method)
		}
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if runs.Load() != 2 || !strings.Contains(out.String(), "deadline exceeded") || !strings.Contains(out.String(), "warning: controller: Storage maintenance deferred") {
			t.Fatalf("completion: runs=%d log=%q", runs.Load(), out.String())
		}
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if runs.Load() != 3 {
			t.Fatalf("periodic cleanup did not run: %d", runs.Load())
		}
		stop()
		time.Sleep(10 * time.Minute)
		if runs.Load() != 3 {
			t.Fatal("cleanup continued after controller shutdown")
		}
	})
}

func TestStorageWorkerCancellationAndDeadline(t *testing.T) {
	for _, beforeStart := range []bool{false, true} {
		for _, timeout := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
				want = context.DeadlineExceeded
			}
			if beforeStart {
				if timeout {
					<-ctx.Done()
				} else {
					cancel()
				}
			} else if !timeout {
				timer := time.AfterFunc(50*time.Millisecond, cancel)
				defer timer.Stop()
			}
			cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & wait")
			err := waitStorageGCProcess(ctx, cmd)
			cancel()
			if !errors.Is(err, want) {
				t.Fatalf("beforeStart=%v timeout=%v: %v, want %v", beforeStart, timeout, err, want)
			}
		}
	}
}

func TestStorageWorkerExitStatus(t *testing.T) {
	for _, code := range []string{"0", "7"} {
		cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit "+code)
		if err := waitStorageGCProcess(t.Context(), cmd); (err != nil) != (code != "0") {
			t.Fatalf("exit %s: %v", code, err)
		}
	}
}

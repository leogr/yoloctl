package session

import (
	"context"
	"errors"
	"testing"
	"time"

	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestBinaryInstallProbeBudget(t *testing.T) {
	if vmhost.GuestProbeTimeout != 10*time.Second || vmhost.BinaryInstallProbeTimeout != 2*time.Minute {
		t.Fatal("ordinary probe policy changed")
	}
	out, err := binaryInstallProbe(t.Context(), func(ctx context.Context, argv ...string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.Now().Add(vmhost.BinaryInstallProbeTimeout)) || !deadline.After(time.Now().Add(vmhost.GuestProbeTimeout)) {
			t.Fatalf("installation deadline %v", deadline)
		}
		if len(argv) != 1 || argv[0] != "hash" {
			t.Fatalf("arguments %v", argv)
		}
		return "full-hash", nil
	}, "hash")
	if err != nil || out != "full-hash" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestBinaryInstallProbeCallerWins(t *testing.T) {
	for _, expired := range []bool{false, true} {
		parent, cancel := context.WithCancel(t.Context())
		ctx := parent
		if expired {
			var stop context.CancelFunc
			ctx, stop = context.WithDeadline(parent, time.Now().Add(-time.Second))
			defer stop()
		}
		calls := 0
		_, err := binaryInstallProbe(ctx, func(probe context.Context, _ ...string) (string, error) {
			calls++
			if !expired {
				cancel()
			}
			<-probe.Done()
			return "late success", nil
		})
		cancel()
		want := context.Canceled
		if expired {
			want = context.DeadlineExceeded
		}
		if !errors.Is(err, want) || calls != 1 {
			t.Fatalf("got %v calls %d", err, calls)
		}
	}
}

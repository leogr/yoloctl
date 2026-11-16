//go:build linux && amd64

package firecracker

import (
	"context"
	"os/exec"
	"syscall"

	"os"
	"path/filepath"
	"testing"
)

func TestBootVMOnlyAdoptsCurrentHelper(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "yfc-")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	published := false
	if err := bootVMAlive(dir, owner, &published); err != nil || published {
		t.Fatalf("prepublication: %v", err)
	}
	stale := owner
	stale.Start = "1"
	state := runtimeState{Helper: stale, VM: stale, Dir: dir}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := bootVMAlive(dir, owner, &published); err != nil || published {
		t.Fatalf("adopted old boot: %v", err)
	}
	state.Helper = owner
	state.VM = process{}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := bootVMAlive(dir, owner, &published); err != nil || !published {
		t.Fatalf("current helper: %v", err)
	}
	state.VM = owner
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := bootVMAlive(dir, owner, &published); err != nil {
		t.Fatal(err)
	}
	state.VM = stale // PID exists but the recorded incarnation does not.
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := bootVMAlive(dir, owner, &published); err == nil {
		t.Fatal("dead matching VMM accepted")
	}
	if err := os.Rename(filepath.Join(dir, "runtime.json"), filepath.Join(dir, "retained.json")); err != nil {
		t.Fatal(err)
	}
	if err := bootVMAlive(dir, owner, &published); err == nil {
		t.Fatal("lost published identity accepted")
	}
}

func TestBootVMUnreadableRecordIsNotDeath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	published := false
	if err := bootVMAlive(dir, process{}, &published); err == nil {
		t.Fatal("unknown observation accepted")
	}
}

func TestBootCancellationSettlesNewHelperWithStaleRecord(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "yfc-")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	stale.Start = "1"
	if err := writeState(dir, runtimeState{Helper: stale, VM: stale, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()
	owner, err := identity(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	published := false
	if err := bootVMAlive(dir, owner, &published); err != nil || published {
		t.Fatalf("old record admitted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	terminated := 0
	err = settleBootFailure(context.WithoutCancel(ctx), dir, owner, published, func() error {
		terminated++
		if err := child.Process.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		_ = child.Wait()
		return nil
	}, func(context.Context) error { t.Fatal("stale runtime was signalled/stopped"); return nil })
	if err != nil || terminated != 1 {
		t.Fatalf("cleanup %v calls %d", err, terminated)
	}
	if live, err := owner.alive(); err != nil || live {
		t.Fatalf("preparing helper leaked: %v %v", live, err)
	}
	retained, err := readState(dir)
	if err != nil || retained.Helper != stale {
		t.Fatalf("old record changed: %+v %v", retained, err)
	}
}

func TestBootCancellationRetainsChangedPublishedOwner(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "yfc-")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	peer := owner
	peer.Start = "1"
	if err := writeState(dir, runtimeState{Helper: peer, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	err = settleBootFailure(t.Context(), dir, owner, true, func() error { t.Fatal("unknown ownership signalled"); return nil }, func(context.Context) error { t.Fatal("peer stopped"); return nil })
	if err == nil {
		t.Fatal("changed ownership accepted")
	}
}

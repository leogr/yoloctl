package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/storagegc"
)

// Maintenance stays in the existing controller, with a short-lived worker for
// its own catalog snapshot. It shares the native mutation lock, never a host
// lock. Interrupted cache cleanup is safe to rescan; no user request is replayed.
func startControllerStorageGC(parent context.Context, server *controller.Server) func() {
	return scheduleStorageGC(parent, server, runStorageGCProcess, logging.L())
}

func scheduleStorageGC(parent context.Context, server *controller.Server, run func(context.Context) error, log *slog.Logger) func() {
	ctx, cancel := context.WithCancel(parent)
	wake := make(chan struct{}, 1)
	// Every admitted mutation can interrupt maintenance, including exec setup
	// and stop. Rearm after all of them; reads do not wake or cancel cleanup.
	server.MutationFinished = func(string) {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	done := make(chan struct{})
	wake <- struct{}{} // startup also recovers obsolete cache after a reboot
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-wake:
			}
			// Coalesce lifecycle bursts and let the foreground finish its reply.
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-wake:
			default:
			}
			_, err := server.RunMaintenance(ctx, func(ctx context.Context) error {
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				return run(ctx)
			})
			if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				log.Warn("Storage maintenance deferred", "actor", logging.Controller, "err", err)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func runStorageGCProcess(ctx context.Context) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "controller-internal", "collect-storage")
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8",
		spaceenv.ContextEnv + "=" + spaceenv.ContextPath}
	cmd.Stderr = os.Stderr
	return waitStorageGCProcess(ctx, cmd)
}

// Keep disposable-storage subprocesses in one killable process group and
// preserve cancellation/deadline identity for the scheduler.
func waitStorageGCProcess(ctx context.Context, cmd *exec.Cmd) error {
	return waitStorageGCProcessStarted(ctx, cmd, nil)
}
func waitStorageGCProcessStarted(ctx context.Context, cmd *exec.Cmd, started func()) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Only disposable cache operations run here. Stop the whole group promptly
	// when a foreground mutation needs the lock, including nerdctl/buildctl.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if started != nil {
		started()
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	err := cmd.Wait()
	close(done)
	<-stopped
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func collectControllerStorage(ctx context.Context) error {
	c := spaceenv.Current()
	if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	unlock, err := spacelock.Acquire(lockCtx, c.Space, true)
	if err != nil {
		return err
	}
	defer unlock()
	catalogs, _, cleanup, err := controller.CatalogSnapshot(c.CatalogDir, spaceenv.RunRoot)
	if err != nil {
		return err
	}
	defer cleanup()
	spaceenv.UseCatalogSnapshot(catalogs)
	if err := nativeStorageCollector(c.Space, os.Stderr).collect(ctx, nil, os.Stderr); err != nil {
		return err
	}
	return trimGuestStorage(ctx, c)
}

// The existing cancellable maintenance worker trims only the two owned ext4
// filesystems, never host grants, overlay mounts or arbitrary user paths.
// Unsupported backing filesystems remain an explicit maintenance diagnostic.
func trimGuestStorage(ctx context.Context, c *spaceenv.Context) error {
	if c.ServiceManager != "microvm" {
		return nil
	}
	for _, mount := range []string{"/run/system", c.DiskMount} {
		cmd := exec.CommandContext(ctx, "fstrim", "--minimum", "1048576", "--", mount)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("trim VM disk %s: %w: %s", mount, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Configure only the rootful BuildKit service that Lima supplies. Deployment
// runs this after draining/stopping the old controller, before admitting builds.
func configureBuildkitGC(ctx context.Context) error {
	mem, err := resources.ReadHostMemory(ctx)
	if err != nil {
		return err
	}
	return configureBuildkitAt(ctx, "/etc/buildkit", "/etc/systemd/system", mem.Total, func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return out, nil
	})
}

type imagePreflightInput struct {
	CleanupID   string   `json:"cleanup_id"`
	ReceiptsDir string   `json:"receipts_dir"`
	References  []string `json:"references"`
	Cleanup     bool     `json:"cleanup"`
}

func imagePreflightWorker(ctx context.Context) error {
	var input imagePreflightInput
	if err := decodeBoundedJSON(os.Stdin, &input, controller.MaxDocument); err != nil {
		return err
	}
	c := spaceenv.Current()
	if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
		return err
	}
	if input.ReceiptsDir != filepath.Join(c.DiskMount, ".controller", "receipts") {
		return errors.New("image preflight receipt-store identity mismatch")
	}
	unlock, err := spacelock.Acquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer unlock()
	catalogs, _, cleanup, err := controller.CatalogSnapshot(c.CatalogDir, spaceenv.RunRoot)
	if err != nil {
		return err
	}
	defer cleanup()
	spaceenv.UseCatalogSnapshot(catalogs)
	disk, err := storagegc.RootDisk()
	if err != nil {
		return err
	}
	if !disk.Low() {
		return nil
	}
	if !input.Cleanup {
		return errors.New("VM disk headroom fell below the 20% reserve before build; no build started; retry after cleanup")
	}
	// Permission is granted only after this native guard was acquired. A
	// waiting cleanup must never reserve the image domain ahead of its lock.
	ready := os.NewFile(3, "cleanup-lock-ready")
	permission := os.NewFile(4, "cleanup-effect-permit")
	if ready == nil || permission == nil {
		return errors.New("private cleanup checkpoints unavailable")
	}
	defer func() { _ = ready.Close() }()
	defer func() { _ = permission.Close() }()
	if _, err := ready.Write([]byte{1}); err != nil {
		return err
	}
	var signal [1]byte
	if _, err := io.ReadFull(permission, signal[:]); err != nil {
		return err
	}
	if signal[0] != 1 {
		return errors.New("cleanup permission refused")
	}
	// No wait under the native guard: a newly reserved/held image store
	// refuses this disposable pass before any deletion.
	store := controller.Receipts{Dir: input.ReceiptsDir}
	permit, err := store.Read(input.CleanupID)
	if err != nil || permit.Method != "image.cleanup" || permit.Generation != c.Generation || permit.DiskID != c.DiskID || permit.EffectPhase != controller.EffectStarted || permit.State != "pending" {
		return errors.New("durable image cleanup permission is unavailable")
	}
	if err := store.CheckImageCleanup(c.Space, c.DiskID, input.CleanupID); err != nil {
		return err
	}
	return nativeStorageCollector(c.Space, os.Stderr).beforeBuild(ctx, input.References, os.Stderr)
}

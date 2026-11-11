package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// New grants exclude every Lima root a space may use, including the one new
// spaces would select; existing grants are revalidated only against storage
// that exists now. Placement still refuses new storage under an old grant.
var (
	limaHomesForNewGrants      = spaceconfig.LimaHomes
	limaHomesForExistingGrants = spaceconfig.RecordedLimaHomes
)

func rejectManagedHostPath(path string) error {
	return rejectManagedHostPathWith(path, limaHomesForNewGrants)
}

func rejectManagedHostPathWith(path string, limaHomes func() ([]string, error)) error {
	root, err := layout.ConfigRoot()
	if err != nil {
		return err
	}
	buildCache, err := layout.VMImageBuildDir("firecracker")
	if err != nil {
		return err
	}
	candidate, _, err := agentbox.CanonicalizeWorkdir(path)
	if err != nil {
		return err
	}
	protectedPaths := []string{root, buildCache}
	if !spaceenv.Native() {
		homes, err := limaHomes()
		if err != nil {
			return err
		}
		protectedPaths = append(protectedPaths, homes...)
	}
	for _, protected := range protectedPaths {
		canonical, _, err := agentbox.CanonicalizeWorkdir(protected)
		if err != nil {
			return err
		}
		if pathsIntersect(candidate, canonical) {
			return asUserError(fmt.Errorf("host mount %s intersects yoloctl managed storage %s", path, canonical))
		}
	}
	return nil
}

// Host paths retain their names in the guest. Reject both native account
// aliases (and their ancestors) before a share can shadow the data disk.
func rejectSpaceHostPath(path string, cfg *spaceconfig.Config) error {
	return rejectSpaceHostPathWith(path, cfg, limaHomesForNewGrants)
}

// rejectExistingSpaceHostPath revalidates a saved grant at start.
func rejectExistingSpaceHostPath(path string, cfg *spaceconfig.Config) error {
	return rejectSpaceHostPathWith(path, cfg, limaHomesForExistingGrants)
}

func rejectSpaceHostPathWith(path string, cfg *spaceconfig.Config, limaHomes func() ([]string, error)) error {
	if err := rejectManagedHostPathWith(path, limaHomes); err != nil {
		return err
	}
	root, err := layout.AccountStateRoot()
	if err != nil {
		return err
	}
	canonical, _, err := agentbox.CanonicalizeWorkdir(path)
	if err != nil {
		return err
	}
	roots := []string{root, "/var/lib/yoloctl", "/etc/yoloctl", "/opt/yoloctl", "/run/yoloctl"}
	if native := spaceenv.Current(); native != nil {
		roots = append(roots, native.ConfigDir, native.HostConfigRoot)
	}
	if cfg != nil {
		roots = append(roots, layout.AccountDiskMount(cfg.AccountDiskID))
	}
	for _, reserved := range roots {
		// Check the literal guest name as well as the host canonical name:
		// a host symlink must not hide a collision with a guest mountpoint.
		resolved, _, err := agentbox.CanonicalizeWorkdir(reserved)
		if err != nil {
			return err
		}
		if pathsIntersect(canonical, reserved) || pathsIntersect(canonical, resolved) || pathsIntersect(path, reserved) {
			return asUserError(fmt.Errorf("host mount %s intersects managed account or controller storage (%s); choose a project directory", path, reserved))
		}
	}
	return nil
}

// Lifecycle requires a successful runtime probe; read-side resolver degradation
// must not be mistaken for proof that no account consumers remain.
func resolveTeardownBoxes(ctx context.Context, space string) ([]boxstate.Box, error) {
	return boxstate.NewResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
}

// Each container gets its normal graceful stop, concurrently with independent
// boxes. Serial 30-second container grace periods can exhaust the entire space
// shutdown budget. Keep per-box ordering and run the space-wide reaper only
// after all stops finish. A failed box must not prevent the others stopping.
func stopSpaceBoxes(ctx context.Context, space string, boxes []boxstate.Box, notice io.Writer) error {
	w := &lifecycleWriter{Writer: notice}
	errs := make([]error, len(boxes))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(8, len(boxes)) {
		workers.Go(func() {
			c := container.New(space)
			for i := range jobs {
				box := boxes[i]
				running := box.Container != nil && boxstate.ContainerRunning(box.Container.Status)
				if running {
					cliNotice(logging.Host, "stopping box %s", box.Name)
				}
				if err := stopAgentboxRuntimePartsObserved(ctx, c, space, box.Name, w, running, false, box.Container == nil); err != nil && !isContainerGone(err) {
					errs[i] = fmt.Errorf("stop box %s: %w", box.Name, err)
				}
			}
		})
	}
enqueue:
	for i := range boxes {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return errors.Join(append(errs, ctx.Err())...)
	}
	errs = append(errs, lifecycleReapOrphanedNetns(ctx, space, w))
	return errors.Join(errs...)
}

type lifecycleWriter struct {
	io.Writer
	mu sync.Mutex
}

func (w *lifecycleWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Writer.Write(p)
}

func stopAccountUsers(ctx context.Context, space, acct string, force bool, notice io.Writer) error {
	boxes, err := resolveTeardownBoxes(ctx, space)
	if err != nil {
		return err
	}
	c := container.New(space)
	for _, b := range boxes {
		if b.Container == nil || !boxstate.ContainerRunning(b.Container.Status) {
			continue
		}
		selected := b.Config != nil && b.Config.Account == acct
		labels := parseLabels(b.Container.Labels)
		actual, actualKnown := labels[agentbox.LabelAccount]
		actualKnown = actualKnown && actual != ""
		// Generic shells deliberately omit the account label. The runtime's
		// explicit agent=none label proves that omission is intentional.
		if !actualKnown && labels[agentbox.LabelAgent] == "none" {
			actualKnown = true
		}
		selected = selected || actual == acct
		// Old/unknown runtime metadata is not proof that a native leaf is unused.
		if !actualKnown {
			return fmt.Errorf("cannot determine runtime account for box %s; stop it first", b.Name)
		}
		if !selected {
			continue
		}
		if !force {
			return asUserError(fmt.Errorf("account %s is used by running box %s; stop its boxes before modifying native state", acct, b.Name))
		}
		if err := stopAgentboxRuntime(ctx, c, space, b.Name, notice); err != nil && !isContainerGone(err) {
			return err
		}
	}
	return nil
}

func stopSpaceRuntime(ctx context.Context, space string, notice io.Writer) error {
	if !spaceenv.Native() {
		if boot, err := spaceconfig.LoadBootstrap(space); err == nil {
			return stopControllerSpace(ctx, boot, notice)
		} else if !errors.Is(err, spaceconfig.ErrLegacySchema) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	inst, found, err := spaceDestroyFindInstance(ctx, space)
	if err != nil {
		return err
	}
	if !found || inst.Status == "Stopped" {
		return nil
	}
	graceful, cancel := context.WithTimeout(ctx, 2*time.Minute)
	boxes, resolveErr := resolveTeardownBoxes(graceful, space)
	if resolveErr == nil {
		for _, b := range boxes {
			if err := stopAgentboxRuntimePartsObserved(graceful, container.New(space), space, b.Name, notice, b.Container != nil && boxstate.ContainerRunning(b.Container.Status), false, b.Container == nil); err != nil && !isContainerGone(err) {
				cliWarn(logging.Host, "stop box %s: %v", b.Name, err)
			}
		}
	} else {
		cliWarn(logging.Host, "guest cleanup unavailable: %v", resolveErr)
	}
	if resolveErr == nil {
		if err := lifecycleReapOrphanedNetns(graceful, space, notice); err != nil {
			cliWarn(logging.Host, "leaked netns cleanup: %v", err)
		}
	}
	err = vmhost.New().Stop(graceful, space)
	cancel()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	cliWarn(logging.Host, "graceful shutdown failed; forcing VM stop; unflushed writes may be lost")
	forced, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return vmhost.New().ForceStop(forced, space, true)
}

func deleteSpaceRuntime(ctx context.Context, space string, notice io.Writer) error {
	pids, pidErr := vmhost.VMProcessIDs(space)
	if err := stopSpaceRuntime(ctx, space, notice); err != nil {
		return err
	}
	if inst, found, err := spaceDestroyFindInstance(ctx, space); err != nil {
		return err
	} else if found {
		force := inst.Status != "Stopped"
		if force {
			if pidErr == nil {
				pidErr = vmhost.RequireVMProcessesStopped(space, inst.VMType, pids)
			}
			if pidErr != nil {
				return fmt.Errorf("VM still reports %s and shutdown could not be verified; repair its Lima configuration and stop it before retrying (account disk and bundle retained): %w", inst.Status, pidErr)
			}
			_, _ = fmt.Fprintf(notice, "VM reports %s after shutdown; verified its processes exited, removing stale runtime metadata\n", inst.Status)
		}
		if err := spaceDestroyDelete(ctx, space, force); err != nil {
			return err
		}
	}
	if _, found, err := spaceDestroyFindInstance(ctx, space); err != nil {
		return err
	} else if found {
		return errors.New("old VM still exists; refusing replacement or account disk deletion")
	}
	return nil
}

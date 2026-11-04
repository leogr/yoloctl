package agentbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

// StartNativeOpenShellForward is used inside existing native mutation/receipt
// admission, with space/entity/port exclusion held until return. It adds no
// authority endpoint. The caller still owns declarations and operation scopes.
// No deferred task or parent-liveness channel stops a successfully owned unit.
func StartNativeOpenShellForward(ctx context.Context, client *container.Client, spec port.Spec, verifyStorage func(context.Context) error) error {
	_, err := startOwnedNativeOpenShellForward(ctx, client, spec, verifyStorage)
	return err
}

type ownedNativeForward struct {
	selected OpenShellRuntime
	spec     port.Spec
	binding  port.ForwardBinding
}

func startOwnedNativeOpenShellForward(ctx context.Context, client *container.Client, spec port.Spec, verifyStorage func(context.Context) error) (ownedNativeForward, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || client == nil || verifyStorage == nil {
		return ownedNativeForward{}, errors.New("OpenShell forward creation requires admitted native VM root")
	}
	if err := spaceenv.RequireSpace(client.Env()); err != nil {
		return ownedNativeForward{}, err
	}
	name, err := port.OpenShellUnitName(spec)
	if err != nil {
		return ownedNativeForward{}, err
	}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := verifyStorage(setup); err != nil {
		return ownedNativeForward{}, err
	}
	selected, err := InspectOpenShellRuntime(setup, client, spec.Agentbox)
	if err != nil {
		return ownedNativeForward{}, err
	}
	if !selected.Running || selected.PID <= 1 || selected.StartedAt.IsZero() {
		return ownedNativeForward{}, errors.New("start the OpenShell box before forwarding")
	}
	if err := preflightForwardCLI(setup); err != nil {
		return ownedNativeForward{}, err
	}
	known, err := port.Inventory(setup, client, "")
	if err != nil {
		// A missing matching-init inventory must fail before Start. Never
		// replace it with an active-only inventory or an empty result.
		return ownedNativeForward{}, fmt.Errorf("complete forward service inventory: %w", err)
	}
	if err := noNativeForwardConflict(known, spec); err != nil {
		return ownedNativeForward{}, err
	}
	var attempt [16]byte
	if _, err := rand.Read(attempt[:]); err != nil {
		return ownedNativeForward{}, err
	}
	binding := selected.ForwardBinding(hex.EncodeToString(attempt[:]))
	args, err := port.ForwardWorkerArgs(spec, binding)
	if err != nil {
		return ownedNativeForward{}, err
	}
	unit := vmservice.Unit{Name: name, Description: fmt.Sprintf("yoloctl OpenShell forward %s %d -> %d", spec.Agentbox, spec.HostPort, spec.ContainerPort),
		Args: append([]string{vmwire.GuestBinary}, args...), Restart: true,
		Env: []string{"HOME=/root", spaceenv.ContextEnv + "=" + spaceenv.ContextPath}}
	err = completeForwardCreation(ctx,
		func(start context.Context) error {
			start, cancel := context.WithTimeout(start, 10*time.Second)
			defer cancel()
			return vmservice.Start(start, client, unit)
		},
		func(ready context.Context) error {
			return waitNativeForwardReady(ready, client, selected, spec, binding, verifyStorage)
		},
		func(cleanup context.Context) error {
			return cleanupNativeForwardAttempt(cleanup, client, selected, spec, binding, verifyStorage)
		})
	if err != nil {
		return ownedNativeForward{}, err
	}
	return ownedNativeForward{selected: selected, spec: spec, binding: binding}, nil
}

func noNativeForwardConflict(known []port.ForwardService, spec port.Spec) error {
	for _, existing := range known {
		if existing.Spec.HostPort == spec.HostPort {
			return fmt.Errorf("%w: published port %d retained by service %s", port.ErrPortInUse, spec.HostPort, existing.Unit)
		}
	}
	return nil
}

// An acknowledged Start is owned continuously under the caller's admission.
// A failed/lost Start reply never triggers an automatic stop or retry. After a
// known Start, failed readiness stops only this attempt using independent proof.
func completeForwardCreation(ctx context.Context, start, ready, cleanup func(context.Context) error) error {
	if err := start(ctx); err != nil {
		return &NativeEffectsUncertain{Cause: fmt.Errorf("forward service creation was not acknowledged; do not retry or adopt it automatically: %w", err)}
	}
	if err := ready(ctx); err != nil {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if cleanupErr := cleanup(stop); cleanupErr != nil {
			return &NativeEffectsUncertain{Cause: errors.Join(err, fmt.Errorf("owned forward cleanup remains unproved: %w", cleanupErr))}
		}
		return err
	}
	return nil
}

func preflightForwardCLI(ctx context.Context) error {
	for dir := filepath.Dir(vmwire.GuestBinary); dir != "/"; dir = filepath.Dir(dir) {
		if err := rootOwnedPath(dir, true); err != nil {
			return err
		}
	}
	if err := rootOwnedPath(vmwire.GuestBinary, false); err != nil {
		return err
	}
	installed, err := os.Stat(vmwire.GuestBinary)
	if err != nil {
		return err
	}
	selected, err := os.Stat("/proc/self/exe") // selected controller's kernel inode
	if err != nil || !os.SameFile(installed, selected) {
		return errors.New("forward worker CLI is not the selected native controller executable")
	}
	identity, err := currentForwardProcessIdentity()
	if err != nil {
		return err
	}
	process, err := pinForwardProcess(ctx, identity, os.Args, selected, 0, 0)
	if err != nil {
		return err
	}
	return process.Close()
}

var errForwardNotReady = errors.New("owned forward has not published a current listener")

func waitNativeForwardReady(ctx context.Context, client *container.Client, selected OpenShellRuntime, spec port.Spec, binding port.ForwardBinding, verifyStorage func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unitName, _ := port.OpenShellUnitName(spec)
	store := forwardStatusStore{directory: filepath.Join(selected.ControlRoot, "generations", binding.Generation, "status", "forwards"), uid: 0, gid: 0}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for owned forward listener: %w", err)
		}
		if err := verifyStorage(ctx); err != nil {
			return err
		}
		observed, err := InspectOpenShellRuntime(ctx, client, spec.Agentbox)
		if err != nil {
			return err
		}
		if !sameForwardRuntime(observed, selected, binding) {
			return errors.New("forward startup container or runtime generation changed")
		}
		unit, err := vmservice.Process(ctx, client, unitName)
		if err != nil {
			return err
		}
		if unit.State == "failed" || unit.State == "inactive" || unit.State == "unknown" || unit.State == "deactivating" {
			return errors.New("owned forward service ended before listener readiness")
		}
		err = store.transaction(ctx, binding.Attempt+".json", func() (err error) {
			raw, status, err := readForwardStatusLocked(store.directory, binding.Attempt+".json", 0, 0)
			if err != nil {
				return err
			}
			if status.Spec != spec || status.Binding != binding || status.Unit != unitName {
				return errors.New("forward listener status belongs to another attempt")
			}
			if unit.State != "active" || unit.PID != status.PID {
				return errForwardNotReady // read-only wait across a supervised restart
			}
			process, err := pinNativeForwardProcess(ctx, unit, forwardProcessIdentity{status.PID, status.StartTicks}, spec, binding)
			if err != nil {
				if errors.Is(err, unix.ESRCH) || errors.Is(err, os.ErrNotExist) {
					return errForwardNotReady
				}
				return err
			}
			defer func() { err = errors.Join(err, process.Close()) }()
			if err := process.verifyListener(ctx, spec.HostPort, forwardListenerIdentity{status.SocketFD, status.SocketInode}); err != nil {
				return err
			}
			if err := verifyStorage(ctx); err != nil {
				return err
			}
			current, err := InspectOpenShellRuntime(ctx, client, spec.Agentbox)
			if err != nil || !sameForwardRuntime(current, selected, binding) {
				return errors.New("runtime changed during owned listener proof")
			}
			again, err := vmservice.Process(ctx, client, unitName)
			if err != nil {
				return err
			}
			if again != unit {
				return errForwardNotReady
			}
			checked, _, err := readForwardStatusLocked(store.directory, binding.Attempt+".json", 0, 0)
			if err != nil || !bytes.Equal(raw, checked) {
				return errors.New("forward listener status changed during proof")
			}
			return process.verifyListener(ctx, spec.HostPort, forwardListenerIdentity{status.SocketFD, status.SocketInode})
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, errForwardNotReady) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func sameForwardRuntime(current, selected OpenShellRuntime, binding port.ForwardBinding) bool {
	return current.matchesForward(binding) && current.ContainerID == selected.ContainerID && current.PID == selected.PID && current.StartedAt.Equal(selected.StartedAt) && current.ControlRoot == selected.ControlRoot
}

func cleanupNativeForwardAttempt(ctx context.Context, client *container.Client, selected OpenShellRuntime, spec port.Spec, binding port.ForwardBinding, verifyStorage func(context.Context) error) error {
	if err := verifyStorage(ctx); err != nil {
		return err
	}
	name, _ := port.OpenShellUnitName(spec)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		unit, err := vmservice.Process(ctx, client, name)
		if err != nil {
			return err
		}
		if unit.State != "active" {
			break
		}
		// Start was acknowledged under uninterrupted native exclusion, but
		// still refuse a current worker with another role/attempt/executable.
		identity, err := observedForwardProcessIdentity(unit.PID)
		if err == nil {
			var process *pinnedForwardProcess
			process, err = pinNativeForwardProcess(ctx, unit, identity, spec, binding)
			if err == nil {
				err = process.Close()
			}
		}
		if err == nil {
			again, probeErr := vmservice.Process(ctx, client, name)
			if probeErr != nil {
				return probeErr
			}
			if again == unit {
				break
			}
		} else if !errors.Is(err, unix.ESRCH) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Only read-only ownership observation repeats across an exiting or
		// supervised-restarting worker; Start/Stop mutations are never retried.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Inactive/activating instances still belong to the acknowledged Start
	// under the caller's continuously held admission; no mutation was retried.
	directory := filepath.Join(selected.ControlRoot, "generations", binding.Generation, "status", "forwards")
	store := forwardStatusStore{directory: directory, uid: 0, gid: 0}
	return stopNativeForwardAndRemoveStatus(ctx, client, spec, binding, store)
}

// This is the actual adapter/status boundary. A failed acknowledgement must
// return before opening/removing status, irrespective of a later unit state.
func stopNativeForwardAndRemoveStatus(ctx context.Context, client *container.Client, spec port.Spec, binding port.ForwardBinding, store forwardStatusStore) error {
	name, err := port.OpenShellUnitName(spec)
	if err != nil {
		return err
	}
	if err := vmservice.StopAcknowledged(ctx, client, name); err != nil {
		return err
	}
	err = store.transaction(ctx, binding.Attempt+".json", func() error {
		raw, status, err := readForwardStatusLocked(store.directory, binding.Attempt+".json", store.uid, store.gid)
		if err != nil {
			return err
		}
		if status.Spec != spec || status.Binding != binding || status.Unit != name {
			return errors.New("refusing cleanup of foreign forward status")
		}
		return store.removeLocked(binding.Attempt+".json", raw)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

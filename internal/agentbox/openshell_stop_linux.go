package agentbox

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// stopNativeRuntime runs under existing native lifecycle admission. The actual
// selected unconfined generation retires its namespace before socket removal;
// confined mode needs only the fresh engine Stop/inactive acknowledgement.
// Neither path deletes retained generation material or grants new activation.
func (a LiveApplier) stopNativeRuntime(ctx context.Context, name, id string) error {
	return a.stopNativeRuntimeWithGeneration(ctx, name, id, nil)
}

// stopNativeRuntimeForResult is the engine stop of `box stop`'s result mode:
// the same ownership checks and the one engine stop (on unconfined_network
// performed by the namespace retirement), without the forward precondition
// and without endpoint removal, which the caller's later step does under its
// own checks (RemoveStoppedEndpoint).
func (a LiveApplier) stopNativeRuntimeForResult(ctx context.Context, name, id string) error {
	return a.stopNativeRuntimeMode(ctx, name, id, nil, true)
}

func (a LiveApplier) stopNativeRuntimeWithGeneration(ctx context.Context, name, id string, attempt *nativeStartedGeneration) error {
	return a.stopNativeRuntimeMode(ctx, name, id, attempt, false)
}

func (a LiveApplier) stopNativeRuntimeMode(ctx context.Context, name, id string, attempt *nativeStartedGeneration, forResult bool) error {
	if attempt != nil {
		if err := attempt.cleanupIdentity(); err != nil {
			return err
		}
	}
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil || a.VerifyStorage == nil {
		return errors.New("native runtime stop requires admitted VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	if err := a.VerifyStorage(ctx); err != nil {
		return err
	}
	selected, err := inspectOpenShellRuntime(ctx, a.Client, name, id)
	if err != nil {
		return err
	}
	if selected.ContainerID != id {
		return errors.New("native stop observation differs from its selected CID")
	}
	if attempt != nil {
		if err := attempt.verifySelected(ctx, selected); err != nil {
			return err
		}
	}
	directory, err := openForwardSocketDirectory(name)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if directory == nil && selected.Running {
		return errors.New("running native runtime lacks its public SSH bind directory")
	}
	if directory != nil {
		defer func() { _ = directory.Close() }()
	}
	// This closure is read-only, including when called repeatedly around actual
	// namespace effects. Endpoint deletion is the separate final callback below.
	resources := func(check context.Context) error {
		if err := a.VerifyStorage(check); err != nil {
			return err
		}
		if attempt != nil {
			if err := attempt.verifySelected(check, selected); err != nil {
				return err
			}
		}
		if !forResult {
			known, err := port.Inventory(check, a.Client, "")
			if err != nil {
				return err
			}
			for _, service := range known {
				if service.Spec.Agentbox == name {
					return errors.New("withdraw retained forwards before native container stop")
				}
			}
		}
		current, err := openForwardSocketDirectory(name)
		if directory == nil {
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			if current != nil {
				_ = current.Close()
			}
			return errors.Join(err, errors.New("absent native stop socket directory was replaced"))
		}
		if err != nil {
			return err
		}
		defer func() { _ = current.Close() }()
		before, err := directory.Stat()
		if err != nil {
			return err
		}
		after, err := current.Stat()
		if err != nil || !os.SameFile(before, after) {
			return errors.Join(err, errors.New("native stop socket directory was replaced"))
		}
		return nil
	}
	checks := nativeRuntimeStopChecks{
		selected: selected, resources: resources,
		read: func(check context.Context) (OpenShellRuntime, *container.Details, error) {
			if err := a.VerifyStorage(check); err != nil {
				return OpenShellRuntime{}, nil, err
			}
			current, err := inspectOpenShellRuntime(check, a.Client, name, id)
			if err != nil {
				return OpenShellRuntime{}, nil, err
			}
			details, err := a.Client.Inspect(check, id)
			if err == nil && attempt != nil && attempt.target.ContainerID != "" {
				err = validateNativeCleanupState(details, id, attempt.target, true)
			}
			return current, details, err
		},
	}
	if err := checks.owned(ctx); err != nil {
		return err
	}
	stop := func(stop context.Context) error { return a.Client.Stop(stop, id) }
	endpoint := func(check context.Context) error {
		if directory == nil || forResult {
			return nil
		}
		if err := resources(check); err != nil {
			return err
		}
		return removeOwnedNativeSSHSocket(directory)
	}
	if attempt != nil {
		attempt.cleanupAttempted = true
		return completeNativeGenerationStop(ctx, attempt.preparation, stop, checks.owned, checks.inactive, endpoint)
	}
	return completeNativeStopRoute(ctx, selected.Record.Launch.UnconfinedNetwork, stop, checks.inactive,
		func(retire context.Context) error {
			return openshell.StopNativeNamespace(retire, selected.Record, selected.Release, id, openshell.NativeNamespaceStop{VerifyOwned: checks.owned, Stop: stop, VerifyInactive: checks.inactive})
		}, endpoint)
}

// These same read-only predicates feed both the outer preflight and actual
// SDK callbacks. Inactive task deletion may erase StartedAt; that observation
// is never a Stop ACK. Present changed history, ambiguous fields and any new
// live instance still refuse before effects.
type nativeRuntimeStopChecks struct {
	selected  OpenShellRuntime
	read      func(context.Context) (OpenShellRuntime, *container.Details, error)
	resources func(context.Context) error
}

func (c nativeRuntimeStopChecks) verify(ctx context.Context, inactive bool) error {
	if c.read == nil || c.resources == nil {
		return errors.New("native Stop lacks current admitted ownership readers")
	}
	current, details, err := c.read(ctx)
	if err != nil {
		return err
	}
	if err := validateNativeStopObservation(c.selected, current, details, inactive); err != nil {
		return err
	}
	return c.resources(ctx)
}
func (c nativeRuntimeStopChecks) owned(ctx context.Context) error    { return c.verify(ctx, false) }
func (c nativeRuntimeStopChecks) inactive(ctx context.Context) error { return c.verify(ctx, true) }
func validateNativeStopObservation(selected, current OpenShellRuntime, d *container.Details, inactive bool) error {
	if d == nil || !sameNativeStopBinding(current, selected) || d.ID != selected.ContainerID || current.Running != d.Running || current.PID != d.PID || d.AmbiguousTaskStateObserved {
		return errors.New("native Stop current container/record/process observation changed or is ambiguous")
	}
	// Keep the metadata read and independently repeated actual inspect paired.
	// A race between the two reads refuses, rather than selecting either one.
	target := openshell.ActivationTarget{ContainerID: selected.ContainerID, PID: selected.PID, StartedAt: selected.StartedAt}
	if err := validateNativeCleanupState(d, selected.ContainerID, target, true); err != nil {
		return err
	}
	if d.Running {
		if inactive || !selected.Running || !current.StartedAt.Equal(selected.StartedAt) {
			return errors.New("native Stop observed a new/still-running task")
		}
	} else if !nativeInactiveCleanupObservation(d) {
		return errors.New("native Stop lacks explicit current inactive fields")
	}
	// validateNativeCleanupState parsed and bound any present nonzero history.
	// Also bind this repeated inspection to the versioned metadata observation.
	observed := current.StartedAt
	if d.StartedAt == "" {
		if !observed.IsZero() {
			return errors.New("native Stop timestamp changed between inspection reads")
		}
	} else {
		at, err := time.Parse(time.RFC3339Nano, d.StartedAt)
		if err != nil || !at.Equal(observed) {
			return errors.Join(err, errors.New("native Stop timestamp changed between inspection reads"))
		}
	}
	return nil
}
func sameNativeStopBinding(a, b OpenShellRuntime) bool {
	return a.ContainerID == b.ContainerID && a.ControlRoot == b.ControlRoot && a.RecordSHA256 == b.RecordSHA256 && a.Launch == b.Launch && a.Record.Generation == b.Record.Generation
}

// RemoveStoppedEndpoint removes the box's leftover endpoint socket: only when
// its exact container exists and is not running, no forward of the box is
// listed, and the socket directory, opened without following links and
// verified, is the same file before and after. A missing directory or socket
// is nothing to remove. Without a container nothing owns the directory's
// content, so it is kept for recreate or destroy.
func (a LiveApplier) RemoveStoppedEndpoint(ctx context.Context, name string) error {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil {
		return errors.New("native endpoint cleanup requires admitted VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	return removeStoppedEndpoint(ctx, func() (*os.File, error) { return openForwardSocketDirectory(name) },
		func(ctx context.Context) (bool, error) {
			details, err := a.Client.Inspect(ctx, name)
			if err != nil {
				return false, err
			}
			return details.Running, nil
		},
		func(ctx context.Context) (bool, error) {
			known, err := port.Inventory(ctx, a.Client, "")
			if err != nil {
				return false, err
			}
			for _, service := range known {
				if service.Spec.Agentbox == name {
					return true, nil
				}
			}
			return false, nil
		})
}

// removeStoppedEndpoint holds the checks of RemoveStoppedEndpoint in order;
// any check that does not hold keeps the socket.
func removeStoppedEndpoint(ctx context.Context, open func() (*os.File, error), running, forwardListed func(context.Context) (bool, error)) error {
	directory, err := open()
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), "ssh.sock", &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	}
	if live, err := running(ctx); err != nil {
		return errors.Join(err, errors.New("the endpoint socket is kept: its container could not be read"))
	} else if live {
		return errors.New("the endpoint socket is kept: its container is running")
	}
	if listed, err := forwardListed(ctx); err != nil {
		return errors.Join(err, errors.New("the endpoint socket is kept: the forwards of the box could not be listed"))
	} else if listed {
		return errors.New("the endpoint socket is kept: a forward of the box is still listed")
	}
	current, err := open()
	if err != nil {
		return errors.Join(err, errors.New("the endpoint socket is kept: its directory could not be reopened"))
	}
	defer func() { _ = current.Close() }()
	before, err := directory.Stat()
	if err != nil {
		return err
	}
	after, err := current.Stat()
	if err != nil || !os.SameFile(before, after) {
		return errors.Join(err, errors.New("the endpoint socket is kept: its directory was replaced"))
	}
	// removeOwnedNativeSSHSocket verifies the protected socket before unlinking.
	return removeOwnedNativeSSHSocket(directory)
}

package agentbox

import (
	"context"
	"errors"
	"fmt"
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
//
// Storage verification guards what the stop reads and removes on the data
// disk (the runtime record, the namespace retirement) and runs at each of its
// usual points. When it fails before the engine stop was invoked, the engine
// stop still runs: the exact container, checked by its engine ownership
// labels, is stopped once by ID and the disk-dependent work is returned as a
// ResultLeftover. When it fails after, nothing is stopped again: the
// unverified checks are the leftover.
func (a LiveApplier) stopNativeRuntimeForResult(ctx context.Context, name, id string) error {
	return stopForResultWithStorageFallback(ctx, a.VerifyStorage,
		func(ctx context.Context, verify func(context.Context) error, invoked func()) error {
			return a.stopNativeRuntimeMode(ctx, name, id, nil, &nativeResultStop{verify: verify, invoked: invoked})
		},
		func(ctx context.Context, cause error) error {
			return stopExactContainerWithoutStorage(ctx, a.Client, name, id, cause)
		})
}

// nativeResultStop selects the result mode of stopNativeRuntimeMode: its
// storage verifier (which marks its failures) and the mark of the engine stop.
type nativeResultStop struct {
	verify  func(context.Context) error
	invoked func()
}

// storageUnverified marks a storage verification failure inside a result-mode
// stop, wherever the route wraps it.
type storageUnverified struct{ cause error }

func (e *storageUnverified) Error() string { return "storage verification failed: " + e.cause.Error() }
func (e *storageUnverified) Unwrap() error { return e.cause }

// stopForResultWithStorageFallback runs the result-mode stop with a marking
// storage verifier and decides a storage failure by whether the engine stop
// was invoked: before it, the engine-only fallback stops the container once;
// after it, the rest is a leftover and nothing stops again.
func stopForResultWithStorageFallback(ctx context.Context, verifyStorage func(context.Context) error,
	run func(context.Context, func(context.Context) error, func()) error, fallback func(context.Context, error) error) error {
	if verifyStorage == nil {
		return errors.New("native runtime stop requires admitted VM-root infrastructure")
	}
	invoked := false
	verify := func(check context.Context) error {
		if err := verifyStorage(check); err != nil {
			return &storageUnverified{cause: err}
		}
		return nil
	}
	err := run(ctx, verify, func() { invoked = true })
	var unverified *storageUnverified
	if !errors.As(err, &unverified) {
		return err
	}
	if !invoked {
		return fallback(ctx, unverified.cause)
	}
	return &ResultLeftover{What: "checks after the stop", Cause: unverified}
}

// stopExactContainerWithoutStorage is the engine-only stop of the result
// mode: the container read by its full ID, still box name's by its engine
// labels, stopped once by that ID.
func stopExactContainerWithoutStorage(ctx context.Context, client *container.Client, name, id string, cause error) error {
	if client == nil {
		return errors.New("runtime stop requires a container client")
	}
	details, err := client.Inspect(ctx, id)
	if err != nil {
		return err
	}
	if details.ID != id || details.Name != name || details.Labels[LabelAgentbox] != name || details.Labels[LabelSpace] != client.Env() {
		return errors.New("the container read is no longer the box's container")
	}
	if details.Running {
		if err := client.StopContainerID(ctx, id, false); err != nil {
			return err
		}
	}
	what := "runtime record checks"
	if DecodeLabels(details.Labels).UnconfinedNetwork {
		what = "network namespace retirement"
	}
	return &ResultLeftover{What: what, Cause: fmt.Errorf("storage verification failed: %w", cause)}
}

func (a LiveApplier) stopNativeRuntimeWithGeneration(ctx context.Context, name, id string, attempt *nativeStartedGeneration) error {
	return a.stopNativeRuntimeMode(ctx, name, id, attempt, nil)
}

// nativeVMRootAdmitted is the native stop's admission: the VM controller's
// root. Only tests that drive the real entry against a fake engine replace it.
var nativeVMRootAdmitted = func() bool { return spaceenv.Native() && os.Geteuid() == 0 && os.Getegid() == 0 }

// stopNativeRuntimeMode is today's native stop, or with result set the result
// mode: no forward precondition, no endpoint removal, result.verify as the
// storage verifier at every point and result.invoked marked right before the
// engine stop.
func (a LiveApplier) stopNativeRuntimeMode(ctx context.Context, name, id string, attempt *nativeStartedGeneration, result *nativeResultStop) error {
	forResult := result != nil
	if attempt != nil {
		if err := attempt.cleanupIdentity(); err != nil {
			return err
		}
	}
	if !nativeVMRootAdmitted() || a.Client == nil || a.VerifyStorage == nil {
		return errors.New("native runtime stop requires admitted VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	verify := a.VerifyStorage
	if forResult {
		verify = result.verify
	}
	// A failed-start cleanup continues on a detached context. Apply the
	// fail-safe at each independent wait, including on repeated SDK callbacks.
	wait := runCallerStep
	if attempt != nil {
		wait = runRuntimeStep
	}
	if err := wait(ctx, verify); err != nil {
		return err
	}
	observe := func(check context.Context) (OpenShellRuntime, error) {
		return inspectOpenShellRuntime(check, a.Client, name, id)
	}
	var selected OpenShellRuntime
	if err := wait(ctx, func(check context.Context) error {
		var err error
		selected, err = observe(check)
		return err
	}); err != nil {
		return err
	}
	if selected.ContainerID != id {
		return errors.New("native stop observation differs from its selected CID")
	}
	if attempt != nil {
		if err := wait(ctx, func(check context.Context) error { return attempt.verifySelected(check, selected) }); err != nil {
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
	resources := func(check context.Context, wait func(context.Context, func(context.Context) error) error) error {
		if err := wait(check, verify); err != nil {
			return err
		}
		if attempt != nil {
			if err := wait(check, func(step context.Context) error { return attempt.verifySelected(step, selected) }); err != nil {
				return err
			}
		}
		if !forResult {
			var known []port.ForwardService
			err := wait(check, func(step context.Context) error {
				var err error
				known, err = port.Inventory(step, a.Client, "")
				return err
			})
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
	ownership := func(wait func(context.Context, func(context.Context) error) error) nativeRuntimeStopChecks {
		return nativeRuntimeStopChecks{
			selected:  selected,
			resources: func(check context.Context) error { return resources(check, wait) },
			read: func(check context.Context) (OpenShellRuntime, *container.Details, error) {
				current, details, err := readNativeStopOwnership(check, wait, verify, observe,
					func(step context.Context) (*container.Details, error) { return a.Client.Inspect(step, id) })
				if err == nil && attempt != nil && attempt.target.ContainerID != "" {
					err = validateNativeCleanupState(details, id, attempt.target, true)
				}
				return current, details, err
			},
		}
	}
	checks := ownership(wait)
	if err := checks.owned(ctx); err != nil {
		return err
	}
	stop := func(stop context.Context) error {
		if forResult {
			result.invoked()
		}
		return a.stopNativeRuntimeEngine(stop, id, attempt != nil)
	}
	endpoint := func(check context.Context) error {
		if directory == nil || forResult {
			return nil
		}
		if err := checks.resources(check); err != nil {
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
			// The SDK calls VerifyInactive after Stop ACK on its detached
			// context, while its pre-effect checks still follow the caller.
			return openshell.StopNativeNamespace(retire, selected.Record, selected.Release, id,
				openshell.NativeNamespaceStop{VerifyOwned: checks.owned, Stop: stop, VerifyInactive: ownership(runRuntimeStep).inactive})
		}, endpoint)
}

func (a LiveApplier) stopNativeRuntimeEngine(ctx context.Context, id string, detached bool) error {
	if detached {
		return runRuntimeStep(ctx, func(engine context.Context) error { return a.Client.Stop(engine, id) })
	}
	return a.Client.Stop(ctx, id)
}

// Each invocation reads fresh admitted ownership. The runtime observation
// and the independently repeated engine inspection each own their wait.
func readNativeStopOwnership(ctx context.Context,
	wait func(context.Context, func(context.Context) error) error,
	verify func(context.Context) error,
	observe func(context.Context) (OpenShellRuntime, error),
	inspect func(context.Context) (*container.Details, error),
) (OpenShellRuntime, *container.Details, error) {
	if err := wait(ctx, verify); err != nil {
		return OpenShellRuntime{}, nil, err
	}
	var current OpenShellRuntime
	if err := wait(ctx, func(check context.Context) error {
		var err error
		current, err = observe(check)
		return err
	}); err != nil {
		return OpenShellRuntime{}, nil, err
	}
	var details *container.Details
	err := wait(ctx, func(check context.Context) error {
		var err error
		details, err = inspect(check)
		return err
	})
	return current, details, err
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
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil || a.VerifyStorage == nil {
		return errors.New("native endpoint cleanup requires admitted VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	// The same verification that guarded today's endpoint removal inside the
	// stop: it is not inherited from the stop, whose errors no longer end it.
	if err := a.VerifyStorage(ctx); err != nil {
		return fmt.Errorf("storage verification failed: %w", err)
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

package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// This descriptor can only be derived from the original in-call reservation
// and attempts. It is not serializable and cannot be minted from a cold phase.
type knownUnboundPin struct {
	reservation         *namespacePinReservation
	directoryMount      uint64
	privateAcknowledged bool
}

func selectKnownUnboundPin(r *namespacePinReservation, d *nativePinDirectoryAttempt, n *nativePinAttempt) (knownUnboundPin, error) {
	if r == nil {
		return knownUnboundPin{}, errors.New("partial cleanup lacks its live creation reservation")
	}
	if r.cleanupAttempted {
		return knownUnboundPin{}, &NativeEffectsUncertain{Cause: errors.New("partial cleanup attempt was already consumed; no retry")}
	}
	if r.files == nil || r.files.directory == nil {
		return knownUnboundPin{}, errors.New("partial cleanup reservation is closed")
	}
	if d == nil {
		if r.consumed || n != nil || r.files.identity != r.original {
			return knownUnboundPin{}, errors.New("consumed/changed partial reservation lacks its actual directory attempt")
		}
		return knownUnboundPin{reservation: r}, nil
	}
	if d.closed || d.reservation != r || !r.consumed {
		return knownUnboundPin{}, errors.New("partial cleanup directory attempt differs from its creator")
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(d.failure, &uncertain) {
		return knownUnboundPin{}, d.failure
	}
	p := d.progress
	if p.bindAttempted != p.bindAcknowledged || p.bindAcknowledged && (!p.boundRecorded || p.directoryMount == 0) || !p.bindAcknowledged && (p.boundRecorded || p.directoryMount != 0 || p.privateAttempted || p.privateAcknowledged || p.ready) || p.privateAttempted != p.privateAcknowledged || p.ready && !p.privateAcknowledged {
		return knownUnboundPin{}, &NativeEffectsUncertain{Cause: errors.New("partial directory creation lacks all attempted mutation acknowledgements")}
	}
	if d.namespaceConsumed {
		if n == nil || n.closed || n.directory != d {
			return knownUnboundPin{}, errors.New("consumed namespace attempt lacks its actual creator state")
		}
		if errors.As(n.failure, &uncertain) {
			return knownUnboundPin{}, n.failure
		}
		if n.progress != (pinNamespaceProgress{}) || n.namespace != nil || n.proof.Schema != 0 {
			return knownUnboundPin{}, &NativeEffectsUncertain{Cause: errors.New("namespace effects were attempted; known-unbound cleanup is unavailable")}
		}
	} else if n != nil {
		return knownUnboundPin{}, errors.New("namespace attempt does not belong to the consumed directory")
	}
	return knownUnboundPin{r, p.directoryMount, p.privateAcknowledged}, nil
}

type partialPinProgress struct {
	stopAttempted, stopAcknowledged       bool
	privateAttempted, privateAcknowledged bool
	unmountAttempted, unmountAcknowledged bool
	ready                                 bool
}

type partialPinHooks struct {
	beforeStop    func(context.Context) error
	stop          func(context.Context) error
	check         func(context.Context, partialPinProgress) error
	persist       func(namespacePinPhase) error
	makePrivate   func() error
	verifyPrivate func(context.Context) error
	unmount       func() error
	verifyBare    func(context.Context) error
}

func runPartialPinRetirement(ctx context.Context, bound, needsPrivate bool, h partialPinHooks) (p partialPinProgress, err error) {
	if h.beforeStop == nil || h.stop == nil || h.check == nil || h.persist == nil || h.makePrivate == nil || h.verifyPrivate == nil || h.unmount == nil || h.verifyBare == nil || needsPrivate && !bound {
		return p, errors.New("incomplete known partial pin cleanup protocol")
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if err := h.beforeStop(ctx); err != nil {
		return p, err
	}
	p.stopAttempted = true
	if err := h.stop(ctx); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.stopAcknowledged = true
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	fail := func(cause error) (partialPinProgress, error) { return p, &NativeEffectsUncertain{Cause: cause} }
	check := func() error {
		if err := cleanup.Err(); err != nil {
			return err
		}
		return h.check(cleanup, p)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	if !bound {
		p.ready = true
		return p, nil
	}
	if needsPrivate {
		if err := h.persist(pinPartialPrivateAttempt); err != nil {
			return fail(err)
		}
		if err := check(); err != nil {
			return fail(err)
		}
		p.privateAttempted = true
		if err := h.makePrivate(); err != nil {
			return fail(err)
		}
		p.privateAcknowledged = true
		if err := h.verifyPrivate(cleanup); err != nil {
			return fail(err)
		}
		if err := h.persist(pinPartialPrivate); err != nil {
			return fail(err)
		}
		if err := check(); err != nil {
			return fail(err)
		}
	}
	if err := h.persist(pinPartialUnmountAttempt); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	p.unmountAttempted = true
	if err := h.unmount(); err != nil {
		return fail(err)
	}
	p.unmountAcknowledged = true
	if err := h.verifyBare(cleanup); err != nil {
		return fail(err)
	}
	if err := h.persist(pinPartialUnmounted); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	p.ready = true
	return p, nil
}

// nativeUnboundPinRetirement retains metadata until a later actual CID-removal
// ACK. It is not a cold cleanup, namespace/table ACK or receipt settlement.
// Close releases only the retained metadata FD through the original reservation.
type nativeUnboundPinRetirement struct {
	source              knownUnboundPin
	progress            partialPinProgress
	failure             error
	removalAttempted    bool
	completionAttempted bool
}

func (p *nativeUnboundPinRetirement) Close() error {
	if p == nil || p.source.reservation == nil {
		return nil
	}
	return p.source.reservation.Close()
}

func retireKnownUnboundNativePin(ctx context.Context, kernel *kernelInfrastructure, r *namespacePinReservation, d *nativePinDirectoryAttempt, n *nativePinAttempt, owner nativePinStopHooks) (*nativeUnboundPinRetirement, error) {
	source, err := selectKnownUnboundPin(r, d, n)
	if err != nil {
		return nil, err
	}
	if owner.owner == nil || owner.stop == nil || owner.inactive == nil {
		return nil, errors.New("known partial cleanup lacks actual admitted owner callbacks")
	}
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if err := owner.owner(ctx); err != nil {
		return nil, err
	}
	f := r.files
	if err := f.checkUnchanged(); err != nil {
		return nil, err
	}
	var state namespacePinRecord
	if err := json.Unmarshal(f.current, &state); err != nil {
		return nil, err
	}
	if state.Scope.KernelBootID != kernel.bootID || state.Scope.InfrastructureMount != kernel.mountID || state.Scope.Namespace == kernel.networkID || state.DirectoryMountID != source.directoryMount || state.PinMountID != 0 || state.ReadbackSHA256 != "" {
		return nil, errors.New("partial pin bookkeeping differs from its actual no-namespace creation attempt")
	}
	initialPrivate := false
	checkView := func(c context.Context, p partialPinProgress) error {
		if err := verifyPinProcPath(kernel); err != nil {
			return err
		}
		view, err := observeNativePinMounts(c, kernel, f)
		if err != nil {
			return err
		}
		defer func() { _ = view.Close() }()
		if view.namespaceMount != 0 || view.namespace != nil {
			return errors.New("known unbound creation has an unexpected namespace mount")
		}
		if source.directoryMount == 0 || p.unmountAcknowledged {
			if view.directoryBound || view.directoryPrivate || view.directoryMount != f.lock.directories[4].identity.mount {
				return errors.New("partial cleanup lacks the exact bare native directory")
			}
		} else if !view.directoryBound || view.directoryMount != source.directoryMount || (source.privateAcknowledged || initialPrivate || p.privateAcknowledged) && !view.directoryPrivate {
			return errors.New("partial cleanup directory bind/private identity changed")
		}
		return nil
	}
	if err := checkView(ctx, partialPinProgress{}); err != nil {
		return nil, err
	}
	view, err := observeNativePinMounts(ctx, kernel, f)
	if err != nil {
		return nil, err
	}
	needsPrivate := source.directoryMount != 0 && !view.directoryPrivate
	initialPrivate = view.directoryPrivate
	_ = view.Close()
	r.cleanupAttempted = true // A failed Stop/private/unmount cannot be retried.
	result := &nativeUnboundPinRetirement{source: source}
	h := partialPinHooks{
		beforeStop: func(c context.Context) error {
			if err := owner.owner(c); err != nil {
				return err
			}
			return checkView(c, partialPinProgress{})
		},
		stop: owner.stop,
		check: func(c context.Context, p partialPinProgress) error {
			if err := owner.inactive(c); err != nil {
				return err
			}
			return checkView(c, p)
		},
		persist: func(phase namespacePinPhase) error {
			next := state
			next.Phase = phase
			if err := f.writeObservation(next); err != nil {
				return err
			}
			state = next
			return nil
		},
		makePrivate: func() error {
			return unix.Mount("", fmt.Sprintf("/proc/thread-self/fd/%d", f.directory.Fd()), "", unix.MS_PRIVATE, "")
		},
		verifyPrivate: func(c context.Context) error {
			if err := owner.inactive(c); err != nil {
				return err
			}
			return checkView(c, partialPinProgress{privateAcknowledged: true})
		},
		unmount: func() error {
			if f.directory == nil {
				return errors.New("partial directory handle was lost")
			}
			err := f.directory.Close()
			f.directory = nil
			if err != nil {
				return err
			}
			return unix.Unmount(fmt.Sprintf("/proc/thread-self/fd/%d/%s", f.lock.directories[4].file.Fd(), f.record.Generation), unix.UMOUNT_NOFOLLOW)
		},
		verifyBare: func(c context.Context) error {
			current, err := f.lock.read(f.record, f.cid)
			if err != nil {
				return err
			}
			defer func() { _ = current.Close() }()
			if current.identity.device != f.identity.device || current.identity.inode != f.identity.inode || !bytes.Equal(current.origin, f.origin) || !bytes.Equal(current.current, f.current) {
				return errors.New("partial unmount changed its immutable metadata mapping")
			}
			f.directory, f.identity = current.directory, current.identity
			current.directory = nil
			if err := owner.inactive(c); err != nil {
				return err
			}
			return checkView(c, partialPinProgress{unmountAcknowledged: true})
		},
	}
	result.progress, result.failure = runPartialPinRetirement(ctx, source.directoryMount != 0, needsPrivate, h)
	return result, result.failure
}

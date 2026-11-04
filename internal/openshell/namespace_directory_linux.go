package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type pinDirectoryProgress struct {
	bindAttempted, bindAcknowledged       bool
	directoryMount                        uint64
	boundRecorded                         bool
	privateAttempted, privateAcknowledged bool
	ready                                 bool
}

// Private callback seam for protocol regressions. Production constructs it
// below from authenticated peer/kernel/reservation objects, never JSON/env.
type pinDirectoryHooks struct {
	check         func() error
	persist       func(namespacePinPhase, uint64) error
	bind          func() error
	reopenBound   func() (uint64, error)
	makePrivate   func() error
	verifyPrivate func() error
}

func runPinDirectoryCreate(ctx context.Context, h pinDirectoryHooks) (p pinDirectoryProgress, err error) {
	if h.check == nil || h.persist == nil || h.bind == nil || h.reopenBound == nil || h.makePrivate == nil || h.verifyPrivate == nil {
		return p, errors.New("incomplete native pin directory protocol")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return h.check()
	}
	persist := func(phase namespacePinPhase, mount uint64) error {
		if err := h.persist(phase, mount); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		return nil
	}
	if err := check(); err != nil {
		return p, err
	}
	if err := persist(pinDirectoryBindAttempt, 0); err != nil {
		return p, err
	}
	if err := check(); err != nil {
		return p, err
	}
	p.bindAttempted = true
	if err := h.bind(); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.bindAcknowledged = true
	p.directoryMount, err = h.reopenBound()
	if err != nil || p.directoryMount == 0 {
		return p, &NativeEffectsUncertain{Cause: errors.Join(err, errors.New("native directory bind lacks exact readback"))}
	}
	if err := persist(pinDirectoryBound, p.directoryMount); err != nil {
		return p, err
	}
	p.boundRecorded = true
	if err := check(); err != nil {
		return p, err
	}
	if err := persist(pinDirectoryPrivateAttempt, p.directoryMount); err != nil {
		return p, err
	}
	if err := check(); err != nil {
		return p, err
	}
	p.privateAttempted = true
	if err := h.makePrivate(); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	p.privateAcknowledged = true
	if err := h.verifyPrivate(); err != nil {
		return p, &NativeEffectsUncertain{Cause: err}
	}
	if err := persist(pinDirectoryPrivate, p.directoryMount); err != nil {
		return p, err
	}
	p.ready = true
	return p, nil
}

// nativePinDirectoryAttempt retains same-call progress, including known
// partial creation on an ordinary pre-effect error. It is not deserializable
// or created by a cold observer. Uncertain errors must fence further effects;
// even a known partial attempt needs actual Stop/inactivity before cleanup.
// Close releases metadata descriptors only, never mounts or other resources.
type nativePinDirectoryAttempt struct {
	reservation       *namespacePinReservation
	progress          pinDirectoryProgress
	failure           error
	closed            bool
	namespaceConsumed bool
}

func (a *nativePinDirectoryAttempt) Close() error {
	if a == nil || a.closed {
		return nil
	}
	a.closed = true
	return a.reservation.Close()
}

func freshPinDirectoryPeer(ctx context.Context, kernel *kernelInfrastructure, r *namespacePinReservation, peer *activationPeer) error {
	if r == nil || r.files == nil || r.files.directory == nil || peer == nil {
		return errors.New("native directory creation lacks its fresh reservation/activation peer")
	}
	if err := kernel.revalidate(ctx); err != nil {
		return err
	}
	if err := nativePreparedIdentity(peer.prepared); err != nil {
		return err
	}
	if err := peer.revalidate(ctx); err != nil {
		return err
	}
	if recordIdentity(peer.prepared.Record) != recordIdentity(r.files.record) || peer.target.ContainerID != r.files.cid {
		return errors.New("native pin reservation differs from its current actual activation peer")
	}
	var origin namespacePinOrigin
	if err := json.Unmarshal(r.files.origin, &origin); err != nil {
		return err
	}
	expected, err := newNamespacePinScope(peer.prepared.Record, peer.target, peer.start, peer.identity, kernel.mountID, kernel.bootID)
	observed := origin.Record.Scope
	started := observed.EngineStartedAt
	observed.EngineStartedAt = expected.EngineStartedAt
	if err != nil || observed != expected || !started.Equal(expected.EngineStartedAt) {
		return errors.New("native pin origin differs from its fresh actual peer/kernel binding")
	}
	_, err = nativePinDirectoryPath(r.files)
	return err
}

// verifyPinProcPath protects the descriptor aliases used by mount(2). The
// retained proc root alone cannot authenticate an overmounted /proc pathname.
func verifyPinProcPath(kernel *kernelInfrastructure) error {
	if kernel == nil || kernel.proc == nil {
		return errors.New("native pin mount lacks its kernel proc handle")
	}
	fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := verifyKernelProcFD(fd, true); err != nil {
		return err
	}
	var want, got unix.Stat_t
	var wantMount, gotMount unix.Statx_t
	if unix.Fstat(int(kernel.proc.Fd()), &want) != nil || unix.Fstat(fd, &got) != nil || want.Dev != got.Dev || want.Ino != got.Ino || unix.Statx(int(kernel.proc.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &wantMount) != nil || unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &gotMount) != nil || wantMount.Mask&unix.STATX_MNT_ID == 0 || gotMount.Mask&unix.STATX_MNT_ID == 0 || wantMount.Mnt_id == 0 || wantMount.Mnt_id != gotMount.Mnt_id {
		return errors.New("native pin mount aliases do not use the pinned kernel proc root")
	}
	return nil
}

// createNativePinDirectory performs only the owned empty directory bind and
// private conversion, in the verified persistent VM infrastructure namespace.
// No namespace/policy/authorization is installed yet. In particular, private
// conversion is acknowledged BEFORE any future namespace bind, so an earlier
// propagated directory copy cannot receive the namespace pin mount event.
// This private entry is not yet wired into ActivateNative/lifecycle.
func createNativePinDirectory(ctx context.Context, kernel *kernelInfrastructure, r *namespacePinReservation, peer *activationPeer) (*nativePinDirectoryAttempt, error) {
	if r != nil && r.cleanupAttempted {
		return nil, &NativeEffectsUncertain{Cause: errors.New("native pin creation was consumed by cleanup; no creation retry")}
	}
	if r == nil || r.consumed || r.files == nil || r.files.directory == nil || r.files.identity != r.original {
		return nil, errors.New("native pin reservation is absent, changed or already consumed")
	}
	if err := freshPinDirectoryPeer(ctx, kernel, r, peer); err != nil {
		return nil, err
	}
	var state namespacePinRecord
	if err := json.Unmarshal(r.files.current, &state); err != nil {
		return nil, err
	}
	if state.Phase != pinPlanned || state.DirectoryMountID != 0 || state.PinMountID != 0 {
		return nil, errors.New("native pin reservation is not the fresh unattempted record")
	}
	view, err := observeNativePinMounts(ctx, kernel, r.files)
	if err != nil {
		return nil, err
	}
	defer func() { _ = view.Close() }()
	if view.directoryBound || view.namespaceMount != 0 || view.directoryMount != r.original.mount {
		return nil, errors.New("fresh native pin directory already has a mount")
	}
	r.consumed = true // A stored planned phase or a failed attempt cannot reset it.
	attempt := &nativePinDirectoryAttempt{reservation: r}
	check := func() error {
		if err := freshPinDirectoryPeer(ctx, kernel, r, peer); err != nil {
			return err
		}
		if err := verifyPinProcPath(kernel); err != nil {
			return err
		}
		observed, err := observeNativePinMounts(ctx, kernel, r.files)
		if err != nil {
			return err
		}
		defer func() { _ = observed.Close() }()
		if observed.namespaceMount != 0 {
			return errors.New("unexpected namespace mount before directory setup")
		}
		if state.DirectoryMountID == 0 {
			if observed.directoryBound || observed.directoryMount != r.original.mount {
				return errors.New("fresh directory target changed before bind")
			}
		} else if !observed.directoryBound || observed.directoryMount != state.DirectoryMountID {
			return errors.New("directory target changed before private conversion")
		}
		return nil
	}
	hooks := pinDirectoryHooks{
		check: check,
		persist: func(phase namespacePinPhase, mount uint64) error {
			next := state
			next.Phase, next.DirectoryMountID = phase, mount
			if err := r.files.writeObservation(next); err != nil {
				return err
			}
			state = next
			return nil
		},
		bind: func() error {
			alias := fmt.Sprintf("/proc/thread-self/fd/%d", r.files.directory.Fd())
			return unix.Mount(alias, alias, "", unix.MS_BIND, "")
		},
		reopenBound: func() (uint64, error) {
			// The pre-bind FD still names the underlying mount. Reopen via
			// the protected parent to obtain the newly attached mount, while
			// retaining the actual bind ACK independently of this observation.
			previous := r.files
			next, err := previous.lock.read(previous.record, previous.cid)
			if err != nil {
				return 0, err
			}
			ok := false
			defer func() {
				if !ok {
					_ = next.Close()
				}
			}()
			if next.identity.device != r.original.device || next.identity.inode != r.original.inode || next.identity.mount == r.original.mount || !bytes.Equal(next.origin, previous.origin) || !bytes.Equal(next.current, previous.current) {
				return 0, errors.New("new directory bind changed its protected origin/inode/state")
			}
			observed, err := observeNativePinMounts(ctx, kernel, next)
			if err != nil {
				return 0, err
			}
			defer func() { _ = observed.Close() }()
			if !observed.directoryBound || observed.namespaceMount != 0 || observed.directoryMount != next.identity.mount {
				return 0, errors.New("new directory bind lacks its exact owned mount readback")
			}
			r.files, ok = next, true
			if err := previous.Close(); err != nil {
				return 0, err
			}
			return observed.directoryMount, nil
		},
		makePrivate: func() error {
			// No recursive operation over unowned children; there are none.
			alias := fmt.Sprintf("/proc/thread-self/fd/%d", r.files.directory.Fd())
			return unix.Mount("", alias, "", unix.MS_PRIVATE, "")
		},
		verifyPrivate: func() error {
			observed, err := observeNativePinMounts(ctx, kernel, r.files)
			if err != nil {
				return err
			}
			defer func() { _ = observed.Close() }()
			if !observed.directoryBound || !observed.directoryPrivate || observed.namespaceMount != 0 || observed.directoryMount != state.DirectoryMountID {
				return errors.New("directory private conversion lacks exact isolated readback")
			}
			return nil
		},
	}
	attempt.progress, attempt.failure = runPinDirectoryCreate(ctx, hooks)
	return attempt, attempt.failure
}

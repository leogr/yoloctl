package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// These callbacks belong to the admitted native adapter, never to decoded
// metadata or workload input. stop must preserve the actual engine reply;
// inactive must independently check the same CID/record and native storage.
// Both checks are read-only. In particular inactive must not unlink sockets.
type nativePinStopHooks struct {
	owner    func(context.Context) error
	stop     func(context.Context) error
	inactive func(context.Context) error
}

type pinRetirementProgress struct {
	stopAttempted, stopAcknowledged                         bool
	policyAttempted, policyAcknowledged                     bool
	pinUnmountAttempted, pinUnmountAcknowledged             bool
	directoryUnmountAttempted, directoryUnmountAcknowledged bool
	completed                                               bool
}

type pinRetirementHooks struct {
	beforeStop          func(context.Context) error
	stop                func(context.Context) error
	check               func(context.Context, pinRetirementProgress) error
	persist             func(namespacePinPhase) error
	retire              func(context.Context) error
	verifyTable         func() error
	unmountPin          func() error
	verifyPinGone       func(context.Context) error
	unmountDirectory    func() error
	verifyDirectoryGone func(context.Context) error
	complete            func(pinRetirementProgress) error
}

// Every invocation requires a fresh positive Stop ACK, including an already
// stopped container. A validated prior subresource completion skips nft and
// unmount effects, but never skips Stop/inactivity or settles an old receipt.
// No failed callback is retried or followed by an observation-as-ACK fallback.
func runPinRetirement(ctx context.Context, alreadyRetired bool, h pinRetirementHooks) (p pinRetirementProgress, err error) {
	if h.beforeStop == nil || h.stop == nil || h.check == nil || h.persist == nil || h.retire == nil || h.verifyTable == nil || h.unmountPin == nil || h.verifyPinGone == nil || h.unmountDirectory == nil || h.verifyDirectoryGone == nil || h.complete == nil {
		return p, errors.New("incomplete native namespace retirement protocol")
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
	// Stop succeeded: finish this one bounded owned cleanup even if the
	// request's transport disappears. This is not a new operation/replay.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return continueAcknowledgedPinRetirement(cleanup, alreadyRetired, h, p)
}

// Shared effect suffix. Its only production callers obtain Stop authority from
// their actual original engine reply or the private container-wide ACK token.
// It never calls stop, mints an ACK from observations, or lifts the caller bound.
func continueAcknowledgedPinRetirement(cleanup context.Context, alreadyRetired bool, h pinRetirementHooks, p pinRetirementProgress) (pinRetirementProgress, error) {
	if p != (pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}) || h.check == nil || h.persist == nil || h.retire == nil || h.verifyTable == nil || h.unmountPin == nil || h.verifyPinGone == nil || h.unmountDirectory == nil || h.verifyDirectoryGone == nil || h.complete == nil {
		return p, errors.New("namespace effect suffix lacks this call's original Stop/initial stage or complete callbacks")
	}
	check := func() error {
		if err := cleanup.Err(); err != nil {
			return err
		}
		return h.check(cleanup, p)
	}
	fail := func(cause error) (pinRetirementProgress, error) { return p, &NativeEffectsUncertain{Cause: cause} }
	if err := check(); err != nil {
		return fail(err)
	}
	if alreadyRetired {
		p.completed = true
		return p, nil
	}
	if err := h.persist(pinRetiring); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	p.policyAttempted = true
	if err := h.retire(cleanup); err != nil {
		return fail(err)
	}
	p.policyAcknowledged = true
	if err := h.verifyTable(); err != nil {
		return fail(err)
	}
	if err := h.persist(pinTableAbsent); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	if err := h.persist(pinUnmountAttempt); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	p.pinUnmountAttempted = true
	if err := h.unmountPin(); err != nil {
		return fail(err)
	}
	p.pinUnmountAcknowledged = true
	if err := h.verifyPinGone(cleanup); err != nil {
		return fail(err)
	}
	if err := h.persist(pinUnmounted); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	if err := h.persist(pinDirectoryUnmountAttempt); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	p.directoryUnmountAttempted = true
	if err := h.unmountDirectory(); err != nil {
		return fail(err)
	}
	p.directoryUnmountAcknowledged = true
	if err := h.verifyDirectoryGone(cleanup); err != nil {
		return fail(err)
	}
	if err := h.persist(pinDirectoryUnmounted); err != nil {
		return fail(err)
	}
	if err := check(); err != nil {
		return fail(err)
	}
	if err := h.complete(p); err != nil {
		return fail(err)
	}
	p.completed = true
	return p, nil
}

// This independently published root-owned marker can only be produced by the
// actual acknowledged retirement path below. A terminal state.json alone is
// not completion evidence. It covers kernel subresources only, not the old
// operation/receipt, helper credentials, forwarding services or socket cleanup.
type namespacePinRetired struct {
	Schema       int    `json:"schema"`
	OriginSHA256 string `json:"origin_sha256"`
	StateSHA256  string `json:"state_sha256"`
	RecordSHA256 string `json:"record_sha256"`
	KernelBootID string `json:"kernel_boot_id"`
}

func pinRetirementBinding(f *namespacePinFiles) (namespacePinRetired, error) {
	if f == nil || f.directory == nil {
		return namespacePinRetired{}, errors.New("closed namespace retirement metadata")
	}
	return namespaceRetiredBindingFromMetadata(f.origin, f.current, f.record, f.cid)
}

func (f *namespacePinFiles) readRetired() (bool, error) {
	if f == nil || f.directory == nil {
		return false, errors.New("closed namespace retirement metadata")
	}
	if err := f.checkUnchanged(); err != nil {
		return false, err
	}
	return f.readRetiredValue()
}

// Pure fixed-file decoding. An admitted different-boot reader performs its
// actual current bare mapping checks around this call; same-boot callers keep
// the original strict checkUnchanged path above.
func (f *namespacePinFiles) readRetiredValue() (bool, error) {
	if f == nil || f.directory == nil || f.lock == nil {
		return false, errors.New("closed namespace retirement metadata")
	}
	raw, _, err := readPinFileAt(int(f.directory.Fd()), "retired.json", f.lock.uid, f.lock.gid, 4096)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := validateRetiredPinMarker(raw, f); err != nil {
		return false, err
	}
	return true, nil
}

func validateRetiredPinMarker(raw []byte, f *namespacePinFiles) error {
	expected, err := pinRetirementBinding(f)
	if err != nil {
		return err
	}
	_, err = decodeNamespaceRetiredMetadata(raw, expected)
	return err
}

func (f *namespacePinFiles) checkUnchanged() error {
	if f == nil || f.directory == nil || f.lock == nil {
		return errors.New("closed namespace metadata")
	}
	current, err := f.lock.read(f.record, f.cid)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	if current.identity != f.identity || !bytes.Equal(current.origin, f.origin) || !bytes.Equal(current.current, f.current) {
		return errors.New("namespace metadata changed during retirement")
	}
	return nil
}

func (f *namespacePinFiles) publishRetired(p pinRetirementProgress) error {
	if !p.stopAcknowledged || !p.policyAcknowledged || !p.pinUnmountAcknowledged || !p.directoryUnmountAcknowledged || p.completed {
		return errors.New("namespace retirement marker lacks this call's complete positive effect acknowledgements")
	}
	if err := f.checkUnchanged(); err != nil {
		return err
	}
	binding, err := pinRetirementBinding(f)
	if err != nil || binding.Schema != 1 {
		return errors.Join(err, errors.New("full namespace retirement cannot certify a known-unbound attempt"))
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	identity, err := writePinFileAtIdentity(int(f.directory.Fd()), "retired.json", raw, false, f.lock.uid, f.lock.gid)
	if err != nil {
		return err
	}
	f.retiredPublication = &namespacePinPublication{identity: identity, raw: bytes.Clone(raw)}
	return nil
}

func decodeNativeEgressRetirement(raw []byte, plan nativeEgressPlan) error {
	if plan.validate(plan.Namespace, plan.Seed) != nil || len(raw) == 0 || len(raw) > 4096 {
		return errors.New("invalid namespace retirement result binding/bound")
	}
	if err := checkJSONKeys(raw); err != nil {
		return err
	}
	var result nativeEgressRetirement
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return err
	}
	if result.Schema != BootstrapSchema || result.Namespace != plan.Namespace || result.RulesSHA256 != plan.RulesSHA256 || !result.TableAbsent {
		return errors.New("namespace retirement child lacks the selected table-absence proof")
	}
	return nil
}

// The observer always reports the opened directory's nonzero mount ID.
// A bare directory uses the native base mount; absence of an extra directory
// bind is expressed by directoryBound=false, never by mount ID zero.
func validatePinRetirementStage(view nativePinMountView, state namespacePinRecord, nativeMount uint64, completed bool, p pinRetirementProgress) error {
	if completed && knownUnboundTerminalObservation(state) {
		if nativeMount == 0 || state.DirectoryMountID != 0 && state.DirectoryMountID == nativeMount || view.directoryBound || view.directoryPrivate || view.directoryMount != nativeMount || view.namespaceMount != 0 || view.namespace != nil {
			return errors.New("completed known-unbound retirement lacks the exact bare native base")
		}
		return nil
	}
	if nativeMount == 0 || state.DirectoryMountID == 0 || state.PinMountID == 0 || state.DirectoryMountID == state.PinMountID || state.DirectoryMountID == nativeMount || state.PinMountID == nativeMount {
		return errors.New("namespace retirement lacks distinct native/directory/pin mount identities")
	}
	if completed || p.directoryUnmountAcknowledged {
		if view.directoryBound || view.directoryPrivate || view.directoryMount != nativeMount || view.namespaceMount != 0 || view.namespace != nil {
			return errors.New("namespace retirement requires the exact bare native base after directory unmount")
		}
		return nil
	}
	wantPin := state.PinMountID
	if p.pinUnmountAcknowledged {
		wantPin = 0
	}
	if !view.directoryBound || !view.directoryPrivate || view.directoryMount != state.DirectoryMountID || view.namespaceMount != wantPin || wantPin == 0 && view.namespace != nil {
		return errors.New("namespace retirement mount mapping differs from the exact acknowledged stage")
	}
	return nil
}

// stopNativeNamespacePin is the shared same-boot retirement adapter. Its admitted caller
// must supply current native storage, release/record/CID and lifecycle authority
// in the fixed callbacks. Cold phases grant no Stop or creation authority.
// This entry handles a present same-boot pin, or independently completed bare
// bookkeeping. Earlier partial creation and cross-boot handling are separate.
func stopNativeNamespacePin(ctx context.Context, kernel *kernelInfrastructure, files *namespacePinFiles, owner nativePinStopHooks) (pinRetirementProgress, error) {
	if owner.stop == nil {
		return pinRetirementProgress{}, errors.New("native namespace Stop lacks its actual engine Stop callback")
	}
	prepared, err := prepareNativePinRetirement(ctx, kernel, files, owner)
	if err != nil {
		return pinRetirementProgress{}, err
	}
	defer func() { _ = prepared.Close() }()
	// The standalone Stop path continues to issue its own fresh actual Stop.
	return runPinRetirement(ctx, prepared.completed, prepared.hooks)
}

// Prepares actual same-boot native effects while retaining the original NSFS
// descriptor and borrowed metadata handle. No Stop or mutation occurs here.
func prepareNativePinRetirement(ctx context.Context, kernel *kernelInfrastructure, files *namespacePinFiles, owner nativePinStopHooks) (_ *nativePreparedPinRetirement, err error) {
	if kernel == nil || files == nil || owner.owner == nil || owner.inactive == nil {
		return nil, errors.New("native namespace Stop lacks its admitted owner callbacks")
	}
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if err := files.checkUnchanged(); err != nil {
		return nil, err
	}
	var state namespacePinRecord
	if err := json.Unmarshal(files.current, &state); err != nil {
		return nil, err
	}
	completed, err := files.readRetired()
	if err != nil {
		return nil, err
	}
	if state.Scope.KernelBootID != kernel.bootID || state.Scope.InfrastructureMount != kernel.mountID || state.Scope.Namespace == kernel.networkID {
		return nil, errors.New("namespace retirement requires the actual same-boot VM binding")
	}
	if !completed && (state.DirectoryMountID == 0 || state.PinMountID == 0 || state.DirectoryMountID == state.PinMountID || state.Phase != pinPresent && state.Phase != pinPolicyApplyAttempt && state.Phase != pinPolicyReadback) {
		return nil, errors.New("namespace retirement refuses incomplete or previously interrupted cleanup without independent completion evidence")
	}
	var namespace *os.File
	closeNamespace := func() error {
		if namespace == nil {
			return nil
		}
		err := namespace.Close()
		namespace = nil
		return err
	}
	defer func() {
		if err != nil {
			_ = closeNamespace()
		}
	}()
	checkView := func(checkCtx context.Context, p pinRetirementProgress) error {
		if err := verifyPinProcPath(kernel); err != nil {
			return err
		}
		view, err := observeNativePinMounts(checkCtx, kernel, files)
		if err != nil {
			return err
		}
		defer func() { _ = view.Close() }()
		if err := validatePinRetirementStage(*view, state, files.lock.directories[4].identity.mount, completed, p); err != nil {
			return err
		}
		if view.namespaceMount != 0 && namespace == nil {
			if view.namespace == nil {
				return errors.New("namespace retirement lacks its actual pinned descriptor")
			}
			namespace, view.namespace = view.namespace, nil
		}
		return nil
	}
	var result []byte
	hooks := pinRetirementHooks{
		beforeStop: func(c context.Context) error {
			if err := owner.owner(c); err != nil {
				return err
			}
			return checkView(c, pinRetirementProgress{})
		},
		stop: owner.stop,
		check: func(c context.Context, p pinRetirementProgress) error {
			if err := owner.inactive(c); err != nil {
				return err
			}
			return checkView(c, p)
		},
		persist: func(phase namespacePinPhase) error {
			next := state
			next.Phase = phase
			if err := files.writeObservation(next); err != nil {
				return err
			}
			state = next
			return nil
		},
		retire: func(c context.Context) error {
			if namespace == nil {
				return errors.New("retirement child lacks the checked owned namespace")
			}
			raw, err := json.Marshal(state.Plan)
			if err != nil {
				return err
			}
			child, cancel := context.WithTimeout(c, 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(child, "/proc/self/exe", NetworkWorkerCommand, "retire")
			cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
			cmd.ExtraFiles = []*os.File{namespace}
			cmd.Stdin = bytes.NewReader(raw)
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			stdout, stderr := boundedCommandOutput(4096), boundedCommandOutput(64<<10)
			cmd.Stdout, cmd.Stderr = stdout, stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("fixed-plan retirement child failed: %w: %s", err, stderr.String())
			}
			result = bytes.Clone(stdout.Bytes())
			return nil
		},
		verifyTable: func() error { return decodeNativeEgressRetirement(result, state.Plan) },
		unmountPin: func() error {
			// An open file on the NSFS mount can itself keep a normal unmount
			// busy. Drop our last child/observation FD after exact table absence
			// and the final mount check, before the one non-lazy unmount.
			if namespace == nil {
				return errors.New("retiring namespace descriptor was lost")
			}
			err := namespace.Close()
			namespace = nil
			if err != nil {
				return err
			}
			return unix.Unmount(fmt.Sprintf("/proc/thread-self/fd/%d/namespace", files.directory.Fd()), unix.UMOUNT_NOFOLLOW)
		},
		verifyPinGone: func(c context.Context) error {
			if err := owner.inactive(c); err != nil {
				return err
			}
			return checkView(c, pinRetirementProgress{pinUnmountAcknowledged: true})
		},
		unmountDirectory: func() error {
			// Use the native parent for the syscall, and release our own
			// directory mount reference first. A failed close/unmount ends the
			// attempt; there is no reopen-as-success or lazy-unmount fallback.
			if files.directory == nil {
				return errors.New("retiring directory descriptor was lost")
			}
			err := files.directory.Close()
			files.directory = nil
			if err != nil {
				return err
			}
			return unix.Unmount(fmt.Sprintf("/proc/thread-self/fd/%d/%s", files.lock.directories[4].file.Fd(), files.record.Generation), unix.UMOUNT_NOFOLLOW)
		},
		verifyDirectoryGone: func(c context.Context) error {
			// Reopen through the protected parent after the positive unmount.
			// No descriptor may retain the just-unmounted directory mount.
			current, err := files.lock.read(files.record, files.cid)
			if err != nil {
				return err
			}
			defer func() { _ = current.Close() }()
			if current.identity.device != files.identity.device || current.identity.inode != files.identity.inode || !bytes.Equal(current.origin, files.origin) || !bytes.Equal(current.current, files.current) {
				return errors.New("unmounted namespace directory changed its immutable mapping")
			}
			files.directory, files.identity = current.directory, current.identity
			current.directory = nil
			if err := owner.inactive(c); err != nil {
				return err
			}
			return checkView(c, pinRetirementProgress{pinUnmountAcknowledged: true, directoryUnmountAcknowledged: true})
		},
		complete: files.publishRetired,
	}
	// Qualify actual native owner and mount/NSFS mapping now, before the
	// enclosing container-wide protocol can issue its one real Stop.
	if err := hooks.beforeStop(ctx); err != nil {
		return nil, err
	}
	return &nativePreparedPinRetirement{record: cloneRuntimeRecord(files.record), cid: files.cid, completed: completed, hooks: hooks, release: closeNamespace, remainingNamespace: func() *os.File { return namespace }, ready: true}, nil
}

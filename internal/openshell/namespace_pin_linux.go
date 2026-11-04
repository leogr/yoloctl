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

type pinNamespaceProgress struct {
	bindAttempted, bindAcknowledged     bool
	mount                               uint64
	pinRecorded                         bool
	policyAttempted, policyAcknowledged bool
	readbackRecorded, ready             bool
}

type pinNamespaceHooks struct {
	check        func() error
	persist      func(namespacePinPhase, uint64, string) error
	bind         func() error
	readPin      func() (uint64, error)
	apply        func() error
	verifyPolicy func() (activationProof, error)
}

func runPinNamespaceCreate(ctx context.Context, rules string, h pinNamespaceHooks) (p pinNamespaceProgress, proof activationProof, err error) {
	if !lowerHex(rules, 32) || h.check == nil || h.persist == nil || h.bind == nil || h.readPin == nil || h.apply == nil || h.verifyPolicy == nil {
		return p, proof, errors.New("incomplete native namespace creation protocol")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return h.check()
	}
	persist := func(phase namespacePinPhase, mount uint64, hash string) error {
		if err := h.persist(phase, mount, hash); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		return nil
	}
	if err := check(); err != nil {
		return p, proof, err
	}
	if err := persist(pinNamespaceBindAttempt, 0, ""); err != nil {
		return p, proof, err
	}
	if err := check(); err != nil {
		return p, proof, err
	}
	p.bindAttempted = true
	if err := h.bind(); err != nil {
		return p, proof, &NativeEffectsUncertain{Cause: err}
	}
	p.bindAcknowledged = true
	p.mount, err = h.readPin()
	if err != nil || p.mount == 0 {
		return p, proof, &NativeEffectsUncertain{Cause: errors.Join(err, errors.New("namespace bind lacks its exact mount readback"))}
	}
	if err := persist(pinPresent, p.mount, ""); err != nil {
		return p, proof, err
	}
	p.pinRecorded = true
	if err := check(); err != nil {
		return p, proof, err
	}
	if err := persist(pinPolicyApplyAttempt, p.mount, ""); err != nil {
		return p, proof, err
	}
	if err := check(); err != nil {
		return p, proof, err
	}
	p.policyAttempted = true
	// Every failed/lost effect-child result is uncertain. Its stderr/exit
	// cannot authenticate a pre-effect failure or permit replay/cleanup.
	if err := h.apply(); err != nil {
		return p, proof, &NativeEffectsUncertain{Cause: err}
	}
	p.policyAcknowledged = true
	proof, err = h.verifyPolicy()
	if err != nil || proof.RulesSHA256 != rules {
		return p, activationProof{}, &NativeEffectsUncertain{Cause: errors.Join(err, errors.New("policy child lacks exact selected proof"))}
	}
	if err := check(); err != nil {
		return p, activationProof{}, err
	}
	if err := persist(pinPolicyReadback, p.mount, rules); err != nil {
		return p, activationProof{}, err
	}
	p.readbackRecorded = true
	if err := check(); err != nil {
		return p, activationProof{}, err
	}
	p.ready = true
	return p, proof, nil
}

type nativePinAttempt struct {
	directory *nativePinDirectoryAttempt
	progress  pinNamespaceProgress
	proof     activationProof
	namespace *os.File
	failure   error
	closed    bool
}

func (a *nativePinAttempt) Close() error {
	if a == nil || a.closed {
		return nil
	}
	a.closed = true
	var err error
	if a.namespace != nil {
		err = a.namespace.Close()
		a.namespace = nil
	}
	return errors.Join(err, a.directory.Close())
}

// validateNativeEgressSeed refuses VM input drift; it never rewrites the
// already durable plan. Retirement deliberately verifies the old exact plan
// and must not use this fresh-activation check to replace its inputs.
func validateNativeEgressSeed(plan nativeEgressPlan, record RuntimeRecord, current infrastructureInputs) error {
	if !record.Launch.UnconfinedNetwork || plan.validate(plan.Namespace, plan.Seed) != nil || plan.Inputs.OwnerSHA256 != egressOwner(record) {
		return errors.New("VM input check lacks the selected unconfined plan/record")
	}
	seed, err := canonicalNativeEgressInputs(EgressInputs{egressOwner(record), current.resolvers, current.prefixes})
	if err != nil {
		return err
	}
	if !sameNativeEgressInputs(seed, plan.Seed) {
		return errors.New("VM network inputs changed after durable namespace policy planning")
	}
	return nil
}

func decodePlannedNetworkResult(raw []byte, plan nativeEgressPlan, record RuntimeRecord) (activationProof, error) {
	if !record.Launch.UnconfinedNetwork || plan.validate(plan.Namespace, plan.Seed) != nil || plan.Inputs.OwnerSHA256 != egressOwner(record) || len(raw) == 0 || len(raw) > 3<<20 {
		return activationProof{}, errors.New("invalid selected native policy result binding")
	}
	if err := checkJSONKeys(raw); err != nil {
		return activationProof{}, err
	}
	var result networkResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return activationProof{}, err
	}
	if result.Schema != BootstrapSchema || result.Namespace != plan.Namespace || !sameNativeEgressInputs(result.Inputs, plan.Inputs) {
		return activationProof{}, errors.New("policy child changed its namespace/complete input binding")
	}
	inputs := copyNativeEgressInputs(plan.Inputs)
	proof, err := checkedActivationProof(record, plan.Namespace, &inputs, result.Readback)
	if err != nil || proof.RulesSHA256 != plan.RulesSHA256 {
		return activationProof{}, errors.Join(err, errors.New("policy child changed its complete canonical table"))
	}
	return proof, nil
}

// createNativeNamespacePin installs a persistent NSFS pin and precisely the
// protected plan, after an actually acknowledged private directory attempt.
// It returns partial in-call state on error; Close is non-destructive. Nothing
// here authorizes the helper or completes the enclosing receipt. The entry is
// still private/unwired until owned Stop/retirement and lifecycle are complete.
func createNativeNamespacePin(ctx context.Context, kernel *kernelInfrastructure, directory *nativePinDirectoryAttempt, peer *activationPeer, received *os.File) (*nativePinAttempt, error) {
	if directory != nil && directory.reservation != nil && directory.reservation.cleanupAttempted {
		return nil, &NativeEffectsUncertain{Cause: errors.New("native namespace creation was consumed by cleanup; no creation retry")}
	}
	if directory == nil || directory.closed || directory.namespaceConsumed || directory.failure != nil || !directory.progress.ready || !directory.progress.bindAcknowledged || !directory.progress.privateAcknowledged || !directory.progress.boundRecorded || directory.progress.directoryMount == 0 || directory.reservation == nil || !directory.reservation.consumed || received == nil {
		return nil, errors.New("namespace pin lacks its successful unused private-directory attempt")
	}
	r := directory.reservation
	if err := freshPinDirectoryPeer(ctx, kernel, r, peer); err != nil {
		return nil, err
	}
	identity, err := kernelNamespaceIdentity(int(received.Fd()), unix.CLONE_NEWNET)
	if err != nil || identity != peer.identity || identity == kernel.networkID {
		return nil, errors.New("namespace pin descriptor differs from its actual peer/VM binding")
	}
	var origin namespacePinOrigin
	if err := json.Unmarshal(r.files.origin, &origin); err != nil {
		return nil, err
	}
	var state namespacePinRecord
	if err := json.Unmarshal(r.files.current, &state); err != nil {
		return nil, err
	}
	if state.Phase != pinDirectoryPrivate || state.DirectoryMountID != directory.progress.directoryMount || state.PinMountID != 0 {
		return nil, errors.New("namespace pin directory has changed or was already attempted")
	}
	directory.namespaceConsumed = true
	attempt := &nativePinAttempt{directory: directory}
	plan := origin.Record.Plan
	var result []byte
	hooks := pinNamespaceHooks{
		check: func() error {
			if err := freshPinDirectoryPeer(ctx, kernel, r, peer); err != nil {
				return err
			}
			if err := verifyPinProcPath(kernel); err != nil {
				return err
			}
			selected, err := nativeInfrastructureInputs()
			if err != nil {
				return err
			}
			if err := validateNativeEgressSeed(plan, r.files.record, selected); err != nil {
				return err
			}
			view, err := observeNativePinMounts(ctx, kernel, r.files)
			if err != nil {
				return err
			}
			defer func() { _ = view.Close() }()
			if !view.directoryPrivate || view.directoryMount != directory.progress.directoryMount || view.namespaceMount != state.PinMountID {
				return errors.New("namespace pin mapping changed before the next effect")
			}
			actual, err := kernelNamespaceIdentity(int(received.Fd()), unix.CLONE_NEWNET)
			if err != nil || actual != identity {
				return errors.New("activation namespace descriptor changed")
			}
			return nil
		},
		persist: func(phase namespacePinPhase, mount uint64, hash string) error {
			next := state
			next.Phase, next.PinMountID, next.ReadbackSHA256 = phase, mount, hash
			if err := r.files.writeObservation(next); err != nil {
				return err
			}
			state = next
			return nil
		},
		bind: func() error {
			source := fmt.Sprintf("/proc/thread-self/fd/%d", received.Fd())
			target := fmt.Sprintf("/proc/thread-self/fd/%d/namespace", r.files.directory.Fd())
			return unix.Mount(source, target, "", unix.MS_BIND, "")
		},
		readPin: func() (uint64, error) {
			view, err := observeNativePinMounts(ctx, kernel, r.files)
			if err != nil {
				return 0, err
			}
			defer func() { _ = view.Close() }()
			if !view.directoryPrivate || view.directoryMount != directory.progress.directoryMount || view.namespaceMount == 0 || view.namespace == nil {
				return 0, errors.New("namespace bind lacks exact private pin readback")
			}
			attempt.namespace = view.namespace
			view.namespace = nil
			return view.namespaceMount, nil
		},
		apply: func() error {
			if attempt.namespace == nil {
				return errors.New("policy child lacks acknowledged owned pin descriptor")
			}
			raw, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			child, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(child, "/proc/self/exe", NetworkWorkerCommand, "apply")
			cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
			cmd.ExtraFiles = []*os.File{attempt.namespace}
			cmd.Stdin = bytes.NewReader(raw)
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			output, stderr := boundedCommandOutput(3<<20), boundedCommandOutput(64<<10)
			cmd.Stdout, cmd.Stderr = output, stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("fixed-plan native policy child failed: %w: %s", err, stderr.String())
			}
			result = bytes.Clone(output.Bytes())
			return nil
		},
		verifyPolicy: func() (activationProof, error) { return decodePlannedNetworkResult(result, plan, r.files.record) },
	}
	attempt.progress, attempt.proof, attempt.failure = runPinNamespaceCreate(ctx, plan.RulesSHA256, hooks)
	return attempt, attempt.failure
}

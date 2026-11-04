package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/leogr/yoloctl/internal/layout"
)

// NativePreparedGeneration can only be returned by this SDK's acknowledged
// fresh native preparation. It is not decoded from disk or a request. Its
// metadata copy alone is never the no-initialization cleanup proof.
type NativePreparedGeneration struct {
	prepared                  PreparedRuntime
	cid                       string
	original                  bool
	activationAttempted       bool
	noInitializationAttempted bool
	activation                *NativeActivation
	failure                   error
	cleanupAttempted, closed  bool
}

func (*NativePreparedGeneration) String() string { return "OpenShell original preparation [redacted]" }
func (*NativePreparedGeneration) GoString() string {
	return "OpenShell original preparation [redacted]"
}

func (g *NativeGeneration) PrepareNextOwned(ctx context.Context, policy []byte, verifyInactive func(context.Context) (GenerationTarget, error)) (*NativePreparedGeneration, error) {
	prepared, err := g.PrepareNext(ctx, policy, verifyInactive)
	if err != nil {
		return nil, err
	}
	return newNativePreparedGeneration(prepared, g.cid), nil
}
func newNativePreparedGeneration(prepared PreparedRuntime, cid string) *NativePreparedGeneration {
	prepared.Record = cloneRuntimeRecord(prepared.Record)
	return &NativePreparedGeneration{prepared: prepared, cid: cid, original: true}
}
func (t *NativePreparedGeneration) Prepared() PreparedRuntime {
	if t == nil {
		return PreparedRuntime{}
	}
	result := t.prepared
	result.Record = cloneRuntimeRecord(result.Record)
	return result
}
func (t *NativePreparedGeneration) Matches(prepared PreparedRuntime, cid string) bool {
	if t == nil || !t.original || cid != t.cid || prepared.ContainerRoot != t.prepared.ContainerRoot || prepared.GenerationRoot != t.prepared.GenerationRoot {
		return false
	}
	original, err := json.Marshal(t.prepared.Record)
	if err != nil {
		return false
	}
	current, err := json.Marshal(prepared.Record)
	return err == nil && bytes.Equal(original, current)
}
func (t *NativePreparedGeneration) ActivationMatches(attempted bool, activation *NativeActivation) bool {
	return t != nil && t.original && attempted == t.activationAttempted && activation == t.activation
}
func (t *NativePreparedGeneration) Close() error {
	if t == nil || t.closed {
		return nil
	}
	t.closed = true
	if t.activation != nil {
		return t.activation.Close()
	}
	return nil
}
func (t *NativePreparedGeneration) usable() error {
	if t == nil || !t.original || !lowerHex(t.cid, 32) || t.closed || t.cleanupAttempted {
		return &NativeEffectsUncertain{Cause: errors.New("original native preparation is missing/closed/consumed; no adoption/retry")}
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(t.failure, &uncertain) {
		return t.failure
	}
	return nil
}

// Activate retains the exact return handle and binds every supplied current
// target to the CID captured by the successful native preparation call.
func (t *NativePreparedGeneration) Activate(ctx context.Context, inspect func(context.Context) (ActivationTarget, error)) (*NativeActivation, error) {
	if err := t.usable(); err != nil {
		return nil, err
	}
	if t.activationAttempted || inspect == nil {
		return nil, &NativeEffectsUncertain{Cause: errors.New("original prepared activation was already invoked or lacks its current verifier")}
	}
	t.activationAttempted = true
	t.activation, t.failure = ActivateNativeOwned(ctx, t.prepared, func(check context.Context) (ActivationTarget, error) {
		target, err := inspect(check)
		if err != nil {
			return ActivationTarget{}, err
		}
		if target.ContainerID != t.cid {
			return ActivationTarget{}, errors.New("activation target differs from original prepared CID")
		}
		return target, nil
	})
	if t.activation == nil {
		t.failure = &NativeEffectsUncertain{Cause: errors.Join(t.failure, errors.New("original prepared activation lost its handle"))}
	}
	return t.activation, t.failure
}

// StopGeneration is used only by the original admitted caller AFTER a known
// no-Start prefix or successful Start ACK. That caller must withhold it after
// an unknown engine Start. Every allowed no-namespace case still obtains a
// FRESH Stop ACK, current inactive proof and durable independent completion.
func (t *NativePreparedGeneration) StopGeneration(ctx context.Context, owner NativeNamespaceStop) error {
	if err := t.usable(); err != nil {
		return err
	}
	if owner.VerifyOwned == nil || owner.Stop == nil || owner.VerifyInactive == nil {
		return errors.New("original preparation cleanup lacks admitted current owner callbacks")
	}
	if err := nativePreparedIdentity(t.prepared); err != nil {
		return err
	}
	if err := owner.VerifyOwned(ctx); err != nil {
		return err
	}
	t.cleanupAttempted = true
	if t.activationAttempted {
		if t.activation == nil {
			return &NativeEffectsUncertain{Cause: errors.New("invoked preparation activation lacks its original result")}
		}
		if recordIdentity(t.activation.prepared.Record) != recordIdentity(t.prepared.Record) || t.activation.prepared.ContainerRoot != t.prepared.ContainerRoot || t.activation.cid != "" && t.activation.cid != t.cid {
			return &NativeEffectsUncertain{Cause: errors.New("activation handle differs from its original preparation")}
		}
		handled, err := t.activation.StopStartedGeneration(ctx, owner)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
	}
	if !t.prepared.Record.Launch.UnconfinedNetwork {
		_, err := runNoInitializationStop(ctx, noInitializationStopHooks{before: owner.VerifyOwned, stop: owner.Stop, inactive: owner.VerifyInactive, complete: func(context.Context, pinRetirementProgress) error { return nil }})
		return err
	}
	return withNativeKernelInfrastructure(ctx, func(kernel *kernelInfrastructure) error {
		controlPath, err := layout.OpenShellControlDir()
		if err != nil {
			return err
		}
		control, err := openNativeGenerationDirectory(controlPath)
		if err != nil {
			return err
		}
		defer func() { _ = control.Close() }()
		lock, err := lockNamespacePinsAt(ctx, control, t.prepared.Record.Launch.Binding.Incarnation, 0, 0)
		if err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
		defer func() { _ = lock.Close() }()
		return t.stopUninitialized(ctx, owner,
			func(c context.Context) error { return verifyNoInitializationKernel(c, kernel, lock, t.prepared.Record) },
			func(_ context.Context, p pinRetirementProgress) error {
				return publishNoInitializationAt(lock, t.prepared.Record, t.cid, kernel.bootID, kernel.mountID, p)
			})
	})
}

// Private protocol seam uses this original preparation's consumed cleanup
// attempt. Production supplies current kernel proof and the protected writer;
// tests can compose ordinary owned files without claiming root/kernel proof.
func (t *NativePreparedGeneration) stopUninitialized(ctx context.Context, owner NativeNamespaceStop, verify func(context.Context) error, publish func(context.Context, pinRetirementProgress) error) error {
	if t == nil || !t.original || !t.cleanupAttempted || t.closed || t.noInitializationAttempted || verify == nil || publish == nil || owner.VerifyOwned == nil || owner.Stop == nil || owner.VerifyInactive == nil {
		return errors.New("no-initialization cleanup lacks its original prepared attempt and complete proof callbacks")
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(t.failure, &uncertain) {
		return t.failure
	}
	t.noInitializationAttempted = true
	_, err := runNoInitializationStop(ctx, noInitializationStopHooks{
		before: func(c context.Context) error {
			if err := owner.VerifyOwned(c); err != nil {
				return err
			}
			return verify(c)
		},
		stop: owner.Stop,
		inactive: func(c context.Context) error {
			if err := owner.VerifyInactive(c); err != nil {
				return err
			}
			return verify(c)
		},
		complete: func(c context.Context, p pinRetirementProgress) error {
			if err := owner.VerifyInactive(c); err != nil {
				return err
			}
			if err := verify(c); err != nil {
				return err
			}
			return publish(c, p)
		},
	})
	return err
}

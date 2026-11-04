package agentbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/openshell"
)

// nativeStartedGeneration is original transaction state, never a cold runtime
// observation or decoded request. The private native start factory fills it
// from its own SDK preparation token, Start replies and retained activation.
// No public Start/restart/default caller is enabled by this private helper.
type nativeStartedGeneration struct {
	cid                               string
	prepared                          openshell.PreparedRuntime
	preparation                       *openshell.NativePreparedGeneration
	startAttempted, startAcknowledged bool
	target                            openshell.ActivationTarget
	activationAttempted               bool
	activation                        *openshell.NativeActivation
	failure                           error
	cleanupAttempted                  bool
}

func (*nativeStartedGeneration) String() string   { return "OpenShell started generation [redacted]" }
func (*nativeStartedGeneration) GoString() string { return "OpenShell started generation [redacted]" }

func (g *nativeStartedGeneration) cleanupIdentity() error {
	if g == nil {
		return errors.New("generation cleanup requires its original launch attempt")
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(g.failure, &uncertain) {
		return g.failure
	}
	if g.cleanupAttempted || g.startAttempted != g.startAcknowledged || g.activationAttempted && !g.startAcknowledged || g.activationAttempted != (g.activation != nil) {
		return &NativeEffectsUncertain{Cause: errors.New("generation cleanup lacks its original complete known Start/activation result; no retry or fallback")}
	}
	if !g.preparation.Matches(g.prepared, g.cid) || !g.preparation.ActivationMatches(g.activationAttempted, g.activation) {
		return &NativeEffectsUncertain{Cause: errors.New("generation cleanup lacks its original SDK preparation/activation result")}
	}
	if !nativeLowerHex(g.cid, 32) || g.prepared.ContainerRoot == "" || g.prepared.GenerationRoot == "" || g.prepared.Record.Generation == "" {
		return errors.New("generation cleanup lacks original prepared CID/record identity")
	}
	if g.target.ContainerID != "" && !g.startAcknowledged || g.target.ContainerID == "" && (g.target.PID != 0 || !g.target.StartedAt.IsZero()) || g.target.ContainerID != "" && (g.target.ContainerID != g.cid || g.target.PID <= 1 || g.target.StartedAt.IsZero()) {
		return errors.New("generation cleanup has a different/incomplete original live target")
	}
	return nil
}

// Pure data comparison is not admission. The actual verifier below also
// checks the retained native prepared record/current root through no-follow IO.
func (g *nativeStartedGeneration) matchesSelected(selected OpenShellRuntime) bool {
	if g == nil || selected.ContainerID != g.cid || selected.ControlRoot != g.prepared.ContainerRoot || selected.Launch != g.prepared.Record.Launch.HelperLaunch() || g.prepared.GenerationRoot != filepath.Join(g.prepared.ContainerRoot, "generations", g.prepared.Record.Generation) {
		return false
	}
	actual, err := json.Marshal(selected.Record)
	if err != nil {
		return false
	}
	original, err := json.Marshal(g.prepared.Record)
	return err == nil && bytes.Equal(actual, original)
}
func (g *nativeStartedGeneration) verifySelected(ctx context.Context, selected OpenShellRuntime) error {
	if !g.matchesSelected(selected) {
		return errors.New("current runtime differs from the original prepared generation")
	}
	return openshell.VerifyPreparedNativeRecord(ctx, g.prepared)
}

// Caller holds the same native space/entity admission and has withdrawn its
// owned forwards. This is cleanup of that original known Start, not permission
// to stop a generation selected from current metadata or retry an unknown one.
func (a LiveApplier) stopNativeStartedGeneration(ctx context.Context, name string, g *nativeStartedGeneration) error {
	if err := g.cleanupIdentity(); err != nil {
		return err
	}
	if err := a.stopNativeRuntimeWithGeneration(ctx, name, g.cid, g); err != nil {
		return err
	}
	// Release this incarnation's retained namespace lock before a separately
	// authorized rollback can create another generation. Close deletes nothing.
	if g.preparation != nil {
		if err := g.preparation.Close(); err != nil {
			return &NativeEffectsUncertain{Cause: err}
		}
	}
	return nil
}

// Both network modes use the SDK's original preparation token. In particular
// the no-Start/no-reservation cases must publish durable independent completion
// before the endpoint suffix; a missing namespace is never a cold fallback.
func completeNativeGenerationStop(ctx context.Context, preparation *openshell.NativePreparedGeneration, stop, owned, inactive, endpoint func(context.Context) error) error {
	if stop == nil || owned == nil || inactive == nil || endpoint == nil {
		return errors.New("original generation Stop lacks admitted callbacks")
	}
	if err := preparation.StopGeneration(ctx, openshell.NativeNamespaceStop{VerifyOwned: owned, Stop: stop, VerifyInactive: inactive}); err != nil {
		return err
	}
	return completeNativeStopEndpoint(ctx, inactive, endpoint)
}

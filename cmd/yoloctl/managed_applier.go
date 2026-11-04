package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/openshellrelease"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// managedApplier wraps the raw LiveApplier with session-aware cleanup before
// recreate and restart. Each launch is authorized first (guardPrivilegedLaunch),
// before this call's cleanup, build, stop or removal; the inner applier's own
// recovery starts belong to that one authorized operation.
type managedApplier struct {
	inner       agentbox.Applier
	client      *container.Client
	creationErr error
	notice      io.Writer
	inspect     func(context.Context, string) (*container.Details, error)
	// cleanup reports ended when the holder is positively stopped and its
	// state (marker, tombstone, socket) removed; an error stops the operation.
	cleanup         func(context.Context, *agentbox.Config) (ended bool, err error)
	prepareRecreate func(context.Context) error
	// onSessionEnded records, under the caller's box lock, that the launch
	// ended. It runs right after a cleanup that proved it, before the
	// container operation, so a later failure cannot leave the record
	// claiming a launch whose evidence is gone.
	onSessionEnded func(detail string) error
}

// Launch close details for the cleanup that a restart or recreate performs.
const (
	sessionEndedByRestart  = "the container was restarted to reload its policy"
	sessionEndedByRecreate = "the container was recreated to apply its declaration"
)

func newManagedLiveApplier(c *container.Client, notice io.Writer) agentbox.Applier {
	inner, selectionErr := defaultCreationApplier(c)
	return managedApplier{
		inner:           inner,
		creationErr:     selectionErr,
		client:          c,
		notice:          notice,
		prepareRecreate: c.RequireWorkloadPool,
	}
}

// Selection is compiled into the controller, never supplied by a box manifest.
// An unavailable selection blocks new allocations without affecting the
// inspected runtime's Start, Stop, policy-restart or privileged-box routes.
func defaultCreationApplier(c *container.Client) (agentbox.LiveApplier, error) {
	a := agentbox.LiveApplier{Client: c, VerifyStorage: controller.VerifyRuntimeStorage}
	if !spaceenv.Native() {
		return a, nil
	}
	release, rego, err := openshellrelease.Load()
	if err != nil {
		return a, err
	}
	return a.WithNativeCreationRelease(release, rego), nil
}

// Only explicit start/restart uses declared-forward restoration. Other live
// policy reconciliation keeps its original observed-forward snapshot.
func newManagedLiveApplierForStart(c *container.Client, notice io.Writer, intent *agentbox.Config) agentbox.Applier {
	a := newManagedLiveApplier(c, notice).(managedApplier)
	a.inner = a.inner.(agentbox.LiveApplier).WithDeclaredForwardRestoration(intent)
	return a
}

func (a managedApplier) Create(ctx context.Context, c *agentbox.Config, policy []byte) error {
	if err := guardPrivilegedLaunch(ctx, c, launchCreate, nil, nil); err != nil {
		return err
	}
	if err := checkDefaultNativeCreation(c); err != nil {
		return err
	}
	if !c.Privileged && a.creationErr != nil {
		return a.creationErr
	}
	return a.inner.Create(ctx, c, policy)
}

func (a managedApplier) Recreate(ctx context.Context, c *agentbox.Config, policy []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := agentbox.ValidateChatDeclaration(c); err != nil {
		return err
	}
	inspect := a.inspector()
	var details *container.Details
	missing := false
	if inspect != nil {
		var err error
		if details, err = inspect(ctx, c.Name); err != nil {
			if !isContainerGone(err) {
				return err
			}
			// Old path: recreating a box whose container is gone (for example
			// after a space recreate) simply creates it.
			details, missing = nil, true
		}
	}
	if err := guardPrivilegedLaunch(ctx, c, launchRecreate, details, nil); err != nil {
		return err
	}
	if err := checkDefaultNativeCreation(c); err != nil {
		return err
	}
	nativeReplacement := false
	if details != nil {
		native, err := agentbox.NativeRuntimeRoute(details)
		if err != nil {
			return err
		}
		nativeReplacement = native
	}
	if !c.Privileged && a.creationErr != nil {
		return a.creationErr
	}
	if missing {
		if a.notice != nil {
			_, _ = fmt.Fprintf(a.notice, "notice: box %s has no container; creating it\n", c.Name)
		}
		if err := a.endSession(ctx, c, sessionEndedByRecreate); err != nil {
			return err
		}
		return a.inner.Create(ctx, c, bytes.Clone(policy))
	}
	if inspect != nil {
		if !agentbox.ChatRecreateAllowed(ctx) {
			if err := agentbox.VerifyChatRuntime(c, details); err != nil {
				return err
			}
		}
		if err := agentbox.CheckRuntimeModeChange(ctx, c, details); err != nil {
			return err
		}
	}
	if a.prepareRecreate != nil {
		if err := a.prepareRecreate(ctx); err != nil {
			return err
		}
	}
	// Qualify the actual replacement before ending a healthy holder. Native
	// implementations check the retained prepared image and qualified helper
	// pair here; the actual replacement repeats its current identity checks.
	policy = bytes.Clone(policy)
	if prepare, ok := a.inner.(interface {
		PreflightRecreate(context.Context, *agentbox.Config, []byte) error
	}); ok {
		if err := prepare.PreflightRecreate(ctx, c, bytes.Clone(policy)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if nativeReplacement {
		return a.recreateNative(ctx, c, policy, details)
	}
	if err := a.endSession(ctx, c, sessionEndedByRecreate); err != nil {
		return err
	}
	return a.inner.Recreate(ctx, c, policy)
}

func (a managedApplier) Restart(ctx context.Context, c *agentbox.Config, policy []byte) error {
	var details *container.Details
	var inspectErr error
	if inspect := a.inspector(); inspect != nil {
		details, inspectErr = inspect(ctx, c.Name)
	}
	if err := guardPrivilegedLaunch(ctx, c, launchRestart, details, inspectErr); err != nil {
		return err
	}
	policy = bytes.Clone(policy)
	if prepare, ok := a.inner.(interface {
		PreflightPolicyRestart(context.Context, *agentbox.Config, []byte) error
	}); ok {
		if err := prepare.PreflightPolicyRestart(ctx, c, bytes.Clone(policy)); err != nil {
			return err
		}
	} else if prepare, ok := a.inner.(interface {
		PreflightRestart(context.Context, *agentbox.Config) error
	}); ok {
		if err := prepare.PreflightRestart(ctx, c); err != nil {
			return err
		}
	}

	if err := a.endSession(ctx, c, sessionEndedByRestart); err != nil {
		return err
	}
	return a.inner.Restart(ctx, c, policy)
}

func (a managedApplier) AddPortForward(ctx context.Context, c *agentbox.Config, pf agentbox.Port) error {
	return a.inner.AddPortForward(ctx, c, pf)
}

func (a managedApplier) RemovePortForward(ctx context.Context, c *agentbox.Config, pf agentbox.Port) error {
	return a.inner.RemovePortForward(ctx, c, pf)
}

// inspector observes the existing container; tests may replace it.
func (a managedApplier) inspector() func(context.Context, string) (*container.Details, error) {
	if a.inspect != nil {
		return a.inspect
	}
	if a.client != nil {
		return a.client.Inspect
	}
	return nil
}

// endSession runs the session cleanup and, when it proved the launch ended,
// records that before the container operation.
func (a managedApplier) endSession(ctx context.Context, c *agentbox.Config, detail string) error {
	ended, err := a.cleanupSession(ctx, c)
	if err != nil {
		return err
	}
	if ended && a.onSessionEnded != nil {
		if err := a.onSessionEnded(detail); err != nil {
			return fmt.Errorf("the session of box %s ended, but recording that for its conversation failed: %w", c.Name, err)
		}
	}
	return nil
}

func (a managedApplier) cleanupSession(ctx context.Context, cfg *agentbox.Config) (bool, error) {
	if a.cleanup != nil {
		return a.cleanup(ctx, cfg)
	}
	if a.client == nil {
		return false, nil
	}
	return cleanupManagedSession(ctx, a.client, cfg.Name, a.notice), nil
}

// cleanupManagedSession stops the box's holder and removes its session state.
// It reports true only when both succeeded, the proof that no holder of the
// launch survives; a failure is a warning, and the marker or tombstone it may
// leave is settled by the next start.
func cleanupManagedSession(ctx context.Context, c *container.Client, name string, notice io.Writer) bool {
	ended := true
	if err := session.StopHolderUnit(ctx, c, name); err != nil {
		ended = false
		if notice != nil {
			_, _ = fmt.Fprintf(notice, "warning: session holder cleanup for %s: %v\n", name, err)
		}
	}
	if err := cleanupStoppedSessionState(ctx, c, name); err != nil {
		ended = false
		if notice != nil {
			_, _ = fmt.Fprintf(notice, "warning: session state cleanup for %s: %v\n", name, err)
		}
	}
	return ended
}

func cleanupStoppedSessionState(ctx context.Context, c *container.Client, name string) error {
	return c.ShellQuiet(ctx, "sudo", "rm", "-rf", session.StateDirFor(name), session.SocketPath(name))
}

// stopManagedAgentboxContainer keeps all existing callers on the same inspected
// stop route, with native disk authority supplied only by controller code.
func stopManagedAgentboxContainer(ctx context.Context, c *container.Client, name string) error {
	return (agentbox.LiveApplier{Client: c, VerifyStorage: controller.VerifyRuntimeStorage}).Stop(ctx, name)
}

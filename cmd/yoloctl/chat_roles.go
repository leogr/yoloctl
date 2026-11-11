package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
)

// desiredChatOperators derives IRC channel roles from current native and live
// identity, never from a nick, account sharing, or a persisted role alone. A
// failed inventory/inspection cannot silently authorize a grant or claim that
// revocation has reached AIRC. Callers hold the native space mutation lock.
func desiredChatOperators(ctx context.Context, r *managedchat.Room, boxes []boxstate.Box, inspect func(context.Context, string) (*container.Details, error)) (map[string]bool, error) {
	if _, err := chatInventory(boxes); err != nil {
		return nil, err
	}
	byName := make(map[string]boxstate.Box, len(boxes))
	for _, box := range boxes {
		byName[box.Name] = box
	}
	want := map[string]bool{}
	for _, m := range r.Members {
		box, ok := byName[m.Name]
		if m.Revoked || !ok || box.Config == nil || box.Config.ID != m.BoxID || box.Container == nil || box.Runtime != boxstate.RuntimeRunning {
			continue
		}
		enabled, err := agentbox.OperatorEnabled(box.Config)
		if err != nil {
			return nil, fmt.Errorf("resolve channel operator grant for %s: %w", m.Name, err)
		}
		if !enabled {
			continue
		}
		details, err := inspect(ctx, m.Name)
		if err != nil {
			return nil, fmt.Errorf("inspect channel operator %s: %w", m.Name, err)
		}
		if !verifiedChatOperator(box.Config, details) {
			continue
		}
		want[m.ID] = true
	}
	return want, nil
}

func verifiedChatOperator(cfg *agentbox.Config, details *container.Details) bool {
	if details == nil {
		return false
	}
	endpoint := operator.Endpoint{Box: cfg.Name, Incarnation: details.Labels[operator.IncarnationLabel]}
	if endpoint.Validate() != nil || validateOperatorRuntime(cfg.Space, controller.Principal{Box: cfg.Name, Incarnation: endpoint.Incarnation}, details) != nil {
		return false
	}
	return agentbox.VerifyChatRuntime(cfg, details) == nil
}

var chatRoleServerControl = managedServerControl
var chatRoleHolderState = vmservice.State

// synchronizeChatRoles persists first, publishes exact private files, then
// applies once to the verified server. Missing/stopped servers keep the new
// configuration for their next explicit start; no workload is started here.
// An uncertain reload is returned, never retried by an autonomous loop.
// synchronizeChatRolesFn is a test seam for the chat runtime dispatcher.
var synchronizeChatRolesFn = synchronizeChatRoles

func synchronizeChatRoles(ctx context.Context, space string, store managedchat.Store, boxes []boxstate.Box, notice io.Writer) error {
	r, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.RuntimeEnabled || !r.Prepared {
		return nil
	}
	i, err := chatInventory(boxes)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	want, err := desiredChatOperators(ctx, r, boxes, container.New(space).Inspect)
	if err != nil {
		return err
	}
	next, err := store.SetOperators(want, r.Generation, i)
	if err != nil {
		return err
	}
	// Do not replay a previously interrupted reload when roles did not change.
	// Explicit chat ensure owns recovery of pending generations.
	if next.Generation == r.Generation {
		return nil
	}
	proof, err := store.Render(next)
	if err != nil {
		return fmt.Errorf("channel roles saved; publication pending: %w", err)
	}
	for _, box := range boxes {
		if box.Name == next.Server.Name && box.Runtime == boxstate.RuntimeRunning {
			if err := applyChangedChatRoles(ctx, next, proof, box.Config, notice); err != nil {
				return fmt.Errorf("channel roles saved; runtime application pending (inspect chat status, then explicitly chat ensure): %w", err)
			}
			return nil
		}
	}
	cliInfo(logging.Chat, "Channel roles saved and published; managed chat server is absent or stopped. Run chat ensure to apply the pending generation.")
	return nil
}

func applyChangedChatRoles(ctx context.Context, r *managedchat.Room, proof managedchat.Rendered, cfg *agentbox.Config, notice io.Writer) error {
	// Validate the reserved declaration, current container identity and binds
	// before treating a missing holder as a legitimate stopped workload.
	control, _, err := chatRoleServerControl(ctx, r, cfg)
	if err != nil {
		return err
	}
	state, err := chatRoleHolderState(ctx, container.New(cfg.Space), session.UnitName(cfg.Name))
	if err != nil {
		return fmt.Errorf("inspect managed chat holder: %w", err)
	}
	var status managedchat.Applied
	switch state {
	case "inactive", "failed", "unknown":
		// Holder loss can leave its confined child alive. Probe once before
		// deferring a role downgrade; never leave a verified live orphan stale.
		status, err = control.Call(ctx, "status")
		var dialErr *net.OpError
		if errors.As(err, &dialErr) && dialErr.Op == "dial" && dialErr.Net == "unix" && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)) {
			cliInfo(logging.Chat, "Channel roles saved and published; managed chat holder is stopped and control is unavailable. Run chat ensure to apply the pending generation.")
			return nil
		}
	case "active", "activating":
		// Container liveness does not imply control readiness. Only read-only
		// startup probes may repeat; a reload is always issued at most once.
		status, err = waitManagedServer(ctx, control)
	default:
		return fmt.Errorf("managed chat holder state %q is not ready", state)
	}
	if err != nil {
		return err
	}
	if err := status.Validate(); err != nil {
		return err
	}
	return applyChatChannelProof(ctx, control, status, proof)
}

// syncNativeChatRoles runs inside the existing mutation guard, after entity
// locks are released and before the native space lock/admission is released.
var syncNativeChatRolesFn = syncNativeChatRoles

func syncNativeChatRoles(ctx context.Context, space string) error {
	if !spaceenv.Native() {
		return nil
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return err
	}
	r, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !r.RuntimeEnabled || !r.Prepared {
		return err
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
	if err != nil {
		return fmt.Errorf("chat role synchronization pending: %w", err)
	}
	// Role publication changes the shared room: the operation's durable
	// footprint includes it before the first role write.
	if err := controller.ExpandEffects(ctx, chatDomain); err != nil {
		return fmt.Errorf("chat role synchronization pending: %w", err)
	}
	return synchronizeChatRoles(ctx, space, store, boxes, os.Stderr)
}

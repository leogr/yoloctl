package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// statusServerControlFn is managedServerControl, a seam for status tests.
var statusServerControlFn = managedServerControl
var statusInspectFn = (*container.Client).Inspect

func observeManagedChat(ctx context.Context, store managedchat.Store, boxes []boxstate.Box) (managedchat.Status, error) {
	var result managedchat.Status
	r, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	wait, stop := context.WithTimeout(ctx, chatOptionalObservationTimeout)
	defer stop()
	result, err = observeManagedChatRoom(wait, spaceenv.Current().Space, store, r, boxes)
	if ctx.Err() != nil {
		return managedchat.Status{}, ctx.Err()
	}
	return finishChatRuntimeObservation(wait, result), err
}

// A verified room establishes the roster independently of live runtime.
func chatStatusFromRoom(room *managedchat.Room) managedchat.Status {
	result := managedchat.Status{Room: room}
	for _, m := range room.Members {
		result.Members = append(result.Members, managedchat.MemberStatus{ID: m.ID, Name: m.Name, BoxID: m.BoxID, Revoked: m.Revoked})
	}
	return result
}

func observeManagedChatRoom(ctx context.Context, space string, store managedchat.Store, r *managedchat.Room, boxes []boxstate.Box) (managedchat.Status, error) {
	result := chatStatusFromRoom(r)
	if !r.RuntimeEnabled || !r.Prepared {
		result.SetupRequired = true
		result.Pending = []string{"run chat ensure to prepare and enable the managed runtime"}
		return result, nil
	}
	proof, err := store.Expected(r)
	if err != nil {
		return result, err
	}
	byName := map[string]boxstate.Box{}
	for _, box := range boxes {
		byName[box.Name] = box
	}
	server, exists := byName[r.Server.Name]
	if ctx.Err() != nil {
		return result, nil
	}
	if !exists || server.Config == nil || server.Container == nil {
		result.SetupRequired = server.ConfigError == ""
		result.Pending = append(result.Pending, "managed server runtime is absent")
	} else {
		control, details, err := statusServerControlFn(ctx, r, server.Config)
		if err != nil {
			if ctx.Err() == nil && details != nil && !details.Running {
				result.SetupRequired = true
			}
			result.Pending = append(result.Pending, err.Error())
		} else {
			result.Server = &managedchat.ServerStatus{ContainerID: details.ID, ContainerIncarnation: details.Labels[managedchat.IncarnationLabel]}
			if reason := agentbox.ChatServerDeploymentDrift(details); reason != "" {
				result.Server.RecreatePending, result.Server.Reason = true, reason
			}
			applied, err := control.Call(ctx, "status")
			if err == nil {
				err = applied.Validate()
			}
			if err == nil {
				result.Server.ProcessIncarnation = applied.Incarnation
				result.Server.AppliedGeneration = applied.Generation
				result.Server.ConfigSHA256 = applied.ConfigSHA256
				err = applied.Matches(proof)
			}
			if err != nil {
				result.Pending = append(result.Pending, err.Error())
			} else {
				active := holderUnitActiveFn(ctx, container.New(server.Config.Space), server.Name)
				switch {
				case ctx.Err() != nil:
					result.Pending = append(result.Pending, "managed server holder state unavailable")
				case !active:
					result.Pending = append(result.Pending, "managed server holder is not active")
				default:
					result.ServerReady = true
				}
			}
		}
	}
	client := container.New(space)
	inspect := func(ctx context.Context, box string) (*container.Details, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return statusInspectFn(client, ctx, box)
	}
	if ctx.Err() == nil {
		operators, roleErr := desiredChatOperators(ctx, r, boxes, inspect)
		if roleErr != nil {
			result.Pending = append(result.Pending, "channel operator inventory unavailable: "+roleErr.Error())
		} else {
			for _, m := range r.Members {
				if m.ChannelOperator != operators[m.ID] {
					result.Pending = append(result.Pending, m.Name+": channel operator grant changed; run chat ensure to synchronize")
				}
			}
		}
	}
	for i, m := range r.Members {
		observed := &result.Members[i]
		box, found := byName[m.Name]
		if m.Revoked && found && box.Container != nil {
			// A declaration may have been deleted/reused while the old container
			// still holds its private leaf. Report that retained binding even
			// though it must never authorize recreation of the replacement ID.
			details, err := inspect(ctx, m.Name)
			if err != nil {
				observed.Reason = "revoked participant runtime could not be inspected"
			} else {
				observed.Bound = details.Labels[managedchat.MemberLabel] == m.ID
				observed.RecreatePending = observed.Bound
				if observed.Bound {
					observed.Reason = "revoked credentials remain privately bound; explicit box recreate removes the old binds"
				}
			}
		} else if !found || box.Config == nil || box.Config.ID != m.BoxID {
			if !m.Revoked {
				observed.Reason = staleMemberReason(m.ID, space)
			}
		} else if box.Container == nil {
			if !m.Revoked {
				observed.Reason = "participant runtime is absent; start the box to install its private binds"
			}
		} else {
			details, err := inspect(ctx, m.Name)
			if err != nil {
				observed.Reason = "participant runtime could not be inspected"
			} else if err := agentbox.VerifyChatRuntime(box.Config, details); err != nil {
				observed.RecreatePending = true
				observed.Reason = err.Error()
			} else {
				observed.Bound = true
			}
		}
		if observed.Reason != "" {
			result.Pending = append(result.Pending, m.Name+": "+observed.Reason)
		}
	}
	result.RuntimeApplied = result.ServerReady && len(result.Pending) == 0
	return result, nil
}

// A late optional source makes application incomplete without erasing a
// previously verified server or any participant evidence already observed.
func finishChatRuntimeObservation(ctx context.Context, result managedchat.Status) managedchat.Status {
	if ctx.Err() != nil {
		result.RuntimeApplied = false
		result.Pending = append(result.Pending, "runtime observation deadline reached")
	}
	return result
}

// staleMemberReason names the explicit command for an active membership whose
// declaration is gone or reused. Revocation stays explicit: the member ID
// names exactly this immutable membership.
func staleMemberReason(memberID, space string) string {
	return fmt.Sprintf("declaration identity is missing or its name was reused; if this membership is stale, run yoloctl chat revoke %s --space %s", memberID, space)
}

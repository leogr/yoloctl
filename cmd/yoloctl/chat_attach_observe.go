package main

import (
	"context"
	"errors"
	"reflect"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// errChatAttachLookupDeadline marks a lookup that ran out of time, as opposed
// to a participant that is missing, revoked or stopped.
var errChatAttachLookupDeadline = errors.New("participant lookup deadline reached; the controller is busy, try again")

// observeChatAttachTarget is the bounded preview behind a Participants click.
// It never takes the native space lock, so a lifecycle mutation or storage
// cleanup holding it cannot stall the click. The preview grants nothing:
// validateChatAttachCommand rechecks identity and start consent under the
// lock. Unavailable runtime evidence is an error, never a stopped box.
func observeChatAttachTarget(ctx context.Context, space string, store managedchat.Store, memberID string) (managedchat.AttachTarget, error) {
	ctx, cancel := context.WithTimeout(ctx, chatObservationTimeout)
	defer cancel()
	if err := checkOperatorChatRuntime(ctx, "chat.attach-target", chatRuntimeRequest{MemberID: memberID}, store); err != nil {
		return managedchat.AttachTarget{}, err
	}
	room, err := store.ReadManifestSnapshot()
	if err != nil {
		return managedchat.AttachTarget{}, err
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
	if ctx.Err() != nil {
		return managedchat.AttachTarget{}, errChatAttachLookupDeadline
	}
	if err != nil {
		return managedchat.AttachTarget{}, errors.New("runtime inventory unavailable: " + err.Error())
	}
	target, err := resolveChatAttachTarget(room, boxes, memberID)
	if err != nil {
		return managedchat.AttachTarget{}, err
	}
	// The room snapshot and the inventory are separate reads; publish only a
	// target whose room, disk, human and membership did not change between them.
	current, err := store.ReadManifestSnapshot()
	if err != nil {
		return managedchat.AttachTarget{}, err
	}
	if !reflect.DeepEqual(room, current) {
		return managedchat.AttachTarget{}, errors.New("managed chat changed during the participant lookup; try again")
	}
	if ctx.Err() != nil {
		return managedchat.AttachTarget{}, errChatAttachLookupDeadline
	}
	return target, nil
}

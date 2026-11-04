package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
)

func decodeChatChannelRequest(method string, raw []byte) (chatChannelRequest, error) {
	var input chatChannelRequest
	if len(raw) == 0 && (method == "chat.channel.list" || method == "chat.channel.catalog") {
		return input, nil
	}
	var fields map[string]json.RawMessage
	if len(raw) > 4096 || json.Unmarshal(raw, &fields) != nil || fields == nil || decodeBoundedJSON(bytes.NewReader(raw), &input, 4096) != nil {
		return input, errors.New("invalid typed channel request")
	}
	var required []string
	switch method {
	case "chat.channel.list", "chat.channel.catalog":
	case "chat.channel.create":
		required = []string{"room_id", "channel", "generation"}
	case "chat.channel.add", "chat.channel.remove":
		required = []string{"room_id", "channel_id", "member_id", "box_id", "generation"}
	case "chat.channel.delete":
		required = []string{"room_id", "channel_id", "generation", "confirm"}
	default:
		return input, errors.New("unknown channel operation")
	}
	if len(fields) != len(required) {
		return input, errors.New("channel request has missing or extra fields")
	}
	for _, name := range required {
		value, present := fields[name]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return input, errors.New("channel request has missing or null fields")
		}
	}
	if method == "chat.channel.list" || method == "chat.channel.catalog" {
		return input, nil
	}
	if input.Generation == 0 || layout.ValidateIdentity(input.RoomID) != nil {
		return input, errors.New("channel request requires a room identity and positive generation")
	}
	if method == "chat.channel.create" {
		return input, managedchat.ValidateChannelName(input.Channel)
	}
	if method == "chat.channel.delete" {
		if !input.Confirm {
			return input, errors.New("channel deletion requires explicit confirmation")
		}
		return input, layout.ValidateIdentity(input.ChannelID)
	}
	for _, id := range []string{input.ChannelID, input.MemberID, input.BoxID} {
		if err := layout.ValidateIdentity(id); err != nil {
			return input, err
		}
	}
	return input, nil
}

func channelSnapshot(status managedchat.Status) (chatChannelSnapshot, error) {
	if status.Room == nil {
		return chatChannelSnapshot{}, errors.New("no managed room; explicitly run chat ensure first")
	}
	header, err := chatstream.RoomHeader(status.Room)
	if err != nil {
		return chatChannelSnapshot{}, err
	}
	return chatChannelSnapshot{RoomID: status.Room.ID, DiskID: status.Room.DiskID, Generation: status.Room.Generation,
		DefaultChannel: status.Room.Channel, Channels: status.Room.ChannelCatalog(), DeletedChannels: slices.Clone(status.Room.DeletedChannels),
		Identities: header.Identities, RuntimeApplied: status.RuntimeApplied, Pending: slices.Clone(status.Pending)}, nil
}

func runControllerChatChannels(ctx context.Context, req controller.Request, out io.Writer) (_ bool, result error) {
	if !chatChannelMethod(req.Method) {
		return false, nil
	}
	if len(req.Args) != 0 || len(req.Flags) != 0 || req.Terminal || req.View {
		return true, errors.New("managed channels accept only typed finite requests")
	}
	input, err := decodeChatChannelRequest(req.Method, req.Payload)
	if err != nil {
		return true, err
	}
	store, err := nativeChatStoreFn()
	if err != nil {
		return true, err
	}
	if req.Method == "chat.channel.catalog" {
		snapshot, err := readDesiredChatCatalog(ctx, store)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(snapshot)
	}
	if req.Method == "chat.channel.list" {
		status, err := observeManagedChatCatalog(ctx, req.Space, store)
		if err != nil {
			return true, err
		}
		snapshot, err := channelSnapshot(status)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(snapshot)
	}
	unlock, err := spacelock.Acquire(ctx, req.Space, true)
	if err != nil {
		return true, err
	}
	defer unlock()
	// A saved change whose publication or application is unconfirmed
	// withholds the completion checkpoint.
	publicationFailed := false
	defer completeBeforeUnlock(ctx, &result, &publicationFailed)
	// Root's core slice installs this trusted principal in the worker context.
	// Reuse its live authority check after lock wait; payload names cannot select it.
	principal := currentControllerPrincipal(ctx)
	if !principal.Admin() {
		if err := checkOperatorPrincipal(ctx, principal); err != nil {
			return true, err
		}
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, req.Space, boxstate.RuntimeInventory)
	if err != nil {
		return true, err
	}
	inventory, err := chatInventory(boxes)
	if err != nil {
		return true, err
	}
	if req.Method != "chat.channel.list" {
		if err := controller.BeginScopedEffects(ctx, chatIntent("")); err != nil {
			return true, err
		}
		if err := applyChatChannelChangeFn(ctx, req.Space, req.Method, input, store, inventory, boxes); err != nil {
			var pending *publicationPending
			publicationFailed = errors.As(err, &pending)
			return true, err
		}
	}
	status, err := observeManagedChat(ctx, store, boxes)
	if err != nil {
		return true, err
	}
	snapshot, err := channelSnapshot(status)
	if err != nil {
		return true, err
	}
	return true, json.NewEncoder(out).Encode(snapshot)
}

// applyChatChannelChangeFn is a test seam for the channel dispatcher.
var applyChatChannelChangeFn = applyChatChannelChange

// publicationPending marks a desired change that was saved but whose private
// publication or live application did not complete. It is a known result,
// never a completed one.
type publicationPending struct{ err error }

func (e *publicationPending) Error() string { return e.err.Error() }
func (e *publicationPending) Unwrap() error { return e.err }

func applyChatChannelChange(ctx context.Context, space, method string, input chatChannelRequest, store managedchat.Store, inventory managedchat.Inventory, boxes []boxstate.Box) error {
	r, err := store.Load()
	if err != nil {
		return err
	}
	if r.ID != input.RoomID || r.Generation != input.Generation || !r.RuntimeEnabled || !r.Prepared {
		return errors.New("channel room/generation changed or runtime is not prepared; inspect before retrying")
	}
	// Do not republish an old +o grant just because a new catalog is requested.
	// Core owns derivation/synchronization; a separate explicit ensure can
	// settle changed roles before this CAS action, without replaying its intent.
	wantOperators, err := desiredChatOperators(ctx, r, boxes, container.New(space).Inspect)
	if err != nil {
		return err
	}
	for _, member := range r.Members {
		if member.ChannelOperator != (!member.Revoked && wantOperators[member.ID]) {
			return errors.New("configured channel roles changed; explicitly chat ensure, then inspect the new catalog before changing access")
		}
	}
	var cfg *agentbox.Config
	for _, box := range boxes {
		if box.Name == r.Server.Name {
			cfg = box.Config
		}
	}
	if cfg == nil || !holderUnitActiveFn(ctx, container.New(space), r.Server.Name) {
		return errors.New("managed server is not running; explicitly chat ensure before changing channels")
	}
	control, _, err := managedServerControl(ctx, r, cfg)
	if err != nil {
		return err
	}
	changed, err := mutateChatChannels(ctx, method, input, store, inventory, r, control)
	if err != nil || !changed {
		return err
	}
	if !holderUnitActiveFn(ctx, container.New(space), r.Server.Name) {
		return errors.New("channel reload was confirmed but server holder is no longer active")
	}
	return nil
}

// mutateChatChannels preflights the running server's capability and exact
// current proof, saves the desired change, publishes it and applies one
// reload. It reports whether the desired generation changed.
func mutateChatChannels(ctx context.Context, method string, input chatChannelRequest, store managedchat.Store, inventory managedchat.Inventory, r *managedchat.Room, control managedchat.Control) (bool, error) {
	before, err := control.Call(ctx, "status")
	if err != nil {
		return false, err
	}
	if err := before.Require("channel-add-reload"); err != nil {
		return false, err
	}
	// An older server cannot purge a deleted channel's data; no restart would.
	if method == "chat.channel.delete" {
		if err := before.Require("channel-delete-reload"); err != nil {
			return false, fmt.Errorf("upgrade required: %w", err)
		}
	}
	oldProof, err := store.Expected(r)
	if err != nil {
		return false, err
	}
	if err := before.Matches(oldProof); err != nil {
		return false, fmt.Errorf("existing managed configuration is pending; explicitly ensure before changing channels: %w", err)
	}
	var next *managedchat.Room
	switch method {
	case "chat.channel.create":
		next, err = store.CreateChannel(input.RoomID, input.Channel, input.Generation, inventory)
	case "chat.channel.delete":
		next, err = store.DeleteChannel(input.RoomID, input.ChannelID, input.Generation, inventory)
	default:
		next, err = store.SetChannelMember(input.RoomID, input.ChannelID, input.MemberID, input.BoxID, method == "chat.channel.add", input.Generation, inventory)
	}
	if err != nil {
		return false, err
	}
	if next.Generation == r.Generation {
		return false, nil // checked no-op: no publication, reload, join or migration
	}
	proof, err := store.Render(next)
	if err != nil {
		return true, &publicationPending{fmt.Errorf("desired channel access saved; private publication pending: %w", err)}
	}
	if err := applyChatChannelProof(ctx, control, before, proof); err != nil {
		return true, &publicationPending{fmt.Errorf("desired channel access saved; application unconfirmed, inspect before explicit recovery: %w", err)}
	}
	return true, nil
}

// One reload only. The old verified peer closure remains attached to every
// connection; a missing acknowledgment is returned even if later status might
// prove application. Status inspection and interrupted-receipt recovery are explicit.
func applyChatChannelProof(ctx context.Context, control managedchat.Control, before managedchat.Applied, proof managedchat.Rendered) error {
	ack, err := control.Call(ctx, "reload")
	if err != nil {
		return err
	}
	if err := ack.MatchesReload(proof, before); err != nil {
		return err
	}
	after, err := control.Call(ctx, "status")
	if err != nil {
		return err
	}
	return after.MatchesIncarnation(proof, before)
}

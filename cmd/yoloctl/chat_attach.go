package main

import (
	"context"
	"errors"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

func resolveChatAttachTarget(r *managedchat.Room, boxes []boxstate.Box, memberID string) (managedchat.AttachTarget, error) {
	result := managedchat.AttachTarget{RoomID: r.ID, MemberID: memberID, State: "missing"}
	if _, err := chatInventory(boxes); err != nil {
		return result, err
	}
	if memberID == r.Human.ID {
		return result, errors.New("the human participant has no box session to attach")
	}
	for _, m := range r.Members {
		if m.ID != memberID {
			continue
		}
		result.BoxID = m.BoxID
		if m.Revoked {
			result.State = "revoked"
			return result, nil
		}
		for _, box := range boxes {
			if box.Name != m.Name || box.Config == nil || box.Config.ID != m.BoxID {
				continue
			}
			result.Box = box.Name
			switch box.Runtime {
			case boxstate.RuntimeRunning:
				result.State = "running"
			case boxstate.RuntimeStopped:
				result.State = "stopped"
			}
			return result, nil
		}
		return result, nil
	}
	return result, errors.New("chat member is not present in the current room identity map")
}

var chatAttachIdentityFlags = []string{"chat-room-id", "chat-member-id", "chat-box-id"}

func addChatAttachFlags(cmd *cobra.Command) {
	for _, name := range chatAttachIdentityFlags {
		cmd.Flags().String(name, "", "Expected managed chat identity for a typed participant attachment")
		_ = cmd.Flags().MarkHidden(name)
	}
	cmd.Flags().Bool("chat-allow-start", false, "The human confirmed starting a stopped matching participant")
	_ = cmd.Flags().MarkHidden("chat-allow-start")
}

func chatAttachGuardPresent(cmd *cobra.Command) bool {
	for _, name := range append([]string{"chat-allow-start"}, chatAttachIdentityFlags...) {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// validateChatAttachCommand runs under the native mutation lock, including the
// existing-holder path. A read-only lookup followed by name-only attach would
// let revocation or deletion/name reuse race the user's click.
func validateChatAttachCommand(cmd *cobra.Command, space string, args []string) error {
	if len(args) != 1 {
		return errors.New("managed chat attachment requires exactly one box")
	}
	ids := make([]string, len(chatAttachIdentityFlags))
	for i, flag := range chatAttachIdentityFlags {
		ids[i], _ = cmd.Flags().GetString(flag)
		if layout.ValidateIdentity(ids[i]) != nil {
			return errors.New("managed chat attachment requires room, member and box identities")
		}
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return err
	}
	r, err := store.Load()
	if err != nil {
		return err
	}
	boxes, err := newBoxStateResolver().ResolveSpace(cmd.Context(), space, boxstate.RuntimeInventory)
	if err != nil {
		return err
	}
	allowStart, _ := cmd.Flags().GetBool("chat-allow-start")
	if err := validateChatAttachIdentity(r, boxes, managedchat.AttachTarget{RoomID: ids[0], MemberID: ids[1], BoxID: ids[2], Box: args[0]}, allowStart); err != nil {
		return err
	}
	for _, box := range boxes {
		if box.Name == args[0] && box.Container != nil {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			details, err := container.New(space).Inspect(ctx, box.Name)
			if err != nil {
				return err
			}
			if !details.Running && !allowStart {
				return errors.New("participant box stopped during inspection; confirm starting it before attaching")
			}
			return agentbox.VerifyChatRuntime(box.Config, details)
		}
	}
	return nil
}

func validateChatAttachIdentity(r *managedchat.Room, boxes []boxstate.Box, expected managedchat.AttachTarget, allowStart bool) error {
	if r.ID != expected.RoomID || !r.Prepared || !r.RuntimeEnabled {
		return errors.New("managed room identity changed; inspect the participant again")
	}
	current, err := resolveChatAttachTarget(r, boxes, expected.MemberID)
	if err != nil {
		return err
	}
	if current.Box == "" || current.Box != expected.Box || current.BoxID != expected.BoxID || current.State == "revoked" {
		return errors.New("chat membership or box identity changed; refusing to attach a replacement")
	}
	if current.State != "running" && !allowStart {
		return errors.New("participant box is no longer running; confirm starting it before attaching")
	}
	return nil
}

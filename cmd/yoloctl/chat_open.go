package main

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

// inspectOrEnsureChat preserves explicit first setup while opening a healthy
// server without repeating a mutation. Pending participant binds are independent.
func inspectOrEnsureChat(ctx context.Context, input chatRuntimeRequest, call func(context.Context, string, chatRuntimeRequest) (managedchat.Status, error)) (chatstream.Header, error) {
	status, err := call(ctx, "chat.status", chatRuntimeRequest{})
	if err != nil {
		return chatstream.Header{}, err
	}
	if status.Room != nil {
		if (input.ServerBox != "" && input.ServerBox != status.Room.Server.Name) ||
			(input.Human != "" && input.Human != status.Room.Human.Name) {
			return chatstream.Header{}, errors.New("managed chat already has a different room owner configuration")
		}
	}
	if !status.ServerReady {
		if status.Room != nil && status.Room.Prepared && status.Room.RuntimeEnabled && !status.SetupRequired {
			return chatstream.Header{}, errors.New("managed chat readiness is unconfirmed; inspect chat status before explicit ensure")
		}
		status, err = call(ctx, "chat.ensure", input)
		if err != nil {
			return chatstream.Header{}, err
		}
	}
	if status.Room == nil || !status.ServerReady {
		return chatstream.Header{}, errors.New("managed chat server is not ready; inspect chat status")
	}
	return chatstream.RoomHeader(status.Room)
}

func openChatHeader(cmd *cobra.Command) (chatstream.Header, error) {
	return inspectOrEnsureChat(cmd.Context(), chatEnsureInput(cmd), func(ctx context.Context, method string, input chatRuntimeRequest) (managedchat.Status, error) {
		return callChatController(ctx, cmd, method, input)
	})
}

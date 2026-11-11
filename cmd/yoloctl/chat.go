package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/user"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

// Chat commands use typed controller operations. They neither boot a VM nor
// read declarations, native state or authentication material on the host.
func newChatCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "chat", Short: "Manage the space-owned collaboration room", Long: "Manage the space-owned collaboration room. Verified space operators can ensure, inspect, enroll, revoke, connect and disconnect participants in their current space. The human view and human nickname remain host operations. Enrollment is durable membership; connecting a named client is separate and does not arm an agent wake listener.", Args: cobra.NoArgs}
	cmd.PersistentFlags().String("space", defaultSpaceName, "Space hosting the managed room")
	chatIdentityFlags(cmd)
	cmd.Flags().String("catalog-read", "single", "Channel refresh: single desired manifest or observed runtime status")
	cmd.RunE = runChatView
	ensure := &cobra.Command{Use: "ensure", Short: "Create or start the owned chat server and apply membership", Args: cobra.NoArgs}
	chatIdentityFlags(ensure)
	ensure.Flags().Bool("json", false, "Print observed desired and applied state as JSON")
	ensure.RunE = func(cmd *cobra.Command, _ []string) error {
		if jsonOutput, _ := cmd.Flags().GetBool("json"); !jsonOutput && recreateOfferInteractive(cmd) {
			// Interactive only: read the room's server, then offer before ensure.
			if status, err := callChatController(cmd.Context(), cmd, "chat.status", chatRuntimeRequest{}); err == nil {
				if err := offerChatServerRecreate(cmd, status, chatEnsureInput(cmd)); err != nil {
					return err
				}
			}
		}
		return runChatCommand(cmd, "chat.ensure", chatEnsureInput(cmd))
	}
	status := &cobra.Command{Use: "status", Short: "Inspect the room, server and pending member binds", Args: cobra.NoArgs}
	status.Flags().Bool("json", false, "Print observed desired and applied state as JSON")
	status.RunE = func(cmd *cobra.Command, _ []string) error {
		return runChatCommand(cmd, "chat.status", chatRuntimeRequest{})
	}
	enroll := &cobra.Command{Use: "enroll BOX", Short: "Enroll this declaration with a private room credential", Long: "Enroll a box with its private managed room credential. Add --connect to connect one named AIRC client after enrollment is acknowledged. Existing boxes need current private binds; only explicit --recreate replaces their container. A connection failure retains enrollment. The agent remains responsible for its wake listener; neither operation is automatically retried.", Args: cobra.ExactArgs(1)}
	revoke := &cobra.Command{Use: "revoke BOX|MEMBER_ID", Short: "Revoke a box's membership and disconnect its clients", Long: "Revoke a box's chat membership and disconnect its clients. A box name revokes its one active membership, even after the box was destroyed or replaced; a member ID from chat status revokes that immutable membership. An argument of 32 lowercase hex characters is always a member ID. The human member and the chat server are not box memberships and cannot be revoked.", Args: cobra.ExactArgs(1)}
	for _, sub := range []*cobra.Command{enroll, revoke} {
		sub.Flags().Uint64("generation", 0, "Require this desired generation (default current generation under the native lock)")
		sub.Flags().Bool("recreate", false, "Explicitly recreate the matching participant box to install/remove binds; private container-home data is lost, native account data and host workdirs survive")
		sub.Flags().Bool("json", false, "Print observed desired and applied state as JSON")
	}
	enroll.Flags().Bool("connect", false, "Connect a named AIRC client only after enrollment succeeds; the agent manages its wake listener")
	enroll.Flags().String("client", "space", "Named AIRC client for --connect (1–32 ASCII letters, digits, underscores or hyphens)")
	enroll.RunE = func(cmd *cobra.Command, args []string) error {
		return runChatEnrollCommand(cmd, args[0], callChatControllerInto)
	}
	revoke.RunE = func(cmd *cobra.Command, args []string) error {
		g, _ := cmd.Flags().GetUint64("generation")
		recreate, _ := cmd.Flags().GetBool("recreate")
		status, err := callChatController(cmd.Context(), cmd, "chat.revoke", chatRuntimeRequest{MemberID: args[0], Generation: g, Recreate: recreate})
		if err != nil {
			return err
		}
		if err := writeChatRevokedName(cmd, args[0], status); err != nil {
			return err
		}
		return writeChatStatus(cmd, status)
	}
	nickname := &cobra.Command{Use: "nickname NICK", Short: "Save your display nickname for the next chat connection", Long: "Save the human display nickname without changing authentication or member identity. Reconnect the human chat view to use it; existing clients, boxes and the server are not restarted.", Args: cobra.ExactArgs(1)}
	nickname.Flags().Uint64("generation", 0, "Require this desired generation (default current generation under the native lock)")
	nickname.Flags().Bool("json", false, "Print observed desired and applied state as JSON")
	nickname.RunE = func(cmd *cobra.Command, args []string) error {
		if err := managedchat.ValidateNickname(args[0]); err != nil {
			return err
		}
		g, _ := cmd.Flags().GetUint64("generation")
		if err := runChatCommand(cmd, "chat.nickname", chatRuntimeRequest{Nickname: args[0], Generation: g}); err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if !jsonOutput {
			cliInfo(logging.Chat, "Nickname saved. Reconnect your human chat view to use it; existing connections keep their current nickname.")
			return nil
		}
		return nil
	}
	cmd.AddCommand(ensure, status, enroll, revoke, nickname, newChatChannelCmd())
	addChatParticipationCommands(cmd)
	return cmd
}

func chatIdentityFlags(cmd *cobra.Command) {
	cmd.Flags().String("box", "", "Server box name on first creation (default chat); an unrelated box is never adopted")
	cmd.Flags().String("human", "", "Human display identity on first creation (default local user)")
}

func chatEnsureInput(cmd *cobra.Command) chatRuntimeRequest {
	box, _ := cmd.Flags().GetString("box")
	human, _ := cmd.Flags().GetString("human")
	return chatRuntimeRequest{ServerBox: box, Human: human, DefaultHuman: defaultChatHuman()}
}

// defaultChatHuman names the human for a room ensure creates, "" if unknown.
func defaultChatHuman() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func callChatController(ctx context.Context, cmd *cobra.Command, method string, input chatRuntimeRequest) (managedchat.Status, error) {
	var result managedchat.Status
	err := callChatControllerInto(ctx, cmd, method, input, &result)
	return result, err
}

func callChatAttachTarget(ctx context.Context, cmd *cobra.Command, memberID string) (managedchat.AttachTarget, error) {
	var result managedchat.AttachTarget
	err := callChatControllerInto(ctx, cmd, "chat.attach-target", chatRuntimeRequest{MemberID: memberID}, &result)
	return result, err
}

func callChatControllerInto(ctx context.Context, cmd *cobra.Command, method string, input any, result any) error {
	target, err := selectControllerTarget(ctx, cmd)
	if err != nil {
		return err
	}
	return callChatTargetInto(ctx, target, method, input, result, cmd.ErrOrStderr())
}

// callChatTargetInto keeps finite CLI diagnostics and interactive captures separate.
func callChatTargetInto(ctx context.Context, target controllerTarget, method string, input any, result any, errw io.Writer) error {
	if target.Operator && !operatorChatMethod(method) {
		return errors.New("this managed chat operation requires the human host endpoint")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req := controller.Request{ID: rand.Text(), Method: method, Space: target.Space, Dash: -1, Payload: payload}
	var raw bytes.Buffer
	err = target.Call(ctx, req, controller.ClientIO{Out: &boundedChatWriter{Out: &raw, Remaining: 2 << 20}, Err: errw})
	if err != nil {
		if !controllerOperations[method].Mutates && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err := json.Unmarshal(raw.Bytes(), result); err != nil {
		return errors.New("invalid managed chat controller response")
	}
	return nil
}

func runChatCommand(cmd *cobra.Command, method string, input chatRuntimeRequest) error {
	status, err := callChatController(cmd.Context(), cmd, method, input)
	if err != nil {
		return err
	}
	return writeChatStatus(cmd, status)
}

// writeChatRevokedName names the ID a box-name revoke resolved. Enrollment
// appends and refuses a reused name until its active membership is revoked,
// so the newest membership of that name is the one just revoked. An ID
// argument keeps the plain status output.
func writeChatRevokedName(cmd *cobra.Command, arg string, status managedchat.Status) error {
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput || layout.ValidateIdentity(arg) == nil || status.Room == nil {
		return nil
	}
	for i := len(status.Room.Members) - 1; i >= 0; i-- {
		if m := status.Room.Members[i]; m.Name == arg {
			cliInfo(logging.Chat, "Revoked chat member %s (ID %s).", m.Name, m.ID)
			return nil
		}
	}
	return nil
}

func writeChatStatus(cmd *cobra.Command, status managedchat.Status) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}
	if status.Room == nil {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No managed room. Create it with: yoloctl chat ensure")
		return err
	}
	state := "pending"
	if status.RuntimeApplied {
		state = "applied"
	} else if status.ServerReady {
		state = "server ready; participant changes pending"
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s on %s: %s (desired generation %d)\n", status.Room.Channel, status.Room.Server.Name, state, status.Room.Generation); err != nil {
		return err
	}
	if s := status.Server; s != nil && s.RecreatePending {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  server %s: %s\n", status.Room.Server.Name, s.Reason); err != nil {
			return err
		}
	}
	for _, member := range status.Members {
		kind := "enrolled"
		if member.Revoked {
			kind = "revoked"
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s  %s\n", member.Name, member.ID, kind); err != nil {
			return err
		}
	}
	for _, pending := range status.Pending {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "  pending:", pending); err != nil {
			return err
		}
	}
	return nil
}

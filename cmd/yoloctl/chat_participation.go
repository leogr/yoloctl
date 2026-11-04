package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

type chatParticipationRequest struct {
	Box    string `json:"box"`
	Client string `json:"client"`
}

// This acknowledges one named client action, not the box's entire presence or
// an agent's wake harness. Membership and credentials are unchanged.
type chatParticipationResult struct {
	RoomID   string `json:"room_id"`
	MemberID string `json:"member_id"`
	BoxID    string `json:"box_id"`
	Box      string `json:"box"`
	Client   string `json:"client"`
	Action   string `json:"action"`
}

func addChatParticipationCommands(parent *cobra.Command) {
	for _, action := range []string{"connect", "disconnect"} {
		cmd := &cobra.Command{
			Use:   action + " BOX",
			Short: action + " a named AIRC client in an enrolled box",
			Long:  "Explicitly " + action + " one named AIRC client in a running enrolled box using its private managed config. Enrollment, credentials and other named clients are unchanged. This does not start or recreate a box, arm an agent wake listener, send a message or automatically reconnect. Inspect after an uncertain reply before trying another action.",
			Args:  cobra.ExactArgs(1),
		}
		cmd.Flags().String("client", "space", "Named AIRC client (1–32 ASCII letters, digits, underscores or hyphens); disconnect affects only this client")
		cmd.Flags().Bool("json", false, "Print the acknowledged named-client action as JSON")
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			client, _ := cmd.Flags().GetString("client")
			input := chatParticipationRequest{Box: args[0], Client: client}
			if err := validateChatParticipationRequest(input); err != nil {
				return err
			}
			var result chatParticipationResult
			if err := callChatControllerInto(cmd.Context(), cmd, "chat."+action, input, &result); err != nil {
				return err
			}
			jsonOutput, _ := cmd.Flags().GetBool("json")
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s acknowledged for client %q in box %q; enrollment is unchanged.\n", result.Action, result.Client, result.Box)
			return err
		}
		parent.AddCommand(cmd)
	}
}

func validateChatParticipationRequest(input chatParticipationRequest) error {
	if err := validateAgentboxName(input.Box); err != nil {
		return err
	}
	if len(input.Client) < 1 || len(input.Client) > 32 {
		return errors.New("client names use 1–32 ASCII letters, digits, underscores or hyphens")
	}
	for _, c := range []byte(input.Client) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return errors.New("client names use 1–32 ASCII letters, digits, underscores or hyphens")
		}
	}
	return nil
}

func runControllerChatParticipation(ctx context.Context, req controller.Request, out, notice io.Writer) (_ bool, outcome error) {
	if req.Method != "chat.connect" && req.Method != "chat.disconnect" {
		return false, nil
	}
	if len(req.Args) != 0 || len(req.Flags) != 0 || req.Terminal || req.View || req.Session != nil {
		return true, errors.New("chat participation accepts only a typed payload")
	}
	var input chatParticipationRequest
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 4096); err != nil {
		return true, errors.New("invalid chat participation request")
	}
	if err := validateChatParticipationRequest(input); err != nil {
		return true, err
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return true, err
	}
	release, err := spacelock.Acquire(ctx, req.Space, true)
	if err != nil {
		return true, err
	}
	defer release()
	defer completeBeforeUnlock(ctx, &outcome, nil)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := checkOperatorChatRuntime(ctx, req.Method, chatRuntimeRequest{}, store); err != nil {
		return true, err
	}
	r, err := store.Load()
	if err != nil {
		return true, err
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, req.Space, boxstate.RuntimeInventory)
	if err != nil {
		return true, err
	}
	cfg, member, err := resolveChatParticipant(r, boxes, input.Box)
	if err != nil {
		return true, err
	}
	details, err := container.New(req.Space).Inspect(ctx, cfg.Name)
	if err != nil {
		return true, err
	}
	if !details.Running {
		return true, errors.New("participant box stopped; start it explicitly before changing its named client")
	}
	if err := agentbox.VerifyChatRuntime(cfg, details); err != nil {
		return true, fmt.Errorf("participant binds are not ready; inspect chat status and explicitly recreate the box if needed: %w", err)
	}
	if req.Method == "chat.connect" {
		if err := requireChatParticipationServer(ctx, store, r, boxes); err != nil {
			return true, err
		}
	}
	result := chatParticipationResult{RoomID: r.ID, MemberID: member.ID, BoxID: member.BoxID, Box: member.Name, Client: input.Client, Action: req.Method[len("chat."):]}
	if err := controller.BeginScopedEffects(ctx, chatIntent("", member.Name)); err != nil {
		return true, err
	}
	if err := executeChatParticipation(ctx, cfg, result, runManagedChatExec, notice); err != nil {
		return true, err
	}
	return true, json.NewEncoder(out).Encode(result)
}

func resolveChatParticipant(r *managedchat.Room, boxes []boxstate.Box, name string) (*agentbox.Config, managedchat.Member, error) {
	var member managedchat.Member
	if r == nil || !r.Prepared || !r.RuntimeEnabled {
		return nil, member, errors.New("run chat ensure before changing participant connections")
	}
	if _, err := chatInventory(boxes); err != nil {
		return nil, member, err
	}
	if name == r.Server.Name {
		return nil, member, errors.New("the managed server uses the human identity; connect an enrolled participant box")
	}
	for _, m := range r.Members {
		if !m.Revoked && m.Name == name {
			member = m
			break
		}
	}
	if member.ID == "" {
		return nil, member, errors.New("box has no active managed chat membership; enroll it explicitly first")
	}
	for _, box := range boxes {
		if box.Name != name {
			continue
		}
		if box.Config == nil || box.Config.ID != member.BoxID {
			return nil, member, errors.New("participant declaration identity changed; revoke the old member before enrolling a reused name")
		}
		if box.Container == nil || box.Runtime != boxstate.RuntimeRunning {
			return nil, member, errors.New("participant box is not running; start it explicitly before changing its named client")
		}
		return box.Config, member, nil
	}
	return nil, member, errors.New("participant declaration is missing")
}

func requireChatParticipationServer(ctx context.Context, store managedchat.Store, r *managedchat.Room, boxes []boxstate.Box) error {
	proof, err := store.Expected(r)
	if err != nil {
		return err
	}
	for _, box := range boxes {
		if box.Name != r.Server.Name {
			continue
		}
		control, _, err := managedServerControl(ctx, r, box.Config)
		if err != nil {
			return err
		}
		applied, err := control.Call(ctx, "status")
		if err != nil {
			return err
		}
		if err := applied.Matches(proof); err != nil {
			return err
		}
		if !holderUnitActiveFn(ctx, container.New(box.Config.Space), box.Name) {
			return errors.New("managed server holder is not active; run chat ensure explicitly")
		}
		return nil
	}
	return errors.New("managed server is absent; run chat ensure explicitly")
}

type chatParticipantExec func(context.Context, *agentbox.Config, []string, *os.File, io.Writer, io.Writer) error

func executeChatParticipation(ctx context.Context, cfg *agentbox.Config, result chatParticipationResult, exec chatParticipantExec, notice io.Writer) error {
	if err := validateChatParticipationRequest(chatParticipationRequest{Box: result.Box, Client: result.Client}); err != nil {
		return err
	}
	if result.Action != "connect" && result.Action != "disconnect" {
		return errors.New("unsupported chat participant action")
	}
	var probe bytes.Buffer
	probeNotice := &boundedChatWriter{Out: notice, Remaining: managedchat.ControlLimit}
	if err := exec(ctx, cfg, []string{"/usr/local/bin/airc", "features"}, nil, &boundedChatWriter{Out: &probe, Remaining: managedchat.ControlLimit}, probeNotice); err != nil {
		return err
	}
	var features managedchat.Features
	if json.Unmarshal(probe.Bytes(), &features) != nil {
		return errors.New("invalid AIRC participant feature probe")
	}
	if err := features.Require("client-unix", "named-multiline", "client-presence"); err != nil {
		return err
	}
	argv := []string{"/usr/local/bin/airc", result.Action}
	if result.Action == "connect" {
		argv = append(argv, "--config", managedchat.IdentityMount+"/client.toml")
	}
	// AIRC accepts leading hyphens in client names. The terminator makes the
	// name data, never a flag or a path selected by the caller.
	argv = append(argv, "--", result.Client)
	stdout := &boundedChatWriter{Out: notice, Remaining: managedchat.ControlLimit}
	stderr := &boundedChatWriter{Out: notice, Remaining: managedchat.ControlLimit}
	if err := exec(ctx, cfg, argv, nil, stdout, stderr); err != nil {
		return fmt.Errorf("named client %s was not acknowledged; inspect its AIRC status before another action: %w", result.Action, err)
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

type chatChannelRequest struct {
	RoomID     string `json:"room_id,omitempty"`
	Channel    string `json:"channel,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
	MemberID   string `json:"member_id,omitempty"`
	BoxID      string `json:"box_id,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	// Confirm is the explicit --yes of a deletion; it is required and true.
	Confirm bool `json:"confirm,omitempty"`
}

type chatChannelSnapshot struct {
	RoomID         string                `json:"room_id"`
	DiskID         string                `json:"disk_id"`
	Generation     uint64                `json:"generation"`
	DefaultChannel string                `json:"default_channel"`
	Channels       []managedchat.Channel `json:"channels"`
	// DeletedChannels are identity records only, never content or access.
	DeletedChannels []managedchat.DeletedChannel    `json:"deleted_channels,omitempty"`
	Identities      []managedchat.BootstrapIdentity `json:"identities"`
	RuntimeApplied  bool                            `json:"runtime_applied"`
	Pending         []string                        `json:"pending,omitempty"`
}

func chatChannelMethod(method string) bool {
	switch method {
	case "chat.channel.list", "chat.channel.catalog", "chat.channel.create", "chat.channel.add", "chat.channel.remove", "chat.channel.delete":
		return true
	}
	return false
}

func newChatChannelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Manage channels and per-agent channel access", Args: cobra.NoArgs}
	list := &cobra.Command{Use: "list", Short: "Inspect the desired catalog and applied status", Args: cobra.NoArgs}
	list.Flags().Bool("json", false, "Print channel identities, access and applied status as JSON")
	list.Flags().Bool("catalog-only", false, "Read one desired catalog without checking server application")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		return runChatChannelList(cmd, callChatControllerInto)
	}

	create := &cobra.Command{Use: "create NAME", Short: "Create a channel for currently enrolled members without restarting chat", Args: cobra.ExactArgs(1)}
	add := &cobra.Command{Use: "add BOX CHANNEL", Short: "Allow an enrolled agent to join a channel", Args: cobra.ExactArgs(2)}
	remove := &cobra.Command{Use: "remove BOX CHANNEL", Short: "Remove channel access and joined clients; keep room enrollment and history", Args: cobra.ExactArgs(2)}
	for _, sub := range []*cobra.Command{create, add, remove} {
		sub.Flags().Uint64("generation", 0, "Require this desired generation (default the catalog read immediately before this change)")
		sub.Flags().Bool("json", false, "Print desired channel access and observed applied status as JSON")
		sub.RunE = runChatChannelMutation
	}
	for _, sub := range []*cobra.Command{add, remove} {
		sub.ValidArgsFunction = completeChatChannelMember
	}
	del := &cobra.Command{Use: "delete NAME", Short: "Permanently delete a channel with its history, attachments and settings", Args: cobra.ExactArgs(1)}
	del.Long = "Delete a channel from the managed room and purge everything the chat server stored for it: messages, published and staged attachments, bans, topic and moderation. Joined sessions are removed from it; every other channel and connection continues. There is no restore. #space cannot be deleted. A later channel with the same name starts empty, with a new identity."
	del.Flags().Uint64("generation", 0, "Require this desired generation (default the catalog read immediately before this change)")
	del.Flags().Bool("json", false, "Print desired channel access and observed applied status as JSON")
	del.Flags().Bool("yes", false, "Confirm permanent deletion of the channel and everything stored for it")
	del.RunE = runChatChannelMutation
	del.ValidArgsFunction = completeChatChannelName
	cmd.AddCommand(list, create, add, remove, del)
	return cmd
}

func channelRequestFromSnapshot(snapshot chatChannelSnapshot, action string, args []string, generation uint64) (chatChannelRequest, error) {
	input := chatChannelRequest{RoomID: snapshot.RoomID, Generation: generation}
	if input.Generation == 0 {
		input.Generation = snapshot.Generation
	}
	name := args[len(args)-1]
	channel, err := managedchat.NormalizeChannelName(name)
	if err != nil {
		return input, err
	}
	if action == "create" {
		input.Channel = channel
		return input, nil
	}
	for _, c := range snapshot.Channels {
		if c.Name == channel {
			input.ChannelID = c.ID
		}
	}
	if input.ChannelID == "" {
		return input, errors.New("channel is not in the managed catalog")
	}
	if action == "delete" {
		if channel == snapshot.DefaultChannel {
			return input, errors.New("the default channel #space cannot be deleted")
		}
		input.Confirm = true
		return input, nil
	}
	for _, identity := range snapshot.Identities {
		if identity.Name == args[0] && identity.Role == "member" && !identity.Revoked {
			input.MemberID, input.BoxID = identity.ID, identity.BoxID
		}
	}
	if input.MemberID == "" || input.BoxID == "" {
		return input, errors.New("box has no active managed-room enrollment")
	}
	return input, nil
}

func runChatChannelMutation(cmd *cobra.Command, args []string) error {
	generation, _ := cmd.Flags().GetUint64("generation")
	if cmd.Flags().Changed("generation") && generation == 0 {
		return errors.New("generation must be positive")
	}
	if cmd.Name() == "delete" {
		if yes, _ := cmd.Flags().GetBool("yes"); !yes {
			return fmt.Errorf("deleting %s permanently removes its history, attachments and settings; rerun with --yes", args[0])
		}
	}
	var snapshot chatChannelSnapshot
	if err := callChatControllerInto(cmd.Context(), cmd, "chat.channel.list", chatChannelRequest{}, &snapshot); err != nil {
		return err
	}
	input, err := channelRequestFromSnapshot(snapshot, cmd.Name(), args, generation)
	if err != nil {
		return err
	}
	if err := callChatControllerInto(cmd.Context(), cmd, "chat.channel."+cmd.Name(), input, &snapshot); err != nil {
		return err // no automatic retry after a race or lost mutation reply
	}
	return printChatChannelSnapshot(cmd, snapshot)
}

func printChatChannelSnapshot(cmd *cobra.Command, snapshot chatChannelSnapshot) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot)
	}
	state := "pending"
	if snapshot.RuntimeApplied {
		state = "applied"
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Channels: %s (desired generation %d)\n", state, snapshot.Generation); err != nil {
		return err
	}
	for _, channel := range snapshot.Channels {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s  %d permitted identities\n", channel.Name, channel.ID, len(channel.Members)); err != nil {
			return err
		}
	}
	if n := len(snapshot.DeletedChannels); n > 0 {
		last := snapshot.DeletedChannels[n-1]
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %d deleted channel identities (latest %s %s at generation %d)\n", n, last.Name, last.ID, last.Generation); err != nil {
			return err
		}
	}
	for _, pending := range snapshot.Pending {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "  pending:", pending); err != nil {
			return err
		}
	}
	return nil
}

// completeChatChannelName offers deletable channels: every one but #space.
func completeChatChannelName(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()
	var snapshot chatChannelSnapshot
	if err := callChatControllerInto(ctx, cmd, "chat.channel.list", chatChannelRequest{}, &snapshot); err != nil {
		return nil, cobra.ShellCompDirectiveError | cobra.ShellCompDirectiveNoFileComp
	}
	var candidates []string
	for _, channel := range snapshot.Channels {
		name := channel.Name
		if name == snapshot.DefaultChannel {
			continue
		}
		if !strings.HasPrefix(prefix, "#") {
			name = strings.TrimPrefix(name, "#")
		}
		if strings.HasPrefix(name, strings.ToLower(prefix)) {
			candidates = append(candidates, name)
		}
	}
	return candidates, cobra.ShellCompDirectiveNoFileComp
}

func completeChatChannelMember(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()
	var snapshot chatChannelSnapshot
	if err := callChatControllerInto(ctx, cmd, "chat.channel.list", chatChannelRequest{}, &snapshot); err != nil {
		return nil, cobra.ShellCompDirectiveError | cobra.ShellCompDirectiveNoFileComp
	}
	var candidates []string
	if len(args) == 0 {
		for _, identity := range snapshot.Identities {
			if identity.Role == "member" && !identity.Revoked && strings.HasPrefix(identity.Name, prefix) {
				candidates = append(candidates, identity.Name)
			}
		}
	} else {
		for _, channel := range snapshot.Channels {
			name := channel.Name
			if !strings.HasPrefix(prefix, "#") {
				name = strings.TrimPrefix(name, "#")
			}
			if strings.HasPrefix(name, strings.ToLower(prefix)) {
				candidates = append(candidates, name)
			}
		}
	}
	return candidates, cobra.ShellCompDirectiveNoFileComp
}

// readChatChannelCatalog is the host's explicit trusted-refresh path for the
// typed driver. It never joins a channel or resumes a saved preference.
//
//nolint:unused // Consumed by the separately integrated host channel view.
func readChatChannelCatalog(ctx context.Context, cmd *cobra.Command) (chatstream.Catalog, error) {
	target, err := selectControllerTarget(ctx, cmd)
	if err != nil {
		return chatstream.Catalog{}, err
	}
	return readChatChannelCatalogTarget(ctx, target)
}

func readChatChannelCatalogTarget(ctx context.Context, target controllerTarget) (chatstream.Catalog, error) {
	var snapshot chatChannelSnapshot
	tail := &stderrTail{}
	if err := callChatTargetInto(ctx, target, "chat.channel.list", chatChannelRequest{}, &snapshot, tail); err != nil {
		return chatstream.Catalog{}, tail.annotate(err)
	}
	return chatChannelCatalog(snapshot), nil
}

func chatChannelCatalog(snapshot chatChannelSnapshot) chatstream.Catalog {
	header := chatstream.Header{Event: "managed-room", Channel: snapshot.DefaultChannel, Bootstrap: managedchat.Bootstrap{
		Schema: 2, RoomID: snapshot.RoomID, DiskID: snapshot.DiskID, Generation: snapshot.Generation,
		DefaultChannel: snapshot.DefaultChannel, Channels: snapshot.Channels, Identities: snapshot.Identities,
		DeletedChannels: snapshot.DeletedChannels,
	}}
	for _, identity := range snapshot.Identities {
		if identity.Role == "human" && !identity.Revoked {
			header.HumanAccount, header.SelfAccount, header.Nick = identity.Account, identity.Account, identity.Nick
		}
	}
	return chatstream.Catalog{Header: header, RuntimeApplied: snapshot.RuntimeApplied, Pending: snapshot.Pending}
}

// Keep the legacy list response and output unchanged unless explicitly selected.
func runChatChannelList(cmd *cobra.Command, call func(context.Context, *cobra.Command, string, any, any) error) error {
	if desired, _ := cmd.Flags().GetBool("catalog-only"); desired {
		var snapshot chatDesiredCatalog
		if err := call(cmd.Context(), cmd, "chat.channel.catalog", struct{}{}, &snapshot); err != nil {
			return err
		}
		return printDesiredChatCatalog(cmd, snapshot)
	}
	var snapshot chatChannelSnapshot
	if err := call(cmd.Context(), cmd, "chat.channel.list", chatChannelRequest{}, &snapshot); err != nil {
		return err
	}
	return printChatChannelSnapshot(cmd, snapshot)
}

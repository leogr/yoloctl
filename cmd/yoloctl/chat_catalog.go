package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

// This DTO describes one desired manifest. It carries no runtime proof.
type chatDesiredCatalog struct {
	RoomID            string                          `json:"room_id"`
	DiskID            string                          `json:"disk_id"`
	Generation        uint64                          `json:"generation"`
	DefaultChannel    string                          `json:"default_channel"`
	Channels          []managedchat.Channel           `json:"channels"`
	DeletedChannels   []managedchat.DeletedChannel    `json:"deleted_channels,omitempty"`
	Identities        []managedchat.BootstrapIdentity `json:"identities"`
	RuntimeUnobserved bool                            `json:"runtime_unobserved"`
}

// readDesiredChatCatalog makes exactly one bounded, verified room-manifest
// read. Independent live operator authority checks do not load the room.
func readDesiredChatCatalog(ctx context.Context, store managedchat.Store) (chatDesiredCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, chatObservationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return chatDesiredCatalog{}, err
	}
	p := currentControllerPrincipal(ctx)
	if !p.Admin() {
		if err := checkOperatorPrincipal(ctx, p); err != nil {
			return chatDesiredCatalog{}, err
		}
	}
	r, err := chatObservationSnapshot(store)
	if errors.Is(err, os.ErrNotExist) {
		return chatDesiredCatalog{}, errors.New("no managed room; explicitly run chat ensure first")
	}
	if err != nil {
		return chatDesiredCatalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return chatDesiredCatalog{}, err
	}
	if err := validateOperatorChatRuntime(p, "chat.channel.catalog", chatRuntimeRequest{}, r); err != nil {
		return chatDesiredCatalog{}, err
	}
	h, err := chatstream.RoomHeader(r)
	if err != nil {
		return chatDesiredCatalog{}, err
	}
	snapshot := chatDesiredCatalog{RoomID: h.RoomID, DiskID: h.DiskID, Generation: h.Generation,
		DefaultChannel: r.Channel, Channels: r.ChannelCatalog(), DeletedChannels: h.DeletedChannels,
		Identities: h.Identities, RuntimeUnobserved: true}
	if err := ctx.Err(); err != nil {
		return chatDesiredCatalog{}, err
	}
	return snapshot, nil
}

type chatCatalogReadMode string

const (
	chatCatalogSingle   chatCatalogReadMode = "single"
	chatCatalogObserved chatCatalogReadMode = "observed"
)

func chatCatalogMode(cmd *cobra.Command) (chatCatalogReadMode, error) {
	value, err := cmd.Flags().GetString("catalog-read")
	if err != nil {
		return "", err
	}
	switch mode := chatCatalogReadMode(value); mode {
	case chatCatalogSingle, chatCatalogObserved:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid --catalog-read %q; use single or observed", value)
	}
}

func readDesiredChatCatalogTarget(ctx context.Context, target controllerTarget) (chatstream.Catalog, error) {
	var snapshot chatDesiredCatalog
	tail := &stderrTail{}
	if err := callChatTargetInto(ctx, target, "chat.channel.catalog", struct{}{}, &snapshot, tail); err != nil {
		return chatstream.Catalog{}, tail.annotate(err)
	}
	if !snapshot.RuntimeUnobserved {
		return chatstream.Catalog{}, errors.New("desired catalog lacks runtime-not-observed discriminator")
	}
	return snapshot.catalog(), nil
}

func (s chatDesiredCatalog) catalog() chatstream.Catalog {
	c := chatChannelCatalog(chatChannelSnapshot{RoomID: s.RoomID, DiskID: s.DiskID, Generation: s.Generation,
		DefaultChannel: s.DefaultChannel, Channels: s.Channels, DeletedChannels: s.DeletedChannels, Identities: s.Identities})
	c.RuntimeUnobserved = true
	return c
}

func printDesiredChatCatalog(cmd *cobra.Command, s chatDesiredCatalog) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(s)
	}
	w := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(w, "Channels: desired generation %d; server application not checked (use chat status)\n", s.Generation); err != nil {
		return err
	}
	for _, channel := range s.Channels {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d members\n", channel.Name, channel.ID, len(channel.Members)); err != nil {
			return err
		}
	}
	return nil
}

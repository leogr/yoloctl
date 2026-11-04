package main

import (
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// These callbacks run in the authorized native completion worker. The host
// and operator frontends use control.complete; neither reads local fallback
// declarations nor starts/repairs a room to populate suggestions.
func completeChatEnrollBoxes(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if err := cmd.Context().Err(); err != nil {
		return complete(nil, err)
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return complete(nil, err)
	}
	names, err := agentbox.ListNames(spaceScope(cmd))
	if err != nil {
		return complete(nil, err)
	}
	room, err := store.ReadManifestSnapshot()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return complete(nil, err)
	}
	names = slices.DeleteFunc(names, func(name string) bool {
		return !strings.HasPrefix(name, prefix) || room != nil && name == room.Server.Name
	})
	return complete(names, nil)
}

func completeChatParticipantBoxes(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	return completeChatMembers(cmd, prefix, false)
}

func completeChatRevokeTargets(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	return completeChatMembers(cmd, prefix, true)
}

// completeChatMembers lists participant box names, or for revoke every active
// membership's name and, once something is typed, matching member IDs.
func completeChatMembers(cmd *cobra.Command, prefix string, revoke bool) ([]string, cobra.ShellCompDirective) {
	if err := cmd.Context().Err(); err != nil {
		return complete(nil, err)
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return complete(nil, err)
	}
	room, err := store.ReadManifestSnapshot()
	if err != nil {
		return complete(nil, err)
	}
	var values []string
	for _, member := range room.Members {
		if err := cmd.Context().Err(); err != nil {
			return complete(nil, err)
		}
		if member.Revoked {
			continue
		}
		if revoke {
			// A deleted/replaced box's membership is still revocable by name. A
			// 32-hex name would be read as an ID, so only its ID is offered.
			if strings.HasPrefix(member.Name, prefix) && layout.ValidateIdentity(member.Name) != nil {
				values = append(values, member.Name+"\t"+member.ID)
			}
			if prefix != "" && strings.HasPrefix(member.ID, prefix) {
				values = append(values, member.ID+"\t"+member.Name)
			}
			continue
		}
		if !strings.HasPrefix(member.Name, prefix) {
			continue
		}
		cfg, err := agentbox.Load(spaceScope(cmd), member.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return complete(nil, err)
		}
		if cfg.ID == member.BoxID {
			values = append(values, member.Name)
		}
	}
	slices.Sort(values)
	return complete(values, nil)
}

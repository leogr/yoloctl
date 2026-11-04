package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
)

// These host-admin-only recovery operations remain explicitly desired-state
// APIs. Public chat commands use the separate runtime/proof service.
type chatStateRequest struct {
	ServerBox  string `json:"server_box,omitempty"`
	Human      string `json:"human,omitempty"`
	Box        string `json:"box,omitempty"`
	MemberID   string `json:"member_id,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

type chatStateResult struct {
	Room           *managedchat.Room `json:"room"`
	RuntimeApplied bool              `json:"runtime_applied"`
	Dependency     string            `json:"dependency"`
}

func chatInventory(boxes []boxstate.Box) (managedchat.Inventory, error) {
	i := managedchat.Inventory{Complete: true, Declarations: map[string]string{}, Runtime: map[string]bool{}}
	seen := map[string]bool{}
	for _, box := range boxes {
		if seen[box.Name] || box.Declaration == boxstate.DeclarationInvalid || box.Runtime == boxstate.RuntimeUnknown || box.Runtime == boxstate.RuntimeUnavailable {
			return i, errors.New("incomplete or inconsistent managed chat inventory")
		}
		seen[box.Name] = true
		switch box.Runtime {
		case boxstate.RuntimeMissing:
			if box.Container != nil {
				return i, errors.New("managed chat inventory has contradictory runtime state")
			}
		case boxstate.RuntimeRunning, boxstate.RuntimeStopped, boxstate.RuntimeOrphaned:
			if box.Container == nil {
				return i, errors.New("managed chat inventory is missing runtime details")
			}
		default:
			return i, errors.New("managed chat inventory has unknown runtime state")
		}
		if box.Declaration != boxstate.DeclarationDeclared && box.Declaration != boxstate.DeclarationMissing {
			return i, errors.New("managed chat inventory has unknown declaration state")
		}
		if box.Declaration == boxstate.DeclarationDeclared {
			if box.Config == nil || box.Config.Name != box.Name {
				return i, errors.New("managed chat inventory lacks a valid declaration")
			}
			i.Declarations[box.Name] = box.Config.ID
		}
		i.Runtime[box.Name] = box.Container != nil
	}
	return i, i.Validate()
}

func runControllerChatState(ctx context.Context, req controller.Request, out io.Writer) (_ bool, result error) {
	if !strings.HasPrefix(req.Method, "chat.state.") {
		return false, nil
	}
	if len(req.Args) != 0 || len(req.Flags) != 0 {
		return true, errors.New("managed chat state API takes only a typed payload")
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return true, err
	}
	var input chatStateRequest
	if len(req.Payload) > 0 {
		if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 4096); err != nil {
			return true, errors.New("invalid managed chat state request")
		}
	}
	var room *managedchat.Room
	if req.Method == "chat.state.get" {
		if input != (chatStateRequest{}) {
			return true, errors.New("managed chat get takes no fields")
		}
		room, err = store.Load()
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		release, lockErr := spacelock.Acquire(ctx, req.Space, true)
		if lockErr != nil {
			return true, lockErr
		}
		defer release()
		defer completeBeforeUnlock(ctx, &result, nil)
		boxes, inventoryErr := newBoxStateResolver().ResolveSpace(ctx, req.Space, boxstate.RuntimeInventory)
		if inventoryErr != nil {
			return true, inventoryErr
		}
		inventory, inventoryErr := chatInventory(boxes)
		if inventoryErr != nil {
			return true, inventoryErr
		}
		// Permission follows the lock and inventory and precedes the first
		// reservation, identity, token or room write.
		intent := chatIntent("")
		switch req.Method {
		case "chat.state.enroll":
			intent = chatIntent("", input.Box)
		case "chat.state.revoke":
			intent = chatIntent(controller.VerifierChatRevoke)
			store.Record = func(raw []byte) error {
				return controller.RecordDocument(ctx, controller.DocumentIntent{
					Kind: controller.DocumentChat, Present: true, Data: raw,
					UID: os.Geteuid(), GID: os.Getegid(), Mode: 0o600,
				})
			}
		}
		if err := controller.BeginScopedEffects(ctx, intent); err != nil {
			return true, err
		}
		switch req.Method {
		case "chat.state.ensure":
			if input.Box != "" || input.MemberID != "" || input.Generation != 0 {
				return true, errors.New("invalid managed chat ensure fields")
			}
			room, err = store.Ensure(input.ServerBox, input.Human, inventory)
		case "chat.state.enroll":
			if input.ServerBox != "" || input.Human != "" || input.MemberID != "" || input.Generation == 0 {
				return true, errors.New("invalid managed chat enroll fields")
			}
			id, exists := inventory.Declarations[input.Box]
			if !exists {
				return true, errors.New("enrollment requires an existing declared box")
			}
			current, loadErr := store.Load()
			if loadErr != nil {
				return true, loadErr
			}
			if !current.Prepared || current.Generation != input.Generation || input.Box == current.Server.Name || input.Box == current.Human.Name {
				return true, errors.New("managed chat enrollment precondition changed; inspect current desired state")
			}
			if id == "" {
				unlock, lockErr := agentbox.LockConfig(req.Space, input.Box)
				if lockErr != nil {
					return true, lockErr
				}
				defer unlock()
				cfg, loadErr := agentbox.Load(req.Space, input.Box)
				if loadErr != nil {
					return true, loadErr
				}
				cfg.ID = agentbox.NewIdentity()
				if err := cfg.Save(); err != nil {
					return true, err
				}
				id = cfg.ID
				inventory.Declarations[input.Box] = id
			}
			room, err = store.Enroll(managedchat.Box{Name: input.Box, ID: id}, input.Generation, inventory)
		case "chat.state.revoke":
			if input.ServerBox != "" || input.Human != "" || input.Box != "" || input.Generation == 0 {
				return true, errors.New("invalid managed chat revoke fields")
			}
			room, err = store.Revoke(input.MemberID, input.Generation, inventory)
		default:
			return true, errors.New("unknown managed chat state operation")
		}
	}
	if err != nil {
		return true, err
	}
	return true, json.NewEncoder(out).Encode(chatStateResult{Room: room, Dependency: "AIRC Unix transports, membership reload and managed session integration are required"})
}

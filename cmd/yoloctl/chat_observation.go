package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

const chatObservationTimeout = 5 * time.Second

// Production always verifies the native store. Tests replace only this provider
// with a private disk fixture so no active controller paths are touched.
var nativeChatStoreFn = controller.NativeChatStore

// Snapshot seam for deterministic interleavings; production always uses the
// strict descriptor-pinned native read.
var chatObservationSnapshot = func(store managedchat.Store) (*managedchat.Room, error) { return store.ReadManifestSnapshot() }

// observeManagedChatRead never takes the mutation lock or repairs state. The
// catalog is one strict atomic native snapshot; runtime evidence is bounded and
// explicitly pending when unavailable. Mutations use the locked inventory path.
func observeManagedChatRead(ctx context.Context, space string, store managedchat.Store) (managedchat.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, chatObservationTimeout)
	defer cancel()
	return observeManagedChatAttempt(ctx, space, store, nil)
}

var errChatObservationChanged = errors.New("managed chat changed during observation; inspect the current catalog")

// The binding covers room/disk identity, the human ID (which derives both
// SelfAccount and HumanAccount), canonical human name and server ownership.
// The nickname and memberships may change; generations may only advance.
type chatObservationBinding struct {
	room, disk, human, humanName, server, serverName string
	generation                                       uint64
}

func (b *chatObservationBinding) check(r *managedchat.Room) error {
	if b.room == "" {
		*b = chatObservationBinding{r.ID, r.DiskID, r.Human.ID, r.Human.Name, r.Server.ID, r.Server.Name, r.Generation}
		return nil
	}
	if b.room != r.ID || b.disk != r.DiskID || b.human != r.Human.ID || b.humanName != r.Human.Name || b.server != r.Server.ID || b.serverName != r.Server.Name {
		return errors.New("managed chat identity changed during observation; reconnect explicitly")
	}
	if r.Generation < b.generation {
		return errors.New("managed chat generation regressed during observation")
	}
	b.generation = r.Generation
	return nil
}

// Only the list read retries; both complete attempts share this one context.
func observeManagedChatCatalog(ctx context.Context, space string, store managedchat.Store) (managedchat.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, chatObservationTimeout)
	defer cancel()
	binding := &chatObservationBinding{}
	var result managedchat.Status
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return managedchat.Status{}, err
		}
		result, err = observeManagedChatAttempt(ctx, space, store, binding)
		if !errors.Is(err, errChatObservationChanged) {
			return result, err
		}
	}
	return result, err
}

func observeManagedChatAttempt(ctx context.Context, space string, store managedchat.Store, binding *chatObservationBinding) (managedchat.Status, error) {
	if err := checkOperatorChatRuntime(ctx, "chat.status", chatRuntimeRequest{}, store); err != nil {
		return managedchat.Status{}, err
	}
	room, err := chatObservationSnapshot(store)
	if errors.Is(err, os.ErrNotExist) && (binding == nil || binding.room == "") {
		return managedchat.Status{}, nil
	}
	if err != nil {
		return managedchat.Status{}, err
	}
	if binding != nil {
		if err := binding.check(room); err != nil {
			return managedchat.Status{}, err
		}
	}
	result := managedchat.Status{Room: room}
	if room.Prepared && room.RuntimeEnabled {
		// Missing/substituted private state is an error, not a runtime absence
		// that could authorize automatic recovery while opening a view.
		if _, err := store.Expected(room); err != nil {
			return managedchat.Status{}, err
		}
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
	if err != nil {
		result.Pending = []string{"runtime inventory unavailable: " + err.Error()}
	} else {
		result, err = observeManagedChatRoom(ctx, space, store, room, boxes)
		if err != nil {
			result = managedchat.Status{Room: room, Pending: []string{"runtime observation unavailable: " + err.Error()}}
		}
	}
	// The multi-file observation must not publish a mixed-generation catalog.
	current, err := chatObservationSnapshot(store)
	if err != nil {
		return managedchat.Status{}, err
	}
	if binding != nil {
		if err := binding.check(current); err != nil {
			return managedchat.Status{}, err
		}
	}
	if !reflect.DeepEqual(room, current) {
		return managedchat.Status{}, errChatObservationChanged
	}
	if err := ctx.Err(); err != nil {
		result.RuntimeApplied, result.ServerReady, result.SetupRequired = false, false, false
		result.Pending = append(result.Pending, "runtime observation deadline reached")
	}
	return result, nil
}

package controller

import (
	"errors"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"gopkg.in/yaml.v3"
)

// NativeChatStore is only available after LoadRuntime has verified the native
// disk. It deliberately has no host/bootstrap fallback. Mutations additionally
// require admission/receipts and the native space lock in the worker dispatcher.
func NativeChatStore() (managedchat.Store, error) {
	c := spaceenv.Current()
	if c == nil {
		return managedchat.Store{}, errors.New("managed chat requires the native controller")
	}
	if err := VerifyStoreDirectories(c.DiskMount); err != nil {
		return managedchat.Store{}, err
	}
	return managedchat.Store{Disk: c.DiskMount, DiskID: c.DiskID, UID: c.UID, GID: c.GID}, nil
}

// ReidentifyImportedChat changes public declaration identities before publishing
// a new store. Secrets are generated inside the destination VM by Prepare, never
// placed in the host seed, argv or receipts. Existing archive account behavior
// is unchanged. Call only for a fresh disk before saving the recovery seed.
func (b *StoreBundle) ReidentifyImportedChat(diskID string) error {
	if len(b.Chat) == 0 {
		return nil
	}
	room, err := managedchat.Parse(b.Chat)
	if err != nil {
		return err
	}
	next, err := room.Reidentify(diskID)
	if err != nil {
		return err
	}
	for name, raw := range b.Boxes {
		box, err := agentbox.ParseManifest(raw)
		if err != nil {
			return err
		}
		if box.ID != "" {
			box.ID = managedchat.ImportedID(diskID, box.ID)
		}
		b.Boxes[name], err = yaml.Marshal(box)
		if err != nil {
			return err
		}
	}
	b.Chat, err = next.Marshal()
	return err
}

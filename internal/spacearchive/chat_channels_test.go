package spacearchive

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestChannelArchivePreservesAccessRelationshipsWithFreshAuthority(t *testing.T) {
	disk, boot, room := chatFixture(t)
	source := managedchat.Store{Disk: disk, DiskID: boot.DiskID, UID: boot.UID, GID: boot.GID}
	member := room.Members[0]
	inventory := managedchat.Inventory{Complete: true, Declarations: map[string]string{member.Name: member.BoxID}, Runtime: map[string]bool{}}
	room, err := source.CreateChannel(room.ID, "#build", room.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	room, err = source.SetChannelMember(room.ID, room.Channels[0].ID, member.ID, member.BoxID, false, room.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := room.Marshal()
	oldToken := token(t, disk, room, member.ID)
	state := stateFile(t)
	if err := WriteState(t.Context(), state, disk, boot); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(state.Name())
	if err != nil || bytes.Contains(raw, oldToken) {
		t.Fatal("archive contains old credential or could not be inspected", err)
	}
	sum, size, err := StateDigest(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	newBoot := *boot
	newBoot.DiskID, newBoot.DiskUUID = managedchat.NewID(), "independent-channel-disk"
	dest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, "account-state"), 0700); err != nil {
		t.Fatal(err)
	}
	restore := func() error {
		if _, err := state.Seek(0, io.SeekStart); err != nil {
			return err
		}
		return RestoreState(t.Context(), state, dest, &newBoot, sum, size)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	next, err := room.Reidentify(newBoot.DiskID)
	if err != nil {
		t.Fatal(err)
	}
	newToken := token(t, dest, next, next.Members[0].ID)
	if bytes.Equal(newToken, oldToken) {
		t.Fatal("import reused source authentication")
	}
	// Repeat the existing explicit import-recovery path, never overwriting a
	// changed data file or turning saved credentials into source authority.
	if err := os.Remove(filepath.Join(dest, ".controller/imported")); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(newToken, token(t, dest, next, next.Members[0].ID)) {
		t.Fatal("recovery rotated destination credential")
	}
	bundle, err := readDeclarations(disk, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.ReidentifyImportedChat(newBoot.DiskID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.PublishStore(dest, &newBoot, *bundle); err != nil {
		t.Fatal(err)
	}
	store := managedchat.Store{Disk: dest, DiskID: newBoot.DiskID, UID: newBoot.UID, GID: newBoot.GID}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != 2 || len(got.Channels) != 2 || got.Prepared {
		t.Fatal("restored catalog or explicit preparation boundary lost")
	}
	for n, channel := range room.Channels {
		if got.Channels[n].Name != channel.Name || got.Channels[n].ID == channel.ID {
			t.Fatal("restored channel lost its name or shares old identity")
		}
		for _, id := range channel.Members {
			if slices.Contains(got.Channels[n].Members, id) || !slices.Contains(got.Channels[n].Members, managedchat.ImportedID(newBoot.DiskID, id)) {
				t.Fatal("restored ACL did not remap its relationships")
			}
		}
	}
	if slices.Contains(got.Channels[0].Members, got.Members[0].ID) || !slices.Contains(got.Channels[1].Members, got.Members[0].ID) {
		t.Fatal("import changed channel inclusion/exclusion")
	}
	base, _ := layout.ChatRoomRelative(got.ID)
	history := filepath.Join(dest, base, "server/data/history.sqlite")
	if raw, err := os.ReadFile(history); err != nil || string(raw) != "synthetic durable history" {
		t.Fatal("synthetic durable data lost", err)
	}
	if err := os.Remove(filepath.Join(dest, ".controller/imported")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, []byte("destination changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err == nil {
		t.Fatal("different destination data overwritten on recovery")
	}
	if raw, _ := os.ReadFile(history); string(raw) != "destination changed" {
		t.Fatal("failed restore lost destination data")
	}
	originalAfter, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	after, _ := originalAfter.Marshal()
	if !bytes.Equal(original, after) || !bytes.Equal(oldToken, token(t, disk, room, member.ID)) {
		t.Fatal("import mutated source state")
	}
}

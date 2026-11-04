package spacearchive

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"gopkg.in/yaml.v3"
)

func chatFixture(t *testing.T) (string, *spaceconfig.Bootstrap, *managedchat.Room) {
	t.Helper()
	disk, boot := fixture(t)
	boot.UID, boot.GID = os.Geteuid(), os.Getegid()
	box := &agentbox.Config{SchemaVersion: 1, Name: "coder", Space: boot.Name, ID: agentbox.NewIdentity(), Agent: "none", Account: "none", Image: "img:1"}
	raw, err := yaml.Marshal(box)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(disk, "config/boxes/coder.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := managedchat.Store{Disk: disk, DiskID: boot.DiskID, UID: boot.UID, GID: boot.GID}
	i := managedchat.Inventory{Complete: true, Declarations: map[string]string{"coder": box.ID}, Runtime: map[string]bool{}}
	r, err := s.Ensure("", "person", i)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Enroll(managedchat.Box{Name: "coder", ID: box.ID}, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	if err := safepath.WriteFile(disk, filepath.Join(base, "server/data/history.sqlite"), []byte("synthetic durable history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(disk, filepath.Join(base, "server/data/artifacts/blob"), []byte("artifact bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	return disk, boot, r
}

func token(t *testing.T, disk string, r *managedchat.Room, member string) []byte {
	t.Helper()
	rel, _ := layout.ChatMemberRelative(r.ID, member)
	raw, err := safepath.ReadFile(disk, filepath.Join(rel, "token"), 128)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChatArchiveIndependentIdentityAndInterruptedRestore(t *testing.T) {
	disk, boot, room := chatFixture(t)
	source := managedchat.Store{Disk: disk, DiskID: boot.DiskID, UID: boot.UID, GID: boot.GID}
	var enableErr error
	room, enableErr = source.EnableRuntime(room.Generation, managedchat.Inventory{Complete: true, Declarations: map[string]string{"coder": room.Members[0].BoxID}, Runtime: map[string]bool{}})
	if enableErr != nil {
		t.Fatal(enableErr)
	}
	if _, err := source.Render(room); err != nil {
		t.Fatal(err)
	}
	oldToken := token(t, disk, room, room.Members[0].ID)
	state := stateFile(t)
	if err := WriteState(t.Context(), state, disk, boot); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(state.Name())
	if bytes.Contains(raw, oldToken) {
		t.Fatal("source room authentication copied into archive")
	}
	sum, size, err := StateDigest(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	newBoot := *boot
	newBoot.DiskID = managedchat.NewID()
	newBoot.DiskUUID = "independent-uuid"
	dest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, "account-state"), 0700); err != nil {
		t.Fatal(err)
	}
	restore := func() error {
		_, _ = state.Seek(0, io.SeekStart)
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
	if next.ID == room.ID || next.Server.ID == room.Server.ID || next.Members[0].ID == room.Members[0].ID || bytes.Equal(oldToken, newToken) {
		t.Fatal("restored room shares identity or authentication")
	}
	// Power loss after data/credentials publication, before import completion.
	if err := os.Remove(filepath.Join(dest, ".controller/imported")); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(newToken, token(t, dest, next, next.Members[0].ID)) {
		t.Fatal("retry rotated destination credentials")
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
	s := managedchat.Store{Disk: dest, DiskID: newBoot.DiskID, UID: newBoot.UID, GID: newBoot.GID}
	got, err := s.Load()
	if err != nil || got.ID != next.ID || got.Prepared || !got.RuntimeEnabled || got.Members[0].BoxID == room.Members[0].BoxID {
		t.Fatal("imported desired state was not independently rebound", err)
	}
	base, _ := layout.ChatRoomRelative(next.ID)
	history := filepath.Join(dest, base, "server/data/history.sqlite")
	if raw, err := os.ReadFile(history); err != nil || string(raw) != "synthetic durable history" {
		t.Fatal("chat history lost", err)
	}
	if err := os.Remove(filepath.Join(dest, ".controller/imported")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, []byte("new history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err == nil {
		t.Fatal("different partial import overwritten")
	}
	if raw, _ := os.ReadFile(history); string(raw) != "new history" {
		t.Fatal("failed recovery destroyed destination changes")
	}
	originalBase, _ := layout.ChatRoomRelative(room.ID)
	if raw, _ := os.ReadFile(filepath.Join(disk, originalBase, "server/data/history.sqlite")); string(raw) != "synthetic durable history" {
		t.Fatal("restore modified source")
	}
}

func TestChatArchiveRejectsUnsafeOrIncompleteDataBeforePublication(t *testing.T) {
	disk, boot, room := chatFixture(t)
	bundle, err := readDeclarations(disk, boot)
	if err != nil {
		t.Fatal(err)
	}
	decl, _ := json.Marshal(bundle)
	for _, header := range []*tar.Header{
		{Name: "chat/" + room.ID + "/members", Typeflag: tar.TypeDir, Mode: 0700},
		{Name: "chat/" + room.ID + "/data/link", Typeflag: tar.TypeSymlink, Linkname: "../../../config/space.yaml", Mode: 0600},
		{Name: "chat/" + room.ID + "/data/../escape", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "chat/" + room.ID + "/data/public", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "chat/" + room.ID + "/data/pipe", Typeflag: tar.TypeFifo, Mode: 0600},
		{Name: "chat/" + managedchat.NewID() + "/data", Typeflag: tar.TypeDir, Mode: 0700},
		nil, // no data root at all
	} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "declarations.json", Typeflag: tar.TypeReg, Size: int64(len(decl)), Mode: 0600})
		_, _ = tw.Write(decl)
		if header != nil {
			_ = tw.WriteHeader(&tar.Header{Name: "chat/" + room.ID, Typeflag: tar.TypeDir, Mode: 0700})
			_ = tw.WriteHeader(&tar.Header{Name: "chat/" + room.ID + "/data", Typeflag: tar.TypeDir, Mode: 0700})
			_ = tw.WriteHeader(header)
		}
		_ = tw.Close()
		dest := t.TempDir()
		_ = os.Mkdir(filepath.Join(dest, "account-state"), 0700)
		newBoot := *boot
		newBoot.DiskID = managedchat.NewID()
		sum := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
		if err := RestoreState(t.Context(), bytes.NewReader(buf.Bytes()), dest, &newBoot, sum, int64(buf.Len())); err == nil {
			t.Fatal("unsafe or incomplete managed chat archive accepted", header)
		}
		if _, err := os.Lstat(filepath.Join(dest, layout.ChatState)); !os.IsNotExist(err) {
			t.Fatal("failed validation published chat data")
		}
	}
}

func TestChatExportRejectsSymlinksAndUnownedState(t *testing.T) {
	for _, kind := range []string{"data link", "ownership marker", "unprepared"} {
		t.Run(kind, func(t *testing.T) {
			disk, boot, room := chatFixture(t)
			base, _ := layout.ChatRoomRelative(room.ID)
			switch kind {
			case "data link":
				if err := os.Symlink("/etc/passwd", filepath.Join(disk, base, "server/data/link")); err != nil {
					t.Fatal(err)
				}
			case "ownership marker":
				if err := os.WriteFile(filepath.Join(disk, base, "owner.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unprepared":
				room.Prepared = false
				raw, _ := room.Marshal()
				if err := safepath.WriteFile(disk, layout.ChatManifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := WriteState(t.Context(), io.Discard, disk, boot); err == nil {
				t.Fatal("unsafe managed chat state exported")
			}
		})
	}
}

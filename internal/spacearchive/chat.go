package spacearchive

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

// Only server-owned durable data is copied. Credentials and generated server
// configuration are projections of the declaration and are rebuilt privately
// in the destination, so a restored space never accepts the old room's tokens.
func writeChatState(ctx context.Context, tw *tar.Writer, disk string, boot *spaceconfig.Bootstrap, raw []byte) error {
	if len(raw) == 0 {
		if _, err := os.Lstat(filepath.Join(disk, layout.ChatState)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("native chat state exists without its ownership declaration")
		}
		return nil
	}
	store := managedchat.Store{Disk: disk, DiskID: boot.DiskID, UID: boot.UID, GID: boot.GID}
	room, err := store.Load()
	if err != nil {
		return err
	}
	if !room.Prepared {
		return errors.New("managed chat storage preparation is incomplete; inspect before exporting")
	}
	if err := store.Verify(room); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(disk, layout.ChatState))
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != room.ID {
		return errors.New("native chat inventory contains unrelated room state")
	}
	base, _ := layout.ChatRoomRelative(room.ID)
	for _, name := range []string{path.Join("chat", room.ID), path.Join("chat", room.ID, "data")} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
			return err
		}
	}
	return writeTreeChecked(ctx, tw, filepath.Join(disk, base, "server/data"), path.Join("chat", room.ID, "data"), nil, func(h *tar.Header) error {
		switch h.Typeflag {
		case tar.TypeDir:
			h.Mode = 0700
		case tar.TypeReg:
			h.Mode = 0600
		}
		return validateChatEntry(h, room)
	})
}

func validateChatEntry(h *tar.Header, room *managedchat.Room) error {
	if room == nil || h.Name != path.Clean(h.Name) || strings.ContainsAny(h.Name, "\\\x00") || h.Size < 0 || h.Size > MaxState {
		return errors.New("unsafe managed chat archive entry")
	}
	base := path.Join("chat", room.ID)
	data := path.Join(base, "data")
	if h.Name != base && h.Name != data && !strings.HasPrefix(h.Name, data+"/") {
		return errors.New("managed chat entry escapes declared server data")
	}
	if h.Name == base || h.Name == data {
		if h.Typeflag != tar.TypeDir {
			return errors.New("managed chat roots must be directories")
		}
	}
	if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg {
		return errors.New("managed chat data cannot contain links, sockets or special files")
	}
	want := int64(0600)
	if h.Typeflag == tar.TypeDir {
		want = 0700
	}
	if h.Mode != want {
		return errors.New("managed chat archive entries must be private")
	}
	return nil
}

func restoreChatState(ctx context.Context, stage, disk string, boot *spaceconfig.Bootstrap, raw []byte) error {
	if len(raw) == 0 {
		if _, err := os.Lstat(filepath.Join(disk, layout.ChatState)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("destination contains unrelated native chat state")
		}
		return nil
	}
	original, err := managedchat.Parse(raw)
	if err != nil {
		return err
	}
	room, err := original.Reidentify(boot.DiskID)
	if err != nil {
		return err
	}
	base, _ := layout.ChatRoomRelative(room.ID)
	for _, rel := range []string{layout.ChatState, base, filepath.Join(base, "server")} {
		if err := safepath.PrivateDirectory(disk, rel, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(disk, layout.ChatState))
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != room.ID {
		return errors.New("destination contains unrelated native chat state")
	}
	source := filepath.Join(stage, "chat", original.ID, "data")
	target := filepath.Join(disk, base, "server/data")
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return errors.New("managed chat import destination is not a directory")
		}
		a, err := treeDigest(ctx, source)
		if err != nil {
			return err
		}
		b, err := treeDigest(ctx, target)
		if err != nil {
			return err
		}
		if a != b {
			return errors.New("partial managed chat import differs; destination retained for inspection")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(source, target); err != nil {
			return err
		}
	} else {
		return err
	}
	if err := atomicfile.SyncDir(filepath.Dir(target)); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	// Generated secrets are created only in their final native leaves. Retrying
	// an interrupted restore verifies/reuses them instead of rotating again.
	store := managedchat.Store{Disk: disk, DiskID: boot.DiskID, UID: boot.UID, GID: boot.GID}
	return store.Prepare(room)
}

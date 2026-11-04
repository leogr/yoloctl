package vmshare

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	fx "github.com/pkg/sftp/v2/encoding/ssh/filexfer"
	"github.com/pkg/sftp/v2/encoding/ssh/filexfer/openssh"
)

func TestNamespaceRepliesSyncAffectedParents(t *testing.T) {
	s, err := Open(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, dir := range []string{"a", "b"} {
		if err := s.root.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var synced []string
	s.syncDir = func(f *os.File) error {
		st, err := f.Stat()
		if err != nil {
			return err
		}
		for _, dir := range []string{"a", "b"} {
			want, err := s.root.Stat(dir)
			if err != nil {
				return err
			}
			if os.SameFile(st, want) {
				synced = append(synced, dir)
				return f.Sync()
			}
		}
		t.Fatal("synced an unrelated parent")
		return nil
	}
	ctx := context.Background()
	for _, test := range []struct {
		name, want string
		op         func() error
	}{
		{"create", "a", func() error {
			f, err := s.Open(ctx, &fx.OpenPacket{Filename: "/a/file", PFlags: fx.FlagWrite | fx.FlagCreate})
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"existing create", "", func() error {
			f, err := s.Open(ctx, &fx.OpenPacket{Filename: "/a/file", PFlags: fx.FlagWrite | fx.FlagCreate})
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"rename", "ab", func() error {
			return s.POSIXRename(ctx, &openssh.POSIXRenameExtendedPacket{OldPath: "/a/file", NewPath: "/b/file"})
		}},
		{"hardlink", "ba", func() error {
			return s.Hardlink(ctx, &openssh.HardlinkExtendedPacket{OldPath: "/b/file", NewPath: "/a/hard"})
		}},
		{"symlink", "a", func() error { return s.Symlink(ctx, &fx.SymlinkPacket{TargetPath: "hard", LinkPath: "/a/link"}) }},
		{"unlink", "a", func() error { return s.Remove(ctx, &fx.RemovePacket{Path: "/a/link"}) }},
		{"mkdir", "b", func() error { return s.Mkdir(ctx, &fx.MkdirPacket{Path: "/b/dir"}) }},
		{"rmdir", "b", func() error { return s.Rmdir(ctx, &fx.RmdirPacket{Path: "/b/dir"}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			synced = nil
			if err := test.op(); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(synced, ""); got != test.want {
				t.Fatalf("synced %q, want %q", got, test.want)
			}
		})
	}
	synced = nil
	if err := s.Remove(ctx, &fx.RemovePacket{Path: "/a/missing"}); err == nil || len(synced) != 0 {
		t.Fatal("failed operation synced")
	}
}

func TestNamespaceSyncKeepsOriginalDirectoryAcrossHostRename(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.root.Mkdir("parent", 0700); err != nil {
		t.Fatal(err)
	}
	before, err := s.root.Stat("parent")
	if err != nil {
		t.Fatal(err)
	}
	s.syncDir = func(f *os.File) error {
		if err := s.root.Rename("parent", "moved"); err != nil {
			return err
		}
		if err := s.root.Mkdir("parent", 0700); err != nil {
			return err
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(before, after) {
			t.Fatal("sync reopened the replacement parent")
		}
		return f.Sync()
	}
	if err := s.Mkdir(t.Context(), &fx.MkdirPacket{Path: "/parent/child"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved/child")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "parent/child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement changed: %v", err)
	}
}

func TestNamespaceSyncFailureIsReportedAfterMutation(t *testing.T) {
	s, err := Open(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.syncDir = func(*os.File) error { return syscall.EIO }
	if err := s.Mkdir(t.Context(), &fx.MkdirPacket{Path: "/created"}); !errors.Is(err, syscall.EIO) {
		t.Fatalf("lost sync error: %v", err)
	}
	if _, err := s.root.Stat("created"); err != nil {
		t.Fatal("failed sync must not pretend to roll back the mutation", err)
	}
	f, err := s.Open(t.Context(), &fx.OpenPacket{Filename: "/file", PFlags: fx.FlagWrite | fx.FlagCreate})
	if f != nil || !errors.Is(err, syscall.EIO) {
		t.Fatalf("open acknowledged unsynced creation: %v %v", f, err)
	}
}

func TestCreateThroughRelativeSymlinkRetainsGrantBoundary(t *testing.T) {
	c, dir := client(t, true)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../target", filepath.Join(dir, "sub/link")); err != nil {
		t.Fatal(err)
	}
	f, err := c.Create("/sub/link")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("inside")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	data, err := os.ReadFile(filepath.Join(dir, "target"))
	if err != nil || string(data) != "inside" {
		t.Fatalf("dangling symlink create: %q %v", data, err)
	}
	if err := os.Symlink("../../outside", filepath.Join(dir, "sub/escape")); err != nil {
		t.Fatal(err)
	}
	if f, err := c.Create("/sub/escape"); err == nil {
		_ = f.Close()
		t.Fatal("created outside grant")
	}
	if f, err := c.OpenFile("/sub/link", os.O_WRONLY|os.O_CREATE|os.O_EXCL); err == nil {
		_ = f.Close()
		t.Fatal("exclusive create followed symlink")
	}
}

package boxcache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClearRecreatesOwnedLeafAndLeavesToolsAndPeerData(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	if err := ensure(root, "one", uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := ensure(root, "peer", uid, gid); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(root, "one")
	if err := os.Mkdir(filepath.Join(leaf, "modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "modules", "download"), make([]byte, 16384), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "installed-tool")
	if err := os.WriteFile(outside, []byte("keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(leaf, "outside")); err != nil {
		t.Fatal(err)
	}
	bytes, err := clear(t.Context(), root, "one", uid, gid)
	if err != nil || bytes == 0 {
		t.Fatal("clear did not report removed cache footprint", bytes, err)
	}
	children, err := os.ReadDir(leaf)
	if err != nil || len(children) != 0 {
		t.Fatal("cache not empty", children, err)
	}
	if err := ensure(root, "one", uid, gid); err != nil {
		t.Fatal("clear changed private ownership", err)
	}
	for _, path := range []string{outside, filepath.Join(root, "peer")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("cleared unrelated state", err)
		}
	}
}

func TestClearCancelledOrUnverifiedNeverDeletes(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	if err := ensure(root, "one", uid, gid); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "one", "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := clear(ctx, root, "one", uid, gid); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := clear(t.Context(), root, "one", uid+1, gid); err == nil {
		t.Fatal("adopted foreign owner")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("removed retained cache", err)
	}
	if n, err := clear(t.Context(), root, "missing", uid, gid); err != nil || n != 0 {
		t.Fatal("missing cache must be a no-op", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatal("created a missing cache", err)
	}
}

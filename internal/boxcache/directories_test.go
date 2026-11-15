package boxcache

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFeatureCacheDirectoriesSurviveClearWithoutFollowingLinks(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	if err := ensure(root, "one", uid, gid); err != nil {
		t.Fatal(err)
	}
	dirs := []string{"cargo/git", "cargo/registry"}
	if err := prepareDirectories(t.Context(), root, "one", uid, gid, dirs); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(root, "one/cargo/git"))
	if err != nil {
		t.Fatal(err)
	}
	st := before.Sys().(*syscall.Stat_t)
	if before.Mode().Perm() != 0o700 || st.Uid != uint32(uid) || st.Gid != uint32(gid) {
		t.Fatal("wrong directory owner/mode", before, st)
	}
	if err := os.WriteFile(filepath.Join(root, "one/cargo/git/cached"), make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	freed, err := clear(t.Context(), root, "one", uid, gid, dirs...)
	if err != nil || freed < 8192 {
		t.Fatal("clear", freed, err)
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, "one", dir))
		if err != nil || len(entries) != 0 {
			t.Fatal(dir, entries, err)
		}
	}
	outside := t.TempDir()
	if err := os.Remove(filepath.Join(root, "one/cargo/git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "one/cargo/git")); err != nil {
		t.Fatal(err)
	}
	if err := prepareDirectories(t.Context(), root, "one", uid, gid, []string{"cargo/git/escape"}); err == nil {
		t.Fatal("followed a cache component link")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("outside directory changed", entries, err)
	}
}

func TestFeatureCacheDirectoriesValidateBeforeEffectsAndKeepMissingMissing(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	if err := prepareDirectories(t.Context(), root, "missing", uid, gid, []string{"cargo/git"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("created missing cache", err)
	}
	if err := ensure(root, "one", uid, gid); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "one/keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"", ".", "..", "../account", "/home", "cargo/../git", "cargo//git", "cargo/./git"} {
		if _, err := clear(t.Context(), root, "one", uid, gid, dir); err == nil {
			t.Fatal("accepted", dir)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatal("cleared before invalid-path refusal", dir, err)
		}
	}
}

func TestClearEmptyCacheRestoresNewFeatureDirectories(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	if err := ensure(root, "empty", uid, gid); err != nil {
		t.Fatal(err)
	}
	freed, err := clear(t.Context(), root, "empty", uid, gid, "cargo/git", "cargo/registry")
	if err != nil || freed != 0 {
		t.Fatal("new directory allocation is not freed bytes", freed, err)
	}
	for _, dir := range []string{"cargo/git", "cargo/registry"} {
		if _, err := os.Stat(filepath.Join(root, "empty", dir)); err != nil {
			t.Fatal(err)
		}
	}
}

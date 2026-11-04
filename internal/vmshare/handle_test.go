package vmshare

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenHandleMetadataAfterRenameAndUnlink(t *testing.T) {
	c, dir := client(t, true)
	f, err := c.Create("/before")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write([]byte("original")); err != nil {
		t.Fatal(err)
	}
	if err = c.Rename("/before", "/after"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "before"), []byte("replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil || st.Size() != 8 {
		t.Fatalf("stat wrong object: %v %v", st, err)
	}
	if err = f.Chmod(0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(filepath.Join(dir, "after"))
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Stat(filepath.Join(dir, "before"))
	if err != nil {
		t.Fatal(err)
	}
	if original.Mode().Perm() != 0600 || replacement.Mode().Perm() != 0644 {
		t.Fatal("chmod wrong object")
	}
	if err = c.Remove("/after"); err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(2); err != nil {
		t.Fatalf("truncate after unlink: %v", err)
	}
	st, err = f.Stat()
	if err != nil || st.Size() != 2 {
		t.Fatalf("stat after unlink: %v %v", st, err)
	}
	if err = f.Sync(); err != nil {
		t.Fatalf("sync after unlink: %v", err)
	}
}

func TestPathMetadataDoesNotRequireReadPermission(t *testing.T) {
	c, dir := client(t, true)
	name := filepath.Join(dir, "private")
	if err := os.WriteFile(name, []byte("private"), 0); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err := c.Chtimes("/private", stamp, stamp); err != nil {
		t.Fatalf("timestamp on mode-000 owned file: %v", err)
	}
	if err := c.Chmod("/private", 0600); err != nil {
		t.Fatalf("restore read permission: %v", err)
	}
	st, err := os.Stat(name)
	if err != nil || st.Mode().Perm() != 0600 || !st.ModTime().Equal(stamp) {
		t.Fatalf("metadata not applied: %v %v", st, err)
	}
}

func TestReadOnlyHandleCannotChangeMode(t *testing.T) {
	c, dir := client(t, false)
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("read"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := c.Open("/file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err = f.Chmod(0600); err == nil {
		t.Fatal("read-only grant allowed fchmod")
	}
	st, err := os.Stat(filepath.Join(dir, "file"))
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatal("read-only grant changed")
	}
}

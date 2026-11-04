package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedReadAndDurableRemoval(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "accounts/test.yaml", []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "accounts/test.yaml", 3); err == nil {
		t.Fatal("read exceeded limit")
	}
	got, err := ReadFile(root, "accounts/test.yaml", 7)
	if err != nil || string(got) != "private" {
		t.Fatalf("read: %q, %v", got, err)
	}
	if err := os.Symlink("test.yaml", filepath.Join(root, "accounts/link.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "accounts/link.yaml", 99); err == nil {
		t.Fatal("read followed symlink")
	}
	if err := RemoveFile(root, "accounts/link.yaml"); err == nil {
		t.Fatal("removal accepted symlink")
	}
	if err := RemoveFile(root, "accounts/test.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "accounts/test.yaml")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

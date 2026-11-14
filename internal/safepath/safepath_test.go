package safepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesFinalSymlinkWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(outside, []byte("host-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "auth.json")
	if err := os.Symlink(outside, dest); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := WriteFile(root, "auth.json", []byte("account-login"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "host-original" {
		t.Fatalf("outside target changed: got %q, err %v", got, err)
	}
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("destination mode = %v, want regular file replacing the symlink", info.Mode())
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "account-login" {
		t.Fatalf("destination = %q, err %v", got, err)
	}
}

func TestOperationsRejectSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(root, "nested")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := WriteFile(root, "nested/auth.json", []byte("secret"), 0o600); err == nil {
		t.Fatal("WriteFile followed a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "auth.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("outside file unexpectedly created: %v", err)
	}
}

func TestRegularFileExistsRejectsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(outside, []byte("host-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "auth.json")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if exists, err := RegularFileExists(root, "auth.json"); err == nil || exists {
		t.Fatalf("RegularFileExists = (%v, %v), want false + symlink error", exists, err)
	}
}

func TestCreateFileDoesNotReplaceExistingEntries(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "fact")
			switch kind {
			case "file":
				if err := os.WriteFile(dest, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", dest); err != nil {
					t.Skip(err)
				}
			case "directory":
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := CreateFile(root, "fact", []byte("new"), 0600); !errors.Is(err, fs.ErrExist) {
				t.Fatal("existing entry replaced", err)
			}
			info, err := os.Lstat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" && info.Mode()&os.ModeSymlink == 0 || kind == "directory" && !info.IsDir() {
				t.Fatal("entry type changed", info.Mode())
			}
			if kind == "file" {
				body, err := os.ReadFile(dest)
				if err != nil || string(body) != "old" {
					t.Fatal("existing bytes changed", err)
				}
			}
		})
	}
	root := t.TempDir()
	if err := CreateFile(root, "new", []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := ReadFile(root, "new", 64)
	if err != nil || string(body) != "complete" {
		t.Fatal(string(body), err)
	}
}

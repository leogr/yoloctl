package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// leftovers returns every directory entry except the named ones.
func leftovers(t *testing.T, dir string, keep ...string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
next:
	for _, e := range entries {
		for _, k := range keep {
			if e.Name() == k {
				continue next
			}
		}
		out = append(out, e.Name())
	}
	return out
}

// Write must produce exactly the requested content and mode. 0600 is the mode
// account manifests and session artifacts rely on; 0644 covers the rest.
func TestWriteContentAndMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o644} {
		dir := t.TempDir()
		path := filepath.Join(dir, "box.yaml")
		if err := Write(path, []byte("hello\n"), mode); err != nil {
			t.Fatalf("Write(%o): %v", mode, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello\n" {
			t.Errorf("content = %q", got)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != mode {
			t.Errorf("mode = %o, want %o", st.Mode().Perm(), mode)
		}
		if extra := leftovers(t, dir, "box.yaml"); len(extra) != 0 {
			t.Errorf("temporary files left behind: %v", extra)
		}
	}
}

// Replacing an existing file swaps content AND mode: a 0644 file rewritten at
// 0600 must not keep its wider mode.
func TestWriteReplacesExistingFileAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

// A failed write must leave the previous file untouched and no temporary file
// behind. Renaming a file onto a non-empty directory fails after the temporary
// file was fully written, which exercises the cleanup path.
func TestWriteFailureKeepsTargetAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(target, []byte("data"), 0o600); err == nil {
		t.Fatal("Write onto a non-empty directory: expected an error, got nil")
	}
	if st, err := os.Stat(filepath.Join(target, "child")); err != nil || !st.IsDir() {
		t.Errorf("target was disturbed: %v", err)
	}
	if extra := leftovers(t, dir, "target"); len(extra) != 0 {
		t.Errorf("temporary files left behind after a failed write: %v", extra)
	}
}

// The parent directory is the caller's responsibility (callers create it with
// their own mode), so a missing one is an error rather than something Write
// papers over.
func TestWriteMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "x.yaml")
	if err := Write(path, []byte("x"), 0o644); err == nil {
		t.Fatal("expected an error for a missing parent directory")
	}
}

// The temporary name must not end in the target's extension: manifest listings
// select entries by suffix and must ignore a leftover from a crashed write.
func TestTemporaryNameDoesNotMatchTargetSuffix(t *testing.T) {
	dir := t.TempDir()
	tmp, err := os.CreateTemp(dir, tempPattern(filepath.Join(dir, "personal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tmp.Close() }()
	if filepath.Ext(tmp.Name()) == ".yaml" {
		t.Errorf("temporary name %q ends in .yaml", filepath.Base(tmp.Name()))
	}
}

func TestCreatePublishesOnlyANewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.txt")
	if err := Create(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("created file %v %v", info, err)
	}
	if err := Create(path, []byte("second"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing file: %v, want os.ErrExist", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "first" {
		t.Fatalf("existing file replaced: %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

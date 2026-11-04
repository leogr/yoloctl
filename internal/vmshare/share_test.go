package vmshare

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

func client(t *testing.T, writable bool) (*sftp.Client, string) {
	t.Helper()
	root := t.TempDir()
	s, e := Open(root, writable)
	if e != nil {
		t.Fatal(e)
	}
	a, b := net.Pipe()
	go func() { _ = s.Serve(a) }()
	c, e := sftp.NewClientPipe(b, b)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close(); _ = a.Close(); _ = b.Close(); _ = s.Close() })
	return c, root
}
func TestGrantBoundary(t *testing.T) {
	c, root := client(t, true)
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(root, "escape")); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Open("/escape/secret"); e == nil {
		t.Fatal("read outside grant")
	}
	if _, e := c.Create("/escape/written"); e == nil {
		t.Fatal("write outside grant")
	}
	if e := c.Rename("/escape/secret", "/stolen"); e == nil {
		t.Fatal("rename outside grant")
	}
	if e := c.Chmod("/escape/secret", 0777); e == nil {
		t.Fatal("chmod outside grant")
	}
	f, e := c.Create("/inside")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte("project")); e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	if e = c.PosixRename("/inside", "/renamed"); e != nil {
		t.Fatal(e)
	}
	f, e = c.Open("/renamed")
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	_ = f.Close()
	if e != nil || string(data) != "project" {
		t.Fatalf("read after rename: %q %v", data, e)
	}
	if e = c.Symlink("renamed", "/relative"); e != nil {
		t.Fatal(e)
	}
	f, e = c.Open("/relative")
	if e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
}
func TestReadOnlyCatalog(t *testing.T) {
	c, root := client(t, false)
	if e := os.WriteFile(filepath.Join(root, "entry"), []byte("v1"), 0644); e != nil {
		t.Fatal(e)
	}
	for name, op := range map[string]func() error{"write": func() error {
		f, e := c.Create("/entry")
		if f != nil {
			_ = f.Close()
		}
		return e
	}, "remove": func() error { return c.Remove("/entry") }, "rename": func() error { return c.Rename("/entry", "/other") }, "chmod": func() error { return c.Chmod("/entry", 0777) }, "mkdir": func() error { return c.Mkdir("/dir") }, "link": func() error { return c.Link("/entry", "/hard") }} {
		if e := op(); e == nil {
			t.Errorf("read-only share allowed %s", name)
		}
	}
	if e := os.WriteFile(filepath.Join(root, "new"), []byte("v2"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(filepath.Join(root, "new"), filepath.Join(root, "entry")); e != nil {
		t.Fatal(e)
	}
	f, e := c.Open("/entry")
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(f)
	_ = f.Close()
	if e != nil || string(b) != "v2" {
		t.Fatalf("catalog replacement: %q %v", b, e)
	}
}

func TestAppendHandleIgnoresClientOffset(t *testing.T) {
	c, root := client(t, true)
	if err := os.WriteFile(filepath.Join(root, "log"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := c.OpenFile("/log", os.O_WRONLY|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, data := range []string{"second\n", "third\n"} {
		if _, err := f.WriteAt([]byte(data), 0); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "log"))
	if err != nil || string(got) != "first\nsecond\nthird\n" {
		t.Fatalf("append overwrote existing data: %q %v", got, err)
	}
}

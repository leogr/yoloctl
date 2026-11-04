//go:build linux && amd64

package firecracker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSparseFormatterPreservesZeroOverwrites(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "disk-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err = f.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	disk := &contextDisk{File: f, ctx: context.Background()}
	if _, err = disk.WriteAt(make([]byte, 8<<20), 0); err != nil {
		t.Fatal(err)
	}
	if _, err = disk.WriteAt([]byte("metadata"), 4093); err != nil {
		t.Fatal(err)
	}
	if _, err = disk.WriteAt(make([]byte, 8), 4093); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8192)
	if _, err = f.ReadAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, make([]byte, len(data))) {
		t.Fatal("zero overwrite was discarded")
	}
}

func TestFormatWithoutHostTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	p := filepath.Join(t.TempDir(), "disk.ext4")
	if err := sparse(p, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := formatDisk(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if clean, err := blank(p); err != nil || clean {
		t.Fatalf("not initialized: %v %v", clean, err)
	}
	if err := formatDisk(context.Background(), p); err == nil {
		t.Fatal("reformatted an initialized disk")
	}
}

func TestBlankDiskRejectsAnyData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "disk")
	if e := sparse(p, 32<<20); e != nil {
		t.Fatal(e)
	}
	ok, e := blank(p)
	if e != nil || !ok {
		t.Fatalf("sparse zero disk: %v %v", ok, e)
	}
	f, e := os.OpenFile(p, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.WriteAt([]byte{1}, 21<<20); e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	ok, e = blank(p)
	if e != nil || ok {
		t.Fatalf("data after sparse hole: %v %v", ok, e)
	}
	if e = formatDisk(context.Background(), p); e == nil {
		t.Fatal("formatted existing data")
	}
}
func TestProcessIdentityRefusesReusedPID(t *testing.T) {
	p, e := identity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	live, e := p.alive()
	if e != nil || !live {
		t.Fatal("current process not live", e)
	}
	p.Start += "0"
	live, e = p.alive()
	if e != nil || live {
		t.Fatal("mismatched process accepted", e)
	}
}

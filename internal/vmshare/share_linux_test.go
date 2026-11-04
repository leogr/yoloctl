//go:build linux

package vmshare

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestShareRejectsSpecialFilesWithoutBlocking(t *testing.T) {
	c, root := client(t, true)
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.ReadDir("/pipe")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("listed a FIFO as a directory")
		}
	case <-time.After(time.Second):
		// Release a blocked open even when checking an unfixed implementation.
		f, _ := os.OpenFile(filepath.Join(root, "pipe"), os.O_RDWR|unix.O_NONBLOCK, 0)
		if f != nil {
			defer func() { _ = f.Close() }()
		}
		t.Fatal("guest request blocked on host FIFO")
	}
	if f, err := c.Open("/pipe"); err == nil {
		_ = f.Close()
		t.Fatal("exposed a FIFO handle")
	}
}

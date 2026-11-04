package main

import (
	"os"
	"testing"
)

// shortSocketDir is a private directory for test Unix sockets. t.TempDir lives
// under macOS's long $TMPDIR, where a socket path can exceed the 104-byte
// sun_path limit and bind fails with "invalid argument".
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "yc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

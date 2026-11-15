package container

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nerdctl start, stop, rm, pull and rmi print only the container ID or an
// image digest on stdout. Those bare hex lines must never reach the user's
// stream; stderr (progress and errors) still does.
func TestNerdctlIDEchoesAreNotShown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	id := strings.Repeat("e8c592d8", 8)
	script := `#!/bin/sh
case "$*" in
  *'nerdctl stop'*|*'nerdctl rm'*) echo ` + id + ` ;;
  *'nerdctl pull'*) echo 'progress line' >&2; echo sha256:` + id + ` ;;
  *'nerdctl rmi'*) echo 'Untagged: unit-image'; echo 'Deleted: sha256:` + id + `' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	c := NewWithOutput("fixture", &stdout, &stderr)
	ctx := t.Context()
	if err := c.StopContainerID(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveContainerID(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.ImagePull(ctx, "unit-image"); err != nil {
		t.Fatal(err)
	}
	if err := c.ImageRemove(ctx, "unit-image"); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("ID echoes reached the user's stream: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "progress line") {
		t.Fatalf("stderr progress lost: %q", stderr.String())
	}
}

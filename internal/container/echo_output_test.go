package container

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nerdctl start, stop, rm and rmi print only the container ID or an image
// digest on stdout. Those bare hex lines must never reach the user's stream;
// stderr still does. Pull writes its progress to stdout (pinned nerdctl sets
// ProgressOutputToStdout), so that output stays visible.
func TestNerdctlIDEchoesAreNotShown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	id := strings.Repeat("e8c592d8", 8)
	script := `#!/bin/sh
case "$*" in
  *'nerdctl stop'*|*'nerdctl rm'*) echo ` + id + ` ;;
  *'nerdctl pull'*) echo 'pull progress line'; echo 'stderr line' >&2 ;;
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
	if stdout.String() != "pull progress line\n" {
		t.Fatalf("want only pull progress on the user's stream, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "stderr line") {
		t.Fatalf("stderr lost: %q", stderr.String())
	}
}

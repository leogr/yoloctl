package container

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyArchiveUsesOnlyFullCIDAndPreservesStreamAndExit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("COPY_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$COPY_LOG\"\nprintf 'tar\\000payload'\nprintf 'copy diagnostic' >&2\nexit 23\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var out, notice bytes.Buffer
	c := NewWithOutput("test", &out, &notice)
	cid := strings.Repeat("a", 64)
	if err := c.CopyArchive(t.Context(), "box", "/tmp", &out); err == nil {
		t.Fatal("name accepted")
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid selector executed")
	}
	if err := c.CopyArchive(t.Context(), cid, "/home/sandbox/.secrets; echo unsafe", &out); err == nil {
		t.Fatal("producer exit discarded")
	}
	if out.String() != "tar\x00payload" || notice.String() != "copy diagnostic" {
		t.Fatal(out.String(), notice.String())
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "shell\nyolo-test\n--\nsudo\nnerdctl\ncp\n--\n" + cid + ":/home/sandbox/.secrets; echo unsafe\n-\n"
	if string(argv) != want {
		t.Fatalf("argv %q", argv)
	}
}

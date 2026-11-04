package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

func TestManifestEditorReleasesLocksAndRefusesConcurrentChanges(t *testing.T) {
	for _, kind := range []string{"box", "account"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			saveSpaceForTest(t, "default", "/work")
			var path, entityLock string
			var run func() error
			if kind == "box" {
				cfg := &agentbox.Config{Name: "coder", Space: "default", Agent: "none", Account: "none", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("coder")}
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
				path = cfg.Path()
				entityLock = strings.TrimSuffix(path, ".yaml") + ".lock"
				run = func() error { return runEdit(t.Context(), io.Discard, "default", "coder") }
			} else {
				cfg := account.New("default", "personal")
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
				path = cfg.Path()
				entityLock = strings.TrimSuffix(path, ".yaml") + ".lock"
				cmd := &cobra.Command{}
				cmd.SetContext(t.Context())
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				run = func() error { return runAccountEdit(cmd, "default", "personal") }
			}
			lock, err := layout.SpaceLockPath("default")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_EDIT_SPACE_LOCK", lock)
			t.Setenv("TEST_EDIT_ENTITY_LOCK", entityLock)
			t.Setenv("TEST_EDIT_MANIFEST", path)
			script := `#!/usr/bin/env python3
import fcntl, os, sys
from pathlib import Path
# An editor can coexist with other commands; neither lock may be retained.
for key in ('TEST_EDIT_SPACE_LOCK', 'TEST_EDIT_ENTITY_LOCK'):
    with open(os.environ[key], 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
current = Path(os.environ['TEST_EDIT_MANIFEST'])
current.write_text(current.read_text() + '\n# concurrent change\n')
edited = Path(sys.argv[1])
edited.write_text(edited.read_text() + '\n# my edit\n')
`
			editor := filepath.Join(t.TempDir(), "editor")
			if err := os.WriteFile(editor, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EDITOR", editor)
			err = run()
			if err == nil || !strings.Contains(err.Error(), "changed while editing") {
				t.Fatalf("concurrent edit: %v", err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Contains(raw, []byte("concurrent change")) || bytes.Contains(raw, []byte("my edit")) {
				t.Fatal("overwrote concurrent change")
			}
			_, kept, ok := strings.Cut(err.Error(), "edits kept at ")
			if !ok {
				t.Fatal(err)
			}
			kept = strings.TrimSuffix(kept, ")")
			t.Cleanup(func() { _ = os.Remove(kept) })
			raw, readErr = os.ReadFile(kept)
			if readErr != nil || !bytes.Contains(raw, []byte("my edit")) {
				t.Fatalf("lost user edits: %v", readErr)
			}
			info, statErr := os.Stat(kept)
			if statErr != nil || info.Mode().Perm() != 0600 {
				t.Fatal(fmt.Sprint("unsafe temp permissions: ", statErr))
			}
		})
	}
}

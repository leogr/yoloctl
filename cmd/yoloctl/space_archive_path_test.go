package main

import (
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceconfig"
)

func TestExportArchivePathExpandsHome(t *testing.T) {
	for _, spelling := range []string{"tilde", "absolute", "relative"} {
		t.Run(spelling, func(t *testing.T) {
			fakeLima(t)
			boot := imageUpdateFixture(t)
			home := filepath.Join(t.TempDir(), "home with spaces")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Chdir(home)
			path := filepath.Join(home, "backup.tar.gz")
			original := []byte("existing private archive")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			arg := "~/backup.tar.gz"
			if spelling == "absolute" {
				arg = path
			}
			if spelling == "relative" {
				arg = "backup.tar.gz"
			}
			cmd := newSpaceExportCmd()
			cmd.SetContext(context.Background())
			err := exportSpace(cmd, []string{boot.Name, arg})
			if err == nil || err.Error() != "archive output already exists" {
				t.Fatalf("export %q did not find the existing home archive before VM work: %v", arg, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("changed existing archive: %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(home, "~")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("literal tilde directory: %v", err)
			}
		})
	}
}

func TestImportArchivePathExpandsHomeBeforeValidation(t *testing.T) {
	for _, spelling := range []string{"tilde", "absolute", "relative", "relative-through-symlink"} {
		t.Run(spelling, func(t *testing.T) {
			limaHome := fakeLima(t)
			home := filepath.Join(t.TempDir(), "home with spaces")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Chdir(home)
			path := filepath.Join(home, "backup.tar.gz")
			if spelling == "relative-through-symlink" {
				// The OS resolves link/.. through the target, whereas lexical
				// cleaning would incorrectly select home/backup.tar.gz.
				nested := filepath.Join(home, "archives", "nested")
				if err := os.MkdirAll(nested, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(nested, filepath.Join(home, "link")); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(home, "archives", "backup.tar.gz")
			}
			original := []byte("not a gzip archive")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			arg := "~/backup.tar.gz"
			if spelling == "absolute" {
				arg = path
			}
			if spelling == "relative" {
				arg = "backup.tar.gz"
			}
			if spelling == "relative-through-symlink" {
				arg = "link/../backup.tar.gz"
			}
			cmd := newSpaceImportCmd()
			cmd.SetContext(context.Background())
			if err := cmd.Flags().Set("backend", "lima"); err != nil {
				t.Fatal(err)
			}
			err := importSpace(cmd, []string{"restored", arg})
			if !errors.Is(err, gzip.ErrHeader) {
				t.Fatalf("import %q did not validate the home archive: %v", arg, err)
			}
			if exists, err := spaceconfig.Exists("restored"); err != nil || exists {
				t.Fatalf("published destination from invalid archive: exists=%t, err=%v", exists, err)
			}
			calls, err := os.ReadFile(filepath.Join(limaHome, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if fields := strings.Fields(line); len(fields) == 0 || fields[0] != "list" {
					t.Fatalf("VM effect before archive validation: %q", calls)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("changed source archive: %q, %v", got, err)
			}
		})
	}
}

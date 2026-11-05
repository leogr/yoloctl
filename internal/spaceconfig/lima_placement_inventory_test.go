package spaceconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestLimaPlacementRejectsUnsearchableSpaceBundle(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs unprivileged filesystem access")
	}
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", filepath.Join(root, "config"))
	shared := filepath.Join(root, "shared")
	c, err := New("old", shared, "personal")
	if err != nil {
		t.Fatal(err)
	}
	c.AccountDiskUUID = "uuid"
	b, _, err := Split(c, 1000, 20, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Save(); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "config", "spaces", "old")
	if err = os.Chmod(bundle, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(bundle, 0700); err != nil {
			t.Error(err)
		}
	})
	if _, err = os.Stat(filepath.Join(bundle, "space.yaml")); !os.IsPermission(err) {
		t.Fatalf("must reproduce permission denial, got %v", err)
	}
	err = ValidateLimaPlacement(filepath.Join(shared, "lima"))
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "old") {
		t.Fatalf("must refuse and identify the inaccessible bundle: %v", err)
	}
}

func TestLimaPlacementPreservesAbsentAndIncompleteBundleSemantics(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	t.Setenv("YOLOCTL_HOME", config)
	home := filepath.Join(root, "lima")
	if err := ValidateLimaPlacement(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("absent inventory was created: %v", err)
	}
	spaces, err := layout.SpacesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(spaces, "incomplete", "boxes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaces, "ordinary-file"), []byte("not a bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimaPlacement(home); err != nil {
		t.Fatal("readable incomplete bundle blocked placement", err)
	}
}

func TestLimaPlacementPropagatesInventoryAccessErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged filesystem access")
	}
	for _, at := range []string{"manifest", "inventory-read", "entry-info"} {
		t.Run(at, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("YOLOCTL_HOME", filepath.Join(root, "config"))
			c, err := New("old", filepath.Join(root, "shared"), "personal")
			if err != nil {
				t.Fatal(err)
			}
			c.AccountDiskUUID = "original-uuid"
			boot, _, err := Split(c, 1000, 20, "generation")
			if err != nil {
				t.Fatal(err)
			}
			if err := boot.Save(); err != nil {
				t.Fatal(err)
			}
			spaces, _ := layout.SpacesDir()
			path := spaces
			mode := os.FileMode(0000)
			if at == "manifest" {
				path, err = layout.SpaceConfigPath("old")
				if err != nil {
					t.Fatal(err)
				}
			}
			if at == "entry-info" {
				mode = 0400
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				restore := os.FileMode(0700)
				if at == "manifest" {
					restore = 0600
				}
				if err := os.Chmod(path, restore); err != nil {
					t.Error(err)
				}
			})
			err = ValidateLimaPlacement(filepath.Join(root, "shared", "lima"))
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("inaccessible %s accepted or hidden: %v", at, err)
			}
			if at != "inventory-read" && !strings.Contains(err.Error(), "old") {
				t.Fatalf("missing bundle diagnostic: %v", err)
			}
		})
	}
}

func TestLimaPlacementRefusesLinkedBundleAndManifest(t *testing.T) {
	for _, link := range []string{"bundle", "manifest"} {
		t.Run(link, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("YOLOCTL_HOME", filepath.Join(root, "config"))
			spaces, _ := layout.SpacesDir()
			if err := os.MkdirAll(spaces, 0700); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if link == "manifest" {
				if err := os.Mkdir(filepath.Join(spaces, "old"), 0700); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(target, "space.yaml")
				if err := os.WriteFile(target, []byte("untrusted"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(spaces, "old")
			if link == "manifest" {
				path = filepath.Join(path, "space.yaml")
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			err := ValidateLimaPlacement(filepath.Join(root, "lima"))
			if err == nil || !strings.Contains(err.Error(), "old") {
				t.Fatalf("linked %s accepted: %v", link, err)
			}
		})
	}
}

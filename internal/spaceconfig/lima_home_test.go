package spaceconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

func TestLimaPlacementAndLongExistingSpaceRoundTrip(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	c, err := New("old", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	c.Name = strings.Repeat("s", 63)
	c.AccountDiskUUID = "same-native-uuid"
	b, settings, err := Split(c, 1000, 20, "same-generation")
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := layout.LimaHome()
	b.Bundle = "same-bundle"
	for _, recorded := range []string{"", filepath.Join(t.TempDir(), "recorded-lima")} {
		b.VM.LimaHome = recorded
		if err := b.Save(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(mustSpacePath(t, b.Name))
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := ParseBootstrap(raw)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.VM.LimaHome != recorded || loaded.DiskUUID != b.DiskUUID {
			t.Fatalf("record lost: %+v", loaded)
		}
		home, err := LimaHome(b.Name)
		if err != nil {
			t.Fatal(err)
		}
		want := recorded
		if want == "" {
			want = legacy
		}
		if home != want {
			t.Fatalf("home %s, want %s", home, want)
		}
		diskHome, err := LimaHomeForDisk(b.DiskID)
		if err != nil || diskHome != want {
			t.Fatalf("disk home %s %v", diskHome, err)
		}
		if _, err := settings.Compose(loaded); err != nil {
			t.Fatal(err)
		}
		if err := loaded.Context("/host/config").Validate(); err != nil {
			t.Fatal(err)
		}
		names, err := List()
		if err != nil || len(names) != 1 || names[0] != b.Name {
			t.Fatalf("legacy list %v %v", names, err)
		}
	}
	if _, err := New(c.Name, "/projects", "personal"); err == nil {
		t.Fatal("admitted a new long space")
	}
}

func mustSpacePath(t *testing.T, name string) string {
	t.Helper()
	p, err := layout.SpaceConfigPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLimaPlacementValidationAndNewSelection(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	c, _ := New("new", "/projects", "personal")
	c.AccountDiskInitializing = true
	b, _, err := Split(c, 1000, 20, "generation")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative", "/a/../b", "/trailing/"} {
		b.VM.LimaHome = bad
		if b.Validate() == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	b.VM.LimaHome = filepath.Join(t.TempDir(), "home")
	b.VM.Backend = "firecracker"
	b.VM.Image = "/image"
	if b.Validate() == nil {
		t.Fatal("Firecracker accepted Lima home")
	}
	b.VM.Backend = "lima"
	b.VM.Image = ""
	if err := b.SelectNewLimaHome(); err != nil {
		t.Fatal(err)
	}
	want := ""
	if runtime.GOOS == "darwin" {
		want, _ = layout.NewLimaHome()
	}
	if b.VM.LimaHome != want {
		t.Fatalf("new platform placement: %s want %s", b.VM.LimaHome, want)
	}
	raw, _ := yaml.Marshal(b)
	if _, err := ParseBootstrap(raw); err != nil {
		t.Fatal(err)
	}
}

func TestLimaDiskPlacementRejectsDuplicateOwnership(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	c, _ := New("one", "/projects", "personal")
	c.AccountDiskInitializing = true
	b, _, err := Split(c, 1000, 20, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	b.Name = "two"
	b.VM.LimaHome = filepath.Join(t.TempDir(), "other")
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := LimaHomeForDisk(b.DiskID); err == nil {
		t.Fatal("ambiguous disk selected a home")
	}
}

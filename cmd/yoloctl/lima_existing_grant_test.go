package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceconfig"
)

// On default macOS new spaces select ~/.config/yoloctl/lima. An older space
// whose grant covers ~/.config keeps starting while no bootstrap records that
// root; a new grant there and new Lima storage under the old grant are still
// refused, and once a space records the root the old grant is refused too.
// The new root is simulated, since it equals the legacy root off macOS.
func TestExistingGrantIgnoresTheUnusedNewLimaHome(t *testing.T) {
	stubOperatorClient(t, nil)
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", filepath.Join(root, "legacy-config"))
	dotConfig := filepath.Join(root, "dot-config")
	if err := os.MkdirAll(dotConfig, 0700); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dotConfig, "yoloctl", "lima")
	saved := limaHomesForNewGrants
	limaHomesForNewGrants = func() ([]string, error) {
		homes, err := spaceconfig.LimaHomes()
		return append(homes, next), err
	}
	t.Cleanup(func() { limaHomesForNewGrants = saved })

	old := imageUpdateFixture(t)
	old.Name = "old"
	old.VM.Backend, old.VM.Image, old.VM.LimaHome = "lima", "", ""
	old.Mounts = []spaceconfig.Mount{{Source: dotConfig}}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := controllerMounts(old); err != nil {
		t.Fatalf("existing grant over the unused new root must still start: %v", err)
	}
	if err := rejectSpaceHostPath(dotConfig, nil); err == nil || !strings.Contains(err.Error(), "managed storage") {
		t.Fatalf("a new grant over the new root must be refused: %v", err)
	}
	if err := spaceconfig.ValidateLimaPlacement(next); err == nil || !strings.Contains(err.Error(), `"old"`) {
		t.Fatalf("new Lima storage under the old grant must be refused: %v", err)
	}

	fresh := *old
	fresh.Name = "new"
	fresh.DiskID = strings.Repeat("a", 32)
	fresh.VM.LimaHome = next
	fresh.Mounts = nil
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := controllerMounts(old); err == nil || !strings.Contains(err.Error(), "managed storage") {
		t.Fatalf("a recorded root inside the grant must refuse it: %v", err)
	}
}

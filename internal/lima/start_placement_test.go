package lima

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

// The start preflight of an initialized space checks only its own socket
// path and reads no other space's grants, so Client.Start goes on to limactl
// beside an unreadable bundle; the grant inventory still refuses at creation
// before limactl runs. Full CLI start also lists Lima homes and disk owners,
// which this test does not exercise.
func TestStartPreflightSkipsTheGrantInventory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs unprivileged filesystem access")
	}
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	target := placementBootstrap(t, "target", filepath.Join(base, "lima"), "1")
	placementBootstrap(t, "other", filepath.Join(base, "lima"), "2")
	spaces, err := layout.SpacesDir()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(spaces, "other")
	if err := os.Chmod(other, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(other, 0700); err != nil {
			t.Error(err)
		}
	})

	if err := ValidateInstancePaths(target.Name); err != nil {
		t.Fatalf("start preflight inspected another space: %v", err)
	}
	start := (&Client{binary: filepath.Join(base, "limactl-missing")}).Start(t.Context(), target.Name)
	if start == nil || errors.Is(start, os.ErrPermission) || strings.Contains(start.Error(), "host grants") {
		t.Fatalf("start must reach limactl, not the grant inventory: %v", start)
	}
	if err := ValidateInstancePlacement(target.Name); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("placement must still fail closed: %v", err)
	}
	c := &Client{binary: filepath.Join(base, "limactl-must-not-run")}
	if err := c.Create(t.Context(), target.Name, nil); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("create must refuse before limactl: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "lima")); !os.IsNotExist(err) {
		t.Fatalf("refused create made the Lima home: %v", err)
	}
}

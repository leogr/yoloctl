package vmservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestAuditSupportQueryIsBoundedAndRefusesOldImages(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_AUDIT_SUPPORT") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditSupportQueryIsBoundedAndRefusesOldImages$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_SUPPORT=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return
	}
	disk := t.TempDir()
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", ServiceManager: "microvm"}); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"new", "old", "unexpected"} {
		calls := 0
		auditCapabilityRequest = func(ctx context.Context, _ *container.Client, req vmwire.UnitRequest) (string, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Fatal("unbounded capability read")
			}
			if req.Action != vmwire.AuditCapabilityAction || req.Unit.Name != vmwire.AuditUnitName || len(req.Unit.Args) != 0 || len(req.Unit.Env) != 0 || req.Unit.Restart {
				t.Fatal("capability query attempted effects", req)
			}
			if scenario == "old" {
				return "", errors.New("unknown unit request")
			}
			if scenario == "unexpected" {
				return "some-other-service/v1", nil
			}
			return vmwire.AuditCapability, nil
		}
		err := AuditSupported(t.Context(), container.New("test"))
		if calls != 1 || (err == nil) != (scenario == "new") {
			t.Fatal(scenario, calls, err)
		}
		if err != nil && !strings.Contains(err.Error(), "--update-vm-image") {
			t.Fatal("missing explicit migration", err)
		}
	}
}

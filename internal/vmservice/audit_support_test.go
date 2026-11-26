package vmservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestAuditSupportQueryFollowsCallerAndRefusesOldImages(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_AUDIT_SUPPORT") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditSupportQueryFollowsCallerAndRefusesOldImages$")
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
	oldExit := exec.CommandContext(t.Context(), "sh", "-c", "exit 1").Run()
	var exit *exec.ExitError
	if !errors.As(oldExit, &exit) || exit.ExitCode() != 1 {
		t.Fatal(oldExit)
	}
	for _, test := range []struct {
		name            string
		answer          string
		queryErr        error
		wantUnsupported bool
	}{
		{name: "supported", answer: vmwire.AuditCapability},
		{name: "old identity check", queryErr: unitRequestError(vmwire.AuditCapabilityAction, oldExit, "microVM: invalid managed service identity\n"), wantUnsupported: true},
		{name: "old action check", queryErr: unitRequestError(vmwire.AuditCapabilityAction, oldExit, "microVM: unknown guest service action \"audit-capability\"\n"), wantUnsupported: true},
		{name: "incompatible reply", answer: "some-other-service/v1", wantUnsupported: true},
		{name: "connection reset", queryErr: unitRequestError(vmwire.AuditCapabilityAction, oldExit, "microVM: read unix /run/control.sock: connection reset by peer\n")},
		{name: "empty reply"},
		{name: "caller deadline", queryErr: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller, stop := context.WithCancel(t.Context())
			defer stop()
			if test.name == "caller deadline" {
				var cancel context.CancelFunc
				caller, cancel = context.WithTimeout(caller, time.Minute)
				defer cancel()
			}
			calls := 0
			auditCapabilityRequest = func(ctx context.Context, _ *container.Client, req vmwire.UnitRequest) (string, error) {
				calls++
				got, gotOK := ctx.Deadline()
				want, wantOK := caller.Deadline()
				if gotOK != wantOK || !got.Equal(want) {
					t.Fatalf("unexpected capability deadline %v, want %v", got, want)
				}
				if req.Action != vmwire.AuditCapabilityAction || req.Unit.Name != vmwire.AuditUnitName || len(req.Unit.Args) != 0 || len(req.Unit.Env) != 0 || req.Unit.Restart {
					t.Fatal("capability query attempted effects", req)
				}
				return test.answer, test.queryErr
			}
			err := AuditSupported(caller, container.New("test"))
			if calls != 1 || (err == nil) != (test.answer == vmwire.AuditCapability) {
				t.Fatal(calls, err)
			}
			if test.queryErr != nil && !errors.Is(err, test.queryErr) {
				t.Fatal("lost query cause", err)
			}
			if errors.Is(err, ErrAuditUnsupported) != test.wantUnsupported || (err != nil && strings.Contains(err.Error(), "--update-vm-image")) != test.wantUnsupported {
				t.Fatal("wrong audit capability classification", err)
			}
		})
	}
	// A host-side transport error never becomes an old-init diagnosis even
	// if its diagnostics contain the same words.
	if err := unitRequestError(vmwire.AuditCapabilityAction, syscall.ECONNRESET, "microVM: invalid managed service identity"); errors.Is(err, ErrAuditUnsupported) || !errors.Is(err, syscall.ECONNRESET) {
		t.Fatal(err)
	}
}

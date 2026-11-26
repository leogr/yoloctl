package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"gopkg.in/yaml.v3"
)

type unreadAuditArchive struct{ t *testing.T }

func (r unreadAuditArchive) Read([]byte) (int, error) {
	r.t.Fatal("old-image refusal consumed an archive")
	return 0, io.EOF
}

func TestAuditOldImageRefusedBeforeEffects(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_AUDIT_OLD_IMAGE") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditOldImageRefusedBeforeEffects$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_OLD_IMAGE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", ServiceManager: "microvm", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	raw, _ := yaml.Marshal(spaceconfig.Settings{SchemaVersion: 1, Name: c.Space, DefaultAccount: "personal", DefaultWorkdir: "/projects", Audit: &spaceconfig.AuditSettings{Version: audit.Version}})
	if err := safepath.WriteFile(c.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	auditBackendSupport = func(context.Context, *container.Client) error {
		return vmservice.ErrAuditUnsupported
	}
	auditAcquire = func(context.Context, string, bool) (func(), error) {
		t.Fatal("old-image refusal acquired mutation guard")
		return nil, nil
	}
	auditKernelSupport = func() error { t.Fatal("old-image refusal reached probe preflight"); return nil }
	status, err := nativeAuditStatus(t.Context())
	if err != nil || status.State != "unsupported" || !strings.Contains(status.Error, "--update-vm-image") {
		t.Fatal(status, err)
	}
	for _, method := range []string{"audit.enable", "audit.disable"} {
		handled, err := runControllerAudit(t.Context(), controller.Request{Method: method, Space: c.Space}, unreadAuditArchive{t}, io.Discard)
		if !handled || err == nil || !strings.Contains(err.Error(), "--update-vm-image") {
			t.Fatal(method, handled, err)
		}
	}
	after, err := safepath.ReadFile(c.ConfigDir, "space.yaml", 1<<20)
	if err != nil || string(after) != string(raw) {
		t.Fatal("old-image refusal changed settings", err)
	}
	known := status
	auditBackendSupport = func(context.Context, *container.Client) error { return syscall.ECONNRESET }
	status, err = nativeAuditStatus(t.Context())
	if err != nil || status.State != "unavailable" || !strings.Contains(status.Error, "connection reset") || strings.Contains(status.Error, "--update-vm-image") {
		t.Fatal("broken capability query was classified as old", status, err)
	}
	if status.Desired != known.Desired || status.Rules != known.Rules || status.Drops != known.Drops || status.MetadataGaps != known.MetadataGaps {
		t.Fatal("broken capability query lost known audit state", status, known)
	}
	synctest.Test(t, func(t *testing.T) {
		auditBackendSupport = func(ctx context.Context, _ *container.Client) error {
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		status, err := nativeAuditStatus(t.Context())
		if err != nil || status.State != "unavailable" || strings.Contains(status.Error, "--update-vm-image") || time.Since(start) != time.Minute {
			t.Fatal("unanswered capability query was classified as old", status, err, time.Since(start))
		}
	})
	for _, rel := range []string{"audit", ".controller"} {
		if _, err := os.Lstat(filepath.Join(disk, rel)); !os.IsNotExist(err) {
			t.Fatal("old-image refusal wrote infrastructure", rel, err)
		}
	}
}

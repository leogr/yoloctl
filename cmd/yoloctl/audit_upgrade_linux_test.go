package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"gopkg.in/yaml.v3"
)

func TestAuditControllerUpgradeKeepsExistingSpacesOff(t *testing.T) {
	scenario := os.Getenv("YOLOCTL_TEST_AUDIT_UPGRADE")
	if scenario == "" {
		for _, scenario := range []string{"lima-absent", "lima-disabled", "microvm-absent", "microvm-disabled"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditControllerUpgradeKeepsExistingSpacesOff$")
				cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_UPGRADE="+scenario)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v", output, err)
				}
			})
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "updated-controller", Generation: "updated-generation", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if strings.HasPrefix(scenario, "microvm-") {
		c.ServiceManager = "microvm"
	}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	settings := spaceconfig.Settings{SchemaVersion: 1, Name: c.Space, DefaultAccount: "personal", DefaultWorkdir: "/projects"}
	if strings.HasSuffix(scenario, "disabled") {
		settings.Audit = &spaceconfig.AuditSettings{Enabled: false}
	}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(c.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	held := false
	auditAcquire = func(_ context.Context, space string, _ bool) (func(), error) {
		if held || space != c.Space {
			t.Fatal("invalid native lock")
		}
		held = true
		return func() { held = false }, nil
	}
	auditBackendSupport = func(context.Context, *container.Client) error {
		t.Fatal("off upgrade queried old-image audit capabilities")
		return nil
	}
	auditKernelSupport = func() error { t.Fatal("off upgrade probed BPF"); return nil }
	auditServiceStart = func(context.Context, *container.Client, vmservice.Unit) error {
		t.Fatal("off upgrade started Falco")
		return nil
	}
	auditServiceStop = func(context.Context, *container.Client, string) error {
		t.Fatal("off upgrade stopped a service")
		return nil
	}
	auditObserveLabels = func(context.Context, string) ([]container.AuditLabelObservation, error) {
		t.Fatal("off upgrade inventoried containers")
		return nil, nil
	}
	metadataPath := filepath.Join(disk, "run", "metadata.tsv")
	auditMetadataPath = func() string { return metadataPath }
	auditServiceState = func(context.Context, *container.Client, string) (string, error) {
		if !held || settings.Audit == nil {
			t.Fatal("legacy absent audit settings reached service observation")
		}
		return "inactive", nil // Existing disabled settings may check retained logs.
	}
	if err := reconcileNativeAudit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if checked, err := maintainNativeAudit(t.Context(), true); err != nil || !checked {
		t.Fatal(checked, err)
	}
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil || nativeAuditDesired(cfg).Enabled {
		t.Fatal(cfg, err)
	}
	after, err := safepath.ReadFile(c.ConfigDir, "space.yaml", 1<<20)
	if err != nil || string(after) != string(raw) || held {
		t.Fatal("off upgrade rewrote settings or retained its lock", err)
	}
	root, err := layout.AuditDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, metadataPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("off upgrade created audit infrastructure", path, err)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"gopkg.in/yaml.v3"
)

func TestAuditMetadataPublicationUsesObservedIDsAndHolds(t *testing.T) {
	scenario := os.Getenv("YOLOCTL_TEST_AUDIT_METADATA")
	if scenario == "" {
		for _, scenario := range []string{"known", "unconfined", "privileged", "startup-order", "disabled", "observation-failed", "audit-held", "lock-failed", "duplicate"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAuditMetadataPublicationUsesObservedIDsAndHolds$")
				cmd.Env = append(os.Environ(), "YOLOCTL_TEST_AUDIT_METADATA="+scenario)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			})
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	settings := spaceconfig.Settings{SchemaVersion: 1, Name: "test", DefaultAccount: "personal", DefaultWorkdir: "/projects", Audit: &spaceconfig.AuditSettings{Enabled: scenario != "disabled", Version: audit.Version, RulesSHA256: audit.RulesDigest()}}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(c.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(disk, "run", filepath.Base(layout.AuditMetadataPath()))
	auditMetadataPath = func() string { return target }
	if err := safepath.WriteFile(filepath.Dir(target), filepath.Base(target), []byte("old observation"), 0600); err != nil {
		t.Fatal(err)
	}
	if scenario == "audit-held" {
		r := controller.Receipt{ID: "audit-metadata-held-0123456789", Method: "audit.enable", State: "uncertain", EffectPhase: controller.EffectStarted, Started: time.Now(), Generation: c.Generation, ProofVersion: controller.ProofVersion, DiskID: c.DiskID, Scope: &controller.Scope{Writes: []controller.Resource{{Kind: controller.ResourceAudit}}}}
		if err := (controller.Receipts{Dir: filepath.Join(disk, ".controller", "receipts")}).Save(r); err != nil {
			t.Fatal(err)
		}
	}
	held, observed, unlocked := false, false, false
	published := make(chan struct{}, 1)
	sentinel := errors.New("unavailable")
	auditAcquire = func(_ context.Context, space string, write bool) (func(), error) {
		if space != c.Space || (write && scenario != "startup-order") {
			t.Fatal("metadata did not use the native read guard")
		}
		if scenario == "lock-failed" {
			return nil, sentinel
		}
		held = true
		return func() {
			held = false
			unlocked = true
			if scenario == "startup-order" && !write {
				select {
				case published <- struct{}{}:
				default:
				}
			}
		}, nil
	}
	auditObserveLabels = func(context.Context, string) ([]container.AuditLabelObservation, error) {
		if !held {
			t.Fatal("observation outside native lock")
		}
		observed = true
		if scenario == "observation-failed" {
			return nil, sentinel
		}
		known := container.AuditLabelObservation{ID: strings.Repeat("a", 64), Schema: "1", Kind: "agentbox", Box: "worker", Space: c.Space, Agent: "none", Account: "none", RuntimeMode: "confined", Network: "false"}
		if scenario == "unconfined" {
			known.Network = "true"
		}
		if scenario == "privileged" {
			known.RuntimeMode = "privileged"
			value := true
			known.Privileged = &value
		}
		if scenario == "duplicate" {
			return []container.AuditLabelObservation{known, known}, nil
		}
		rows := []container.AuditLabelObservation{known}
		for _, bad := range []string{"legacy", "wrong-space", "invalid-name", "privilege-mismatch", "missing-network"} {
			row := known
			row.ID = strings.Repeat("b", 64)
			switch bad {
			case "legacy":
				row.Schema = ""
			case "wrong-space":
				row.Space = "other"
			case "invalid-name":
				row.Box = "../escape"
			case "privilege-mismatch":
				value := row.RuntimeMode != "privileged"
				row.Privileged = &value
			case "missing-network":
				row.Network = ""
			}
			rows = append(rows, row)
		}
		return rows, nil
	}
	if scenario == "startup-order" {
		started := make(chan struct{})
		auditServiceState = func(context.Context, *container.Client, string) (string, error) { return "inactive", nil }
		auditServiceStart = func(context.Context, *container.Client, vmservice.Unit) error { close(started); return nil }
		observe := auditObserveLabels
		auditObserveLabels = func(ctx context.Context, space string) ([]container.AuditLabelObservation, error) {
			select {
			case <-started:
			default:
				return nil, errors.New("metadata competed with audit startup")
			}
			return observe(ctx, space)
		}
		stop := startAuditRetention(t.Context(), &controller.Server{})
		defer stop()
		select {
		case <-published:
		case <-time.After(3 * time.Second):
			t.Fatal("metadata did not start after audit reconciliation")
		}
		stop()
	} else {
		err = publishAuditMetadata(t.Context())
	}
	success := scenario == "known" || scenario == "unconfined" || scenario == "privileged" || scenario == "startup-order"
	wantErr := !success && scenario != "disabled"
	if (err != nil) != wantErr {
		t.Fatal(err, wantErr)
	}
	if held || (scenario != "lock-failed" && !unlocked) {
		t.Fatal("native guard leaked")
	}
	if (scenario == "disabled" || scenario == "audit-held" || scenario == "lock-failed") && observed {
		t.Fatal("observation preceded admission")
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if success {
		if !bytes.Contains(after, []byte("\t1\t"+layout.AuditMetadataGeneration(c.Generation)+"\n")) || !bytes.Contains(after, []byte(strings.Repeat("a", 64)+"\tworker\t")) {
			t.Fatal(string(after))
		}
		if bytes.Contains(after, []byte(strings.Repeat("b", 64))) {
			t.Fatal("published unavailable metadata", string(after))
		}
		if scenario == "unconfined" && !bytes.Contains(after, []byte("\tconfined\ttrue\n")) {
			t.Fatal(string(after))
		}
		if scenario == "privileged" && !bytes.Contains(after, []byte("\tprivileged\tfalse\n")) {
			t.Fatal(string(after))
		}
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(info, err)
		}
	} else if string(after) != "old observation" {
		t.Fatal("failed/held observation refreshed old metadata", string(after))
	}
	if filepath.Base(layout.AuditMetadataPath()) != "metadata.tsv" {
		t.Fatal("compatible controller updates moved the snapshot")
	}
}

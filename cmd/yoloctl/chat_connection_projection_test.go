package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"gopkg.in/yaml.v3"
)

// Setup validates the managed server with agentbox.VerifyChatRuntime, which
// refreshes the loaded config's private chat projection. Verify must still
// accept the unchanged manifest and reject a changed one. This runs in a
// native context so the manifest load, projection and runtime bind check are
// the real ones; only container inspection, the control socket and SSH are
// fixtures.
func TestChatConnectionAcceptsItsOwnProjectionRefresh(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_CHAT_CONNECTION_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatConnectionAcceptsItsOwnProjectionRefresh$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_CONNECTION_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native chat connection fixture: %v\n%s", err, out)
		}
		return
	}
	f := newChatConnectionFixture(t)
	s := f.store
	for _, rel := range []string{"config/boxes", "config/accounts", ".controller"} {
		if err := os.MkdirAll(filepath.Join(s.Disk, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Render(f.room); err != nil {
		t.Fatal(err)
	}
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: s.DiskID, DiskUUID: "fixture", DiskMount: s.Disk, ConfigDir: filepath.Join(s.Disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(s.Disk, "host"), UID: s.UID, GID: s.GID, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	projection, err := s.Projection(managedchat.Box{Name: f.room.Server.Name, ID: f.room.Server.ID})
	if err != nil || projection == nil || !projection.Server {
		t.Fatal("server fixture has no private projection", projection, err)
	}
	f.cfg.SchemaVersion = agentbox.SchemaVersion
	manifest, err := layout.BoxPath(f.cfg.Space, f.cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	writeDeclaration := func() {
		t.Helper()
		raw, err := yaml.Marshal(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeDeclaration()
	details := &container.Details{ID: f.id, Name: f.cfg.Name, Running: true, Labels: map[string]string{
		agentbox.LabelAgentbox: f.cfg.Name, agentbox.LabelSpace: f.cfg.Space,
		managedchat.RoomLabel: projection.RoomID, managedchat.BoxLabel: projection.BoxID, managedchat.MemberLabel: projection.MemberID,
		managedchat.RoleLabel: "server", managedchat.IncarnationLabel: managedchat.NewID()}}
	for _, b := range projection.Binds {
		details.Mounts = append(details.Mounts, container.DetailMount{Source: b.Source, Destination: b.Target, ReadOnly: b.ReadOnly})
	}
	fixtureControl := f.ops.control
	f.ops.load = agentbox.Load
	f.ops.control = func(ctx context.Context, r *managedchat.Room, cfg *agentbox.Config) (managedchat.Control, *container.Details, error) {
		control, _, err := fixtureControl(ctx, r, cfg)
		if err != nil {
			return control, nil, err
		}
		// managedServerControl's runtime check, including its projection refresh.
		return control, details, agentbox.VerifyChatRuntime(cfg, details)
	}

	conn, err := openManagedChatConnectionWith(t.Context(), "work", s, f.ops)
	if err != nil {
		t.Fatal("unchanged declaration rejected after its own projection refresh:", err)
	}
	defer func() { _ = conn.Close() }()
	fresh, err := agentbox.Load(f.cfg.Space, f.cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(conn.Config, fresh) || !agentbox.SameDeclaration(conn.Config, fresh) {
		t.Fatal("fixture did not exercise a refreshed projection on an unchanged declaration")
	}
	if err := conn.Verify(t.Context()); err != nil {
		t.Fatal("repeated verification rejected the unchanged declaration:", err)
	}

	f.cfg.Resources.CPUs = "1"
	writeDeclaration()
	if err := conn.Verify(t.Context()); err == nil || !strings.Contains(err.Error(), "declaration changed") {
		t.Fatal("changed declaration accepted:", err)
	}
	if f.dials.Load() != 1 {
		t.Fatal("redialed mutable name", f.dials.Load())
	}
}

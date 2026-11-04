package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"gopkg.in/yaml.v3"
)

// A managed server container created before its temporary directory was set
// keeps the old child environment. That is deployment drift, not authority:
// runtime verification still passes, so the room keeps serving connections,
// and status names the drift without failing readiness. Full inspection (the
// Diff behind box.drift, start/restart and chat ensure) requires an explicit
// recreation, which replaces it. Native, like
// TestChatConnectionAcceptsItsOwnProjectionRefresh: only container
// inspection, the control socket and SSH are fixtures.
func TestChatServerTemporaryDirectoryIsDeploymentDrift(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_CHAT_SERVER_TMPDIR_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatServerTemporaryDirectoryIsDeploymentDrift$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_SERVER_TMPDIR_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native chat server fixture: %v\n%s", err, out)
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
	raw, err := yaml.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	mounts := make([]map[string]any, 0, len(projection.Binds))
	var chatMounts []agentbox.Mount
	for _, b := range projection.Binds {
		mounts = append(mounts, map[string]any{"Source": b.Source, "Destination": b.Target, "RW": !b.ReadOnly})
		chatMounts = append(chatMounts, agentbox.Mount{Source: b.Source, Target: b.Target, ReadOnly: b.ReadOnly})
	}
	labels := map[string]string{agentbox.LabelAgentbox: cfg.Name, agentbox.LabelSpace: cfg.Space,
		managedchat.RoomLabel: projection.RoomID, managedchat.BoxLabel: projection.BoxID, managedchat.MemberLabel: projection.MemberID,
		managedchat.RoleLabel: "server", managedchat.IncarnationLabel: managedchat.NewID()}
	bin := t.TempDir()
	t.Setenv("YOLOCTL_TEST_CHAT_SERVER_INSPECT", filepath.Join(t.TempDir(), "inspect.json"))
	spy := "#!/bin/sh\nif [ \"$#\" = 2 ] && [ \"$1\" = inspect ] && [ \"$2\" = " + cfg.Name + " ]; then exec /bin/cat \"$YOLOCTL_TEST_CHAT_SERVER_INSPECT\"; fi\nexit 91\n"
	if err := os.WriteFile(filepath.Join(bin, "nerdctl"), []byte(spy), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	inspect := func(env ...string) *container.Details {
		t.Helper()
		raw, err := json.Marshal([]any{map[string]any{"Id": f.id, "Name": cfg.Name,
			"State":  map[string]any{"Status": "running", "Running": true},
			"Config": map[string]any{"Labels": labels, "Env": env}, "Mounts": mounts}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("YOLOCTL_TEST_CHAT_SERVER_INSPECT"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		details, err := container.New(cfg.Space).Inspect(t.Context(), cfg.Name)
		if err != nil {
			t.Fatal(err)
		}
		return details
	}
	// The server as an earlier build created it: discovery only, plus an
	// unrelated private value that must never be printed.
	legacy := inspect("PATH=/usr/bin", `OPENSHELL_USER_ENVIRONMENT={"YOLOCTL_CHAT_ROOM":"r","PRIVATE":"private-fixture"}`)
	if err := agentbox.VerifyChatRuntime(&cfg, legacy); err != nil {
		t.Fatal("the older server's runtime authority:", err)
	}
	reason := agentbox.ChatServerDeploymentDrift(legacy)
	if !strings.Contains(reason, "lacks its temporary directory") || strings.Contains(reason, "private-fixture") {
		t.Fatalf("deployment drift: %q", reason)
	}
	// Full inspection: the Diff behind box.drift, start/restart and ensure.
	actual := agentbox.ActualState{Exists: true, Labels: legacy.Labels, ChatMounts: chatMounts, ReadOnlyWD: !cfg.WorkdirWritable(), Env: legacy.Env}
	plan := agentbox.Diff(&cfg, actual, "", nil)
	if !plan.NeedsRecreate || plan.RecreateReason != reason {
		t.Fatalf("full inspection: %+v", plan)
	}
	// chat ensure refuses it rather than recreating or reporting it repaired.
	err = managedServerPlanError(plan, cfg.Name, cfg.Space)
	want := fmt.Sprintf("the managed chat server box %q runtime changed (%s); the server was not recreated. Inspect with yoloctl box diff %s --space %s, then explicitly use yoloctl box recreate %s --space %s", cfg.Name, reason, cfg.Name, cfg.Space, cfg.Name, cfg.Space)
	if err == nil || err.Error() != want || strings.Contains(err.Error(), "private-fixture") {
		t.Fatalf("ensure names the exact commands for the server box:\n got %v\nwant %s", err, want)
	}
	if err := managedServerPlanError(agentbox.DiffPlan{}, cfg.Name, cfg.Space); err != nil {
		t.Fatalf("no drift, no refusal: %v", err)
	}

	// Connections keep their identity and bind checks only.
	f.ops.load = agentbox.Load
	fixtureControl := f.ops.control
	f.ops.control = func(ctx context.Context, r *managedchat.Room, c *agentbox.Config) (managedchat.Control, *container.Details, error) {
		control, _, err := fixtureControl(ctx, r, c)
		if err != nil {
			return control, nil, err
		}
		return control, legacy, agentbox.VerifyChatRuntime(c, legacy)
	}
	conn, err := openManagedChatConnectionWith(t.Context(), "work", s, f.ops)
	if err != nil {
		t.Fatal("the older server refused a connection:", err)
	}
	_ = conn.Close()

	// Status names the drift beside an otherwise ready server.
	previousControl, previousHolder := statusServerControlFn, holderUnitActiveFn
	t.Cleanup(func() { statusServerControlFn, holderUnitActiveFn = previousControl, previousHolder })
	statusServerControlFn = f.ops.control
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return true }
	boxes := []boxstate.Box{{Name: cfg.Name, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeRunning, Config: &cfg, Container: &container.Container{Name: cfg.Name, Status: "Up"}}}
	status, err := observeManagedChatRoom(t.Context(), "work", s, f.room, boxes)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ServerReady || !status.RuntimeApplied || len(status.Pending) != 0 || status.Server == nil || !status.Server.RecreatePending || status.Server.Reason != reason {
		t.Fatalf("status: %+v %+v", status, status.Server)
	}

	// Only an explicit recreation replaces it.
	var calls []string
	inner := &fakeManagedInner{}
	applier := managedApplier{inner: inner, client: container.New(cfg.Space), cleanup: func(_ context.Context, c *agentbox.Config) (bool, error) {
		calls = append(calls, "cleanup:"+c.Name)
		return true, nil
	}}
	if err := applier.Recreate(agentbox.WithChatRecreate(t.Context()), &cfg, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"cleanup:" + cfg.Name}) || !slices.Equal(inner.calls, []string{"recreate"}) {
		t.Fatalf("explicit recreation: %v %v", calls, inner.calls)
	}
	// A server this build created has no such drift.
	current := inspect("PATH=/usr/bin", `OPENSHELL_USER_ENVIRONMENT={"SQLITE_TMPDIR":"/tmp","TMPDIR":"/tmp","YOLOCTL_CHAT_ROOM":"r"}`)
	if agentbox.ChatServerDeploymentDrift(current) != "" || agentbox.VerifyChatRuntime(&cfg, current) != nil {
		t.Fatal("current server reported drift")
	}
	actual.Env = current.Env
	// The fixture omits unrelated labels; only the temporary directory matters.
	if plan := agentbox.Diff(&cfg, actual, "", nil); strings.Contains(plan.RecreateReason, "temporary directory") {
		t.Fatalf("current server: %+v", plan)
	}
}

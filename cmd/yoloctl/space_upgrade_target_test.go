package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestSpaceUpgradeWorkerRecreateConsent(t *testing.T) {
	// Native execution context is immutable. Configure it only in the
	// subprocess, which uses a disposable declaration and managed room store.
	const child = "YOLOCTL_TEST_UPGRADE_WORKER"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSpaceUpgradeWorkerRecreateConsent$", "-test.v")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native worker fixture: %v\n%s", err, out)
		}
		return
	}
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(store.Disk, "config")
	if err := spaceenv.Configure(spaceenv.Context{
		Schema: 1, Space: "work", DiskID: store.DiskID, DiskUUID: "fixture", DiskMount: store.Disk,
		ConfigDir: config, CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(store.Disk, "host"),
		UID: store.UID, GID: store.GID, Bundle: "fixture", Generation: "fixture",
	}); err != nil {
		t.Fatal(err)
	}
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }

	base := agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion, Space: "work", Name: room.Server.Name, ID: room.Server.ID,
		Agent: "none", Account: "none", Image: defaultAgentboxImage,
		Workdir: agentbox.NewEphemeralWorkdir(room.Server.Name), Features: []string{"yolo.chat.skill"},
		SessionCommand: agentbox.ChatServerCommand(),
	}
	if err := base.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := agentbox.Load(base.Space, base.Name)
	if err != nil {
		t.Fatal(err)
	}
	base = *saved
	hash, err := upgradeManifestHash(&base)
	if err != nil {
		t.Fatal(err)
	}
	good := upgradeRecreateBinding{
		Target: upgradeTarget{ManifestHash: hash, Run: container.Run{ID: strings.Repeat("a", 64)}},
		Room:   room.ID,
	}
	for _, tc := range []struct {
		name, want                  string
		operator, noForce, attach   bool
		malformed, changed, nonRoom bool
		roomID                      *string
	}{
		{name: "approved"},
		{name: "operator", want: "invalid host upgrade replacement binding", operator: true},
		{name: "missing-force", want: "invalid host upgrade replacement binding", noForce: true},
		{name: "missing-no-attach", want: "invalid host upgrade replacement binding", attach: true},
		{name: "malformed", want: "invalid host upgrade replacement binding", malformed: true},
		{name: "edited", want: "box declaration changed", changed: true},
		{name: "room-changed", want: "managed chat server changed", roomID: new(managedchat.NewID())},
		{name: "server-no-longer-selected", want: "managed chat server changed", nonRoom: true},
		{name: "became-server", want: "managed chat server changed", roomID: new("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, cfg := good, base
			if tc.changed {
				cfg.Image = "other:1"
			}
			if tc.nonRoom {
				cfg.Name = "workload"
				cfg.SessionCommand = []string{"true"}
				binding.Target.ManifestHash, err = upgradeManifestHash(&cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.roomID != nil {
				binding.Room = *tc.roomID
			}
			raw, err := json.Marshal(binding)
			if err != nil {
				t.Fatal(err)
			}
			if tc.malformed {
				raw = append(raw, '{', '}')
			}
			ctx := t.Context()
			if tc.operator {
				ctx = withControllerPrincipal(ctx, controller.Principal{Box: "operator", Incarnation: "fixture"})
			}
			cmd := newAgentboxRecreateCmd()
			cmd.SetContext(ctx)
			if err := cmd.Flags().Set(upgradeRecreateFlag, string(raw)); err != nil {
				t.Fatal(err)
			}
			got, guarded, err := readUpgradeRecreateBinding(cmd, &cfg, !tc.noForce, !tc.attach)
			if tc.want == "" {
				if err != nil || !guarded || got != good {
					t.Fatalf("approved target refused: guarded %v, %v", guarded, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || guarded {
				t.Fatalf("binding guard = %v, %v, want refusal %q", guarded, err, tc.want)
			}
		})
	}
	t.Run("workload-flag-needs-non-room-binding", func(t *testing.T) {
		for _, bind := range []bool{false, true} {
			cmd := newAgentboxRecreateCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			args := []string{base.Name, "--force", "--no-attach", "--" + startWorkloadFlag}
			if bind {
				raw, err := json.Marshal(good)
				if err != nil {
					t.Fatal(err)
				}
				args = append(args, "--"+upgradeRecreateFlag+"="+string(raw))
			}
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), "restoring a recreated workload requires a host upgrade target") {
				t.Fatalf("workload guard with binding %v: %v", bind, err)
			}
		}
	})
}

func TestSpaceUpgradeChatEnsureRoomBinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	oldStore := nativeChatStoreFn
	t.Cleanup(func() { nativeChatStoreFn = oldStore })
	reads := 0
	nativeChatStoreFn = func() (managedchat.Store, error) {
		reads++
		return store, nil
	}
	for _, tc := range []struct {
		name, method, want string
		payload            chatRuntimeRequest
		operator, loadRoom bool
	}{
		{"another-method", "chat.status", "invalid host chat restore binding", chatRuntimeRequest{ExpectedRoom: room.ID}, false, false},
		{"extra-field", "chat.ensure", "invalid host chat restore binding", chatRuntimeRequest{ExpectedRoom: room.ID, Human: "other"}, false, false},
		{"operator", "chat.ensure", "invalid host chat restore binding", chatRuntimeRequest{ExpectedRoom: room.ID}, true, false},
		{"malformed", "chat.ensure", "invalid host chat restore binding", chatRuntimeRequest{ExpectedRoom: "not-a-room-id"}, false, false},
		{"changed", "chat.ensure", "managed chat room changed", chatRuntimeRequest{ExpectedRoom: managedchat.NewID()}, false, true},
		{"unprepared", "chat.ensure", "managed chat room changed", chatRuntimeRequest{ExpectedRoom: room.ID}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unprepared" {
				room.Prepared, room.RuntimeEnabled = false, false
				raw, err := room.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store.Disk, layout.ChatManifest), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if tc.operator {
				ctx = withControllerPrincipal(ctx, controller.Principal{Box: "operator", Incarnation: "fixture"})
			}
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			before := reads
			req := controller.Request{Method: tc.method, Space: "work", Payload: payload}
			handled, err := runControllerChatRuntime(ctx, req, io.Discard, io.Discard)
			if !handled || err == nil || !strings.Contains(err.Error(), tc.want) || (reads != before) != tc.loadRoom {
				t.Fatalf("chat ensure guard: handled %v, read room %v, %v", handled, reads != before, err)
			}
		})
	}
}

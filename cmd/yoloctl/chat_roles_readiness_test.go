package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/spf13/cobra"
)

// Real room publication and the ordinary lifecycle mutation guard, with inert
// runtime inspection/service seams and a private Unix control listener. No VM,
// holder launch or workload is reachable from this fixture.
func TestChatRoleLifecycleHolderReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, state, failure string
		delayed, orphan      bool
		wantErr              bool
		reloads              int
	}{
		{name: "recreated-no-attach", state: "inactive"},
		{name: "exited-holder", state: "failed"},
		{name: "absent-holder", state: "unknown"},
		{name: "refused-control", state: "inactive", failure: "refused"},
		{name: "inactive-live-orphan", state: "inactive", orphan: true, reloads: 1},
		{name: "failed-live-orphan", state: "failed", orphan: true, reloads: 1},
		{name: "unknown-live-orphan", state: "unknown", orphan: true, reloads: 1},
		{name: "orphan-peer-enoent", state: "inactive", orphan: true, failure: "peer-enoent", wantErr: true},
		{name: "orphan-peer-error", state: "inactive", orphan: true, failure: "peer", wantErr: true},
		{name: "orphan-invalid-status", state: "inactive", orphan: true, failure: "readiness", wantErr: true},
		{name: "orphan-lost-status", state: "inactive", orphan: true, failure: "status", wantErr: true},
		{name: "orphan-lost-reload", state: "inactive", orphan: true, failure: "reload", wantErr: true, reloads: 1},
		{name: "orphan-ack-incarnation", state: "inactive", orphan: true, failure: "ack-incarnation", wantErr: true, reloads: 1},
		{name: "orphan-status-incarnation", state: "inactive", orphan: true, failure: "after-incarnation", wantErr: true, reloads: 1},
		{name: "live-holder", state: "active", reloads: 1},
		{name: "starting-holder", state: "activating", delayed: true, reloads: 1},
		{name: "service-transport-error", state: "unknown", failure: "service", wantErr: true},
		{name: "unexpected-state", state: "deactivating", wantErr: true},
		{name: "invalid-server-before-stopped-check", state: "inactive", failure: "identity", wantErr: true},
		{name: "partial-inventory", state: "inactive", failure: "inventory", wantErr: true},
		{name: "publication-error", state: "inactive", failure: "publication", wantErr: true},
		{name: "live-control-missing", state: "active", failure: "missing", wantErr: true},
		{name: "peer-verification-error", state: "active", failure: "peer", wantErr: true},
		{name: "invalid-readiness-proof", state: "active", failure: "readiness", wantErr: true},
		{name: "uncertain-reload", state: "active", failure: "reload", wantErr: true, reloads: 1},
		{name: "post-reload-proof-error", state: "active", failure: "proof", wantErr: true, reloads: 1},
		{name: "reload-ack-proof-error", state: "active", failure: "ack-proof", wantErr: true, reloads: 1},
		{name: "reload-ack-incarnation", state: "active", failure: "ack-incarnation", wantErr: true, reloads: 1},
		{name: "status-incarnation", state: "active", failure: "after-incarnation", wantErr: true, reloads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			store, inv := roomRuntimeFixture(t)
			r, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inv)
			if err != nil {
				t.Fatal(err)
			}
			member := &agentbox.Config{Name: "coder", ID: agentbox.NewIdentity(), Space: "work"}
			inv.Declarations[member.Name] = member.ID
			r, err = store.Enroll(managedchat.Box{Name: member.Name, ID: member.ID}, r.Generation, inv)
			if err != nil {
				t.Fatal(err)
			}
			r, err = store.SetOperators(map[string]bool{r.Members[0].ID: true}, r.Generation, inv)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Render(r); err != nil {
				t.Fatal(err)
			}
			cfg := &agentbox.Config{Name: r.Server.Name, ID: r.Server.ID, Space: "work"}
			boxes := []boxstate.Box{
				{Name: cfg.Name, Config: cfg, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeRunning, Container: &container.Container{Name: cfg.Name}},
				{Name: member.Name, Config: member, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeMissing},
			}
			if tc.failure == "inventory" {
				boxes[1].Runtime = boxstate.RuntimeUnknown
			}
			if tc.failure == "publication" {
				leaf, _ := layout.ChatMemberRelative(r.ID, r.Human.ID)
				path := filepath.Join(store.Disk, leaf, managedchat.ClientConfigFile)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			oldControl, oldState, oldSync := chatRoleServerControl, chatRoleHolderState, syncNativeChatRolesFn
			t.Cleanup(func() {
				chatRoleServerControl, chatRoleHolderState, syncNativeChatRolesFn = oldControl, oldState, oldSync
			})
			var mu sync.Mutex
			ops := []string{}
			path := filepath.Join(shortSocketDir(t), "control.sock")
			if tc.failure == "refused" {
				stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				stale.SetUnlinkOnClose(false)
				if err := stale.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var listener net.Listener
			if tc.state == "active" || tc.state == "activating" || tc.orphan {
				if tc.failure != "missing" {
					listener, err = net.Listen("unix", path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = listener.Close() })
					if tc.delayed {
						// Hide the live socket until the first read-only readiness probe fails.
						if err := os.Rename(path, path+".waiting"); err != nil {
							t.Fatal(err)
						}
					}
					go func() {
						for {
							conn, err := listener.Accept()
							if err != nil {
								return
							}
							var request struct {
								Op string `json:"op"`
							}
							if json.NewDecoder(conn).Decode(&request) != nil {
								_ = conn.Close()
								continue
							}
							mu.Lock()
							ops = append(ops, request.Op)
							count := len(ops)
							mu.Unlock()
							if tc.failure == "reload" && request.Op == "reload" || tc.failure == "status" && request.Op == "status" {
								_ = conn.Close()
								continue
							}
							current, err := store.Load()
							if err != nil {
								_ = conn.Close()
								continue
							}
							proof, err := store.Expected(current)
							if err != nil {
								_ = conn.Close()
								continue
							}
							applied := managedchat.Applied{
								Features:    managedchat.Features{Protocol: 1, Features: []string{"unix-listeners", "control-reload", "acl-revoke", "server-presence"}},
								Incarnation: "test-incarnation-1234", Generation: proof.Generation, ConfigSHA256: proof.ConfigSHA256,
								ConfiguredAccounts: uint64(len(proof.Accounts)), Listeners: managedchat.Listeners{IRC: "unix", Artifacts: "unix"},
							}
							if tc.failure == "readiness" {
								applied.Incarnation = ""
							}
							if tc.failure == "proof" && count >= 3 || tc.failure == "ack-proof" && request.Op == "reload" {
								applied.Generation--
							}
							if tc.failure == "ack-incarnation" && request.Op == "reload" || tc.failure == "after-incarnation" && count >= 3 {
								applied.Incarnation = "replacement-process-1234"
							}
							_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "result": applied})
							_ = conn.Close()
						}
					}()
				}
			}
			controlCalls, stateCalls := 0, 0
			chatRoleServerControl = func(ctx context.Context, room *managedchat.Room, config *agentbox.Config) (managedchat.Control, *container.Details, error) {
				controlCalls++
				if tc.failure == "identity" {
					// Exercise the production reservation check before any holder probe.
					replacement := *config
					replacement.ID = agentbox.NewIdentity()
					return managedServerControl(ctx, room, &replacement)
				}
				return managedchat.Control{Socket: path, VerifyPeer: func(*net.UnixConn) error {
					if tc.failure == "peer-enoent" {
						return os.ErrNotExist
					}
					if tc.failure == "peer" {
						return errors.New("wrong control peer")
					}
					return nil
				}}, &container.Details{Running: true}, nil
			}
			chatRoleHolderState = func(_ context.Context, c *container.Client, unit string) (string, error) {
				stateCalls++
				if controlCalls == 0 || c.Env() != cfg.Space || unit != session.UnitName(cfg.Name) {
					t.Error("holder probe preceded verification or targeted another server")
				}
				if tc.failure == "service" {
					return tc.state, errors.New("service transport failed")
				}
				if tc.delayed {
					timer := time.AfterFunc(40*time.Millisecond, func() { _ = os.Rename(path+".waiting", path) })
					t.Cleanup(func() { timer.Stop() })
				}
				return tc.state, nil
			}
			var notice bytes.Buffer
			syncNativeChatRolesFn = func(ctx context.Context, space string) error {
				return synchronizeChatRoles(ctx, space, store, boxes, &notice)
			}
			// Drive the real release hook of box recreate --no-attach. The body models
			// its completed container replacement; runtime inventory has no session data.
			root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
			box := &cobra.Command{Use: "box"}
			recreate := &cobra.Command{Use: "recreate", RunE: func(*cobra.Command, []string) error { return nil }}
			recreate.Flags().String("space", "work", "")
			recreate.Flags().Bool("no-attach", false, "")
			box.AddCommand(recreate)
			root.AddCommand(box)
			registerSpaceOperations(root)
			root.SetArgs([]string{"box", "recreate", "--no-attach"})
			ctx := t.Context()
			if tc.failure == "missing" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 180*time.Millisecond)
				defer cancel()
			}
			err = root.ExecuteContext(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("lifecycle error = %v; want error %v", err, tc.wantErr)
			}
			saved, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if tc.failure == "inventory" {
				if saved.Generation != r.Generation || !saved.Members[0].ChannelOperator || controlCalls != 0 {
					t.Fatal("uncertain inventory changed roles")
				}
				return
			}
			if saved.Generation != r.Generation+1 || saved.Members[0].ChannelOperator {
				t.Fatal("desired downgrade was not retained")
			}
			if tc.failure == "publication" {
				if controlCalls != 0 || stateCalls != 0 {
					t.Fatal("failed publication reached runtime")
				}
				return
			}
			if err := store.VerifyPublished(saved); err != nil {
				t.Fatal("desired roles were not published", err)
			}
			if tc.failure == "identity" && stateCalls != 0 {
				t.Fatal("invalid server reached holder probe")
			}
			if !tc.wantErr && tc.reloads == 0 && !strings.Contains(notice.String(), "pending generation") {
				t.Fatal("stopped holder lost pending notice", notice.String())
			}
			beforeControl, beforeState := controlCalls, stateCalls
			// Even an uncertain reload must not be retried by another lifecycle sync.
			if err := synchronizeChatRoles(t.Context(), "work", store, boxes, &notice); err != nil {
				t.Fatal(err)
			}
			if controlCalls != beforeControl || stateCalls != beforeState {
				t.Fatal("unchanged roles replayed pending application")
			}
			mu.Lock()
			got := append([]string(nil), ops...)
			mu.Unlock()
			reloads := 0
			for _, op := range got {
				if op == "reload" {
					reloads++
				}
			}
			if reloads != tc.reloads {
				t.Fatalf("control requests %v; want %d reloads", got, tc.reloads)
			}
			if strings.HasPrefix(tc.failure, "ack-") && len(got) != 2 {
				t.Fatalf("uncertain acknowledgment reached another request: %v", got)
			}
			if !tc.wantErr && tc.orphan && notice.Len() != 0 {
				t.Fatalf("live orphan incorrectly deferred: %s", notice.String())
			}
			if tc.reloads != 0 && (len(got) < 2 || got[0] != "status" || got[1] != "reload") {
				t.Fatalf("reload preceded readiness: %v", got)
			}
		})
	}
}

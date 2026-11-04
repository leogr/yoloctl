package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

func TestChatOperatorRequiresCurrentNativeAndLiveAuthority(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_CHAT_ROLE_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatOperatorRequiresCurrentNativeAndLiveAuthority$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_ROLE_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native role fixture: %v\n%s", err, out)
		}
		return
	}
	s, inv := roomRuntimeFixture(t)
	if s.UID == 0 {
		s.UID = 1000
	}
	if s.GID == 0 {
		s.GID = 1000
	}
	r, err := ensureManagedRoom(s, chatRuntimeRequest{Human: "person"}, inv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{Name: "coder", ID: agentbox.NewIdentity(), Space: "work", Capabilities: []string{operator.Capability}}
	inv.Declarations[cfg.Name] = cfg.ID
	r, err = s.Enroll(managedchat.Box{Name: cfg.Name, ID: cfg.ID}, r.Generation, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: cfg.Space, DiskID: s.DiskID, DiskUUID: "fixture", DiskMount: s.Disk, ConfigDir: filepath.Join(s.Disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(s.Disk, "host"), UID: s.UID, GID: s.GID, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	spaceenv.UseCatalogSnapshot(filepath.Join(s.Disk, "catalogs"))
	p, err := s.Projection(managedchat.Box{Name: cfg.Name, ID: cfg.ID})
	if err != nil {
		t.Fatal(err)
	}
	e := operator.Endpoint{Box: cfg.Name, Incarnation: agentbox.NewIdentity()}
	d := &container.Details{ID: "actual-runtime", Name: cfg.Name, Running: true, Labels: map[string]string{
		agentbox.LabelSpace: cfg.Space, agentbox.LabelAgentbox: cfg.Name, managedchat.RoomLabel: r.ID, managedchat.BoxLabel: cfg.ID,
		managedchat.MemberLabel: r.Members[0].ID, managedchat.RoleLabel: "member", managedchat.IncarnationLabel: agentbox.NewIdentity(), operator.IncarnationLabel: e.Incarnation,
	}}
	for _, bind := range p.Binds {
		d.Mounts = append(d.Mounts, container.DetailMount{Source: bind.Source, Destination: bind.Target, ReadOnly: bind.ReadOnly})
	}
	d.Mounts = append(d.Mounts, container.DetailMount{Source: e.Directory(), Destination: operator.Mount, ReadOnly: true})
	box := boxstate.Box{Name: cfg.Name, Config: cfg, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeRunning, Container: &container.Container{Name: cfg.Name}}
	check := func(b boxstate.Box, detail *container.Details, inspectErr error) (map[string]bool, error) {
		return desiredChatOperators(t.Context(), r, []boxstate.Box{b}, func(context.Context, string) (*container.Details, error) { return detail, inspectErr })
	}
	want, err := check(box, d, nil)
	if err != nil || !want[r.Members[0].ID] || len(want) != 1 {
		t.Fatal("valid runtime did not receive account-based role", want, err)
	}
	for name, mutate := range map[string]func(*container.Details){
		"stopped":                func(d *container.Details) { d.Running = false },
		"foreign-space":          func(d *container.Details) { d.Labels[agentbox.LabelSpace] = "other" },
		"missing-incarnation":    func(d *container.Details) { delete(d.Labels, operator.IncarnationLabel) },
		"wrong-incarnation":      func(d *container.Details) { d.Labels[operator.IncarnationLabel] = agentbox.NewIdentity() },
		"copied-membership":      func(d *container.Details) { d.Labels[managedchat.MemberLabel] = managedchat.NewID() },
		"foreign-chat-bind":      func(d *container.Details) { d.Mounts[0].Source = "/unrelated" },
		"writable-operator-bind": func(d *container.Details) { d.Mounts[len(d.Mounts)-1].ReadOnly = false },
	} {
		t.Run(name, func(t *testing.T) {
			next := *d
			next.Labels, next.Mounts = maps.Clone(d.Labels), slices.Clone(d.Mounts)
			mutate(&next)
			got, err := check(box, &next, nil)
			if err != nil || len(got) != 0 {
				t.Fatal("invalid runtime inherited IRC operator", got, err)
			}
		})
	}
	if _, err := check(box, nil, errors.New("inspection failed")); err == nil {
		t.Fatal("failed inspection produced an authoritative role snapshot")
	}
	bad := box
	bad.Runtime = boxstate.RuntimeUnknown
	if _, err := check(bad, d, nil); err == nil {
		t.Fatal("partial inventory authorized role changes")
	}
	for _, mutation := range []func(){func() { cfg.Capabilities = nil }, func() { cfg.Capabilities = []string{operator.Capability}; cfg.ID = agentbox.NewIdentity() }, func() { cfg.ID = r.Members[0].BoxID; r.Members[0].Revoked = true }} {
		mutation()
		got, err := check(box, d, nil)
		if err != nil || len(got) != 0 {
			t.Fatal("revoked/reused declaration inherited role", got, err)
		}
	}
}

func TestChatRoleLifecycleSyncHoldsLockAndFailsBeforeStream(t *testing.T) {
	for _, tc := range []struct {
		name    string
		early   bool
		failure bool
		dry     bool
	}{{name: "finite"}, {name: "stream", early: true}, {name: "failed-stream", early: true, failure: true}, {name: "dry-read", dry: true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			previous := syncNativeChatRolesFn
			defer func() { syncNativeChatRolesFn = previous }()
			calls := 0
			sentinel := errors.New("role reload uncertain")
			syncNativeChatRolesFn = func(ctx context.Context, space string) error {
				calls++
				probe, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
				defer cancel()
				if unlock, err := spacelock.Acquire(probe, space, true); err == nil {
					unlock()
					t.Error("role sync ran after native mutation lock release")
				}
				if tc.failure {
					return sentinel
				}
				return nil
			}
			streamed := false
			root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
			box := &cobra.Command{Use: "box"}
			command := &cobra.Command{Use: "reconcile", RunE: func(cmd *cobra.Command, _ []string) error {
				if tc.early {
					if err := releaseSpaceOperation(cmd.Context()); err != nil {
						return err
					}
					if calls != 1 {
						t.Error("stream began before role sync")
					}
					streamed = true
				}
				return nil
			}}
			command.Flags().String("space", "work", "")
			command.Flags().Bool("dry-run", false, "")
			box.AddCommand(command)
			root.AddCommand(box)
			registerSpaceOperations(root)
			args := []string{"box", "reconcile"}
			if tc.dry {
				args = append(args, "--dry-run")
			}
			root.SetArgs(args)
			err := root.ExecuteContext(t.Context())
			if errors.Is(err, sentinel) != tc.failure || tc.failure && streamed || tc.dry && calls != 0 || !tc.dry && calls != 1 {
				t.Fatal("incorrect lifecycle outcome", calls, streamed, err)
			}
		})
	}
}

func TestChatOperatorExecSetupDoesNotSynchronizeRoles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previous, previousRevalidate := syncNativeChatRolesFn, revalidateOperatorFn
	t.Cleanup(func() { syncNativeChatRolesFn, revalidateOperatorFn = previous, previousRevalidate })
	revalidateOperatorFn = func(context.Context) error { return nil } // the fixture has no operator runtime
	syncNativeChatRolesFn = func(context.Context, string) error {
		t.Fatal("exec target selection attempted to repair chat roles")
		return nil
	}
	called := false
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	box := &cobra.Command{Use: "box"}
	command := &cobra.Command{Use: "exec", RunE: func(cmd *cobra.Command, _ []string) error {
		called = true
		probe, cancel := context.WithTimeout(cmd.Context(), 25*time.Millisecond)
		defer cancel()
		if unlock, err := spacelock.Acquire(probe, "work", true); err == nil {
			unlock()
			t.Fatal("operator exec selected a target without setup locking")
		}
		return releaseSpaceOperation(cmd.Context())
	}}
	command.Flags().String("space", "work", "")
	box.AddCommand(command)
	root.AddCommand(box)
	registerSpaceOperations(root)
	root.SetArgs([]string{"box", "exec", "--space", "work"})
	ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "caller", Incarnation: "live"})
	if err := root.ExecuteContext(ctx); err != nil || !called {
		t.Fatal("operator exec setup failed", err, called)
	}
}

func TestChatNicknameRejectsForeignMutationFieldsBeforeNativeAccess(t *testing.T) {
	for _, input := range []chatRuntimeRequest{{}, {Nickname: "valid", Box: "coder"}, {Nickname: "valid", MemberID: managedchat.NewID()}, {Nickname: "valid", ServerBox: "chat"}, {Nickname: "valid", Human: "replacement"}, {Nickname: "valid", Recreate: true}} {
		payload, _ := json.Marshal(input)
		handled, err := runControllerChatRuntime(t.Context(), controller.Request{Method: "chat.nickname", Payload: payload}, io.Discard, io.Discard)
		if !handled || err == nil || err.Error() != "chat nickname accepts only nickname and desired generation" {
			t.Fatal("invalid nickname request reached native access", input, err)
		}
	}
}

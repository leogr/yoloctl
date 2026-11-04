package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// destroyChatFixture is a prepared room with "worker" enrolled under id,
// the destroyed box already absent from the space inventory, and the chat
// server's runtime application recorded instead of reloaded.
func destroyChatFixture(t *testing.T) (managedchat.Store, string, *[]uint64) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	id := agentbox.NewIdentity()
	inventory.Declarations["worker"] = id
	if _, err := store.Enroll(managedchat.Box{Name: "worker", ID: id}, room.Generation, inventory); err != nil {
		t.Fatal(err)
	}
	oldStore, oldResolver, oldApply := nativeChatStoreFn, newBoxStateResolver, destroyChatApplyFn
	t.Cleanup(func() { nativeChatStoreFn, newBoxStateResolver, destroyChatApplyFn = oldStore, oldResolver, oldApply })
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) { return nil, nil },
		}
	}
	var applied []uint64
	destroyChatApplyFn = func(_ context.Context, r *managedchat.Room, _ managedchat.Rendered, _ []boxstate.Box) error {
		applied = append(applied, r.Generation)
		return nil
	}
	return store, id, &applied
}

func loadRoom(t *testing.T, store managedchat.Store) *managedchat.Room {
	t.Helper()
	r, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Destroying an enrolled box revokes its own membership, applies it to the
// server and says so; a new box with the same name can then be enrolled.
func TestDestroyRevokesOwnChatMembershipSoTheNameCanBeReused(t *testing.T) {
	store, id, applied := destroyChatFixture(t)
	before := loadRoom(t, store)
	member, _ := activeChatMember(before, "worker")
	var notice bytes.Buffer
	revokeDestroyedChatMember(t.Context(), "work", "worker", id, &notice)
	after := loadRoom(t, store)
	if _, active := activeChatMember(after, "worker"); active {
		t.Fatal("destroyed box kept an active membership")
	}
	if after.Generation != before.Generation+1 || len(*applied) != 1 || (*applied)[0] != after.Generation {
		t.Fatalf("revocation not saved then applied once: generation %d→%d, applied %v", before.Generation, after.Generation, *applied)
	}
	if want := "notice: revoked box worker's chat membership (member " + member.ID + "); the name can be enrolled again\n"; notice.String() != want {
		t.Fatalf("notice %q", notice.String())
	}
	// The reused name now enrolls: the refusal that motivated this is gone.
	reused := agentbox.NewIdentity()
	inventory := managedchat.Inventory{Complete: true, Declarations: map[string]string{"worker": reused}, Runtime: map[string]bool{}}
	r, err := store.Enroll(managedchat.Box{Name: "worker", ID: reused}, after.Generation, inventory)
	if err != nil {
		t.Fatal("reused name refused after destroy:", err)
	}
	if m, ok := activeChatMember(r, "worker"); !ok || m.BoxID != reused {
		t.Fatal("reused name not enrolled as the new box", m)
	}
}

// Only the destroyed box's own membership goes: another identity under the
// same name, an unknown identity, other names and a missing room are left
// alone, with a warning naming the explicit command where one is due.
func TestDestroyLeavesOtherChatMembershipsAlone(t *testing.T) {
	store, id, applied := destroyChatFixture(t)
	for _, tc := range []struct{ name, id, warn string }{
		{"worker", agentbox.NewIdentity(), "belongs to another box identity and was kept; if it is stale, run yoloctl chat revoke worker --space work"},
		{"worker", "", "was kept: the destroyed box's identity is unknown (its declaration was unreadable); if it is stale, run yoloctl chat revoke worker --space work"},
		{"someone-else", id, ""},
	} {
		var notice bytes.Buffer
		revokeDestroyedChatMember(t.Context(), "work", tc.name, tc.id, &notice)
		if _, active := activeChatMember(loadRoom(t, store), "worker"); !active || len(*applied) != 0 {
			t.Fatalf("%s/%q revoked another membership", tc.name, tc.id)
		}
		if tc.warn == "" && notice.Len() != 0 || tc.warn != "" && !strings.Contains(notice.String(), tc.warn) {
			t.Fatalf("%s/%q notice %q", tc.name, tc.id, notice.String())
		}
	}
	nativeChatStoreFn = func() (managedchat.Store, error) {
		empty, _ := roomRuntimeFixture(t)
		return empty, nil
	}
	var notice bytes.Buffer
	revokeDestroyedChatMember(t.Context(), "work", "worker", id, &notice)
	if notice.Len() != 0 {
		t.Fatalf("no room still printed %q", notice.String())
	}
}

// The cleanup itself never fails: a saved revocation the server did not
// apply says so and names chat ensure; an unavailable store names chat revoke.
// (The release-path outcome is covered by the wrapped tests below.)
func TestDestroyChatRevocationFailuresOnlyWarn(t *testing.T) {
	store, id, _ := destroyChatFixture(t)
	destroyChatApplyFn = func(context.Context, *managedchat.Room, managedchat.Rendered, []boxstate.Box) error {
		return errors.New("managed server is absent")
	}
	var notice bytes.Buffer
	revokeDestroyedChatMember(t.Context(), "work", "worker", id, &notice)
	if _, active := activeChatMember(loadRoom(t, store), "worker"); active {
		t.Fatal("desired revocation was not saved before application")
	}
	if got := notice.String(); !strings.HasPrefix(got, "warning: box worker's chat membership") || !strings.Contains(got, "has not applied it yet (managed server is absent); run yoloctl chat ensure --space work") {
		t.Fatalf("apply failure notice %q", got)
	}
	nativeChatStoreFn = func() (managedchat.Store, error) { return managedchat.Store{}, errors.New("native disk unavailable") }
	notice.Reset()
	revokeDestroyedChatMember(t.Context(), "work", "worker", id, &notice)
	if got := notice.String(); !strings.Contains(got, "could not be checked (native disk unavailable)") || !strings.Contains(got, "run yoloctl chat revoke worker --space work") {
		t.Fatalf("store failure notice %q", got)
	}
}

// A destroy that will revoke a membership records the room and its server
// in its effect footprint, as chat revoke does; --keep-manifest does not.
func TestDestroyFootprintCoversTheRevokedMembership(t *testing.T) {
	runtimeAccountFixture(t) // a declared ephemeral box "dev" in space "work"
	oldResolver, oldMember := newBoxStateResolver, chatMemberServer
	t.Cleanup(func() { newBoxStateResolver, chatMemberServer = oldResolver, oldMember })
	newBoxStateResolver = runtimeAccountResolver(t, nil, nil)
	chatMemberServer = func(name string) (string, bool, error) { return "chat", name == "dev", nil }
	intent := func(keep bool, name string) *controller.Scope {
		cmd := &cobra.Command{Use: "destroy"}
		cmd.Flags().Bool("keep-manifest", false, "")
		if keep {
			_ = cmd.Flags().Set("keep-manifest", "true")
		}
		cmd.SetContext(t.Context())
		return spaceOperationIntent(cmd, "box destroy", "work", []string{name}).Scope
	}
	covers := func(s *controller.Scope) bool {
		return s != nil && !s.Global && slices.Contains(s.Writes, chatDomain) && slices.Contains(s.Writes, boxResource("chat"))
	}
	if s := intent(false, "dev"); !covers(s) {
		t.Fatalf("destroy of a member lacks the chat footprint: %+v", s)
	}
	if s := intent(true, "dev"); s == nil || s.Global || slices.Contains(s.Writes, boxResource("chat")) {
		t.Fatalf("--keep-manifest claims the chat server: %+v", s)
	}
	if s := intent(false, "other"); s == nil || s.Global || slices.Contains(s.Writes, boxResource("chat")) {
		t.Fatalf("a non-member destroy claims the chat server: %+v", s)
	}
	chatMemberServer = func(string) (string, bool, error) { return "", false, errors.New("unreadable room") }
	if s := intent(false, "dev"); s == nil || !s.Global {
		t.Fatalf("unreadable room did not fail closed: %+v", s)
	}
}

// wrappedDestroy drives the real space-operation release of `box destroy`:
// the body models a completed destroy that then runs the membership cleanup,
// and the release synchronizes chat roles from the same store.
func wrappedDestroy(t *testing.T, store managedchat.Store, boxID string, notice *bytes.Buffer) error {
	t.Helper()
	previous := syncNativeChatRolesFn
	t.Cleanup(func() { syncNativeChatRolesFn = previous })
	syncNativeChatRolesFn = func(ctx context.Context, space string) error {
		boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
		if err != nil {
			return err
		}
		if _, err := nativeChatStoreFn(); err != nil {
			return err // the release reads the same native store
		}
		return synchronizeChatRoles(ctx, space, store, boxes, notice)
	}
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	box := &cobra.Command{Use: "box"}
	destroy := &cobra.Command{Use: "destroy", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		revokeDestroyedChat(cmd.Context(), "work", args[0], boxID, notice)
		return nil
	}}
	destroy.Flags().String("space", "work", "")
	destroy.Flags().Bool("keep-manifest", false, "")
	box.AddCommand(destroy)
	root.AddCommand(box)
	registerSpaceOperations(root)
	root.SetArgs([]string{"box", "destroy", "worker", "--space", "work"})
	return root.ExecuteContext(t.Context())
}

// Through the real release path: a revocation the server did not apply keeps
// the destroy successful, with one application attempt and no reload replay
// at role publication; the next enrollment of the name is not refused.
func TestWrappedDestroyKeepsSavedButUnappliedRevocationSuccessful(t *testing.T) {
	store, id, _ := destroyChatFixture(t)
	attempts := 0
	destroyChatApplyFn = func(context.Context, *managedchat.Room, managedchat.Rendered, []boxstate.Box) error {
		attempts++
		return errors.New("managed server is absent")
	}
	var notice bytes.Buffer
	if err := wrappedDestroy(t, store, id, &notice); err != nil {
		t.Fatal("saved-but-unapplied revocation failed the destroy:", err)
	}
	if attempts != 1 || !strings.Contains(notice.String(), "run yoloctl chat ensure --space work") {
		t.Fatalf("attempts %d, notice %q", attempts, notice.String())
	}
	if _, active := activeChatMember(loadRoom(t, store), "worker"); active {
		t.Fatal("revocation was not durable")
	}
}

// An unreadable chat store is not new to destroy: role publication at
// release already fails every box lifecycle command on it, and the cleanup
// does not hide that. The destroy reports the release failure, and the
// cleanup's warning names the explicit command for the membership.
func TestWrappedDestroyWithUnreadableChatStoreKeepsTheReleaseFailure(t *testing.T) {
	store, id, _ := destroyChatFixture(t)
	unreadable := errors.New("native disk unavailable")
	nativeChatStoreFn = func() (managedchat.Store, error) { return managedchat.Store{}, unreadable }
	var notice bytes.Buffer
	err := wrappedDestroy(t, store, id, &notice)
	if !errors.Is(err, unreadable) {
		t.Fatal("release did not report the unreadable store", err)
	}
	if !strings.Contains(notice.String(), "could not be checked (native disk unavailable)") || !strings.Contains(notice.String(), "run yoloctl chat revoke worker --space work") {
		t.Fatalf("notice %q", notice.String())
	}
}

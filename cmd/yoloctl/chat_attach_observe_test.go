package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// attachObserveFixture enrolls one declared box in a private room fixture.
// Declarations live under a temporary XDG config root; no native context or
// VM is configured, so the resolver fixtures below are the only runtime.
func attachObserveFixture(t *testing.T) (managedchat.Store, managedchat.Inventory, *managedchat.Room, *agentbox.Config) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "work", "/tmp/def")
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: "coder", ID: agentbox.NewIdentity(), Space: "work",
		Agent: "none", Account: "none", Image: "img", Workdir: agentbox.NewEphemeralWorkdir("coder")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	inventory.Declarations[cfg.Name] = cfg.ID
	room, err = store.Enroll(managedchat.Box{Name: cfg.Name, ID: cfg.ID}, room.Generation, inventory)
	if err != nil || len(room.Members) != 1 {
		t.Fatal("enroll fixture", room, err)
	}
	oldStore, oldResolver := nativeChatStoreFn, newBoxStateResolver
	t.Cleanup(func() { nativeChatStoreFn, newBoxStateResolver = oldStore, oldResolver })
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
	return store, inventory, room, cfg
}

func attachObserveRuntime(list func(context.Context) ([]container.Container, error)) func() boxstate.Resolver {
	return func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(ctx context.Context, _ string) ([]container.Container, error) { return list(ctx) },
		}
	}
}

func lookupAttachTarget(ctx context.Context, memberID string) (managedchat.AttachTarget, error) {
	payload, _ := json.Marshal(chatRuntimeRequest{MemberID: memberID})
	var out bytes.Buffer
	_, err := runControllerChatRuntime(ctx, controller.Request{Method: "chat.attach-target", Space: "work", Payload: payload}, &out, io.Discard)
	var target managedchat.AttachTarget
	if err == nil {
		if err := json.Unmarshal(out.Bytes(), &target); err != nil {
			return target, err
		}
	}
	return target, err
}

func TestChatAttachTargetBypassesHeldSpaceLock(t *testing.T) {
	store, _, room, cfg := attachObserveFixture(t)
	manifest := filepath.Join(store.Disk, layout.ChatManifest)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// A real kernel flock on the private fixture path stands in for a
	// lifecycle mutation or storage cleanup holding the exclusive lock.
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for status, want := range map[string]string{"Up 1 minute": "running", "Exited (0) 1 minute ago": "stopped"} {
		newBoxStateResolver = attachObserveRuntime(func(context.Context) ([]container.Container, error) {
			return []container.Container{{Name: cfg.Name, Status: status}}, nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		target, err := lookupAttachTarget(ctx, room.Members[0].ID)
		cancel()
		if err != nil || target.State != want || target.Box != cfg.Name || target.BoxID != cfg.ID || target.RoomID != room.ID || target.MemberID != room.Members[0].ID {
			t.Fatal("lock-free lookup", status, target, err)
		}
	}
	after, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("attach lookup mutated native state", err)
	}
}

func TestChatAttachTargetPreviewFailsClosed(t *testing.T) {
	store, inventory, room, cfg := attachObserveFixture(t)
	member := room.Members[0].ID
	running := []container.Container{{Name: cfg.Name, Status: "Up 1 minute"}}
	for name, list := range map[string]func(context.Context) ([]container.Container, error){
		// A slow inventory ends at the bound with an explicit deadline, never
		// a stopped or missing participant.
		"slow-inventory": func(ctx context.Context) ([]container.Container, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		// A helper that answers right at the deadline is still too late.
		"answer-at-deadline": func(ctx context.Context) ([]container.Container, error) {
			<-ctx.Done()
			return running, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			newBoxStateResolver = attachObserveRuntime(list)
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			target, err := lookupAttachTarget(ctx, member)
			if !errors.Is(err, errChatAttachLookupDeadline) || target != (managedchat.AttachTarget{}) {
				t.Fatal("deadline became a participant state", target, err)
			}
		})
	}
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
			return nil, errors.New("vm list failed")
		}}
	}
	if target, err := lookupAttachTarget(t.Context(), member); err == nil || !strings.Contains(err.Error(), "runtime inventory unavailable") || errors.Is(err, errChatAttachLookupDeadline) {
		t.Fatal("unavailable inventory was not an explicit failure", target, err)
	}
	newBoxStateResolver = attachObserveRuntime(func(context.Context) ([]container.Container, error) {
		return nil, errors.New("container list failed")
	})
	if target, err := lookupAttachTarget(t.Context(), member); err == nil || target.State != "" {
		t.Fatal("failed container listing became a participant state", target, err)
	}
	// A room change between the two snapshots refuses the whole preview.
	newBoxStateResolver = attachObserveRuntime(func(context.Context) ([]container.Container, error) {
		if _, err := store.SetHumanNickname("renamed", room.Generation, inventory); err != nil {
			t.Fatal(err)
		}
		return running, nil
	})
	if target, err := lookupAttachTarget(t.Context(), member); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatal("published a preview across a room change", target, err)
	}
	room, err := store.ReadManifestSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	newBoxStateResolver = attachObserveRuntime(func(context.Context) ([]container.Container, error) { return running, nil })
	if _, err := lookupAttachTarget(t.Context(), room.Human.ID); err == nil {
		t.Fatal("human resolved to a box")
	}
	// Name reuse: the box was deleted and redeclared with a new immutable
	// identity under the same name.
	declaration, err := layout.BoxPath("work", cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	redeclare := func(c agentbox.Config) {
		t.Helper()
		if err := os.Remove(declaration); err != nil {
			t.Fatal(err)
		}
		if err := c.Save(); err != nil {
			t.Fatal(err)
		}
	}
	replaced := *cfg
	replaced.ID = agentbox.NewIdentity()
	redeclare(replaced)
	if target, err := lookupAttachTarget(t.Context(), member); err != nil || target.Box != "" || target.State == "running" || target.State == "stopped" {
		t.Fatal("replacement declaration resolved as the member's box", target, err)
	}
	redeclare(*cfg)
	if _, err := store.Revoke(member, room.Generation, inventory); err != nil {
		t.Fatal(err)
	}
	if target, err := lookupAttachTarget(t.Context(), member); err != nil || target.State != "revoked" {
		t.Fatal("revoked member", target, err)
	}
}

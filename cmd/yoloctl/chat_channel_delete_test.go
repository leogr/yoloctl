package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// fakeChatControl answers status and reload like the managed AIRC control
// socket: a reload applies whatever desired state the store now renders.
type fakeChatControl struct {
	mu         sync.Mutex
	features   []string
	loseReload bool
	applied    managedchat.Rendered
	calls      []string
}

func (f *fakeChatControl) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeChatControl) serve(t *testing.T, store managedchat.Store) managedchat.Control {
	t.Helper()
	dir, err := os.MkdirTemp("", "chat-delete-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	incarnation := managedchat.NewID()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			var request struct {
				Op string `json:"op"`
			}
			_ = json.Unmarshal([]byte(line), &request)
			f.mu.Lock()
			f.calls = append(f.calls, request.Op)
			var reply any
			switch {
			case request.Op == "reload" && f.loseReload:
			case request.Op == "reload":
				r, err := store.Load()
				if err == nil {
					f.applied, err = store.Expected(r)
				}
				if err == nil {
					reply = map[string]any{"ok": true, "result": map[string]any{"incarnation": incarnation, "applied_generation": f.applied.Generation, "config_sha256": f.applied.ConfigSHA256}}
				}
			default:
				reply = map[string]any{"ok": true, "result": managedchat.Applied{
					Features: managedchat.Features{Protocol: 1, Features: f.features}, Incarnation: incarnation,
					Generation: f.applied.Generation, ConfigSHA256: f.applied.ConfigSHA256,
					Listeners:          managedchat.Listeners{IRC: "unix", Artifacts: "unix"},
					ConfiguredAccounts: uint64(len(f.applied.Accounts)), Accounts: map[string]managedchat.Connections{},
				}}
			}
			f.mu.Unlock()
			if reply != nil {
				_ = json.NewEncoder(conn).Encode(reply)
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return managedchat.Control{Socket: socket, VerifyPeer: func(*net.UnixConn) error { return nil }}
}

// deletableRoom is a prepared, published room with #build applied by f.
func deletableRoom(t *testing.T, f *fakeChatControl) (managedchat.Store, managedchat.Inventory, *managedchat.Room) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{DefaultHuman: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	room, err = store.CreateChannel(room.ID, "#build", room.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if f.applied, err = store.Render(room); err != nil {
		t.Fatal(err)
	}
	return store, inventory, room
}

func serverConfigBytes(t *testing.T, store managedchat.Store, r *managedchat.Room) []byte {
	t.Helper()
	base, _ := layout.ChatRoomRelative(r.ID)
	raw, err := os.ReadFile(filepath.Join(store.Disk, base, "server/config", managedchat.ServerConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var deleteFeatures = []string{"unix-listeners", "control-reload", "acl-revoke", "server-presence", "channel-add-reload", "channel-delete-reload"}

func TestChatChannelDeleteRequiresTheServerCapabilityBeforeAnyEffect(t *testing.T) {
	f := &fakeChatControl{features: deleteFeatures[:5]}
	store, inventory, room := deletableRoom(t, f)
	control := f.serve(t, store)
	before := serverConfigBytes(t, store, room)
	input := chatChannelRequest{RoomID: room.ID, ChannelID: room.Channels[1].ID, Generation: room.Generation, Confirm: true}
	changed, err := mutateChatChannels(t.Context(), "chat.channel.delete", input, store, inventory, room, control)
	if changed || err == nil || !strings.Contains(err.Error(), "upgrade required") || !strings.Contains(err.Error(), "channel-delete-reload") {
		t.Fatal("old server was not refused as needing an upgrade", changed, err)
	}
	kept, _ := store.Load()
	if kept.Generation != room.Generation || len(kept.Channels) != 2 || kept.DeletedChannels != nil || !bytes.Equal(before, serverConfigBytes(t, store, room)) {
		t.Fatal("refused deletion saved or published state")
	}
	if !slices.Equal(f.callLog(), []string{"status"}) {
		t.Fatal("refused deletion reloaded the server", f.callLog())
	}
}

func TestChatChannelDeleteAppliesOnceAndNeverReplaysAnUnconfirmedReload(t *testing.T) {
	f := &fakeChatControl{features: deleteFeatures}
	store, inventory, room := deletableRoom(t, f)
	control := f.serve(t, store)
	build := room.Channels[1]
	input := chatChannelRequest{RoomID: room.ID, ChannelID: build.ID, Generation: room.Generation, Confirm: true}
	changed, err := mutateChatChannels(t.Context(), "chat.channel.delete", input, store, inventory, room, control)
	if !changed || err != nil {
		t.Fatal(changed, err)
	}
	deleted, _ := store.Load()
	if !slices.Equal(f.callLog(), []string{"status", "reload", "status"}) || len(deleted.Channels) != 1 || len(deleted.DeletedChannels) != 1 || deleted.DeletedChannels[0].ID != build.ID {
		t.Fatal("deletion was not one confirmed reload of the saved desired state", f.callLog(), deleted.DeletedChannels)
	}
	if !bytes.Contains(serverConfigBytes(t, store, deleted), []byte(`removed_channels = ['#build']`)) {
		t.Fatal("published server configuration lacks the explicit deletion intent")
	}
	// Recreate the name, then delete it again with a lost acknowledgment: the
	// desired state stays saved but unconfirmed and nothing is retried.
	if _, err := mutateChatChannels(t.Context(), "chat.channel.create", chatChannelRequest{RoomID: room.ID, Channel: "#build", Generation: deleted.Generation}, store, inventory, deleted, control); err != nil {
		t.Fatal(err)
	}
	recreated, _ := store.Load()
	if recreated.Channels[1].ID == build.ID {
		t.Fatal("recreated channel reused the deleted identity")
	}
	f.mu.Lock()
	f.calls, f.loseReload = nil, true
	f.mu.Unlock()
	input = chatChannelRequest{RoomID: room.ID, ChannelID: recreated.Channels[1].ID, Generation: recreated.Generation, Confirm: true}
	changed, err = mutateChatChannels(t.Context(), "chat.channel.delete", input, store, inventory, recreated, control)
	var pendingPublication *publicationPending
	if !changed || err == nil || !strings.Contains(err.Error(), "application unconfirmed") || !errors.As(err, &pendingPublication) {
		t.Fatal("lost acknowledgment reported as applied", changed, err)
	}
	if !slices.Equal(f.callLog(), []string{"status", "reload"}) {
		t.Fatal("unconfirmed reload was retried", f.callLog())
	}
	pending, _ := store.Load()
	if pending.Generation != recreated.Generation+1 || len(pending.DeletedChannels) != 2 {
		t.Fatal("unconfirmed deletion lost its saved desired state")
	}
	// Until explicit inspection settles it, another change (a same-name create
	// included) is refused before any save or reload.
	f.mu.Lock()
	f.calls, f.loseReload = nil, false
	f.applied.Generation = recreated.Generation
	f.mu.Unlock()
	if _, err := mutateChatChannels(t.Context(), "chat.channel.create", chatChannelRequest{RoomID: room.ID, Channel: "#build", Generation: pending.Generation}, store, inventory, pending, control); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatal("create accepted while a deletion was unconfirmed", err)
	}
	if kept, _ := store.Load(); kept.Generation != pending.Generation || !slices.Equal(f.callLog(), []string{"status"}) {
		t.Fatal("refused create saved or reloaded", f.callLog())
	}
}

func TestChatChannelDeleteRequestAndCLIAreExplicit(t *testing.T) {
	room, channel := managedchat.NewID(), managedchat.NewID()
	valid := `{"room_id":"` + room + `","channel_id":"` + channel + `","generation":7,"confirm":true}`
	if input, err := decodeChatChannelRequest("chat.channel.delete", []byte(valid)); err != nil || !input.Confirm || input.ChannelID != channel {
		t.Fatal(input, err)
	}
	operation, ok := controllerOperations["chat.channel.delete"]
	if !ok || !operation.BoxAllowed || !operation.Mutates || !operation.EffectGated || !operatorChatMethod("chat.channel.delete") || !chatChannelMethod("chat.channel.delete") {
		t.Fatal("delete authority/admission registry is inconsistent")
	}
	for _, raw := range []string{
		strings.Replace(valid, `"confirm":true`, `"confirm":false`, 1),
		strings.Replace(valid, `,"confirm":true`, ``, 1),
		strings.Replace(valid, `"confirm":true`, `"confirm":null`, 1),
		strings.Replace(valid, `"`+channel+`"`, `"#build"`, 1),
		strings.Replace(valid, `"generation":7`, `"generation":0`, 1),
		strings.TrimSuffix(valid, "}") + `,"member_id":"` + managedchat.NewID() + `"}`,
	} {
		if _, err := decodeChatChannelRequest("chat.channel.delete", []byte(raw)); err == nil {
			t.Fatal("invalid delete request accepted", raw)
		}
	}
	snapshot := chatChannelSnapshot{RoomID: room, Generation: 7, DefaultChannel: managedchat.DefaultChannel, Channels: []managedchat.Channel{{ID: managedchat.NewID(), Name: managedchat.DefaultChannel}, {ID: channel, Name: "#build"}}}
	input, err := channelRequestFromSnapshot(snapshot, "delete", []string{"BUILD"}, 0)
	if err != nil || input.ChannelID != channel || !input.Confirm || input.Generation != 7 || input.MemberID != "" {
		t.Fatal("delete did not resolve one immutable identity", input, err)
	}
	for _, name := range []string{"space", "#absent"} {
		if _, err := channelRequestFromSnapshot(snapshot, "delete", []string{name}, 0); err == nil {
			t.Fatal("delete target accepted", name)
		}
	}
	cmd := newChatCmd()
	cmd.SetArgs([]string{"channel", "delete", "build"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatal("deletion ran without explicit confirmation", err)
	}
}

func TestChatStreamRefusesANameDeletedAfterItOpened(t *testing.T) {
	f := &fakeChatControl{features: deleteFeatures}
	store, inventory, room := deletableRoom(t, f)
	origin := newChatStreamOrigin(room)
	check := func(origin chatStreamOrigin, channel string) error {
		return authorizeChatStreamChannel(t.Context(), store, origin, channel)
	}
	if err := check(origin, "#build"); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteChannel(room.ID, room.Channels[1].ID, room.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	recreated, err := store.CreateChannel(room.ID, "#build", deleted.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := check(origin, "#build"); err == nil || !strings.Contains(err.Error(), "open a new chat connection") {
		t.Fatal("old stream may address a replacement with the deleted name", err)
	}
	if err := check(origin, "#space"); err != nil {
		t.Fatal("deletion stopped another channel's access", err)
	}
	// A stream opened after the deletion may use the replacement.
	if err := check(newChatStreamOrigin(recreated), "#build"); err != nil {
		t.Fatal(err)
	}
	// Original catalog IDs are checked too, not only deletion generations.
	stale := newChatStreamOrigin(recreated)
	stale.Channels["#build"] = managedchat.NewID()
	if err := check(stale, "#build"); err == nil {
		t.Fatal("a different original channel identity was authorized")
	}
}

// The view binds each channel view to its identity, so the host catalog
// carries channel IDs and the deletion ledger, also in the first catalog.
func TestChatCatalogCarriesChannelIdentityToTheView(t *testing.T) {
	f := &fakeChatControl{features: deleteFeatures}
	store, inventory, room := deletableRoom(t, f)
	deleted, err := store.DeleteChannel(room.ID, room.Channels[1].ID, room.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	header, err := chatstream.RoomHeader(deleted)
	if err != nil {
		t.Fatal(err)
	}
	msg := chatCatalog(chatstream.Catalog{Header: header})
	if len(msg.Channels) != 1 || msg.Channels[0].ID != deleted.Channels[0].ID || !slices.Equal(msg.Deleted, []string{room.Channels[1].ID}) {
		t.Fatalf("view catalog lost channel identity or deletion records: %+v", msg)
	}
}

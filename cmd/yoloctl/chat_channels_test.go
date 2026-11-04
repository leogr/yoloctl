package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
)

func TestChatChannelTypedRequestsAreStrict(t *testing.T) {
	room, channel, member, box := managedchat.NewID(), managedchat.NewID(), managedchat.NewID(), managedchat.NewID()
	create := `{"room_id":"` + room + `","channel":"#build","generation":7}`
	edit := `{"room_id":"` + room + `","channel_id":"` + channel + `","member_id":"` + member + `","box_id":"` + box + `","generation":7}`
	for _, test := range []struct{ method, raw string }{{"chat.channel.list", `{}`}, {"chat.channel.create", create}, {"chat.channel.add", edit}, {"chat.channel.remove", edit}} {
		if _, err := decodeChatChannelRequest(test.method, []byte(test.raw)); err != nil {
			t.Fatal(test.method, err)
		}
		operation, ok := controllerOperations[test.method]
		if !ok || !operation.BoxAllowed || !operatorChatMethod(test.method) || operation.Mutates != (test.method != "chat.channel.list") {
			t.Fatal("channel authority/admission registry is inconsistent", test.method)
		}
	}
	for _, test := range []struct{ method, raw string }{
		{"chat.channel.list", `null`}, {"chat.channel.list", create}, {"chat.channel.create", `{}`},
		{"chat.channel.create", strings.Replace(create, `"#build"`, `"#Build"`, 1)},
		{"chat.channel.create", strings.Replace(create, `"#build"`, `"#build,#space"`, 1)},
		{"chat.channel.create", strings.Replace(create, `"#build"`, `null`, 1)},
		{"chat.channel.create", strings.Replace(create, `"generation":7`, `"generation":0`, 1)},
		{"chat.channel.create", strings.TrimSuffix(create, "}") + `,"extra":false}`},
		{"chat.channel.add", create}, {"chat.channel.add", strings.Replace(edit, `"`+member+`"`, `null`, 1)},
		{"chat.channel.remove", strings.Replace(edit, `"`+box+`"`, `"../box"`, 1)},
		{"chat.channel.delete", edit},
	} {
		if _, err := decodeChatChannelRequest(test.method, []byte(test.raw)); err == nil {
			t.Fatal("invalid typed request accepted", test.method, test.raw)
		}
	}
	for _, method := range []string{"chat.stream", "chat.nickname", "chat.attach-target", "chat.channel.raw"} {
		if chatChannelMethod(method) {
			t.Fatal("channel surface widened", method)
		}
	}
}

func TestChatChannelRelayReloadsNativeCatalogWithoutLockWait(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{DefaultHuman: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	check := func(roomID, diskID, humanID, channel string) error {
		return authorizeChatStreamChannel(t.Context(), store, chatStreamOrigin{RoomID: roomID, DiskID: diskID, HumanID: humanID, Generation: room.Generation}, channel)
	}
	if err := check(room.ID, room.DiskID, room.Human.ID, "#space"); err != nil {
		t.Fatal(err)
	}
	if err := check(room.ID, room.DiskID, room.Human.ID, "#build"); err == nil {
		t.Fatal("unknown channel authorized")
	}
	if _, err := store.CreateChannel(room.ID, "#build", room.Generation, inventory); err != nil {
		t.Fatal(err)
	}
	if err := check(room.ID, room.DiskID, room.Human.ID, "#build"); err != nil {
		t.Fatal("relay retained its startup-only catalog", err)
	}
	for _, ids := range [][3]string{{managedchat.NewID(), room.DiskID, room.Human.ID}, {room.ID, managedchat.NewID(), room.Human.ID}, {room.ID, room.DiskID, managedchat.NewID()}} {
		if err := check(ids[0], ids[1], ids[2], "#space"); err == nil {
			t.Fatal("relay accepted changed immutable authority")
		}
	}
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = authorizeChatStreamChannel(ctx, store, newChatStreamOrigin(room), "#build")
	unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatal("catalog snapshot ignored cancellation", err)
	}
	manifest := filepath.Join(store.Disk, "config", "chat.json")
	if err := os.Rename(manifest, manifest+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manifest+".saved", manifest); err != nil {
		t.Fatal(err)
	}
	if err := check(room.ID, room.DiskID, room.Human.ID, "#space"); err == nil {
		t.Fatal("relay followed a substituted native declaration")
	}
}

func TestChatChannelRelaySendSurvivesExclusiveSpaceLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{DefaultHuman: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	// A real OS space lock, held throughout both forwarded requests and the
	// invalid-authority checks. Its path is private to this fixture's XDG root.
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var out bytes.Buffer
	request := "{\"id\":\"send\",\"op\":\"send\",\"target\":\"#space\",\"text\":\"during mutation\"}\n"
	authorize := func(channel string) error {
		return authorizeChatStreamChannel(ctx, store, newChatStreamOrigin(room), channel)
	}
	if err := chatstream.FilterChannels(strings.NewReader(request+request), &out, authorize, false); err != nil {
		t.Fatal("exclusive mutation ended the valid stream", err)
	}
	if strings.Count(out.String(), "during mutation") != 2 {
		t.Fatal("stream stopped after its first valid request", out.String())
	}
	for _, ids := range [][3]string{{managedchat.NewID(), room.DiskID, room.Human.ID}, {room.ID, managedchat.NewID(), room.Human.ID}, {room.ID, room.DiskID, managedchat.NewID()}} {
		out.Reset()
		err := chatstream.FilterChannels(strings.NewReader(request), &out, func(channel string) error {
			return authorizeChatStreamChannel(ctx, store, chatStreamOrigin{RoomID: ids[0], DiskID: ids[1], HumanID: ids[2], Generation: room.Generation}, channel)
		}, false)
		if err == nil || out.Len() != 0 {
			t.Fatal("lock-free snapshot admitted invalid identity", ids, err)
		}
	}
}

func TestChatChannelCLIResolvesOneImmutableSnapshot(t *testing.T) {
	room, channel, member, box := managedchat.NewID(), managedchat.NewID(), managedchat.NewID(), managedchat.NewID()
	snapshot := chatChannelSnapshot{RoomID: room, Generation: 7, Channels: []managedchat.Channel{{ID: channel, Name: "#build"}}, Identities: []managedchat.BootstrapIdentity{{ID: managedchat.NewID(), Name: "coder", Role: "member", Revoked: true}, {ID: member, BoxID: box, Name: "coder", Role: "member"}}}
	input, err := channelRequestFromSnapshot(snapshot, "add", []string{"coder", "BuIlD"}, 0)
	if err != nil || input.RoomID != room || input.ChannelID != channel || input.MemberID != member || input.BoxID != box || input.Generation != 7 {
		t.Fatal("CLI lost immutable selection/CAS", err)
	}
	input, err = channelRequestFromSnapshot(snapshot, "remove", []string{"coder", "#build"}, 4)
	if err != nil || input.Generation != 4 {
		t.Fatal("explicit CAS overridden", err)
	}
	input, err = channelRequestFromSnapshot(snapshot, "create", []string{"Other"}, 0)
	if err != nil || input.Channel != "#other" || input.ChannelID != "" {
		t.Fatal("create did not normalize once", err)
	}
	if _, err := channelRequestFromSnapshot(snapshot, "add", []string{"unknown", "#build"}, 0); err == nil {
		t.Fatal("missing enrollment was invented")
	}
	if _, err := channelRequestFromSnapshot(snapshot, "remove", []string{"coder", "#absent"}, 0); err == nil {
		t.Fatal("missing catalog target was invented")
	}
	cmd := newChatCmd()
	for _, args := range [][]string{{"channel", "list"}, {"channel", "create"}, {"channel", "add"}, {"channel", "remove"}} {
		found, _, err := cmd.Find(args)
		if err != nil || found == nil || found.Name() != args[1] {
			t.Fatal("channel CLI is not registered", args, err)
		}
	}
}

func TestChatChannelApplyUsesOneReloadAndExactProcessProof(t *testing.T) {
	for _, kind := range []string{"success", "lost ack", "replacement ack", "replacement status", "wrong digest"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "chat-channel-control-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			socket := filepath.Join(dir, "control.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			before := managedchat.Applied{Features: managedchat.Features{Protocol: 1, Features: []string{"unix-listeners", "control-reload", "acl-revoke", "server-presence", "channel-add-reload"}}, Incarnation: managedchat.NewID(), Generation: 3, ConfigSHA256: strings.Repeat("a", 64), Listeners: managedchat.Listeners{IRC: "unix", Artifacts: "unix"}, ConfiguredAccounts: 1, Accounts: map[string]managedchat.Connections{"human": {Connections: 1}}}
			proof := managedchat.Rendered{Generation: 4, ConfigSHA256: strings.Repeat("b", 64), Accounts: []string{"human"}}
			var calls atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					conn, err := listener.AcceptUnix()
					if err != nil {
						return
					}
					line, err := bufio.NewReader(conn).ReadString('\n')
					if err != nil {
						_ = conn.Close()
						return
					}
					var request struct {
						Op string `json:"op"`
					}
					_ = json.Unmarshal([]byte(line), &request)
					calls.Add(1)
					if kind == "lost ack" {
						_ = conn.Close()
						continue
					}
					if request.Op == "reload" {
						incarnation := before.Incarnation
						if kind == "replacement ack" {
							incarnation = managedchat.NewID()
						}
						digest := proof.ConfigSHA256
						if kind == "wrong digest" {
							digest = before.ConfigSHA256
						}
						_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "result": map[string]any{"incarnation": incarnation, "applied_generation": proof.Generation, "config_sha256": digest}})
					} else {
						after := before
						after.Generation, after.ConfigSHA256 = proof.Generation, proof.ConfigSHA256
						if kind == "replacement status" {
							after.Incarnation = managedchat.NewID()
						}
						_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "result": after})
					}
					_ = conn.Close()
				}
			}()
			t.Cleanup(func() { _ = listener.Close(); <-done })
			control := managedchat.Control{Socket: socket, VerifyPeer: func(*net.UnixConn) error { return nil }}
			err = applyChatChannelProof(t.Context(), control, before, proof)
			if (err == nil) != (kind == "success") {
				t.Fatal("wrong applied result", kind, err)
			}
			want := int32(1)
			if kind == "success" || kind == "replacement status" {
				want = 2
			}
			if calls.Load() != want {
				t.Fatal("reload was retried or acknowledgment was ignored", calls.Load(), want)
			}
		})
	}
}

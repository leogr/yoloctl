package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

func desiredCatalogRPC(ctx context.Context) (chatDesiredCatalog, error) {
	var out bytes.Buffer
	handled, err := runControllerChatChannels(ctx, controller.Request{Method: "chat.channel.catalog", Space: "work", Payload: []byte(`{}`)}, &out)
	if !handled && err == nil {
		return chatDesiredCatalog{}, errors.New("catalog method was not handled")
	}
	var got chatDesiredCatalog
	if err == nil {
		err = json.Unmarshal(out.Bytes(), &got)
	}
	return got, err
}

func TestDesiredCatalogOneReadWithoutRuntimePrivateStateOrLock(t *testing.T) {
	store, _, room, _ := attachObserveFixture(t)
	original := chatObservationSnapshot
	t.Cleanup(func() { chatObservationSnapshot = original })
	newBoxStateResolver = func() boxstate.Resolver { panic("desired catalog requested runtime inventory") }
	store.Record = func([]byte) error { panic("desired catalog wrote native state") }
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	reads := 0
	chatObservationSnapshot = func(s managedchat.Store) (*managedchat.Room, error) {
		reads++
		r, err := s.ReadManifestSnapshot() // count actual native reads
		if err != nil {
			return nil, err
		}
		// Any hidden Load/Expected/render read now fails. The captured public
		// document is enough; neither private credentials nor runtime are needed.
		if err := os.Rename(filepath.Join(s.Disk, layout.ChatManifest), filepath.Join(s.Disk, "config", "captured.json")); err != nil {
			t.Fatal(err)
		}
		return r, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := desiredCatalogFramedRPC(ctx)
	if err != nil || reads != 1 || got.RoomID != room.ID || !got.RuntimeUnobserved || len(got.Identities) != 2 || len(got.Channels) != 1 {
		t.Fatalf("single desired read: %+v reads=%d err=%v", got, reads, err)
	}
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte(`"runtime_applied"`)) || bytes.Contains(raw, []byte(`"pending"`)) || bytes.Contains(raw, []byte(`"token"`)) {
		t.Fatal("desired DTO leaked runtime proof or private data", string(raw))
	}
}

func TestDesiredCatalogCapturesOneGeneration(t *testing.T) {
	for _, change := range []string{"member", "revoke", "nickname", "delete"} {
		t.Run(change, func(t *testing.T) {
			store, inventory, room, _ := attachObserveFixture(t)
			room, err := store.CreateChannel(room.ID, "#build", room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			before, err := chatstream.RoomHeader(room)
			if err != nil {
				t.Fatal(err)
			}
			original := chatObservationSnapshot
			t.Cleanup(func() { chatObservationSnapshot = original })
			reads := 0
			chatObservationSnapshot = func(s managedchat.Store) (*managedchat.Room, error) {
				reads++
				r, err := s.ReadManifestSnapshot()
				if err != nil || reads != 1 {
					return r, err
				}
				switch change {
				case "member":
					id := agentbox.NewIdentity()
					inventory.Declarations["other"] = id
					_, err = s.Enroll(managedchat.Box{Name: "other", ID: id}, r.Generation, inventory)
				case "revoke":
					_, err = s.Revoke(r.Members[0].ID, r.Generation, inventory)
				case "nickname":
					_, err = s.SetHumanNickname("changed", r.Generation, inventory)
				case "delete":
					_, err = s.DeleteChannel(r.ID, r.Channels[1].ID, r.Generation, inventory)
				}
				if err != nil {
					t.Fatal(err)
				}
				return r, nil
			}
			got, err := desiredCatalogRPC(t.Context())
			if err != nil || reads != 1 || got.Generation != before.Generation || !reflect.DeepEqual(got.Identities, before.Identities) || !reflect.DeepEqual(got.Channels, before.Channels) {
				t.Fatalf("mixed/retried snapshot: %+v reads=%d err=%v", got, reads, err)
			}
			next, err := desiredCatalogRPC(t.Context())
			current, loadErr := store.Load()
			if err != nil || loadErr != nil {
				t.Fatal(err, loadErr)
			}
			after, err := chatstream.RoomHeader(current)
			if err != nil || reads != 2 || next.Generation != after.Generation || !reflect.DeepEqual(next.Identities, after.Identities) || !reflect.DeepEqual(next.Channels, after.Channels) || !reflect.DeepEqual(next.DeletedChannels, after.DeletedChannels) {
				t.Fatalf("next snapshot: %+v reads=%d err=%v", next, reads, err)
			}
		})
	}
}

func TestDesiredCatalogRejectsInvalidStorageAndCancellation(t *testing.T) {
	for _, kind := range []string{"missing", "mode", "symlink", "hardlink", "disk", "unknown-field", "malformed", "cancelled", "deadline", "cancel-after-read"} {
		t.Run(kind, func(t *testing.T) {
			store, _, _, _ := attachObserveFixture(t)
			path := filepath.Join(store.Disk, layout.ChatManifest)
			original := chatObservationSnapshot
			t.Cleanup(func() { chatObservationSnapshot = original })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var err error
			switch kind {
			case "missing":
				err = os.Remove(path)
			case "mode":
				err = os.Chmod(path, 0644)
			case "symlink":
				err = os.Rename(path, path+".saved")
				if err == nil {
					err = os.Symlink(path+".saved", path)
				}
			case "hardlink":
				err = os.Link(path, path+".link")
			case "disk":
				store.DiskID = managedchat.NewID()
				nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
			case "unknown-field", "malformed":
				raw := []byte(`{"surprise":true}`)
				if kind == "malformed" {
					raw = []byte(`{`)
				}
				err = os.WriteFile(path, raw, 0600)
			case "cancelled":
				cancel()
			case "deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
			case "cancel-after-read":
				chatObservationSnapshot = func(s managedchat.Store) (*managedchat.Room, error) {
					r, err := s.ReadManifestSnapshot()
					cancel()
					return r, err
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := desiredCatalogRPC(ctx)
			if err == nil || got.RoomID != "" {
				t.Fatalf("invalid catalog succeeded: %+v %v", got, err)
			}
			if kind == "missing" && !strings.Contains(err.Error(), "ensure first") {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if kind == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
}

func TestDesiredCatalogTypedRequestAndRegistry(t *testing.T) {
	op := controllerOperations["chat.channel.catalog"]
	if !op.BoxAllowed || op.Mutates || op.EffectGated || !operatorChatMethod("chat.channel.catalog") {
		t.Fatal(op)
	}
	for _, raw := range []string{`{"generation":1}`, `{"room_id":"x"}`, `{"confirm":false}`, `{"box_id":null}`, `{"unknown":true}`, `null`, `[]`, `{} {}`} {
		if _, err := decodeChatChannelRequest("chat.channel.catalog", []byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, req := range []controller.Request{
		{Args: []string{"x"}}, {Flags: map[string][]string{"json": {"true"}}}, {Terminal: true}, {View: true},
	} {
		req.Method = "chat.channel.catalog"
		if _, err := runControllerChatChannels(t.Context(), req, io.Discard); err == nil {
			t.Fatal("nonfinite request accepted", req)
		}
	}
}

func TestChatCatalogModeRefreshAndPreJoin(t *testing.T) {
	for _, value := range []string{"default", "single", "observed"} {
		t.Run(value, func(t *testing.T) {
			cmd := newChatCmd()
			if value != "default" {
				if err := cmd.Flags().Set("catalog-read", value); err != nil {
					t.Fatal(err)
				}
			}
			mode, err := chatCatalogMode(cmd)
			if err != nil {
				t.Fatal(err)
			}
			want := "chat.channel.catalog"
			if value == "observed" {
				want = "chat.channel.list"
			}
			calls := 0
			target := controllerTarget{Space: "work", Call: func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
				calls++
				if req.Method != want || string(req.Payload) != "{}" {
					t.Fatalf("wrong route: %+v", req)
				}
				if want == "chat.channel.catalog" {
					return json.NewEncoder(streams.Out).Encode(chatDesiredCatalog{RuntimeUnobserved: true})
				}
				return json.NewEncoder(streams.Out).Encode(chatChannelSnapshot{RuntimeApplied: true})
			}}
			l := &chatLink{id: 1, catalogs: make(chan chatstream.Catalog, 1), actions: make(chan chatstream.Action, 1)}
			s := &chatSession{ctx: t.Context(), target: target, link: l, catalogRead: mode}
			if msg := s.refreshCatalog(); msg != nil {
				t.Fatal(msg)
			}
			got := <-l.catalogs
			if got.RuntimeUnobserved != (value != "observed") || got.RuntimeApplied != (value == "observed") || got.Read.Link != l.id {
				t.Fatal(got)
			}
			if msg := s.control(chatui.Action{ID: "join", Kind: "join", Channel: "#build"}, s.readCatalog)(); msg != nil {
				t.Fatal(msg)
			}
			a := <-l.actions
			if calls != 2 || a.Catalog == nil || a.Catalog.RuntimeUnobserved != got.RuntimeUnobserved {
				t.Fatal(calls, a)
			}
		})
	}
	cmd := newChatCmd()
	if err := cmd.Flags().Set("catalog-read", "typo"); err != nil {
		t.Fatal(err)
	}
	// Invalid values fail before terminal, storage, operator or RPC discovery.
	if err := runChatView(cmd, nil); err == nil || !strings.Contains(err.Error(), "invalid --catalog-read") {
		t.Fatal(err)
	}
}

func TestDesiredCatalogCoalescingAndViewEvent(t *testing.T) {
	l := &chatLink{catalogs: make(chan chatstream.Catalog, 1)}
	observed := chatstream.Catalog{Header: chatstream.Header{Bootstrap: managedchat.Bootstrap{RoomID: "r", DiskID: "d", SelfAccount: "h", Generation: 4}}, RuntimeApplied: true}
	desired := observed
	desired.RuntimeApplied, desired.RuntimeUnobserved = false, true
	if err := l.offerCatalog(observed); err != nil {
		t.Fatal(err)
	}
	if err := l.offerCatalog(desired); err != nil {
		t.Fatal(err)
	}
	got := <-l.catalogs
	msg := chatViewEvent(chatstream.Event{Kind: "catalog", Catalog: &got}).(chatui.CatalogMsg)
	if !msg.RuntimeUnobserved || msg.Applied {
		t.Fatal("equal-generation refresh retained stale proof", msg)
	}
}

func TestDesiredCatalogListIsExplicitAndLegacyOutputUnchanged(t *testing.T) {
	for _, desired := range []bool{false, true} {
		for _, asJSON := range []bool{false, true} {
			cmd := newChatChannelCmd()
			list, _, err := cmd.Find([]string{"list"})
			if err != nil {
				t.Fatal(err)
			}
			if desired {
				_ = list.Flags().Set("catalog-only", "true")
			}
			if asJSON {
				_ = list.Flags().Set("json", "true")
			}
			var out bytes.Buffer
			list.SetOut(&out)
			calls := 0
			err = runChatChannelList(list, func(_ context.Context, _ *cobra.Command, method string, input, dst any) error {
				calls++
				raw, _ := json.Marshal(input)
				if string(raw) != "{}" {
					t.Fatal(string(raw))
				}
				if desired {
					if method != "chat.channel.catalog" {
						t.Fatal(method)
					}
					*dst.(*chatDesiredCatalog) = chatDesiredCatalog{RoomID: "room", Generation: 8, RuntimeUnobserved: true}
				} else {
					if method != "chat.channel.list" {
						t.Fatal(method)
					}
					*dst.(*chatChannelSnapshot) = chatChannelSnapshot{RoomID: "room", Generation: 8, RuntimeApplied: true}
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatal(calls, err)
			}
			text := out.String()
			if desired {
				if asJSON {
					if !strings.Contains(text, `"runtime_unobserved":true`) || strings.Contains(text, `"runtime_applied"`) || strings.Contains(text, `"pending"`) {
						t.Fatal(text)
					}
				} else if !strings.Contains(text, "server application not checked") {
					t.Fatal(text)
				}
			} else {
				var want bytes.Buffer
				list.SetOut(&want)
				if err := printChatChannelSnapshot(list, chatChannelSnapshot{RoomID: "room", Generation: 8, RuntimeApplied: true}); err != nil || text != want.String() {
					t.Fatal("legacy output changed", text, err)
				}
				if asJSON && (!strings.Contains(text, `"runtime_applied":true`) || strings.Contains(text, "runtime_unobserved")) {
					t.Fatal(text)
				}
			}
		}
	}
}

func TestDesiredCatalogProjectsLegacyWithoutWriting(t *testing.T) {
	store, _, room, _ := attachObserveFixture(t)
	room.Schema, room.Channels, room.DeletedChannels = 1, nil, nil
	raw, err := room.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Disk, layout.ChatManifest)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := desiredCatalogRPC(t.Context())
	if err != nil || got.DefaultChannel != room.Channel || len(got.Channels) != 1 || got.Channels[0].ID != managedchat.LegacyChannelID(room.ID) {
		t.Fatal(got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("read migrated legacy manifest", err)
	}
}

func TestDesiredCatalogFailureHasPrivateDiagnosticsAndNoFallback(t *testing.T) {
	calls := 0
	target := controllerTarget{Space: "work", Call: func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
		calls++
		if req.Method != "chat.channel.catalog" {
			t.Fatal("fell back", req.Method)
		}
		_, _ = io.WriteString(streams.Err, "\x1b[31mError: strict storage failure\r\n")
		return &controller.ExitError{Status: 2}
	}}
	l := &chatLink{id: 4, catalogs: make(chan chatstream.Catalog, 1), actions: make(chan chatstream.Action, 1)}
	s := &chatSession{ctx: t.Context(), target: target, link: l}
	withoutCatalogTerminalWrites(t, func() {
		msg := s.refreshCatalog().(chatui.RefreshResultMsg)
		if msg.Read.Link != l.id || msg.Err == nil || !strings.Contains(msg.Err.Error(), "strict storage failure") || strings.ContainsAny(msg.Err.Error(), "\x1b\r\n") {
			t.Fatal(msg)
		}
	})
	if calls != 1 || len(l.catalogs) != 0 {
		t.Fatal("failed refresh retried or queued", calls)
	}
	msg := s.control(chatui.Action{Kind: "join"}, s.readCatalog)().(chatui.ControlMsg)
	if calls != 2 || msg.Err == nil || len(l.actions) != 0 {
		t.Fatal("failed pre-JOIN retried or queued", calls, msg)
	}
}

// The handler/storage fixtures above retain typed error identity for cancellation
// tests. These calls additionally exercise the actual public controller framing,
// finite operation registry and endpoint-derived principal before that handler.
func desiredCatalogFramedRPC(ctx context.Context) (chatDesiredCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, chatObservationTimeout)
	defer cancel()
	principal := currentControllerPrincipal(ctx)
	hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "fixture", Space: "work", Generation: "fixture"}
	server := &controller.Server{Hello: hello, Operations: map[string]controller.Operation{"chat.channel.catalog": controllerOperations["chat.channel.catalog"]},
		CheckPrincipal: func(ctx context.Context, p controller.Principal) error {
			if p.Admin() {
				return nil
			}
			return checkOperatorPrincipal(ctx, p)
		},
		Execute: func(ctx context.Context, p controller.Principal, req controller.Request, streams controller.Streams) (int, error) {
			handled, err := runControllerChatChannels(withControllerPrincipal(ctx, p), req, streams.Out)
			if !handled && err == nil {
				err = errors.New("catalog method was not handled")
			}
			if err != nil {
				return 2, err
			}
			return 0, nil
		},
	}
	client, remote := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.ServeConn(ctx, remote, principal) }()
	var out bytes.Buffer
	req := controller.Request{ID: "desired-catalog-request-0001", Method: "chat.channel.catalog", Space: "work", Dash: -1, Payload: []byte(`{}`)}
	err := controller.Call(ctx, client, hello, req, controller.ClientIO{Out: &out})
	_ = client.Close()
	<-done
	var got chatDesiredCatalog
	if err == nil {
		err = json.Unmarshal(out.Bytes(), &got)
	}
	return got, err
}

func TestDesiredCatalogDoesNotAuthorizeStaleActions(t *testing.T) {
	for _, change := range []string{"delete", "replace-channel", "revoke-member"} {
		t.Run(change, func(t *testing.T) {
			store, inventory, room, _ := attachObserveFixture(t)
			room, err := store.CreateChannel(room.ID, "#build", room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := desiredCatalogRPC(t.Context())
			if err != nil || snapshot.Generation != room.Generation {
				t.Fatal(snapshot, err)
			}
			origin := newChatStreamOrigin(room)
			if change == "revoke-member" {
				member := room.Members[0]
				boxes := []boxstate.Box{{Name: member.Name, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeRunning, Config: &agentbox.Config{Name: member.Name, ID: member.BoxID}, Container: &container.Container{Name: member.Name}}}
				target, err := resolveChatAttachTarget(room, boxes, member.ID)
				if err != nil || target.State != "running" {
					t.Fatal("fixture participant could not attach before revocation", target, err)
				}
				if err := validateChatAttachIdentity(room, boxes, target, false); err != nil {
					t.Fatal("fixture attachment was not authorized before revocation", err)
				}
				current, err := store.Revoke(member.ID, room.Generation, inventory)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateChatAttachIdentity(current, boxes, target, false); err == nil {
					t.Fatal("stale desired identity authorized a revoked participant attachment")
				}
				return
			}
			if err := authorizeChatStreamChannel(t.Context(), store, origin, "#build"); err != nil {
				t.Fatal("fixture send was not authorized before channel deletion", err)
			}
			current, err := store.DeleteChannel(room.ID, room.Channels[1].ID, room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			if change == "replace-channel" {
				if _, err := store.CreateChannel(room.ID, "#build", current.Generation, inventory); err != nil {
					t.Fatal(err)
				}
			}
			var forwarded bytes.Buffer
			request := `{"id":"stale-send","op":"send","target":"#build","text":"must remain unsent"}` + "\n"
			err = chatstream.FilterChannels(strings.NewReader(request), &forwarded, func(channel string) error { return authorizeChatStreamChannel(t.Context(), store, origin, channel) }, false)
			if err == nil || forwarded.Len() != 0 {
				t.Fatal("stale desired channel authorized or replayed a send", err, forwarded.String())
			}
		})
	}
}

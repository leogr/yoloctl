package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacelock"
	"golang.org/x/crypto/ssh"
)

type chatConnectionFixture struct {
	store     managedchat.Store
	room      *managedchat.Room
	ops       managedChatConnectionOps
	id        string
	cfg       agentbox.Config
	dials     atomic.Int32
	commands  atomic.Int32
	stallOpen atomic.Bool
	// humanLive is the human account's live connection count in status.
	humanLive atomic.Uint64
}

// Real native manifest, Unix control protocol and SSH sessions. Only runtime
// inspection/cgroup authority are substituted; this is not a VM validation.
func newChatConnectionFixture(t *testing.T) *chatConnectionFixture {
	t.Helper()
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "human"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := store.Expected(room)
	if err != nil {
		t.Fatal(err)
	}
	f := &chatConnectionFixture{store: store, room: room, id: strings.Repeat("a", 64)}
	f.cfg = agentbox.Config{Name: room.Server.Name, ID: room.Server.ID, Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(room.Server.Name), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
	applied := managedchat.Applied{Features: managedchat.Features{Protocol: 1, Features: []string{"unix-listeners", "control-reload", "acl-revoke", "server-presence"}},
		Incarnation: strings.Repeat("b", 32), Generation: room.Generation, ConfigSHA256: proof.ConfigSHA256,
		Listeners: managedchat.Listeners{IRC: "unix", Artifacts: "unix"}, ConfiguredAccounts: uint64(len(proof.Accounts))}
	humanAccount, err := managedchat.AuthAccount(room.Human.ID)
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(shortSocketDir(t), "control.sock")
	controlListener, err := net.Listen("unix", controlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlListener.Close() })
	go func() {
		for {
			conn, err := controlListener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var request map[string]string
			if json.NewDecoder(conn).Decode(&request) == nil {
				reply := applied
				if live := f.humanLive.Load(); live > 0 {
					reply.Accounts = map[string]managedchat.Connections{humanAccount: {Connections: live}}
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "result": reply})
			}
			_ = conn.Close()
		}
	}()
	sshPath := filepath.Join(shortSocketDir(t), "ssh.sock")
	listener, err := net.Listen("unix", sshPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if f.stallOpen.Load() {
						_ = server.Wait() // cancellation must close a stalled channel open
						return
					}
					ch, requests, err := channel.Accept()
					if err != nil {
						return
					}
					for req := range requests {
						if req.Type != "exec" {
							_ = req.Reply(false, nil)
							continue
						}
						var command struct{ Command string }
						_ = ssh.Unmarshal(req.Payload, &command)
						f.commands.Add(1)
						if strings.Contains(command.Command, "stall") {
							continue // wait for cancellation without an exec reply
						}
						_ = req.Reply(true, nil)
						output := command.Command
						if strings.Contains(command.Command, "features") {
							output = `{"protocol":1,"features":["client-unix","jsonl-v2","client-presence","client-topic","nick-regain"]}`
						}
						_, _ = io.WriteString(ch, output+"\n")
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						_ = ch.Close()
						break
					}
				}
			}()
		}
	}()
	f.ops = managedChatConnectionOps{
		load: func(string, string) (*agentbox.Config, error) { cfg := f.cfg; return &cfg, nil },
		control: func(_ context.Context, r *managedchat.Room, cfg *agentbox.Config) (managedchat.Control, *container.Details, error) {
			if err := validateManagedServer(cfg, r); err != nil {
				return managedchat.Control{}, nil, err
			}
			return managedchat.Control{Socket: controlPath, VerifyPeer: func(*net.UnixConn) error { return nil }},
				&container.Details{ID: f.id, Running: true, Labels: map[string]string{managedchat.IncarnationLabel: strings.Repeat("c", 32)}}, nil
		},
		dial: func(ctx context.Context, _ *agentbox.Config, _ *container.Details) (net.Conn, error) {
			f.dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, "unix", sshPath)
		},
		active: func(context.Context, *container.Client, string) bool { return true },
	}
	return f
}

func TestChatPinnedConnectionRejectsReplacementAndBoundsExec(t *testing.T) {
	for _, change := range []string{"container", "declaration", "room", "holder", "stall", "channel-stall"} {
		t.Run(change, func(t *testing.T) {
			f := newChatConnectionFixture(t)
			conn, err := openManagedChatConnectionWith(t.Context(), "work", f.store, f.ops)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.Run(t.Context(), []string{"features"}, nil, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "container":
				f.id = strings.Repeat("d", 64)
			case "declaration":
				f.cfg.ID = managedchat.NewID()
			case "room":
				inventory := managedchat.Inventory{Complete: true, Declarations: map[string]string{}, Runtime: map[string]bool{}}
				if _, err := f.store.SetHumanNickname("another", f.room.Generation, inventory); err != nil {
					t.Fatal(err)
				}
			case "holder":
				conn.ops.active = func(context.Context, *container.Client, string) bool { return false }
			case "stall", "channel-stall":
				f.stallOpen.Store(change == "channel-stall")
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer cancel()
				start := time.Now()
				if err := conn.Run(ctx, []string{"stall"}, nil, io.Discard, io.Discard); err == nil || time.Since(start) > time.Second {
					t.Fatal("exec cancellation was not bounded", err)
				}
			}
			if change != "stall" && change != "channel-stall" && conn.Verify(t.Context()) == nil {
				t.Fatal("replacement accepted")
			}
			if f.dials.Load() != 1 {
				t.Fatal("redialed mutable name", f.dials.Load())
			}
		})
	}
}

func TestHealthyChatStreamSetupBypassesExclusiveLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newChatConnectionFixture(t)
	oldStore, oldOpen := nativeChatStoreFn, openManagedChatConnectionFn
	t.Cleanup(func() { nativeChatStoreFn, openManagedChatConnectionFn = oldStore, oldOpen })
	nativeChatStoreFn = func() (managedchat.Store, error) { return f.store, nil }
	openManagedChatConnectionFn = func(ctx context.Context, space string, store managedchat.Store) (*managedChatConnection, error) {
		return openManagedChatConnectionWith(ctx, space, store, f.ops)
	}
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	effects, released := 0, 0
	ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { effects++; return nil })
	ctx = context.WithValue(ctx, controllerReleaseKey{}, func() error { released++; return nil })
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	var out bytes.Buffer
	if err := runControllerChatStream(ctx, controller.Request{Method: "chat.stream", Space: "work"}, input, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if effects != 1 || released != 1 || f.dials.Load() != 1 || f.commands.Load() != 2 || !bytes.Contains(out.Bytes(), []byte(`"managed-room"`)) {
		t.Fatal("stream bypassed proof or redialed", effects, released, f.dials.Load(), f.commands.Load(), out.String())
	}
}

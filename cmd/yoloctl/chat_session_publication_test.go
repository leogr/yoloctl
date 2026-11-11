package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"gopkg.in/yaml.v3"
)

// Exercise the ordinary session-revival path with a running container and a
// normally exited holder. Native files are real private temporary fixtures;
// every service command is an inert PATH spy, never a VM or active-room action.
func TestChatServerHolderRevivalRequiresCurrentPublication(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_CHAT_PUBLICATION_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatServerHolderRevivalRequiresCurrentPublication$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_PUBLICATION_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native holder-publication fixture: %v\n%s", err, out)
		}
		return
	}
	s, inventory := roomRuntimeFixture(t)
	if s.UID == 0 {
		s.UID = 1000
	}
	if s.GID == 0 {
		s.GID = 1000
	}
	r, err := ensureManagedRoom(s, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	inventory.Declarations["coder"] = agentbox.NewIdentity()
	r, err = s.Enroll(managedchat.Box{Name: "coder", ID: inventory.Declarations["coder"]}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SetOperators(map[string]bool{r.Members[0].ID: true}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(r); err != nil {
		t.Fatal(err)
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	serverPath := filepath.Join(s.Disk, base, "server/config", managedchat.ServerConfigFile)
	before, err := os.ReadFile(serverPath)
	if err != nil {
		t.Fatal(err)
	}
	// Persist a role downgrade, then fail an actual client-file publication
	// before the living server config is replaced. Restore the failed target,
	// but deliberately do not render the new generation before session revival.
	r, err = s.SetOperators(map[string]bool{}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := layout.ChatMemberRelative(r.ID, r.Human.ID)
	clientPath := filepath.Join(s.Disk, leaf, managedchat.ClientConfigFile)
	if err := os.Rename(clientPath, clientPath+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(clientPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(r); err == nil {
		t.Fatal("directory at the client-file target did not fail publication")
	}
	if err := os.Remove(clientPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(clientPath+".retained", clientPath); err != nil {
		t.Fatal(err)
	}
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: s.DiskID, DiskUUID: "fixture", DiskMount: s.Disk, ConfigDir: filepath.Join(s.Disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(s.Disk, "host"), UID: s.UID, GID: s.GID, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	spaceenv.UseCatalogSnapshot(filepath.Join(s.Disk, "catalogs"))
	cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: r.Server.Name, ID: r.Server.ID, Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(r.Server.Name), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
	manifest, err := layout.BoxPath(cfg.Space, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		t.Fatal(err)
	}
	writeDeclaration := func() {
		t.Helper()
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeDeclaration()
	previousResolver := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previousResolver })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			InspectActual: func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
				return agentbox.ActualState{Exists: true, Image: "local/current:test"}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) { return nil, nil },
			ResolvedImage: func(*agentbox.Config) (string, bool, error) { return "local/current:test", false, nil },
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: cfg.Space, Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: cfg.Name, Status: "Up"}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionExited}
			},
		}
	}
	swapBinarySeams(t, false, false, func(context.Context, *container.Client) error { return nil })
	bin, starts := t.TempDir(), filepath.Join(t.TempDir(), "starts")
	t.Setenv("YOLOCTL_TEST_CHAT_STARTS", starts)
	t.Setenv("YOLOCTL_TEST_CHAT_TOMBSTONE", session.TombstonePath(cfg.Name))
	t.Setenv("YOLOCTL_TEST_CHAT_MARKER", session.MarkerPath(cfg.Name))
	t.Setenv("YOLOCTL_TEST_CHAT_SOCKET", session.SocketPath(cfg.Name))
	for name, body := range map[string]string{
		"cat": `if [ "$#" = 1 ] && [ "$1" = "$YOLOCTL_TEST_CHAT_TOMBSTONE" ]; then
  printf 'exit_code: 0\nexited_at: 2026-10-19T20:00:00Z\n'
  exit 0
fi
exit 91`,
		"rm": `if [ "$#" = 2 ] && [ "$1" = -f ] && [ "$2" = "$YOLOCTL_TEST_CHAT_TOMBSTONE" ]; then exit 0; fi
exit 92`,
		"test": `if [ "$#" = 2 ] && [ "$1" = -f ] && [ "$2" = "$YOLOCTL_TEST_CHAT_MARKER" ]; then exit 1; fi
if [ "$#" = 2 ] && [ "$1" = -S ] && [ "$2" = "$YOLOCTL_TEST_CHAT_SOCKET" ]; then exit 0; fi
exit 93`,
		"systemd-run": `printf '%s\n' "$*" >> "$YOLOCTL_TEST_CHAT_STARTS"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// No real service tool is reachable even if the expected path regresses.
	t.Setenv("PATH", bin)
	c := container.NewWithOutput(cfg.Space, io.Discard, io.Discard)
	var notice bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &notice))
	if _, err := ensureAgentSession(t.Context(), c, cfg.Space, cfg.Name, &notice, true, nil, sessionStartOptions{}); err == nil || !strings.Contains(err.Error(), "publication is pending") {
		t.Fatal("running-container revival accepted stale server authority", err)
	}
	if !strings.Contains(notice.String(), "previous session exited with status 0") {
		t.Fatal("fixture did not exercise the normal-child-exit path", notice.String())
	}
	if raw, err := os.ReadFile(starts); !os.IsNotExist(err) {
		t.Fatalf("stale publication reached the holder launch: %q (%v)", raw, err)
	}
	unchanged, err := os.ReadFile(serverPath)
	if err != nil || !bytes.Equal(before, unchanged) {
		t.Fatal("ordinary session revival repaired publication implicitly", err)
	}
	retained, err := s.Load()
	if err != nil || retained.Generation != r.Generation || retained.Members[0].ChannelOperator {
		t.Fatal("refused revival lost the desired downgrade/recovery generation", err)
	}
	// Reusing an already living holder never launches or reloads it.
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return true }
	if _, err := ensureAgentSession(t.Context(), c, cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{}); err != nil {
		t.Fatal("publication guard interrupted a living holder", err)
	}
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return false }
	// Explicit publication recovery permits exactly one fresh holder launch.
	if _, err := s.Render(r); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureAgentSession(t.Context(), c, cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{}); err != nil {
		t.Fatal("current publication prevented normal session revival", err)
	}
	launched, err := os.ReadFile(starts)
	if err != nil || bytes.Count(launched, []byte{'\n'}) != 1 || !bytes.Contains(launched, []byte("session-hold "+cfg.Name)) {
		t.Fatal("expected one holder launch after explicit recovery", err)
	}
	// The agentless server is never conversation-tracked: no launch token,
	// no record and no box lock (chat ensure may already hold it).
	if bytes.Contains(launched, []byte("--launch-token")) {
		t.Fatal("an agentless server launch carried a conversation token")
	}
	// A same-name declaration cannot use the reserved server's publication.
	cfg.ID = agentbox.NewIdentity()
	writeDeclaration()
	if _, err := ensureAgentSession(t.Context(), c, cfg.Space, cfg.Name, io.Discard, true, nil, sessionStartOptions{}); err == nil {
		t.Fatal("replacement declaration launched the reserved server")
	}
	final, err := os.ReadFile(starts)
	if err != nil || !bytes.Equal(launched, final) {
		t.Fatal("invalid reservation reached another holder launch", err)
	}
}

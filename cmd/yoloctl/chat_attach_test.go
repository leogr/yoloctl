package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
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
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"gopkg.in/yaml.v3"
)

func TestChatAttachTargetHostHelperRefusesOperator(t *testing.T) {
	stubOperatorClient(t, &controller.OperatorClient{Space: "work", Generation: "fixture"})
	t.Setenv("PATH", t.TempDir())
	_, err := callChatAttachTarget(t.Context(), newChatCmd(), managedchat.NewID())
	if err == nil || !strings.Contains(err.Error(), "human host endpoint") {
		t.Fatal("operator got human attachment lookup", err)
	}
}

func TestChatAttachRevalidatesIdentityAfterLookup(t *testing.T) {
	m := managedchat.Member{ID: managedchat.NewID(), Name: "coder", BoxID: managedchat.NewID()}
	r := &managedchat.Room{ID: managedchat.NewID(), Prepared: true, RuntimeEnabled: true, Human: managedchat.Member{ID: managedchat.NewID()}, Members: []managedchat.Member{m}}
	box := boxstate.Box{Name: m.Name, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeRunning,
		Config: &agentbox.Config{Name: m.Name, ID: m.BoxID}, Container: &container.Container{Name: m.Name}}
	target, err := resolveChatAttachTarget(r, []boxstate.Box{box}, m.ID)
	if err != nil || target.Box != m.Name || target.BoxID != m.BoxID || target.RoomID != r.ID || target.State != "running" {
		t.Fatal("valid immutable participant did not resolve", target, err)
	}
	if err := validateChatAttachIdentity(r, []boxstate.Box{box}, target, false); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*managedchat.Room, *boxstate.Box){
		"revoked":       func(r *managedchat.Room, _ *boxstate.Box) { r.Members[0].Revoked = true },
		"room-replaced": func(r *managedchat.Room, _ *boxstate.Box) { r.ID = managedchat.NewID() },
		"name-reused":   func(_ *managedchat.Room, b *boxstate.Box) { b.Config.ID = managedchat.NewID() },
		"declaration-deleted": func(_ *managedchat.Room, b *boxstate.Box) {
			b.Declaration = boxstate.DeclarationMissing
			b.Config = nil
			b.Runtime = boxstate.RuntimeOrphaned
		},
		"inventory-unknown": func(_ *managedchat.Room, b *boxstate.Box) { b.Runtime = boxstate.RuntimeUnknown },
	} {
		t.Run(name, func(t *testing.T) {
			next := *r
			next.Members = append([]managedchat.Member(nil), r.Members...)
			current, cfg := box, *box.Config
			current.Config = &cfg
			mutate(&next, &current)
			if err := validateChatAttachIdentity(&next, []boxstate.Box{current}, target, true); err == nil {
				t.Fatal("changed participant attached after stale lookup")
			}
		})
	}
	box.Runtime = boxstate.RuntimeStopped
	if err := validateChatAttachIdentity(r, []boxstate.Box{box}, target, false); err == nil {
		t.Fatal("box stopped after lookup was started without confirmation")
	}
	if err := validateChatAttachIdentity(r, []boxstate.Box{box}, target, true); err != nil {
		t.Fatal("explicit matching-box start confirmation rejected", err)
	}
	box.Runtime, box.Container = boxstate.RuntimeMissing, nil
	if err := validateChatAttachIdentity(r, []boxstate.Box{box}, target, false); err == nil {
		t.Fatal("absent runtime created without confirmation")
	}
	if _, err := resolveChatAttachTarget(r, []boxstate.Box{box}, r.Human.ID); err == nil {
		t.Fatal("human mapped to a box")
	}
}

// Exercise the actual guarded command validator with private native files,
// then select its transport before release can replace the declaration/name.
// Inspect and socket selection are inert fixtures; no live endpoint is opened.
func TestChatAttachGuardedSelectionPinsValidatedIdentity(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_CHAT_ATTACH_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatAttachGuardedSelectionPinsValidatedIdentity$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_ATTACH_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native guarded-attachment fixture: %v\n%s", err, out)
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
	for _, rel := range []string{"config/boxes", "config/accounts", ".controller"} {
		if err := os.MkdirAll(filepath.Join(s.Disk, rel), 0700); err != nil {
			t.Fatal(err)
		}
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
	if _, err := s.Render(r); err != nil {
		t.Fatal(err)
	}
	m := r.Members[0]
	projection, err := s.Projection(managedchat.Box{Name: m.Name, ID: m.BoxID})
	if err != nil || projection == nil {
		t.Fatal("member fixture has no private projection", err)
	}
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: s.DiskID, DiskUUID: "fixture", DiskMount: s.Disk, ConfigDir: filepath.Join(s.Disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(s.Disk, "host"), UID: s.UID, GID: s.GID, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: m.Name, ID: m.BoxID, Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(m.Name), Features: []string{"yolo.chat.skill"}}
	manifest, err := layout.BoxPath(cfg.Space, cfg.Name)
	if err != nil {
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
	previousResolver, previousDial := newBoxStateResolver, dialSessionVMFn
	t.Cleanup(func() { newBoxStateResolver, dialSessionVMFn = previousResolver, previousDial })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: cfg.Space, Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: cfg.Name, Status: "Up"}}, nil
			},
		}
	}
	mounts := make([]map[string]any, 0, len(projection.Binds))
	for _, bind := range projection.Binds {
		mounts = append(mounts, map[string]any{"Source": bind.Source, "Destination": bind.Target, "RW": !bind.ReadOnly})
	}
	labels := map[string]string{agentbox.LabelAgentbox: m.Name, agentbox.LabelSpace: cfg.Space,
		managedchat.RoomLabel: r.ID, managedchat.BoxLabel: m.BoxID, managedchat.MemberLabel: m.ID,
		managedchat.RoleLabel: "member", managedchat.IncarnationLabel: managedchat.NewID()}
	inspection, err := json.Marshal([]any{map[string]any{"Id": "fixture-container", "Name": m.Name,
		"State":  map[string]any{"Status": "running", "Running": true},
		"Config": map[string]any{"Labels": labels}, "Mounts": mounts}})
	if err != nil {
		t.Fatal(err)
	}
	inspectPath, bin := filepath.Join(t.TempDir(), "inspect.json"), t.TempDir()
	if err := os.WriteFile(inspectPath, inspection, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOLOCTL_TEST_CHAT_ATTACH_INSPECT", inspectPath)
	spy := "#!/bin/sh\nif [ \"$#\" = 2 ] && [ \"$1\" = inspect ] && [ \"$2\" = coder ]; then exec /bin/cat \"$YOLOCTL_TEST_CHAT_ATTACH_INSPECT\"; fi\nexit 91\n"
	if err := os.WriteFile(filepath.Join(bin, "nerdctl"), []byte(spy), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cmd := newAgentboxAttachCmd()
	for name, value := range map[string]string{"chat-room-id": r.ID, "chat-member-id": m.ID, "chat-box-id": m.BoxID} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	original, replacement := &operatorChatTransportFixture{}, &operatorChatTransportFixture{}
	liveTarget := original
	released := false
	ctx := context.WithValue(t.Context(), spaceReleaseKey{}, func() error {
		released = true
		cfg.ID = agentbox.NewIdentity()
		writeDeclaration()
		liveTarget = replacement
		return nil
	})
	cmd.SetContext(ctx)
	if !chatAttachGuardPresent(cmd) {
		t.Fatal("typed attachment omitted its identity guard")
	}
	if err := validateChatAttachCommand(cmd, cfg.Space, []string{m.Name}); err != nil {
		t.Fatal("initial guarded identity was rejected", err)
	}
	selected := false
	dialSessionVMFn = func(ctx context.Context, space, name string) (io.ReadWriteCloser, error) {
		selected = true
		deadline, bounded := ctx.Deadline()
		if released || !bounded || time.Until(deadline) > operatorTargetSetupTimeout || space != cfg.Space || name != m.Name {
			t.Fatal("guarded target selection escaped bounded setup", released, space, name)
		}
		if err := validateChatAttachCommand(cmd, space, []string{name}); err != nil {
			t.Fatal("validated identity changed before selecting its socket", err)
		}
		return liveTarget, nil
	}
	transport, err := selectSessionTransport(ctx, cfg.Space, m.Name)
	if err != nil || !selected || !released || transport != original {
		t.Fatal("guarded attachment followed the name after release", err, selected, released)
	}
	if err := validateChatAttachCommand(cmd, cfg.Space, []string{m.Name}); err == nil {
		t.Fatal("same-name replacement still passed the original command guard")
	}
	_ = transport.Close()
}

func TestChatAttachGuardsAreCompleteHiddenAndReadOnlyLookupIsStrict(t *testing.T) {
	cmd := newAgentboxAttachCmd()
	if chatAttachGuardPresent(cmd) {
		t.Fatal("ordinary attach unexpectedly guarded")
	}
	for _, name := range chatAttachIdentityFlags {
		if cmd.Flag(name) == nil || !cmd.Flag(name).Hidden {
			t.Fatal("guard is absent or exposed as everyday CLI", name)
		}
	}
	if err := cmd.Flags().Set("chat-room-id", managedchat.NewID()); err != nil {
		t.Fatal(err)
	}
	if !chatAttachGuardPresent(cmd) || validateChatAttachCommand(cmd, "work", []string{"coder"}) == nil {
		t.Fatal("partial identity guard bypassed validation")
	}
	for _, input := range []chatRuntimeRequest{{}, {MemberID: "coder"}, {MemberID: managedchat.NewID(), Recreate: true}, {MemberID: managedchat.NewID(), Box: "coder"}, {MemberID: managedchat.NewID(), Nickname: "new"}} {
		payload, _ := json.Marshal(input)
		handled, err := runControllerChatRuntime(t.Context(), controller.Request{Method: "chat.attach-target", Payload: payload}, io.Discard, io.Discard)
		if !handled || err == nil || err.Error() != "chat attach target requires only an immutable member ID" {
			t.Fatal("target lookup accepted a mutation or display name", input, err)
		}
	}
}

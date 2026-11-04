package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// An isolated process installs a fixture-native context. The only runtime
// command on PATH is a finite inspect spy, never a VM/container executable.
func TestDesiredCatalogOperatorUsesLiveGrantAndOneRoomRead(t *testing.T) {
	const child = "YOLOCTL_TEST_DESIRED_CATALOG_OPERATOR"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDesiredCatalogOperatorUsesLiveGrantAndOneRoomRead$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("operator fixture: %v\n%s", err, out)
		}
		return
	}
	store, inventory := roomRuntimeFixture(t)
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: store.DiskID, DiskUUID: "fixture", DiskMount: store.Disk,
		ConfigDir: filepath.Join(store.Disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(store.Disk, "host"),
		UID: store.UID, GID: store.GID, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: "caller", ID: agentbox.NewIdentity(), Space: "work",
		Agent: "none", Account: "none", Image: "img", Workdir: agentbox.NewEphemeralWorkdir("caller"), Capabilities: []string{operator.Capability}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	p := controller.Principal{Box: cfg.Name, Incarnation: managedchat.NewID()}
	e := operator.Endpoint{Box: p.Box, Incarnation: p.Incarnation}
	raw, err := json.Marshal([]any{map[string]any{"Name": p.Box, "State": map[string]any{"Running": true},
		"Config": map[string]any{"Labels": map[string]string{agentbox.LabelAgentbox: p.Box, agentbox.LabelSpace: "work", operator.IncarnationLabel: p.Incarnation}},
		"Mounts": []any{map[string]any{"Source": e.Directory(), "Destination": operator.Mount, "RW": false}}}})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	inspect := filepath.Join(bin, "inspect.json")
	if err := os.WriteFile(inspect, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOLOCTL_CATALOG_INSPECT_FIXTURE", inspect)
	spy := "#!/bin/sh\nif [ \"$#\" = 2 ] && [ \"$1\" = inspect ] && [ \"$2\" = caller ]; then exec /bin/cat \"$YOLOCTL_CATALOG_INSPECT_FIXTURE\"; fi\nexit 91\n"
	if err := os.WriteFile(filepath.Join(bin, "nerdctl"), []byte(spy), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	nativeChatStoreFn = func() (managedchat.Store, error) { return store, nil }
	newBoxStateResolver = func() boxstate.Resolver { panic("operator catalog requested runtime-wide inventory") }
	reads := 0
	manifest := filepath.Join(store.Disk, layout.ChatManifest)
	chatObservationSnapshot = func(s managedchat.Store) (*managedchat.Room, error) {
		reads++
		r, err := s.ReadManifestSnapshot()
		if err == nil {
			err = os.Rename(manifest, manifest+".captured")
		}
		return r, err
	}
	got, err := desiredCatalogFramedRPC(withControllerPrincipal(t.Context(), p))
	if err != nil || reads != 1 || got.RoomID != room.ID {
		t.Fatalf("operator snapshot: %+v reads=%d %v", got, reads, err)
	}
	// The same immutable listener identity is rejected after declaration grant
	// revocation or container replacement, before reaching any manifest read.
	for _, kind := range []string{"grant", "incarnation", "missing-incarnation"} {
		cfg.Capabilities = []string{operator.Capability}
		if kind == "grant" {
			cfg.Capabilities = nil
		}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		caller := p
		if kind == "incarnation" {
			caller.Incarnation = managedchat.NewID()
		}
		if kind == "missing-incarnation" {
			caller.Incarnation = ""
		}
		if _, err := desiredCatalogRPC(withControllerPrincipal(context.Background(), caller)); err == nil || reads != 1 {
			t.Fatal(kind, reads, err)
		}
	}
}

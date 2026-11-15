package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// Exercise the real missing-container ensure entry and worker re-entry. The
// binary-preparation seam continues through the creation preflight and image
// consumer, then stops before native storage/container effects.
func TestMissingSessionRetainsExistingImageBeforeApply(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_SESSION_NATIVE_CACHE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMissingSessionRetainsExistingImageBeforeApply$", "-test.v")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_SESSION_NATIVE_CACHE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native ensure fixture: %v\n%s", err, out)
		}
		return
	}
	disk := t.TempDir()
	work := t.TempDir()
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: work, Writable: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := (&spaceconfig.Config{Name: "work", DefaultAccount: "personal", AccountDiskID: strings.Repeat("a", 32), AccountDiskUUID: "fixture", DefaultWorkdir: work, Mounts: []spaceconfig.Mount{{Source: work, Writable: true}}}).Save(); err != nil {
		t.Fatal(err)
	}
	// The original plain-context composed-cache check sees a locally present
	// image. No external container command, build or network access is allowed.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "nerdctl"), []byte("#!/bin/sh\ncase \"$1 $2\" in 'image inspect') exit 0;; *) echo unexpected-container-command >&2; exit 97;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldActive, oldBinary, oldResolver := holderUnitActiveFn, ensureVMBinaryFn, newBoxStateResolver
	t.Cleanup(func() { holderUnitActiveFn, ensureVMBinaryFn, newBoxStateResolver = oldActive, oldBinary, oldResolver })
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return false }
	for _, tc := range []struct {
		name     string
		features []string
	}{{"custom", nil}, {"composed", []string{"go"}}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: "cached-" + tc.name, Space: "work", Agent: "none", Account: "none", Image: "local/fixture:cached", Features: tc.features, Workdir: agentbox.NewEphemeralWorkdir("cached-" + tc.name)}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			newBoxStateResolver = func() boxstate.Resolver {
				return boxstate.Resolver{
					ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
						return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
					},
					ListContainers: func(context.Context, string) ([]container.Container, error) { return nil, nil },
				}
			}
			f := newNativeWorkerImageFixture(t)
			f.state.enabled, f.state.imagesEnabled = true, true
			f.state.call = func(ctx context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
				if r.Action == "check-desired" {
					return controller.ImageReply{}, nil
				}
				return f.call(ctx, r)
			}
			candidates, err := agentbox.RuntimeFeatures(cfg)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := cfg.FeaturePlan()
			if err != nil {
				t.Fatal(err)
			}
			reached := errors.New("creation image consumed before native effects")
			consumed, passes := 0, 0
			ensureVMBinaryFn = func(ctx context.Context, c *container.Client) error {
				return preflightAgentboxRuntimeQualified(defaultCreationContext(ctx, cfg), c, cfg, true, func(check context.Context, _ *agentbox.Config) (bool, error) {
					ref, err := agentboximage.RequirePreparedImage(check, c, cfg.Image, candidates, agentboximage.Options{FeaturePlan: plan})
					if err != nil {
						return true, err
					}
					if ref != f.target.Digest {
						t.Fatalf("creation consumed %q, want retained digest %q", ref, f.target.Digest)
					}
					consumed++
					return true, reached
				})
			}
			err = runPreparedControllerWorker(t.Context(), controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
				passes++
				f.executing = true
				defer func() { f.executing = false }()
				_, err := ensureAgentSession(held(ctx), container.New("work"), "work", cfg.Name, io.Discard, true, nil, sessionStartOptions{})
				return err
			})
			if !errors.Is(err, reached) || passes != 2 || consumed != 1 || len(f.recipes) != 1 || len(f.publications) != 0 {
				t.Fatalf("missing-box ensure lost cached preparation: err=%v passes=%d consumed=%d recipes=%d actions=%v", err, passes, consumed, len(f.recipes), f.actions)
			}
			if f.recipes[0].Builder != imagejob.LocalRetainBuilder {
				t.Fatal("existing image was rebuilt instead of retained", f.recipes[0].Builder)
			}
		})
	}
}

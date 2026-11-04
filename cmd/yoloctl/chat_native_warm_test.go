package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Exercise the same native context selected by pre-effect chat server/member
// warming against the real retained-image protocol. Cached presence alone may
// not skip retention: the later recreation must consume this exact lease.
func TestNativeChatPrewarmRetainsCachedImageBeforeApply(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_NATIVE_CHAT_PREWARM") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChatPrewarmRetainsCachedImageBeforeApply$", "-test.v")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_NATIVE_CHAT_PREWARM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native context fixture: %v\n%s", err, out)
		}
		return
	}
	disk := t.TempDir()
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chat", "member"} {
		f := newNativeWorkerImageFixture(t)
		ctx := context.WithValue(t.Context(), warmKey{}, f.state)
		ctx = defaultCreationContext(ctx, &agentbox.Config{Name: name})
		p := agentboximage.PreparedContext{DesiredRef: "local/chat:cached", BaseRef: "local/chat:cached"}
		err := requireWarmImage(ctx, nil, p) // nil forbids fallback to cached tag inspection
		var needed *imageWarmNeeded
		if !errors.As(err, &needed) || !needed.retain || f.state.applied || len(f.actions) != 0 {
			t.Fatal("pre-effect miss did not request exact retention", err)
		}
		if err := f.state.prepareNativeCreationImage(ctx, needed.prepared); err != nil {
			t.Fatal(err)
		}
		if err := requireWarmImage(ctx, nil, p); err != nil {
			t.Fatal("retained preparation unusable before apply", err)
		}
		f.state.applied = true // the original effect gate, never reset by this fix
		if err := requireWarmImage(ctx, nil, p); err != nil {
			t.Fatal("recreation lost the original cached-image lease", err)
		}
		ref, handled, err := f.state.consume(ctx, p)
		if err != nil || !handled || ref != f.target.Digest {
			t.Fatal("did not consume original retained digest", ref, handled, err)
		}
		changed := p
		changed.DesiredRef = "local/chat:changed"
		err = requireWarmImage(ctx, nil, changed)
		if err == nil || imageWarmPending(err) {
			t.Fatal("post-effect miss could replay the chat handler", err)
		}
		for _, action := range f.actions {
			if action == "publish" || action == "resolve" {
				t.Fatal("cached chat path contacted registry or retagged")
			}
		}
	}
	if nativeCreationImagePreparation(defaultCreationContext(t.Context(), &agentbox.Config{Privileged: true})) {
		t.Fatal("privileged chat path changed")
	}
}

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

// box edit/set check the image before effects, then the reconcile's creation
// preflight checks it again after apply began. On a native box both checks
// must use the native creation mode: the first retains the preparation the
// second requires, so the first apply succeeds instead of reporting "native
// image recipe changed after apply began" on every edit.
func TestNativeBoxEditPreEffectCheckRetainsWhatReconcileRequires(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_NATIVE_BOX_EDIT_WARM") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeBoxEditPreEffectCheckRetainsWhatReconcileRequires$", "-test.v")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_NATIVE_BOX_EDIT_WARM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native context fixture: %v\n%s", err, out)
		}
		return
	}
	disk := t.TempDir()
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{Name: "showcase"}
	f := newNativeWorkerImageFixture(t)
	base := context.WithValue(t.Context(), warmKey{}, f.state)
	p := agentboximage.PreparedContext{DesiredRef: "local/showcase:cached", BaseRef: "local/showcase:cached"}

	// First pass: the edit's pre-effect check (the mode the edit now uses)
	// asks for retention even though the image is cached.
	edit := defaultCreationContext(base, cfg)
	var needed *imageWarmNeeded
	if err := requireWarmImage(edit, nil, p); !errors.As(err, &needed) || !needed.retain {
		t.Fatal("edit pre-effect check did not request native retention", err)
	}
	if err := f.state.prepareNativeCreationImage(edit, needed.prepared); err != nil {
		t.Fatal(err)
	}
	// Re-entered pass: the pre-effect check passes, then apply begins.
	if err := requireWarmImage(edit, nil, p); err != nil {
		t.Fatal("retained preparation unusable before apply", err)
	}
	f.state.applied = true
	// The reconcile's creation preflight after the save finds that preparation.
	reconcile := defaultCreationContext(base, cfg)
	if err := requireWarmImage(reconcile, nil, p); err != nil {
		t.Fatal("first apply after an edit refused", err)
	}
	if _, handled, err := f.state.consume(reconcile, p); err != nil || !handled {
		t.Fatal("recreation could not consume the edit's preparation", handled, err)
	}
}

// The pre-effect checks of box edit, box set and mutate-and-reconcile must
// not drop back to the plain request context (the original bug).
func TestBoxMutationPreEffectChecksUseTheCreationMode(t *testing.T) {
	for _, name := range []string{"controller_documents.go", "agentbox_reconcile.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "warmBoxBeforeEffects(ctx,") {
			t.Errorf("%s runs a pre-effect image check outside defaultCreationContext", name)
		}
	}
}

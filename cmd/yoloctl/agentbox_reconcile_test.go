package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"gopkg.in/yaml.v3"
)

// These tests pin `box set` / `box edit` as save-only-what-resolves and
// `box set` as convergent: a manifest is never persisted with a name no catalog
// defines, and re-running a set whose value the manifest already holds still
// reconciles the runtime.

// seedSetBox saves a generic-shell box declaring caps and returns its manifest
// path.
func seedSetBox(t *testing.T, caps ...string) string {
	t.Helper()
	cfg := &agentbox.Config{
		Name:         "coder",
		Space:        "default",
		Image:        "img:1",
		Workdir:      agentbox.NewEphemeralWorkdir("coder"),
		Capabilities: caps,
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	return cfg.Path()
}

// stubSetReconcile swaps `box set`'s reconcile tail, which needs a live VM, for
// a recorder of the capability list each call was asked to apply.
func stubSetReconcile(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	old := setReconcile
	setReconcile = func(_ context.Context, _ *agentsession.BoxLock, _ io.Writer, _ *container.Client, cfg *agentbox.Config, _ string) error {
		calls = append(calls, slices.Clone(cfg.Capabilities))
		return nil
	}
	t.Cleanup(func() { setReconcile = old })
	return &calls
}

func readManifest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return string(raw)
}

// A name no catalog defines is refused BEFORE the manifest is saved.
// Config.Validate checks names only syntactically, so without the guard the
// typo is persisted and only the reconcile that follows fails — leaving a
// manifest nothing can apply. A build older than fs.pty.allocate wedged a box
// exactly this way.
func TestBoxSetRejectsUnresolvableNamesBeforeSave(t *testing.T) {
	bogusFeatures := []string{"bogus-feature"}
	for _, tc := range []struct {
		name string
		opts setOptions
		want string
	}{
		{"capability", setOptions{Capabilities: []string{"bogus.capability"}}, `unknown capability "bogus.capability"`},
		{"feature", setOptions{Features: &bogusFeatures}, `unknown feature "bogus-feature"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := seedSetBox(t, capabilities.WorkdirWriteCapability)
			before := readManifest(t, path)
			calls := stubSetReconcile(t)

			err := runSet(t.Context(), io.Discard, "default", "coder", tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runSet error = %v, want it to contain %q", err, tc.want)
			}
			if !IsUserError(err) {
				t.Errorf("error %q must be a user error (exit 1): the caller fixes the name", err)
			}
			if after := readManifest(t, path); after != before {
				t.Errorf("rejected set changed the manifest:\nbefore=%s\nafter=%s", before, after)
			}
			if len(*calls) != 0 {
				t.Errorf("rejected set reached the reconcile tail %d time(s)", len(*calls))
			}
		})
	}
}

// --privileged and --privileged=false save the explicit setting and hand that
// declaration to the reconcile, which refuses the mode change until an explicit
// recreate; repeating the stored value rewrites nothing. The retired capability
// is refused before anything is saved.
func TestBoxSetPrivilegedSetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, capabilities.WorkdirWriteCapability)
	var reconciled []bool
	old := setReconcile
	setReconcile = func(_ context.Context, _ *agentsession.BoxLock, _ io.Writer, _ *container.Client, cfg *agentbox.Config, _ string) error {
		reconciled = append(reconciled, cfg.Privileged)
		return nil
	}
	t.Cleanup(func() { setReconcile = old })
	on, off := true, false
	if err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{Privileged: &on}); err != nil {
		t.Fatal(err)
	}
	enabled := readManifest(t, path)
	if !strings.Contains(enabled, "privileged: true") {
		t.Fatalf("manifest after --privileged:\n%s", enabled)
	}
	if err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{Privileged: &on}); err != nil {
		t.Fatal(err)
	}
	if again := readManifest(t, path); again != enabled {
		t.Fatalf("repeated --privileged rewrote the manifest:\n%s", again)
	}
	err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{Capabilities: []string{capabilities.RetiredPrivilegedCapability}})
	if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "--privileged") {
		t.Fatalf("retired capability = %v", err)
	}
	if after := readManifest(t, path); after != enabled {
		t.Fatalf("refused set changed the manifest:\n%s", after)
	}
	if err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{Privileged: &off}); err != nil {
		t.Fatal(err)
	}
	if disabled := readManifest(t, path); strings.Contains(disabled, "privileged") {
		t.Fatalf("manifest after --privileged=false:\n%s", disabled)
	}
	if !slices.Equal(reconciled, []bool{true, true, false}) {
		t.Fatalf("reconciled declarations = %v", reconciled)
	}
}

// A stored declaration that still grants the retired capability is not
// converted: box set refuses it, --privileged included, before anything is
// saved or reconciled. Removing the grant takes an explicit box edit.
func TestBoxSetRefusesStoredRetiredPrivilege(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, capabilities.WorkdirWriteCapability)
	stored, err := agentbox.Load("default", "coder")
	if err != nil {
		t.Fatal(err)
	}
	stored.Capabilities = append(stored.Capabilities, capabilities.RetiredPrivilegedCapability)
	raw, err := yaml.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := stubSetReconcile(t)
	on := true
	for name, opts := range map[string]setOptions{
		"--privileged": {Privileged: &on},
		"--capability": {Capabilities: []string{capabilities.PTYAllocateCapability}},
	} {
		err := runSet(t.Context(), io.Discard, "default", "coder", opts)
		if !errors.Is(err, capabilities.ErrRetiredPrivileged) || !IsUserError(err) || !strings.Contains(err.Error(), "--privileged") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if readManifest(t, path) != string(raw) || len(*calls) != 0 {
		t.Fatalf("refused set saved or reconciled: %q", *calls)
	}
}

func TestBoxSetRejectsInvalidImageBeforeSave(t *testing.T) {
	for _, image := range []string{"", "ghcr.io/MyOrg/x", "bad image"} {
		t.Run(image, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := seedSetBox(t)
			before := readManifest(t, path)
			calls := stubSetReconcile(t)
			err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{Image: &image})
			if err == nil || !strings.Contains(err.Error(), "coder") || !strings.Contains(err.Error(), "image") {
				t.Fatalf("expected box-attributed image error, got %v", err)
			}
			if readManifest(t, path) != before || len(*calls) != 0 {
				t.Fatal("invalid image was persisted or reconciled")
			}
		})
	}
}

func TestInvalidExistingImageCanBeLoadedButBlocksGCWithBoxName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t)
	raw := strings.ReplaceAll(readManifest(t, path), "image: img:1\n", "")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentbox.Load("default", "coder")
	if err != nil {
		t.Fatalf("must remain loadable for repair: %v", err)
	}
	if cfg.Image != "" {
		t.Fatal("fixture image was not removed")
	}
	if _, err := declaredImageReferences("default"); err == nil || !strings.Contains(err.Error(), "coder") || !strings.Contains(err.Error(), "image") {
		t.Fatalf("missing actionable GC error: %v", err)
	}
	// This is also the persistence gate used after an editor removes image.
	if err := cfg.Save(); err == nil || readManifest(t, path) != raw {
		t.Fatal("saved invalid edited image")
	}
	cfg.Image = "alpine:latest"
	if err := cfg.Save(); err != nil {
		t.Fatalf("could not repair image: %v", err)
	}
	if _, err := declaredImageReferences("default"); err != nil {
		t.Fatalf("cleanup still blocked after repair: %v", err)
	}
}

// Setting what the manifest already holds must still reconcile: the manifest
// can be ahead of the runtime (a set that died between its save and its
// reconcile), so answering "no fields specified; nothing to do" was both untrue
// and a dead end. Nothing is rewritten.
func TestBoxSetAlreadyGrantedStillReconciles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, capabilities.WorkdirWriteCapability, "fs.pty.allocate")
	before := readManifest(t, path)
	calls := stubSetReconcile(t)

	var out bytes.Buffer

	t.Cleanup(bindCLITestLogger(t, &out))
	err := runSet(t.Context(), &out, "default", "coder", setOptions{Capabilities: []string{"fs.pty.allocate"}})
	if err != nil {
		t.Fatalf("runSet: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("reconcile tail ran %d time(s), want 1", len(*calls))
	}
	if strings.Contains(out.String(), "no fields specified") {
		t.Errorf("output %q claims no field was specified although --capability was", out.String())
	}
	if !strings.Contains(out.String(), "already") {
		t.Errorf("output %q should say the manifest already holds the value", out.String())
	}
	if after := readManifest(t, path); after != before {
		t.Errorf("no-change set rewrote the manifest:\nbefore=%s\nafter=%s", before, after)
	}
}

// Every field is compared, not only the list-shaped ones: a scalar flag equal
// to the manifest's value is "already set", never a rewrite.
func TestBoxSetUnchangedScalarsAreNotRewritten(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, capabilities.WorkdirWriteCapability)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := stubSetReconcile(t)

	image, readOnly := "img:1", false
	var out bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &out))
	if err := runSet(t.Context(), &out, "default", "coder", setOptions{Image: &image, ReadOnly: &readOnly}); err != nil {
		t.Fatalf("runSet: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Save replaces the file atomically (tmp + rename), so a rewrite shows as
	// a new inode even when the bytes are identical.
	if !os.SameFile(info, after) {
		t.Error("set with unchanged values replaced the manifest file")
	}
	if len(*calls) != 1 || !strings.Contains(out.String(), "already") {
		t.Errorf("want one reconcile and an already-set notice; calls=%d out=%q", len(*calls), out.String())
	}
}

// No field flag at all stays a pure no-op with the usage hint: nothing to
// converge on.
func TestBoxSetWithoutFieldsIsANoOp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedSetBox(t, capabilities.WorkdirWriteCapability)
	calls := stubSetReconcile(t)

	var out bytes.Buffer

	t.Cleanup(bindCLITestLogger(t, &out))
	if err := runSet(t.Context(), &out, "default", "coder", setOptions{}); err != nil {
		t.Fatalf("runSet: %v", err)
	}
	if !strings.Contains(out.String(), "no fields specified") {
		t.Errorf("output %q, want the no-fields usage hint", out.String())
	}
	if len(*calls) != 0 {
		t.Errorf("a set without fields reached the reconcile tail %d time(s)", len(*calls))
	}
}

// The ordinary path: a new, known grant is saved and handed to the reconcile
// tail.
func TestBoxSetSavesThenReconciles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedSetBox(t, capabilities.WorkdirWriteCapability)
	calls := stubSetReconcile(t)

	err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{Capabilities: []string{"fs.pty.allocate"}})
	if err != nil {
		t.Fatalf("runSet: %v", err)
	}
	want := []string{capabilities.WorkdirWriteCapability, "fs.pty.allocate"}
	saved, err := agentbox.Load("default", "coder")
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if !slices.Equal(saved.Capabilities, want) {
		t.Errorf("saved capabilities = %v, want %v", saved.Capabilities, want)
	}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Errorf("reconcile tail calls = %v, want one call with %v", *calls, want)
	}
}

// `box edit` shares the rule: an edit naming an unknown capability fails
// before the save, the previous manifest stands, and no VM work starts.
func TestBoxEditRejectsUnknownCapability(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, capabilities.WorkdirWriteCapability)
	before := readManifest(t, path)

	script := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(script,
		[]byte("#!/bin/sh\nsed 's/fs\\.workdir\\.write/bogus.capability/' \"$1\" > \"$1.tmp\" && mv \"$1.tmp\" \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	err := runEdit(t.Context(), io.Discard, "default", "coder")
	if err == nil || !strings.Contains(err.Error(), `unknown capability "bogus.capability"`) {
		t.Fatalf("runEdit error = %v, want the unknown-capability error", err)
	}
	if !IsUserError(err) {
		t.Errorf("error %q must be a user error (exit 1)", err)
	}
	if after := readManifest(t, path); after != before {
		t.Errorf("rejected edit changed the manifest:\nbefore=%s\nafter=%s", before, after)
	}
}

// The port commands share the save guard. On a manifest that no longer
// resolves (here a granted custom capability that was destroyed since), a port
// change used to be persisted and only then fail in the reconcile that follows,
// leaving the declaration ahead of a runtime nothing could converge.
func TestPortCommandsRefuseAnUnresolvableManifestBeforeSave(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"forward", []string{"forward", "coder", "3000:3000"}},
		{"unforward", []string{"unforward", "coder", "8080:80"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			cfg := &agentbox.Config{
				Name:         "coder",
				Space:        "default",
				Image:        "img:1",
				Workdir:      agentbox.NewEphemeralWorkdir("coder"),
				Capabilities: []string{capabilities.WorkdirWriteCapability, "gone.capability"},
				Ports:        []agentbox.Port{{Published: 8080, Target: 80}},
			}
			if err := cfg.Save(); err != nil {
				t.Fatalf("seed Save: %v", err)
			}
			before := readManifest(t, cfg.Path())

			_, _, err := execCmd(t, newPortCmd(), "", tc.args...)
			if err == nil || !strings.Contains(err.Error(), `unknown capability "gone.capability"`) {
				t.Fatalf("port %s error = %v, want the unknown-capability error", tc.name, err)
			}
			if !IsUserError(err) {
				t.Errorf("error %q must be a user error (exit 1): the caller fixes the manifest", err)
			}
			if after := readManifest(t, cfg.Path()); after != before {
				t.Errorf("refused port %s changed the manifest:\nbefore=%s\nafter=%s", tc.name, before, after)
			}
		})
	}
}

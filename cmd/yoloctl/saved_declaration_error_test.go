package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

func TestSavedDeclarationErrorExplainsEveryApplyFailure(t *testing.T) {
	cfg := &agentbox.Config{Name: "coder", Space: "work"}
	for _, cause := range []error{errors.New("BuildKit worker unavailable"), context.Canceled, context.DeadlineExceeded} {
		got := savedDeclarationError(t.Context(), cfg, cause)
		want := "box coder declaration is saved; applying it failed: " + cause.Error() + "; inspect the current box state and fix the cause before running yoloctl box reconcile coder --space work to retry"
		if got == nil || got.Error() != want || !IsUserError(got) || !errors.Is(got, cause) {
			t.Errorf("saved apply error = %v, want user error %q preserving cause", got, want)
		}
	}
	if got := savedDeclarationError(t.Context(), cfg, nil); got != nil {
		t.Fatalf("successful apply = %v", got)
	}
}

// Set, local edit and controller document edit all enter this tail only after
// their declaration save. Even a reconcile preflight failure must name that
// persisted state, while retaining its underlying cause.
func TestSavedReconcileTailExplainsPreflightFailure(t *testing.T) {
	cause := errors.New("runtime inspection unavailable")
	previous := preflightManagedReconcile
	preflightManagedReconcile = func(context.Context, *container.Client, *agentbox.Config, bool) error { return cause }
	t.Cleanup(func() { preflightManagedReconcile = previous })
	cfg := &agentbox.Config{Name: "coder", Space: "work"}
	got := reconcileAndPrint(t.Context(), nil, io.Discard, nil, cfg, cfg.Name)
	want := "box coder declaration is saved; applying it failed: runtime inspection unavailable; inspect the current box state and fix the cause before running yoloctl box reconcile coder --space work to retry"
	if got == nil || got.Error() != want || !errors.Is(got, cause) || !IsUserError(got) {
		t.Fatalf("reconcile error = %v, want %q", got, want)
	}
}

func TestSavedDeclarationPermissionWarning(t *testing.T) {
	cause := errors.New("apply failed")
	base := &agentbox.Config{Name: "coder", Space: "work", Privileged: true, UnconfinedNetwork: true, Features: []string{"yolo.space.operator"}, Capabilities: []string{"fs.workdir.write"}, Mounts: []agentbox.Mount{{Source: "/source", Target: "/target"}}, Ports: []agentbox.Port{{Target: 80, Published: 8080}}}
	for _, tc := range []struct {
		name   string
		change func(*agentbox.Config)
	}{
		{"privileged", func(c *agentbox.Config) { c.Privileged = false }},
		{"network", func(c *agentbox.Config) { c.UnconfinedNetwork = false }},
		{"operator", func(c *agentbox.Config) { c.Features = nil }},
		{"capability", func(c *agentbox.Config) { c.Capabilities = nil }},
		{"mount", func(c *agentbox.Config) { c.Mounts = nil }},
		{"readonly mount", func(c *agentbox.Config) {
			c.Mounts = append([]agentbox.Mount(nil), c.Mounts...)
			c.Mounts[0].ReadOnly = true
		}},
		{"port", func(c *agentbox.Config) { c.Ports = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := *base
			ctx := withPreviousBoxPermissions(t.Context(), &next)
			tc.change(&next)
			err := savedDeclarationError(ctx, &next, cause)
			if !strings.Contains(err.Error(), "the previous container may still run with the removed permission(s) until 'yoloctl box reconcile coder' (or recreate/stop) succeeds") || !errors.Is(err, cause) {
				t.Fatal(err)
			}
			if err := savedDeclarationError(ctx, &next, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, changed := range []bool{false, true} {
		next := *base
		ctx := withPreviousBoxPermissions(t.Context(), &next)
		if changed {
			next.Capabilities = append(append([]string(nil), next.Capabilities...), "network.github")
		}
		if err := savedDeclarationError(ctx, &next, cause); strings.Contains(err.Error(), "previous container") {
			t.Fatal(err)
		}
	}
	// Equivalent default port spellings and a read-only -> writable mount do
	// not remove the original grant.
	next := *base
	ctx := withPreviousBoxPermissions(t.Context(), &next)
	next.Ports = []agentbox.Port{{Target: 80, Published: 8080, HostIP: "127.0.0.1", Protocol: "tcp"}}
	if removedDeclarationPermissions(ctx, &next) {
		t.Fatal("equivalent port changed authority")
	}
}

func TestBoxSetSavedPermissionRemovalFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t, "fs.workdir.write")
	cause := errors.New("preflight unavailable")
	previous := preflightManagedReconcile
	preflightManagedReconcile = func(context.Context, *container.Client, *agentbox.Config, bool) error { return cause }
	t.Cleanup(func() { preflightManagedReconcile = previous })
	readonly := true
	err := runSet(t.Context(), io.Discard, "default", "coder", setOptions{ReadOnly: &readonly})
	if err == nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "previous container may still run") {
		t.Fatal(err)
	}
	if strings.Contains(readManifest(t, path), "fs.workdir.write") {
		t.Fatal("new declaration not saved")
	}
}

// The runtime create can complete before conversation recording fails. The
// error must not claim the runtime was untouched or that no settings applied.
type savedRecordFailureApplier struct {
	fakeManagedInner
	path string
}

func (a *savedRecordFailureApplier) Create(ctx context.Context, cfg *agentbox.Config, policy []byte) error {
	if err := a.fakeManagedInner.Create(ctx, cfg, policy); err != nil {
		return err
	}
	return os.WriteFile(a.path, []byte("invalid: ["), 0o600)
}
func TestSavedReconcileReportsPostApplyRecordFailure(t *testing.T) {
	f := newReplacementFixture(t)
	path, err := layout.BoxSessionPath("work", "coder")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	applier := &savedRecordFailureApplier{path: path}
	managedReconcileRuntime = func(*container.Client, io.Writer) (agentbox.StateInspector, agentbox.Applier) {
		return fixedInspector{state: &agentbox.ActualState{}}, applier
	}
	lock, err := lockSessionBoxFn("work", "coder")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = reconcileAndPrint(t.Context(), lock, io.Discard, container.New("work"), f.cfg, "coder")
	if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "declaration is saved; applying it failed") || !strings.Contains(err.Error(), "recording that for its conversation failed") || !slices.Equal(applier.calls, []string{"create"}) {
		t.Fatalf("completed calls %v, error %v", applier.calls, err)
	}
}

func TestBoxEditSavedPermissionRemovalFailure(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "local editor", true: "controller document"}[native], func(t *testing.T) {
			f := newReplacementFixture(t)
			f.cfg.UnconfinedNetwork = true
			if err := f.cfg.Save(); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("runtime preflight unavailable")
			preflightManagedReconcile = func(context.Context, *container.Client, *agentbox.Config, bool) error { return cause }
			var err error
			if native {
				before, marshalErr := yaml.Marshal(f.cfg)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				edited := *f.cfg
				edited.UnconfinedNetwork = false
				after, marshalErr := yaml.Marshal(&edited)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				oldDoc, oldSync, oldValidate := controllerDocumentFn, syncNativeChatRolesFn, revalidateOperatorFn
				t.Cleanup(func() {
					controllerDocumentFn, syncNativeChatRolesFn, revalidateOperatorFn = oldDoc, oldSync, oldValidate
				})
				controllerDocumentFn = func(string, string, string) (editDocument, error) {
					return editDocument{Revision: "current", Data: before}, nil
				}
				syncNativeChatRolesFn = func(context.Context, string) error { return nil }
				revalidateOperatorFn = func(context.Context) error { return nil }
				payload, marshalErr := json.Marshal(editDocument{Revision: "current", Data: after})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error { return nil })
				_, err = runControllerDocument(ctx, controller.Request{Method: "box.edit.apply", Space: "work", Args: []string{"coder"}, Payload: payload}, "")
			} else {
				editor := filepath.Join(t.TempDir(), "editor")
				script := "#!/bin/sh\nsed '/^unconfined_network:/d' \"$1\" > \"$1.out\"\ncat \"$1.out\" > \"$1\"\n"
				if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("EDITOR", editor)
				err = runEdit(t.Context(), io.Discard, "work", "coder")
			}
			if err == nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "previous container may still run") {
				t.Fatal(err)
			}
			saved, loadErr := agentbox.Load("work", "coder")
			if loadErr != nil || saved.UnconfinedNetwork {
				t.Fatalf("saved declaration = %+v, %v", saved, loadErr)
			}
		})
	}
}

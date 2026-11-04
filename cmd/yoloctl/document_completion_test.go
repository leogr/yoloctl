package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spacelock"
	"gopkg.in/yaml.v3"
)

// The document dispatcher bypasses the Cobra wrapper, so it owns completion:
// recorded from its own outcome after a successful role publication and
// before its native lock is released, never after a failed one or a panic.
func TestDocumentDispatcherOwnsCompletionBeforeUnlock(t *testing.T) {
	sentinel := errors.New("role publication pending")
	for _, tc := range []struct {
		name, method string
		syncErr      error
		panics       bool
		want         []string
	}{
		// Refused before the gate: no effect and no role publication.
		{"box before the gate", "box.edit.apply", sentinel, false, []string{"complete-locked"}},
		{"account", "account.edit.apply", nil, false, []string{"complete-locked"}},
		{"panic", "account.edit.apply", nil, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			previousDoc, previousSync, previousRevalidate := controllerDocumentFn, syncNativeChatRolesFn, revalidateOperatorFn
			t.Cleanup(func() {
				controllerDocumentFn, syncNativeChatRolesFn, revalidateOperatorFn = previousDoc, previousSync, previousRevalidate
			})
			var events []string
			locked := func() bool {
				probe, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer stop()
				unlock, err := spacelock.Acquire(probe, "work", true)
				if err == nil {
					unlock()
				}
				return err != nil
			}
			controllerDocumentFn = func(string, string, string) (editDocument, error) {
				if tc.panics {
					panic("after the lock")
				}
				return editDocument{Revision: "current"}, nil
			}
			syncNativeChatRolesFn = func(context.Context, string) error {
				events = append(events, "role-sync")
				return tc.syncErr
			}
			revalidateOperatorFn = func(context.Context) error { return nil }
			recorder := &lockedCompletionRecorder{locked: locked, events: &events}
			ctx := controller.WithEffectRecorder(t.Context(), recorder)
			completion := &workerCompletion{}
			ctx = context.WithValue(ctx, workerCompletionKey{}, completion)
			// A stale revision is an ordinary failed result after the lock.
			payload, _ := json.Marshal(editDocument{Revision: "stale", Data: []byte("name: dev\n")})
			req := controller.Request{Method: tc.method, Space: "work", Args: []string{"dev"}, Payload: payload}
			func() {
				defer func() {
					if p := recover(); (p != nil) != tc.panics {
						t.Fatal("panic", p)
					}
				}()
				if handled, err := runControllerDocument(ctx, req, ""); !handled || err == nil {
					t.Fatal(handled, err)
				}
			}()
			if !slices.Equal(events, tc.want) {
				t.Fatalf("events %v, want %v", events, tc.want)
			}
			if !tc.panics && !completion.owned {
				t.Fatal("dispatcher left completion to the generic worker tail")
			}
			if len(recorder.status) == 1 && recorder.status[0] != exitUser {
				t.Fatal("a failed result was recorded as", recorder.status)
			}
		})
	}
}

func TestBoxDocumentCompletionAfterGateAndRolePublication(t *testing.T) {
	for _, failedPublication := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "publication failed"}[failedPublication], func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			saveSpaceForTest(t, "work", "/work")
			cfg := &agentbox.Config{SchemaVersion: agentbox.SchemaVersion, Name: "dev", Space: "work", Agent: "none", Account: "none", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("dev")}
			if err := requireResolvableManifest(t.Context(), cfg); err != nil {
				t.Fatal("fixture must reach the gate", err)
			}
			// Force a known save failure after permission, without touching a
			// runtime. Deferred role publication must still run under the lock.
			path, err := layout.BoxPath("work", "dev")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			oldDoc, oldSync, oldRevalidate := controllerDocumentFn, syncNativeChatRolesFn, revalidateOperatorFn
			t.Cleanup(func() {
				controllerDocumentFn, syncNativeChatRolesFn, revalidateOperatorFn = oldDoc, oldSync, oldRevalidate
			})
			controllerDocumentFn = func(string, string, string) (editDocument, error) { return editDocument{Revision: "current"}, nil }
			revalidateOperatorFn = func(context.Context) error { return nil }
			var events []string
			locked := func() bool {
				probe, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
				defer cancel()
				release, err := spacelock.Acquire(probe, "work", true)
				if err == nil {
					release()
				}
				return err != nil
			}
			syncNativeChatRolesFn = func(context.Context, string) error {
				if !locked() {
					t.Error("role publication ran after unlock")
				}
				events = append(events, "role-sync")
				if failedPublication {
					return errors.New("role publication pending")
				}
				return nil
			}
			recorder := &lockedCompletionRecorder{locked: locked, events: &events}
			ctx := controller.WithEffectRecorder(t.Context(), recorder)
			ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { events = append(events, "gate"); return nil })
			completion := &workerCompletion{}
			ctx = context.WithValue(ctx, workerCompletionKey{}, completion)
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(editDocument{Revision: "current", Data: raw})
			if err != nil {
				t.Fatal(err)
			}
			handled, err := runControllerDocument(ctx, controller.Request{Method: "box.edit.apply", Space: "work", Args: []string{"dev"}, Payload: payload}, "")
			if !handled || err == nil || !completion.owned {
				t.Fatal(handled, err, completion)
			}
			want := []string{"gate", "role-sync"}
			if !failedPublication {
				want = append(want, "complete-locked")
			}
			if !slices.Equal(events, want) {
				t.Fatalf("events %v, want %v", events, want)
			}
			if !failedPublication && (len(recorder.status) != 1 || recorder.status[0] == 0) {
				t.Fatal("save failure was not recorded as failure", recorder.status)
			}
		})
	}
}

type lockedCompletionRecorder struct {
	locked func() bool
	events *[]string
	status []int
}

func (r *lockedCompletionRecorder) ExpandEffects(context.Context, []controller.Resource) error {
	return nil
}
func (r *lockedCompletionRecorder) RecordDocument(context.Context, controller.DocumentIntent) error {
	return nil
}
func (r *lockedCompletionRecorder) CompleteEffects(_ context.Context, status int) error {
	if r.locked() {
		*r.events = append(*r.events, "complete-locked")
	} else {
		*r.events = append(*r.events, "complete-unlocked")
	}
	r.status = append(r.status, status)
	return nil
}

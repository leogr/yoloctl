package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// replacementFixture drives the real edit and chat reconcile seams of a
// tracked Claude box with only the VM faked: the inspector reports the
// observed container, the managed applier's session cleanup removes the
// holder evidence, and the inner applier records lifecycle calls.
type replacementFixture struct {
	*identityFixture
	cfg      *agentbox.Config
	actual   agentbox.ActualState
	inner    *fakeManagedInner
	failWith error
	cleanups int
	// cleanupFails makes the session cleanup fail to prove the launch ended:
	// the holder unit and marker stay.
	cleanupFails bool
}

type fixedInspector struct{ state *agentbox.ActualState }

func (i fixedInspector) Inspect(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
	return *i.state, nil
}

// failingInner fails the inner Recreate after the managed cleanup ran, as a
// replacement whose new container never came up.
type failingInner struct {
	*fakeManagedInner
	err error
}

func (f failingInner) Recreate(ctx context.Context, c *agentbox.Config, p []byte) error {
	_ = f.fakeManagedInner.Recreate(ctx, c, p)
	return f.err
}

// Restart fails after the managed cleanup ran, as a policy reload whose
// container never came back.
func (f failingInner) Restart(ctx context.Context, c *agentbox.Config, p []byte) error {
	_ = f.fakeManagedInner.Restart(ctx, c, p)
	return f.err
}

func newReplacementFixture(t *testing.T) *replacementFixture {
	t.Helper()
	f := &replacementFixture{identityFixture: newIdentityFixture(t), inner: &fakeManagedInner{}}
	saveSpaceForTest(t, "work", t.TempDir())
	if err := account.New("work", "main").Save(); err != nil {
		t.Fatal(err)
	}
	f.cfg = claudeBox()
	f.cfg.ID = agentbox.NewIdentity()
	f.cfg.Image = defaultAgentboxImage
	f.cfg.Workdir = agentbox.NewEphemeralWorkdir("coder")
	if err := f.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// An image drift makes the next reconcile replace the container.
	f.actual = agentbox.ActualState{Exists: true, Image: "img:old"}
	oldRuntime, oldPreflight := managedReconcileRuntime, preflightManagedReconcile
	managedReconcileRuntime = func(*container.Client, io.Writer) (agentbox.StateInspector, agentbox.Applier) {
		var inner agentbox.Applier = f.inner
		if f.failWith != nil {
			inner = failingInner{fakeManagedInner: f.inner, err: f.failWith}
		}
		return fixedInspector{state: &f.actual}, managedApplier{inner: inner, cleanup: func(_ context.Context, cfg *agentbox.Config) (bool, error) {
			f.cleanups++
			if f.cleanupFails {
				return false, nil
			}
			f.unit[cfg.Name] = false
			delete(f.marker, cfg.Name)
			return true, nil
		}}
	}
	preflightManagedReconcile = func(context.Context, *container.Client, *agentbox.Config, bool) error { return nil }
	t.Cleanup(func() { managedReconcileRuntime, preflightManagedReconcile = oldRuntime, oldPreflight })
	return f
}

// editWithAppendedComment runs the host `box edit` with an editor that only
// appends a comment, so the save succeeds and the reconcile tail runs.
func (f *replacementFixture) editWithAppendedComment() error {
	f.t.Helper()
	editor := filepath.Join(f.t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf '# edited\\n' >> \"$1\"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.t.Setenv("EDITOR", editor)
	return runEdit(f.t.Context(), io.Discard, "work", "coder")
}

func (f *replacementFixture) recreateMember() error {
	return recreateChatMember(f.t.Context(), "work", managedchat.Member{ID: managedchat.NewID(), Name: "coder", BoxID: f.cfg.ID}, io.Discard)
}

func (f *replacementFixture) record() *agentsession.Record {
	f.t.Helper()
	rec, ok, err := agentsession.Load("work", "coder")
	if err != nil || !ok {
		f.t.Fatalf("load record: ok=%v err=%v", ok, err)
	}
	return rec
}

// A known launched conversation survives a verified replacement through
// either seam: the next managed launch resumes the same ID.
func TestReplacementSeamsResumeKnownConversation(t *testing.T) {
	for _, seam := range []string{"box edit", "chat enroll --recreate"} {
		t.Run(seam, func(t *testing.T) {
			f := newReplacementFixture(t)
			first, err := f.launchOnce(f.cfg, sessionStartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			replace := f.editWithAppendedComment
			if seam == "chat enroll --recreate" {
				replace = f.recreateMember
			}
			if err := replace(); err != nil {
				t.Fatalf("replacement: %v", err)
			}
			if f.cleanups != 1 || !slices.Equal(f.inner.calls, []string{"recreate"}) {
				t.Fatalf("replacement did not run once: cleanups=%d calls=%v", f.cleanups, f.inner.calls)
			}
			if len(f.starts) != 1 {
				t.Fatal("the replacement launched a session")
			}
			next, err := f.launchOnce(f.cfg, sessionStartOptions{})
			if err != nil {
				t.Fatalf("next launch after replacement: %v", err)
			}
			if next.Assigned.ID != first.Assigned.ID || !slices.Contains(f.starts[1], "--resume") || !slices.Contains(f.starts[1], first.Assigned.ID) {
				t.Fatalf("next launch did not resume %s: %v", first.Assigned.ID, f.starts[1])
			}
		})
	}
}

// policyDrift makes the next reconcile restart the container to reload its
// policy instead of recreating it.
func (f *replacementFixture) policyDrift() {
	f.t.Helper()
	image, _, err := agentbox.ResolvedImage(f.cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	f.actual = agentbox.ActualState{Exists: true, Image: image, PolicyContent: []byte("old policy"), ReadOnlyWD: true}
}

// A policy restart (box set --capability, a policy-only edit) ends the
// launch at its session cleanup: the record closes with the restart detail
// and the next plain start resumes the same conversation.
func TestPolicyRestartClosesTheLaunchAndResumes(t *testing.T) {
	f := newReplacementFixture(t)
	first, err := f.launchOnce(f.cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.policyDrift()
	if err := f.editWithAppendedComment(); err != nil {
		t.Fatalf("policy restart: %v", err)
	}
	if f.cleanups != 1 || !slices.Equal(f.inner.calls, []string{"restart"}) {
		t.Fatalf("the restart did not run once: cleanups=%d calls=%v", f.cleanups, f.inner.calls)
	}
	if rec := f.record(); rec.Launch.State != agentsession.StateClosed || rec.Launch.Detail != sessionEndedByRestart {
		t.Fatalf("the restart did not close the launch: %+v", rec.Launch)
	}
	next, err := f.launchOnce(f.cfg, sessionStartOptions{})
	if err != nil {
		t.Fatalf("next launch after the restart: %v", err)
	}
	if next.Assigned.ID != first.Assigned.ID || !slices.Contains(f.starts[1], "--resume") || !slices.Contains(f.starts[1], first.Assigned.ID) {
		t.Fatalf("next launch did not resume %s: %v", first.Assigned.ID, f.starts[1])
	}
}

// The cleanup is the proof, not the container operation: a restart or
// recreate that fails after a successful cleanup still closes the launch,
// so the next start resumes instead of refusing an uncertain one.
func TestReplacementFailingAfterCleanupClosesTheLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		drift        func(*replacementFixture)
	}{
		{"restart", sessionEndedByRestart, (*replacementFixture).policyDrift},
		{"recreate", sessionEndedByRecreate, func(*replacementFixture) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplacementFixture(t)
			first, err := f.launchOnce(f.cfg, sessionStartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			tc.drift(f)
			f.failWith = errors.New("the container did not come back")
			if err := f.editWithAppendedComment(); err == nil {
				t.Fatal("a failed container operation succeeded")
			}
			if rec := f.record(); rec.Launch.State != agentsession.StateClosed || rec.Launch.Detail != tc.detail {
				t.Fatalf("a proved cleanup did not close the launch: %+v", rec.Launch)
			}
			f.failWith = nil
			next, err := f.launchOnce(f.cfg, sessionStartOptions{})
			if err != nil {
				t.Fatalf("next launch: %v", err)
			}
			if next.Assigned.ID != first.Assigned.ID || !slices.Contains(f.starts[1], first.Assigned.ID) {
				t.Fatalf("next launch did not resume %s: %v", first.Assigned.ID, f.starts[1])
			}
		})
	}
}

// A cleanup that could not stop the holder or remove its state proves
// nothing: the launch stays for the next start to settle by its unit,
// marker or tombstone, through either seam.
func TestUnprovedCleanupLeavesLaunchUnsettled(t *testing.T) {
	for _, seam := range []string{"box edit", "chat enroll --recreate"} {
		t.Run(seam, func(t *testing.T) {
			f := newReplacementFixture(t)
			if _, err := f.launchOnce(f.cfg, sessionStartOptions{}); err != nil {
				t.Fatal(err)
			}
			f.cleanupFails = true
			replace := f.editWithAppendedComment
			if seam == "chat enroll --recreate" {
				replace = f.recreateMember
			}
			if err := replace(); err != nil {
				t.Fatalf("replacement: %v", err)
			}
			if rec := f.record(); rec.Launch.State != agentsession.StateLaunched {
				t.Fatalf("an unproved cleanup settled the launch: %+v", rec.Launch)
			}
		})
	}
}

// An interrupted first assignment may never have created its conversation:
// replacement proof keeps it uncertain until an explicit new session.
func TestReplacementKeepsInterruptedFirstAssignmentUncertain(t *testing.T) {
	f := newReplacementFixture(t)
	f.startErr = errors.New("lost reply")
	_, _ = f.launchOnce(f.cfg, sessionStartOptions{})
	f.startErr = nil
	if err := f.recreateMember(); err != nil {
		t.Fatal(err)
	}
	if rec := f.record(); rec.Launch.State != agentsession.StateUncertain {
		t.Fatalf("interrupted first assignment = %+v", rec.Launch)
	}
	if _, err := f.launchOnce(f.cfg, sessionStartOptions{}); err == nil {
		t.Fatal("an interrupted first assignment was resumed or reassigned")
	}
}

// Without a replacement there is no proof: an in-sync box keeps its record
// and its live holder untouched.
func TestInSyncReconcileDoesNotSettle(t *testing.T) {
	f := newReplacementFixture(t)
	if _, err := f.launchOnce(f.cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	image, _, err := agentbox.ResolvedImage(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentbox.ComposePolicy(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.actual = agentbox.ActualState{Exists: true, Image: image, PolicyContent: policy, ReadOnlyWD: true}
	if err := f.recreateMember(); err != nil {
		t.Fatal(err)
	}
	if f.cleanups != 0 || len(f.inner.calls) != 0 {
		t.Fatalf("in-sync reconcile replaced: cleanups=%d calls=%v", f.cleanups, f.inner.calls)
	}
	if rec := f.record(); rec.Launch.State != agentsession.StateLaunched {
		t.Fatalf("in-sync reconcile changed the launch: %+v", rec.Launch)
	}
}

// Chat --recreate of a member whose container is missing materializes it
// through the shared create path: no session cleanup runs, the creation is
// the proof, and the next launch resumes the same conversation.
func TestChatRecreateMaterializesMissingMemberAndResumes(t *testing.T) {
	f := newReplacementFixture(t)
	first, err := f.launchOnce(f.cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The container and its holder are gone; no exit record was written.
	f.actual = agentbox.ActualState{}
	f.unit["coder"] = false
	delete(f.marker, "coder")
	if err := f.recreateMember(); err != nil {
		t.Fatalf("materialization: %v", err)
	}
	if f.cleanups != 0 || !slices.Equal(f.inner.calls, []string{"create"}) {
		t.Fatalf("materialization did not create once: cleanups=%d calls=%v", f.cleanups, f.inner.calls)
	}
	if rec := f.record(); rec.Launch.State != agentsession.StateClosed || len(f.starts) != 1 {
		t.Fatalf("creation did not settle the launch without launching: %+v starts=%d", rec.Launch, len(f.starts))
	}
	next, err := f.launchOnce(f.cfg, sessionStartOptions{})
	if err != nil {
		t.Fatalf("next launch after materialization: %v", err)
	}
	if next.Assigned.ID != first.Assigned.ID || !slices.Contains(f.starts[1], "--resume") || !slices.Contains(f.starts[1], first.Assigned.ID) {
		t.Fatalf("next launch did not resume %s: %v", first.Assigned.ID, f.starts[1])
	}
}

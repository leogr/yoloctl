package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestMissingSavedConversationSelectsOneFreshIdentity(t *testing.T) {
	f := newIdentityFixture(t)
	boxArgs := []string{"--effort", "high", "--model", "opus"}
	cfg := claudeBox(boxArgs...)
	before, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	old := before.Assigned.ID
	f.exit(cfg.Name, 0)
	f.noteExit(cfg)
	// A missing target's reports must not seed the replacement conversation.
	f.observeLast(cfg, old, sessionNow(), sessionNow(), "xhigh", "claude-fable-5-1")
	probes := 0
	resumeFilePresentFn = func(_ context.Context, c *agentbox.Config, spec *agentprofile.SessionSpec, id string) (bool, error) {
		probes++
		if c.Account != cfg.Account || spec.ResumeFile != "projects/*/{{session_id}}.jsonl" || id != old {
			t.Fatal("presence probe escaped the selected identity")
		}
		return false, nil
	}
	after, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 || len(f.starts) != 2 || after.Assigned.ID == old || after.Launch.Kind != agentsession.KindAssign || after.Launch.Generation != 2 {
		t.Fatal("absence did not select exactly one fresh launch")
	}
	if slices.Contains(f.starts[1], "--resume") || !slices.Contains(f.starts[1], after.Assigned.ID) || !slices.Contains(f.starts[1], "--session-id") {
		t.Fatal("absence retained resume argv")
	}
	argv := f.starts[1]
	if !slices.Equal(argv[len(argv)-len(boxArgs):], boxArgs) || slices.Contains(argv, "xhigh") || slices.Contains(argv, "claude-fable-5-1") {
		t.Fatal("fresh assignment inherited the missing conversation's model or effort")
	}
	if len(after.Previous) != 1 || after.Previous[0].ID != old || after.Previous[0].Why != agentsession.WhySavedConversationMissing {
		t.Fatal("missing target history was lost")
	}
	want := "no saved conversation found for " + old + "; starting a new conversation " + after.Assigned.ID
	if !strings.Contains(f.notices.String(), want) {
		t.Fatal("missing replacement notice")
	}
	profile, _ := agentprofile.Lookup(cfg.Agent)
	status := agentsession.Describe(cfg, profile, false, "")
	if !strings.Contains(status.Line(), "no saved conversation found for "+old) || !strings.Contains(status.Previous[0].Text(), "new conversation selected") {
		t.Fatal("persistent shared status did not explain replacement")
	}
}

func TestResumeProbeUsesObservedTargetAndErrorsLeaveRecordUnchanged(t *testing.T) {
	for _, outcome := range []string{"present", "absent", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			f := newIdentityFixture(t)
			cfg := claudeBox()
			if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
				t.Fatal(err)
			}
			f.exit(cfg.Name, 0)
			f.noteExit(cfg)
			l := f.begin(cfg)
			const target = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
			l.rec.Current = &agentsession.Current{ID: target, Source: "clear", Generation: l.rec.LastGeneration, At: sessionNow(), ReadAt: sessionNow()}
			if err := l.save(); err != nil {
				t.Fatal(err)
			}
			l.Release()
			before, _, _ := agentsession.Load(cfg.Space, cfg.Name)
			resumeFilePresentFn = func(_ context.Context, _ *agentbox.Config, _ *agentprofile.SessionSpec, id string) (bool, error) {
				if id != target {
					t.Fatal("inspected Assigned instead of the observed Target")
				}
				if outcome == "unavailable" {
					return false, errors.New("native state unavailable")
				}
				return outcome == "present", nil
			}
			after, err := f.launchOnce(cfg, sessionStartOptions{})
			if outcome == "unavailable" {
				if err == nil || len(f.starts) != 1 {
					t.Fatal("unknown storage launched an agent")
				}
				persisted, _, _ := agentsession.Load(cfg.Space, cfg.Name)
				if !reflect.DeepEqual(before, persisted) {
					t.Fatal("storage failure changed conversation state")
				}
			} else if outcome == "absent" {
				if err != nil || after.Assigned.ID == target || after.Assigned.ID == before.Assigned.ID || len(after.Previous) != 2 {
					t.Fatal("missing switched target fell back to an older assignment", err)
				}
				status := agentsession.Describe(cfg, f.tracking(cfg).profile, false, "")
				if !strings.Contains(status.Note, target) {
					t.Fatal("replacement note named the wrong prior target")
				}
			} else if err != nil || after.Assigned.ID != target || !slices.Contains(f.starts[1], "--resume") {
				t.Fatal("saved observed target was not resumed", err)
			}
		})
	}
}

func TestPersistedFreshAllocationDoesNotProbeOrMintAnotherIdentity(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit(cfg.Name, 0)
	f.noteExit(cfg)
	l := f.begin(cfg)
	l.rec.Retire(agentsession.WhySavedConversationMissing, sessionNow())
	if err := l.rec.Allocate(agentsession.KindAssign, sessionNow()); err != nil {
		t.Fatal(err)
	}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	id, token, generation := l.rec.Assigned.ID, l.rec.Launch.Token, l.rec.Launch.Generation
	l.Release()
	resumeFilePresentFn = func(context.Context, *agentbox.Config, *agentprofile.SessionSpec, string) (bool, error) {
		t.Fatal("undispatched allocation was reprobed")
		return false, nil
	}
	rec, err := f.launchOnce(cfg, sessionStartOptions{})
	if err != nil || rec.Assigned.ID != id || rec.Launch.Token != token || rec.Launch.Generation != generation {
		t.Fatal("allocated replacement was rewritten", err)
	}
}

func TestNativeResumeProbeRequiresExplicitOutcomeAndHasDeadline(t *testing.T) {
	const id = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	previous := accountStateOperation
	t.Cleanup(func() { accountStateOperation = previous })
	for _, outcome := range []string{"present", "absent", ""} {
		accountStateOperation = func(ctx context.Context, _ string, req accountstate.Request) (accountstate.Result, error) {
			if _, ok := ctx.Deadline(); !ok || req.Action != "resume-file" || string(req.Data) != id || req.Account != "account" {
				t.Fatal("resume probe lost its bound or scope")
			}
			return accountstate.Result{Outcome: outcome}, nil
		}
		present, err := resumeFilePresent(t.Context(), &agentbox.Config{Space: "space", Agent: "claude", Account: "account"}, &agentprofile.SessionSpec{ResumeFile: "projects/*/{{session_id}}.jsonl"}, id)
		if (err != nil) != (outcome == "") || (err == nil && present != (outcome == "present")) {
			t.Fatal("ambiguous native reply became absence")
		}
	}
}

func TestMissingConversationSaveFailureDoesNotStartHolder(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.exit(cfg.Name, 0)
	f.noteExit(cfg)
	l := f.begin(cfg)
	defer l.Release()
	kind, err := l.plan(sessionStartOptions{}, &f.notices)
	if err != nil {
		t.Fatal(err)
	}
	path, err := layout.BoxSessionPath(cfg.Space, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	// Only this disposable fixture's record is replaced by an unwritable shape.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	resumeFilePresentFn = func(context.Context, *agentbox.Config, *agentprofile.SessionSpec, string) (bool, error) {
		return false, nil
	}
	if err := l.launch(t.Context(), container.New(cfg.Space), cfg, kind, f.start(cfg.Name)); err == nil || len(f.starts) != 1 {
		t.Fatal("failed allocation save dispatched a holder")
	}
	if strings.Contains(f.notices.String(), "starting a new conversation") {
		t.Fatal("failed allocation save announced a start")
	}
}

func TestExplicitNewAndNeverDoNotProbeSavedFiles(t *testing.T) {
	for _, option := range []string{"new", "never", "no-field"} {
		t.Run(option, func(t *testing.T) {
			f := newIdentityFixture(t)
			cfg := claudeBox()
			if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
				t.Fatal(err)
			}
			f.exit(cfg.Name, 0)
			f.noteExit(cfg)
			if option == "never" {
				cfg.AgentSession = &agentbox.AgentSession{Resume: agentbox.AgentSessionResumeNever}
			}
			l := f.begin(cfg)
			defer l.Release()
			if option == "no-field" {
				l.profile.Session.ResumeFile = ""
			}
			resumeFilePresentFn = func(context.Context, *agentbox.Config, *agentprofile.SessionSpec, string) (bool, error) {
				t.Fatal("profile/policy bypass inspected saved files")
				return false, nil
			}
			kind, err := l.plan(sessionStartOptions{NewSession: option == "new"}, &f.notices)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.launch(t.Context(), container.New(cfg.Space), cfg, kind, f.start(cfg.Name)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A direct caller cannot retire an unresolved launch before Allocate's guard.
func TestUncertainLaunchCannotBeRetiredByMissingFileFallback(t *testing.T) {
	f := newIdentityFixture(t)
	cfg := claudeBox()
	if _, err := f.launchOnce(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	l := f.begin(cfg)
	defer l.Release()
	if err := l.rec.Uncertain(l.rec.Launch.Generation, l.rec.Launch.Token, "unknown", sessionNow()); err != nil {
		t.Fatal(err)
	}
	resumeFilePresentFn = func(context.Context, *agentbox.Config, *agentprofile.SessionSpec, string) (bool, error) {
		t.Fatal("uncertain launch inspected transcripts")
		return false, nil
	}
	if err := l.launch(t.Context(), container.New(cfg.Space), cfg, agentsession.KindResume, f.start(cfg.Name)); err == nil {
		t.Fatal("uncertain launch replaced automatically")
	}
}

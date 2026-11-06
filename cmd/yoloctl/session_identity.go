package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Conversation identity for agent boxes whose profile declares a session
// block (internal/agentsession). Every record change happens under the box
// entity lock, which tracked starts take inside the command's native
// space-operation lock; nothing here takes or releases the space lock.

const newSessionFlag = "new-session"

// sessionStartOptions changes how ensureAgentSession launches a session.
type sessionStartOptions struct {
	// NewSession replaces the box's conversation before the launch. It is
	// refused while a holder is live or its liveness is uncertain.
	NewSession bool
}

// Seams for tests; production reads the VM and the native disk.
var (
	lockSessionBoxFn  = agentsession.LockBox
	readVMMarkerFn    = session.ReadVMMarker
	readVMTombstoneFn = session.ReadVMTombstone
	// Error-preserving probes for identity decisions: an unknown holder or
	// marker refuses instead of reading as absent.
	holderUnitStateFn = session.HolderUnitState
	probeVMMarkerFn   = session.ProbeVMMarker
	sessionNow        = func() time.Time { return time.Now().UTC() }
)

// conversationTracking is a tracked box's profile, policy and record scope.
type conversationTracking struct {
	profile *agentprofile.Profile
	policy  agentsession.Effective
	scope   agentsession.Scope
}

// sessionTrackingFor returns the box's tracking, or nil when yoloctl does not
// own its conversation (agentless, unsupported profile or manual policy). An
// explicit last or never contradicted by agent_extra_args is an error.
func sessionTrackingFor(cfg *agentbox.Config) (*conversationTracking, agentsession.Effective, error) {
	if !cfg.HasAgent() {
		return nil, agentsession.Effective{Policy: agentsession.PolicyUnsupported}, nil
	}
	profile, err := lookupAgentProfile(cfg.Agent)
	if err != nil {
		return nil, agentsession.Effective{}, err
	}
	policy, err := agentsession.EffectivePolicy(profile.Session, cfg)
	if err != nil {
		return nil, agentsession.Effective{}, asUserError(fmt.Errorf("box %s: %w", cfg.Name, err))
	}
	if !policy.Tracked() {
		return nil, policy, nil
	}
	return &conversationTracking{profile: profile, policy: policy, scope: agentsession.Scope{BoxID: cfg.ID, Account: cfg.Account, Provider: cfg.Agent}}, policy, nil
}

// validateAgentSessionPolicy is the create/edit check of a declaration's
// conversation policy against its agent profile.
func validateAgentSessionPolicy(cfg *agentbox.Config) error {
	_, _, err := sessionTrackingFor(cfg)
	return err
}

// trackedLaunch carries one tracked box through ensureAgentSession under its
// box lock.
type trackedLaunch struct {
	*conversationTracking
	space, name string
	lock        *agentsession.BoxLock
	rec         *agentsession.Record
}

// beginTrackedSession takes the box lock inside the command's space lock and
// loads the record. The caller must Release the returned launch.
func beginTrackedSession(ctx context.Context, t *conversationTracking, space, name string) (*trackedLaunch, error) {
	if !spaceOperationHeld(ctx) {
		return nil, fmt.Errorf("internal error: tracked session start for %s without the space operation lock", name)
	}
	lock, err := lockSessionBoxFn(space, name)
	if err != nil {
		return nil, fmt.Errorf("lock box %s: %w", name, err)
	}
	rec, err := agentsession.LoadLocked(lock, t.scope, sessionNow())
	if err != nil {
		lock.Release()
		return nil, err
	}
	return &trackedLaunch{conversationTracking: t, space: space, name: name, lock: lock, rec: rec}, nil
}

func (l *trackedLaunch) Release() {
	if l != nil {
		l.lock.Release()
	}
}

func (l *trackedLaunch) save() error { return agentsession.Save(l.lock, l.rec) }

func (l *trackedLaunch) conversation() string {
	if l.rec != nil && l.rec.Assigned != nil {
		return l.rec.Assigned.ID
	}
	return ""
}

// noteExit closes the launch whose tombstone carries its token. It runs
// before the tombstone is cleared, so a crash in between repeats it.
func (l *trackedLaunch) noteExit(ts session.Tombstone) error {
	if l.rec == nil || l.rec.Launch == nil || ts.LaunchToken == "" || ts.LaunchToken != l.rec.Launch.Token {
		return nil
	}
	if err := l.rec.CloseExit(ts.LaunchToken, ts.ExitCode, ts.ExitedAt, sessionNow()); err != nil {
		return nil
	}
	return l.save()
}

// exitHint explains a nonzero exit right after a resume request: the exit
// status cannot tell a missing conversation from a resumed one that failed.
func (l *trackedLaunch) exitHint(ts session.Tombstone) string {
	if l.rec == nil || l.rec.Launch == nil || l.rec.Launch.Kind != agentsession.KindResume || ts.LaunchToken != l.rec.Launch.Token || ts.ExitCode == 0 {
		return ""
	}
	return fmt.Sprintf("notice: that session was resuming conversation %s; if the agent could not find it, start a new one with yoloctl box restart %s --new-session\n", l.conversation(), l.name)
}

// noteHolderLost marks the launch uncertain when its holder died with the
// agent possibly still running (marker without unit).
func (l *trackedLaunch) noteHolderLost(m session.Marker) error {
	if l.rec == nil || l.rec.Launch == nil || m.LaunchToken == "" || m.LaunchToken != l.rec.Launch.Token {
		return nil
	}
	if err := l.rec.Uncertain(l.rec.Launch.Generation, l.rec.Launch.Token, "the session holder died while its agent may still run", sessionNow()); err != nil {
		return nil
	}
	return l.save()
}

// noteSandboxReplaced applies the proof that no holder or agent survived.
func (l *trackedLaunch) noteSandboxReplaced(detail string) error {
	if l.rec != nil && l.rec.SandboxReplaced(detail, sessionNow()) {
		return l.save()
	}
	return nil
}

// plan decides the next launch with the box running and no holder unit,
// marker or tombstone. It resumes the last observed conversation (Target),
// which Allocate adopts when it differs from the assigned one; a profile that
// cannot assign an ID learns it from the agent's first report instead. It
// refuses an uncertain launch until --new-session, and it never starts a new
// conversation in place of a failed resume: it requests the same one again
// and says so on errw.
func (l *trackedLaunch) plan(opts sessionStartOptions, errw io.Writer) (string, error) {
	now := sessionNow()
	if l.rec == nil {
		l.rec = agentsession.NewRecord(l.scope)
	}
	rec := l.rec
	if rec.Launch != nil {
		switch rec.Launch.State {
		case agentsession.StateDispatching, agentsession.StateLaunched:
			// The box runs with no unit, marker or tombstone of this launch:
			// no evidence says whether it ran or still runs.
			if err := rec.Uncertain(rec.Launch.Generation, rec.Launch.Token, "no holder, marker or exit record of this launch was found", now); err != nil {
				return "", err
			}
			if err := l.save(); err != nil {
				return "", err
			}
		}
	}
	if rec.Launch != nil && rec.Launch.State == agentsession.StateUncertain {
		if !opts.NewSession {
			return "", l.uncertainError()
		}
		rec.Retire(agentsession.WhyLaunchUncertain, now)
	}
	allocated := rec.Launch != nil && rec.Launch.State == agentsession.StateAllocated
	switch {
	case allocated:
	case opts.NewSession && (rec.Assigned != nil || rec.Current != nil):
		rec.Retire(agentsession.WhyNewSession, now)
	case l.policy.Policy == agentsession.PolicyNever && (rec.Assigned != nil || rec.Current != nil):
		// never starts a new conversation on every start: the finished
		// one, and any it switched to, is retired first.
		rec.Retire(agentsession.WhyNever, now)
	}
	kind := agentsession.KindResume
	switch {
	case allocated:
		kind = rec.Launch.Kind
	case rec.Assigned == nil && len(l.profile.Session.AssignArgs) == 0:
		kind = agentsession.KindLearn
	case rec.Assigned == nil:
		kind = agentsession.KindAssign
	}
	if r := rec.Resume; !allocated && kind == agentsession.KindResume && r != nil && r.Outcome == agentsession.OutcomeFailed && rec.Launch != nil && r.Generation == rec.Launch.Generation {
		_, _ = fmt.Fprintf(errw, "warning: the previous resume of %s failed (%s); retrying — yoloctl box restart %s --new-session starts a new conversation\n", r.Requested, r.Detail, l.name)
	}
	return kind, nil
}

func (l *trackedLaunch) uncertainError() error {
	launch := l.rec.Launch
	operation := ""
	if launch.Operation != "" {
		operation = fmt.Sprintf(" (operation %s)", launch.Operation)
	}
	conversation := "conversation " + l.conversation()
	if l.conversation() == "" {
		conversation = "a new conversation"
	}
	return asUserError(fmt.Errorf("%s (generation %d) of box %s may have launched%s: %s. Inspect the box, then replace it with yoloctl box restart %s --new-session",
		conversation, launch.Generation, l.name, operation, launch.Detail, l.name))
}

// checkOwner refuses to resume a conversation that another box in this
// space may run: one with the same account and agent whose record holds the
// same ID, assigned or last observed, in a dispatched or uncertain launch, or
// launched with a live holder of that launch. An unreadable record or
// unprobeable box refuses too.
func (l *trackedLaunch) checkOwner(ctx context.Context, c *container.Client, id string) error {
	names, err := listBoxNamesFn(l.space)
	if err != nil {
		return asUserError(fmt.Errorf("ownership inventory uncertain: %w", err))
	}
	for _, other := range names {
		if other == l.name {
			continue
		}
		rec, ok, err := agentsession.Load(l.space, other)
		if err != nil {
			return asUserError(fmt.Errorf("ownership inventory uncertain: box %s (%v)", other, err))
		}
		if !ok || rec.Account != l.scope.Account || rec.Provider != l.scope.Provider || rec.Launch == nil || !namesConversation(rec, id) {
			continue
		}
		switch rec.Launch.State {
		case agentsession.StateClosed:
			continue
		case agentsession.StateLaunched:
			running, err := sessionMayRun(ctx, c, other)
			if err != nil {
				return asUserError(fmt.Errorf("ownership inventory uncertain: box %s (%v)", other, err))
			}
			if !running {
				continue
			}
			return asUserError(fmt.Errorf("conversation %s is running in box %s", id, other))
		default:
			return asUserError(fmt.Errorf("conversation %s is %s in box %s; inspect it before resuming it here", id, rec.Launch.State, other))
		}
	}
	return nil
}

// namesConversation reports whether rec's assigned or last observed
// conversation is id.
func namesConversation(rec *agentsession.Record, id string) bool {
	return (rec.Assigned != nil && rec.Assigned.ID == id) || (rec.Current != nil && rec.Current.ID == id)
}

var listBoxNamesFn = agentbox.ListNames

// launch allocates, dispatches and starts the holder for kind, then binds the
// launch to the holder whose marker carries its token. The dispatching write
// precedes StartHolderUnit.
func (l *trackedLaunch) launch(ctx context.Context, c *container.Client, cfg *agentbox.Config, kind string, start func(argv []string, token string) error) error {
	now := sessionNow()
	rec := l.rec
	var carried agentsession.Carried
	if kind == agentsession.KindResume {
		// The target, which Allocate adopts below.
		if err := l.checkOwner(ctx, c, rec.Target()); err != nil {
			return err
		}
		// Read before the new launch's observation replaces the last one.
		carried = rec.Carried(rec.Target())
	}
	// Only a closed previous launch proves its holder gone (same-token
	// tombstone or the fresh-sandbox proof); missing markers never do.
	previousClosed := rec.Launch != nil && rec.Launch.State == agentsession.StateClosed
	previousGeneration := uint64(0)
	if rec.Launch != nil {
		previousGeneration = rec.Launch.Generation
	}
	if err := rec.Allocate(kind, now); err != nil {
		return err
	}
	if err := l.save(); err != nil {
		return err
	}
	generation, token := rec.Launch.Generation, rec.Launch.Token
	env, err := l.observationEnv(ctx, c, cfg, generation, previousClosed && previousGeneration+1 == generation)
	if err != nil {
		return err
	}
	incarnation := ""
	if current := spaceenv.Current(); current != nil {
		incarnation = current.Generation
	}
	if err := rec.Dispatch(generation, token, controllerRequestID(ctx), incarnation, sessionNow()); err != nil {
		return err
	}
	if err := l.save(); err != nil {
		return err
	}
	argv, err := agentsession.Argv(l.profile, kind, l.conversation(), cfg.AgentExtraArgs)
	if err != nil {
		return err
	}
	argv = append(argv, agentsession.CarryArgs(l.profile, carried)...)
	argv = applyLaunchEnv(l.profile, argv)
	if len(env) > 0 {
		argv = append(append([]string{"env"}, env...), argv...)
	}
	startErr := start(argv, token)
	return errors.Join(startErr, l.settle(ctx, c, generation, token, startErr))
}

// settle binds or closes the dispatched launch from same-token evidence, and
// otherwise leaves it uncertain.
func (l *trackedLaunch) settle(ctx context.Context, c *container.Client, generation uint64, token string, startErr error) error {
	now := sessionNow()
	if m, ok := readVMMarkerFn(ctx, c, l.name); ok && m.LaunchToken == token {
		if err := l.rec.Bind(token, m.Identity(), now); err != nil {
			return err
		}
		return l.save()
	}
	if ts, ok := readVMTombstoneFn(ctx, c, l.name); ok && ts.LaunchToken == token {
		if err := l.rec.CloseExit(token, ts.ExitCode, ts.ExitedAt, now); err != nil {
			return err
		}
		return l.save()
	}
	detail := "the holder left no marker of this launch"
	if startErr != nil {
		detail = "starting the holder failed: " + startErr.Error()
	}
	if err := l.rec.Uncertain(generation, token, detail, now); err != nil {
		return err
	}
	return l.save()
}

// retireForNewSession replaces the box's conversation for start or restart
// --new-session once nothing can still run it: the caller holds the box
// lock and has proven a fresh sandbox or a stopped holder.
func retireForNewSession(l *agentsession.BoxLock, t *conversationTracking, errw io.Writer) error {
	rec, err := agentsession.LoadLocked(l, t.scope, sessionNow())
	if err != nil || rec == nil || (rec.Assigned == nil && rec.Current == nil) {
		return err
	}
	id := rec.Target()
	why := agentsession.WhyNewSession
	if rec.Pending() {
		why = agentsession.WhyLaunchUncertain
	}
	rec.Retire(why, sessionNow())
	if err := agentsession.Save(l, rec); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(errw, "notice: conversation %s of box %s is replaced; the next session starts a new one\n", id, l.Box())
	return nil
}

// applySessionStart runs under a start's box lock once the lifecycle start
// succeeded: fresh is the proof that the runtime was positively stopped or
// missing before it, so no holder or agent of the box survived; newSession
// then retires the conversation.
func applySessionStart(l *agentsession.BoxLock, t *conversationTracking, fresh, newSession bool, errw io.Writer) error {
	if t == nil {
		return nil
	}
	if fresh {
		rec, err := agentsession.LoadLocked(l, t.scope, sessionNow())
		if err != nil {
			return err
		}
		if rec.SandboxReplaced("the box was started from a stopped or missing runtime", sessionNow()) {
			if err := agentsession.Save(l, rec); err != nil {
				return err
			}
		}
	}
	if newSession {
		return retireForNewSession(l, t, errw)
	}
	return nil
}

// recordSessionEnded closes the tracked launch with detail once a managed
// cleanup proved that no holder or agent of it survives. A launch already
// closed, or one never dispatched, is left as it is.
func recordSessionEnded(l *agentsession.BoxLock, t *conversationTracking, detail string) error {
	rec, err := agentsession.LoadLocked(l, t.scope, sessionNow())
	if err != nil {
		return err
	}
	if rec.SandboxReplaced(detail, sessionNow()) {
		return agentsession.Save(l, rec)
	}
	return nil
}

// bindLiveLaunch settles a launch left dispatching or uncertain, for example
// by a lost reply, once its own holder is live: the marker must carry the
// launch's token. It is best effort and never blocks joining the session;
// without the space-operation lock or a pending record it does nothing, and
// it never mints a record for a session that predates tracking.
func bindLiveLaunch(ctx context.Context, c *container.Client, space, name string) {
	rec, ok, err := agentsession.Load(space, name)
	if err != nil || !ok || !rec.Pending() || rec.Launch.State == agentsession.StateAllocated || !spaceOperationHeld(ctx) {
		return
	}
	m, ok := readVMMarkerFn(ctx, c, name)
	if !ok || m.LaunchToken == "" || m.LaunchToken != rec.Launch.Token {
		return
	}
	lock, err := lockSessionBoxFn(space, name)
	if err != nil {
		return
	}
	defer lock.Release()
	current, ok, err := agentsession.Load(space, name)
	if err != nil || !ok || current.Bind(m.LaunchToken, m.Identity(), sessionNow()) != nil {
		return
	}
	_ = agentsession.Save(lock, current)
}

// sessionMayRun reports whether a session of box name may still run: a live
// holder unit, or a marker its holder left. An undecidable probe is an
// error, never "not running".
func sessionMayRun(ctx context.Context, c *container.Client, name string) (bool, error) {
	active, err := holderUnitStateFn(ctx, c, name)
	if err != nil || active {
		return active, err
	}
	_, present, err := probeVMMarkerFn(ctx, c, name)
	return present, err
}

// liveHolderError refuses --new-session while a holder is live or its
// liveness is uncertain; restart owns replacing a live conversation.
func liveHolderError(ctx context.Context, c *container.Client, name string) error {
	running, err := sessionMayRun(ctx, c, name)
	switch {
	case err != nil:
		return asUserError(fmt.Errorf("cannot tell whether a session runs in box %s (%v); nothing was changed — inspect it, or replace its conversation with yoloctl box restart %s --new-session", name, err, name))
	case running:
		return asUserError(fmt.Errorf("a session is running in box %s, or its holder left a marker; replace its conversation with yoloctl box restart %s --new-session", name, name))
	}
	return nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Agent observation of tracked launches. Each launch of a box whose container
// binds the observation root gets a fresh generation directory and
// YOLOCTL_AGENT_GEN; before the next launch is planned, the current launch's
// new reports are folded into the record under the box lock, so the next
// resume requests the conversation the agent last reported. Observation never fails or blocks a
// launch: an unsupported container or an unavailable root launches without
// the variable and records why.

// The container and filesystem steps are replaceable so tests run without a VM.
var (
	observationBoundFn = func(ctx context.Context, c *container.Client, cfg *agentbox.Config) (bool, error) {
		details, err := c.Inspect(ctx, cfg.Name)
		if err != nil {
			return false, err
		}
		return agentbox.ObservationBound(cfg, details)
	}
	prepareObservationFn = observe.Prepare
	readObservationFn    = observe.ReadWithin
	observationOwnerFn   = func() (uid, gid int, ok bool) {
		native := spaceenv.Current()
		if native == nil {
			return 0, 0, false
		}
		return native.UID, native.GID, true
	}
)

// observationViewBudget is one command's cooperative live-read budget: every
// read runs under a context ending at it, which ReadWithin checks between
// steps, so a read can overrun it by one step and a hung kernel call is not
// bounded (see ReadWithin). The budget is process-global, so it is a per-command deadline only because each
// command's views run in their own controller worker process (records are
// read only there); a long-lived caller would need its own budget. Past it, a
// view shows its live values as unavailable instead of waiting.
var observationViewBudget = 2 * time.Second

var viewReads struct {
	sync.Mutex
	start time.Time
	// parent, when set, is the caller's context the reads are canceled with.
	parent context.Context
}

// beginObservationViewBudget starts a new live-read budget under ctx, for a
// worker that reads one box at a time as separate commands would: the reads
// end at ctx's deadline when that comes first, and with its cancellation.
func beginObservationViewBudget(ctx context.Context) {
	start := time.Now()
	if limit, ok := ctx.Deadline(); ok && limit.Before(start.Add(observationViewBudget)) {
		start = limit.Add(-observationViewBudget)
	}
	viewReads.Lock()
	viewReads.start, viewReads.parent = start, ctx
	viewReads.Unlock()
}

// resetObservationViewBudget forgets the budget and its context, so the next
// read starts a budget of its own.
func resetObservationViewBudget() {
	viewReads.Lock()
	viewReads.start, viewReads.parent = time.Time{}, nil
	viewReads.Unlock()
}

// The controller reads current reports for views; on a host build the owner
// is unknown and views show only the persisted evidence.
func init() {
	agentsession.ReadCurrentReports = func(box string, gen uint64, cur observe.Cursor) (observe.Result, error) {
		viewReads.Lock()
		if viewReads.start.IsZero() {
			viewReads.start = time.Now()
		}
		deadline := viewReads.start.Add(observationViewBudget)
		parent := viewReads.parent
		viewReads.Unlock()
		if parent == nil {
			parent = context.Background()
		}
		if !time.Now().Before(deadline) {
			return observe.Result{}, errors.New("skipped: this command's live-read budget is spent")
		}
		dir, err := layout.AgentObserveDir(box)
		uid, _, ok := observationOwnerFn()
		if err != nil || !ok {
			return observe.Result{}, observe.ErrUnavailable
		}
		ctx, cancel := context.WithDeadline(parent, deadline)
		defer cancel()
		return readObservationFn(ctx, dir, gen, cur, uid, sessionNow())
	}
}

// foldLiveLaunch applies a live, launched session's new reports under the
// box lock when the command holds the space operation, as the holder bind
// does. A fast-path attach without it changes nothing; views still show the
// reports read-only.
func foldLiveLaunch(ctx context.Context, space, name string) {
	if !spaceOperationHeld(ctx) {
		return
	}
	rec, ok, err := agentsession.Load(space, name)
	if err != nil || !ok || rec.Launch == nil || rec.Launch.State != agentsession.StateLaunched || !rec.Observable(rec.Launch.Generation) {
		return
	}
	lock, err := lockSessionBoxFn(space, name)
	if err != nil {
		return
	}
	defer lock.Release()
	current, ok, err := agentsession.Load(space, name)
	if err != nil || !ok || current.Launch == nil || current.Launch.Generation != rec.Launch.Generation || current.Launch.State != agentsession.StateLaunched {
		return
	}
	gen := current.Launch.Generation
	dir, err := layout.AgentObserveDir(name)
	uid, _, owned := observationOwnerFn()
	if err != nil || !owned {
		return
	}
	res, err := readObservationFn(ctx, dir, gen, current.ObservationCursor(gen), uid, sessionNow())
	if err != nil || !current.Apply(res) {
		return
	}
	_ = agentsession.Save(lock, current)
}

// unsupportedObservation is the note for a container created before the
// observation bind. The profile requires its reporter (session.observe.push),
// so the box's next recreate brings both; nothing changes a running box.
func unsupportedObservation(cfg *agentbox.Config) string {
	return "the container predates agent observation; it starts with the box's next yoloctl box recreate " + cfg.Name
}

// observationEnv prepares launch generation gen and returns its environment
// for the launch command. previousGone must be the record's own evidence that
// the previous launch ended (it was closed by its same-token tombstone or the
// fresh-sandbox proof); only then may Prepare drop that launch's directory to
// free space. A missing report is optional, but a bound container whose
// capped root cannot be verified fails closed: the error comes before the
// dispatching write, so no holder starts and the allocated launch is reused.
func (l *trackedLaunch) observationEnv(ctx context.Context, c *container.Client, cfg *agentbox.Config, gen uint64, previousGone bool) ([]string, error) {
	bound, err := observationBoundFn(ctx, c, cfg)
	switch {
	case errors.Is(err, agentbox.ErrObservationBind):
		return nil, fmt.Errorf("box %s: %w; nothing was launched — inspect it, or recover with yoloctl box recreate %s", cfg.Name, err, cfg.Name)
	case err != nil:
		l.rec.BeginObservation(gen, observe.HealthUnavailable, "cannot inspect the container: "+err.Error())
		return nil, nil
	case !bound:
		l.rec.BeginObservation(gen, observe.HealthUnsupported, unsupportedObservation(cfg))
		return nil, nil
	}
	dir, err := layout.AgentObserveDir(cfg.Name)
	uid, gid, ok := observationOwnerFn()
	if err != nil || !ok {
		l.rec.BeginObservation(gen, observe.HealthUnavailable, "no observation root for this box")
		return nil, nil
	}
	dropped, err := prepareObservationFn(dir, gen, uid, gid, previousGone)
	if errors.Is(err, observe.ErrRootUnverified) {
		return nil, fmt.Errorf("box %s binds an agent observation root that cannot be verified (%v); nothing was launched — inspect it, or recover with yoloctl box recreate %s", cfg.Name, err, cfg.Name)
	}
	if err != nil {
		l.rec.BeginObservation(gen, observe.HealthUnavailable, err.Error())
		return nil, nil
	}
	detail := ""
	if dropped {
		detail = "the previous launch's unread reports were discarded to free space (a gap)"
	}
	l.rec.BeginObservation(gen, observe.HealthNoReport, detail)
	return []string{observe.EnvGeneration + "=" + strconv.FormatUint(gen, 10)}, nil
}

func sourceLabel(source string) string {
	if source == "" {
		return "unlabelled start"
	}
	return source
}

// observeCurrentLaunch folds the current launch's new reports into the record
// before the next launch is planned. A box without a readable root has
// nothing to fold; that never stops the start.
func (l *trackedLaunch) observeCurrentLaunch(ctx context.Context, cfg *agentbox.Config) error {
	rec := l.rec
	if rec == nil || rec.Launch == nil || !rec.Observable(rec.Launch.Generation) {
		return nil
	}
	dir, err := layout.AgentObserveDir(cfg.Name)
	uid, _, ok := observationOwnerFn()
	if err != nil || !ok {
		return nil
	}
	gen := rec.Launch.Generation
	res, err := readObservationFn(ctx, dir, gen, rec.ObservationCursor(gen), uid, sessionNow())
	if err != nil {
		return nil
	}
	// Apply first: only a read that found no main-session start can settle
	// a nonzero resume exit as failed.
	changed := rec.Apply(res)
	if !rec.SettleResume() && !changed {
		return nil
	}
	return l.save()
}

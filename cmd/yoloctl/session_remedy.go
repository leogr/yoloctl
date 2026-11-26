package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/session"
)

// A session started by another yoloctl build keeps running after an update,
// but this build cannot attach to it (session.ErrBuildMismatch). Its box
// needs a restart on this build, or an explicit recreation when its
// declaration also drifted in a way only a new container applies, which a
// restart refuses. Only a full drift resolution of the declared box decides
// which; without one, the guidance says so and points at box inspect. Nothing
// here restarts or recreates anything.

type sessionRemedy int

const (
	remedyUnknown sessionRemedy = iota
	remedyRestart
	remedyRecreate
)

// staleSessionInspectTimeout bounds only refuseRecreateBeforeImageWork, an
// optional early refusal whose unreadable evidence decides nothing: the later
// authoritative check still runs, so giving up there fails no command.
var staleSessionInspectTimeout = 30 * time.Second

// driftViewBudget is the box.drift view's one partial-data minute. Rows
// resolved by then keep their result; every other box reports its evidence as
// unavailable. The caller's own cancellation still ends the view.
var driftViewBudget = time.Minute

// ownedSetupGuardFailSafe bounds the fresh-session image guard only in
// controller-owned attach setup: that setup can outlive its viewer while it
// holds the native space lock and, for a tracked box, the box lock, so nothing
// else could end a silent resolution there.
var ownedSetupGuardFailSafe = 2 * time.Minute

// sessionRemedyOf classifies one full drift resolution. The reason names
// the recreation drift, or why neither remedy could be proved.
func sessionRemedyOf(b boxstate.Box) (sessionRemedy, string) {
	switch b.Drift {
	case boxstate.DriftNeedsRecreate:
		for _, r := range b.Reasons {
			if r.Code == boxstate.ReasonDriftNeedsRecreate && r.Message != "" {
				return remedyRecreate, r.Message
			}
		}
		return remedyRecreate, "drift that only a new container applies"
	case boxstate.DriftInSync, boxstate.DriftNeedsRestart, boxstate.DriftLiveOnly:
		return remedyRestart, ""
	case boxstate.DriftError:
		return remedyUnknown, b.DriftError
	}
	return remedyUnknown, "its drift is " + string(b.Drift)
}

// resolveStaleSessionsFn is the full, read-only drift resolution of only the
// named boxes: one inventory of the space under the caller's context, with one
// result per distinct name in order. each, when set, receives every box as
// soon as its own resolution ends (boxstate.Resolver.Each).
var resolveStaleSessionsFn = func(ctx context.Context, space string, names []string, each func(context.Context, boxstate.Box)) ([]boxstate.Box, error) {
	r := newBoxStateResolver()
	r.Each = each
	return r.ResolveNamed(ctx, space, names, boxstate.FullDiff)
}

// staleSessionError replaces the fixed advice of a holder of another build
// and still matches session.ErrBuildMismatch.
type staleSessionError struct {
	msg   string
	cause error
}

func (e *staleSessionError) Error() string { return e.msg }
func (e *staleSessionError) Unwrap() error { return e.cause }

// staleSessionGuidance turns an attach refused by a holder of another build
// into the remedy this build can prove for the box. Any other outcome passes
// through unchanged.
func staleSessionGuidance(ctx context.Context, space, name string, err error) error {
	if !errors.Is(err, session.ErrBuildMismatch) {
		return err
	}
	var b boxstate.Box
	remedy, reason := remedyUnknown, ""
	if boxes, rerr := resolveStaleSessionsFn(ctx, space, []string{name}, nil); rerr != nil {
		reason = rerr.Error()
	} else {
		b = boxes[0]
		remedy, reason = sessionRemedyOf(b)
	}
	return &staleSessionError{msg: staleSessionMessage(space, name, remedy, reason, conversationNote(b)), cause: err}
}

// conversationNote says what an agent box's next session does with its
// conversation after a restart or recreation, both of which keep the
// conversation record; "" for a box without an agent conversation.
func conversationNote(b boxstate.Box) string {
	if b.Config == nil || !b.Config.HasAgent() {
		return ""
	}
	return restartConversationNote(b.Conversation)
}

func staleSessionMessage(space, name string, remedy sessionRemedy, reason, conversation string) string {
	const lead = "this session runs another yoloctl build, which this build cannot attach to"
	note := ""
	if conversation != "" {
		note = "; " + conversation
	}
	switch remedy {
	case remedyRestart:
		return fmt.Sprintf("%s: restart its box on this build with yoloctl box restart %s --space %s --no-attach, then attach again. The restart ends the agent's current process%s", lead, name, space, note)
	case remedyRecreate:
		return fmt.Sprintf("%s, and box %q also requires recreation (%s), which a restart refuses: inspect with yoloctl box inspect %s --space %s, then explicitly use yoloctl box recreate %s --space %s --no-attach and attach again. Recreation ends the agent's current process and replaces its container: the workdir and the account's native state are kept, other files in the container are lost%s",
			lead, name, reason, name, space, name, space, note)
	}
	return fmt.Sprintf("%s; whether box %q needs a restart or a recreation could not be determined (%s): inspect it with yoloctl box inspect %s --space %s", lead, name, reason, name, space)
}

// boxDriftMethod is the narrow read behind the session offer after a
// controller update: one full drift resolution of only the named boxes,
// which a periodic inventory never performs.
const boxDriftMethod = "box.drift"

// maxDriftBoxes bounds one box.drift request.
const maxDriftBoxes = 64

// boxDriftView is one box of a box.drift reply: its resolved drift, or the
// resolution's error.
type boxDriftView struct {
	Name       string              `json:"name"`
	Drift      boxstate.DriftState `json:"drift,omitempty"`
	Reasons    []boxstate.Reason   `json:"reasons,omitempty"`
	DriftError string              `json:"drift_error,omitempty"`
	Error      string              `json:"error,omitempty"`
	// LegacyRuntime: the box runs the pre-native runtime. Its restart stops
	// it and refuses with the recreation remedy; it is never set aside.
	LegacyRuntime bool           `json:"legacy_runtime,omitempty"`
	Upgrade       *upgradeTarget `json:"upgrade,omitempty"`
}

func collectBoxDrift(ctx context.Context, space string, names []string) ([]boxDriftView, error) {
	return collectBoxDriftView(ctx, space, names, false)
}

// collectBoxDriftView reads both box.drift views (the post-update restart offer
// and, with upgrade, space upgrade) within one partial-data minute: a box not
// resolved by then reports its evidence as unavailable, the others keep their
// rows. The caller's cancellation ends the view with its error.
func collectBoxDriftView(parent context.Context, space string, names []string, upgrade bool) ([]boxDriftView, error) {
	if len(names) == 0 || len(names) > maxDriftBoxes {
		return nil, fmt.Errorf("box.drift takes 1 to %d boxes", maxDriftBoxes)
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if err := validateAgentboxName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("box.drift names box %s twice", name)
		}
		seen[name] = true
	}
	ctx, cancel := context.WithTimeout(parent, driftViewBudget)
	defer cancel()
	// The upgrade view reads each box's target as soon as that box's own
	// drift is resolved, on the same shared inventory, so a later slow box
	// cannot use the minute up before a healthy box's target is read.
	var targets map[string]upgradeTargetRead
	var each func(context.Context, boxstate.Box)
	if upgrade {
		targets = map[string]upgradeTargetRead{}
		each = func(ctx context.Context, b boxstate.Box) {
			if upgradeTargetWanted(b) {
				target, earlierHelper, err := readUpgradeTargetFn(ctx, b)
				targets[b.Name] = upgradeTargetRead{target: target, earlierHelper: earlierHelper, err: err}
			}
		}
	}
	boxes, err := resolveStaleSessionsFn(ctx, space, names, each)
	view := make([]boxDriftView, len(names))
	for i, name := range names {
		view[i].Name = name
		switch {
		case err != nil:
			view[i].Error = err.Error()
		case i < len(boxes) && boxes[i].Name == name:
			view[i].Drift, view[i].Reasons, view[i].DriftError = boxes[i].Drift, boxes[i].Reasons, boxes[i].DriftError
			view[i].LegacyRuntime = boxes[i].LegacyRuntime
			if b := boxes[i]; upgrade && upgradeTargetWanted(b) {
				read, ok := targets[b.Name]
				if !ok {
					read.err = errors.New("upgrade target not read")
				}
				target, earlierHelper, targetErr := read.target, read.earlierHelper, read.err
				view[i].Upgrade = target
				if targetErr != nil {
					view[i].Error = targetErr.Error()
				} else if earlierHelper {
					switch view[i].Drift {
					case boxstate.DriftInSync, boxstate.DriftNeedsRestart, boxstate.DriftLiveOnly, boxstate.DriftNeedsRecreate:
						view[i].Drift = boxstate.DriftNeedsRecreate
						view[i].Reasons = slices.Concat(view[i].Reasons, []boxstate.Reason{{
							Code: boxstate.ReasonHelperRecreate, Message: "box runs the OpenShell helper of an earlier yoloctl build",
						}})
					}
				}
			}
		default:
			view[i].Error = "not resolved"
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if expired := ctx.Err(); expired != nil {
		// The minute ended: a box whose drift was not determined by then shows
		// its own drift error, else the expiry, as its unavailable evidence.
		for i := range view {
			if view[i].Error == "" && !driftDetermined(view[i].Drift) {
				view[i].Error = cmp.Or(view[i].DriftError, expired.Error())
			}
		}
	}
	return view, nil
}

// upgradeTargetRead is one box's upgrade target, read right after its drift.
type upgradeTargetRead struct {
	target        *upgradeTarget
	earlierHelper bool
	err           error
}

// upgradeTargetWanted: only a declared box with a container has a target.
func upgradeTargetWanted(b boxstate.Box) bool {
	return b.Declaration == boxstate.DeclarationDeclared && (b.Runtime == boxstate.RuntimeRunning || b.Runtime == boxstate.RuntimeStopped)
}

// driftDetermined reports a drift that a completed resolution established.
func driftDetermined(d boxstate.DriftState) bool {
	switch d {
	case boxstate.DriftInSync, boxstate.DriftNeedsRestart, boxstate.DriftLiveOnly, boxstate.DriftNeedsRecreate, boxstate.DriftNeedsCreate:
		return true
	}
	return false
}

// remedy classifies one box.drift entry.
func (v boxDriftView) remedy() (sessionRemedy, string) {
	if v.Error != "" {
		return remedyUnknown, v.Error
	}
	return sessionRemedyOf(boxstate.Box{Drift: v.Drift, Reasons: v.Reasons, DriftError: v.DriftError})
}

// New holders must not run the new launch contract in a container whose recipe
// image is still old. Live-holder attachment never calls this guard. FullDiff
// owns comparison/normalization; unavailable evidence is not called drift.
func guardFreshSessionImage(ctx context.Context, space, name string) error {
	// An ordinary start follows its caller. Owned attach setup gets one
	// fail-safe: on expiry the evidence is unavailable, so the guard refuses
	// and its callers release their locks without launching.
	if ctx.Value(controllerOwnedSetupKey{}) == true {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ownedSetupGuardFailSafe)
		defer cancel()
	}
	b, err := newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.FullDiff)
	if err != nil {
		return fmt.Errorf("cannot check the image for a new session of box %s: %w", name, err)
	}
	switch {
	case b.Runtime == boxstate.RuntimeUnavailable || b.Drift == boxstate.DriftUnknown || b.Drift == boxstate.DriftError:
		return fmt.Errorf("cannot check whether box %s needs a new image (%s); inspect with yoloctl box inspect %s --space %s; no session was started", name, boxStateReasonField(b), name, space)
	case b.DiffPlan != nil && b.DiffPlan.ImageRecreateReason != "":
		return asUserError(fmt.Errorf("box %q needs recreation before a new agent session (%s). Inspect with yoloctl box inspect %s --space %s, then explicitly use yoloctl box recreate %s --space %s --no-attach and attach again. Recreation ends its sessions and replaces its container: the workdir and the account's native state are kept, other files in the container are lost", name, b.DiffPlan.ImageRecreateReason, name, space, name, space))
	}
	return nil
}

// refuseRecreateBeforeImageWork refuses recreate-only drift of an existing
// container before any image build, with the start/restart refusal text. It
// reads through the shared box-state resolver (the same Diff as box diff and
// agentbox.CheckStart). Evidence it cannot read decides nothing: the later
// CheckStart after preparation stays the authoritative guard.
func refuseRecreateBeforeImageWork(ctx context.Context, space string, cfg *agentbox.Config) error {
	ctx, cancel := context.WithTimeout(ctx, staleSessionInspectTimeout)
	defer cancel()
	b, err := newBoxStateResolver().ResolveOne(ctx, space, cfg.Name, boxstate.FullDiff)
	if err != nil || b.DiffPlan == nil || !b.DiffPlan.BlocksStart() {
		return nil
	}
	return asUserError(agentbox.StartRecreateError(cfg, b.DiffPlan.RecreateReason))
}

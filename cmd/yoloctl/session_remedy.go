package main

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// staleSessionInspectTimeout bounds one request's whole drift resolution.
var staleSessionInspectTimeout = 30 * time.Second

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
// named boxes: one inventory of the space and one bound for the whole batch,
// with one result per distinct name in order.
var resolveStaleSessionsFn = func(ctx context.Context, space string, names []string) ([]boxstate.Box, error) {
	ctx, cancel := context.WithTimeout(ctx, staleSessionInspectTimeout)
	defer cancel()
	return newBoxStateResolver().ResolveNamed(ctx, space, names, boxstate.FullDiff)
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
	if boxes, rerr := resolveStaleSessionsFn(ctx, space, []string{name}); rerr != nil {
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
}

func collectBoxDrift(ctx context.Context, space string, names []string) ([]boxDriftView, error) {
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
	boxes, err := resolveStaleSessionsFn(ctx, space, names)
	view := make([]boxDriftView, len(names))
	for i, name := range names {
		view[i].Name = name
		switch {
		case err != nil:
			view[i].Error = err.Error()
		case i < len(boxes) && boxes[i].Name == name:
			view[i].Drift, view[i].Reasons, view[i].DriftError = boxes[i].Drift, boxes[i].Reasons, boxes[i].DriftError
		default:
			view[i].Error = "not resolved"
		}
	}
	return view, nil
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
	ctx, cancel := context.WithTimeout(ctx, staleSessionInspectTimeout)
	defer cancel()
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

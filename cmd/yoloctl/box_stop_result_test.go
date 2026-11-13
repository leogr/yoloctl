package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

const (
	firstID       = "1111111111111111111111111111111111111111111111111111111111111111"
	replacementID = "2222222222222222222222222222222222222222222222222222222222222222"
)

// fakeStop is a scripted box: its container's state before and after, the
// steps that fail, and the calls made.
type fakeStop struct {
	present, running, native bool
	after                    bool // running at the final read
	replaced                 bool // later reads see another container
	readErr, finalErr        error
	fail                     map[string]error
	calls                    []string
	stopped                  []string // the IDs the engine stop targeted
	reads                    int
}

func (f *fakeStop) steps() boxStopSteps {
	step := func(name string) func(context.Context) error {
		return func(context.Context) error { f.calls = append(f.calls, name); return f.fail[name] }
	}
	return boxStopSteps{
		read: func(context.Context) (boxTarget, error) {
			f.reads++
			if f.reads == 1 {
				return boxTarget{ID: firstID, Present: f.present, Running: f.running, Native: f.native}, f.readErr
			}
			id := firstID
			if f.replaced {
				id = replacementID
			}
			return boxTarget{ID: id, Present: f.present, Running: f.after, Native: f.native}, f.finalErr
		},
		holder:       func(ctx context.Context, _ bool) error { return step("holder")(ctx) },
		listedHolder: step("listedHolder"),
		forwards:     func(ctx context.Context, _ bool) error { return step("forwards")(ctx) },
		stop: func(ctx context.Context, t boxTarget) error {
			f.stopped = append(f.stopped, t.ID)
			return step("stop")(ctx)
		},
		endpoint:       step("endpoint"),
		sessionState:   step("sessionState"),
		reapNamespaces: step("reap"),
	}
}

func TestBoxStopTakesItsResultFromTheFinalRead(t *testing.T) {
	uncertain := &agentbox.NativeEffectsUncertain{Cause: errors.New("lost reply")}
	for _, native := range []bool{false, true} {
		cleanup := []string{"holder", "forwards", "sessionState", "reap"}
		if native {
			cleanup = []string{"holder", "forwards", "endpoint", "sessionState"}
		}
		alone := map[string][]string{"all": cleanup}
		for _, name := range cleanup {
			alone[name] = []string{name}
		}
		for label, failing := range alone {
			for _, lost := range []bool{false, true} {
				f := &fakeStop{present: true, running: true, native: native, fail: map[string]error{}}
				for _, name := range failing {
					f.fail[name] = errors.New(name + " refused")
					if lost {
						f.fail[name] = uncertain // a lost reply is a leftover too
					}
				}
				result, err := stopAgentboxForResult(t.Context(), f.steps(), false)
				if err != nil || result.Running || len(result.Leftovers) != len(failing) {
					t.Fatalf("native=%v %s lost=%v: %+v %v", native, label, lost, result, err)
				}
				if n := countCalls(f.calls, "stop"); n != 1 {
					t.Fatalf("native=%v %s: engine stop invoked %d times", native, label, n)
				}
				if native && slices.Contains(f.calls, "reap") || !native && slices.Contains(f.calls, "endpoint") {
					t.Fatalf("native=%v: wrong route steps %v", native, f.calls)
				}
			}
		}
	}
}

func countCalls(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

func TestBoxStopEngineReplyNeverDecides(t *testing.T) {
	// The engine stop failed but the container is stopped: stopped.
	f := &fakeStop{present: true, running: true, after: false, fail: map[string]error{"stop": &agentbox.NativeEffectsUncertain{Cause: errors.New("no ack")}}}
	result, err := stopAgentboxForResult(t.Context(), f.steps(), false)
	if err != nil || result.Running {
		t.Fatalf("%+v %v", result, err)
	}
	// The engine stop succeeded but the container runs: still running, and
	// no uncertainty marker leaves the policy.
	f = &fakeStop{present: true, running: true, after: true, fail: map[string]error{"stop": &agentbox.NativeEffectsUncertain{Cause: errors.New("no ack")}, "forwards": errors.New("unit busy")}}
	err = runBoxStop(t.Context(), "coder", f.steps())
	var marker *agentbox.NativeEffectsUncertain
	if err == nil || !strings.Contains(err.Error(), "stop failed: box coder is still running") || !strings.Contains(err.Error(), "container stop: ") ||
		!strings.Contains(err.Error(), "port forwards: unit busy") || errors.As(err, &marker) {
		t.Fatalf("still running: %v", err)
	}
}

// The engine stop targets the first read's container; a replacement that
// the later reads see is never stopped, and the final read decides.
func TestBoxStopTargetsTheFirstReadContainer(t *testing.T) {
	f := &fakeStop{present: true, running: true, after: true, replaced: true}
	err := runBoxStop(t.Context(), "coder", f.steps())
	if !slices.Equal(f.stopped, []string{firstID}) || err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("stopped %v: %v", f.stopped, err)
	}
}

// A stop step that did its effect but left disk-dependent work (storage
// verification failed) reports that work as a leftover, not as a failure.
func TestBoxStopStorageFailureLeavesDiskWorkAsLeftover(t *testing.T) {
	f := &fakeStop{present: true, running: true, native: true, fail: map[string]error{
		"stop":     &agentbox.ResultLeftover{What: "network namespace retirement", Cause: errors.New("storage verification failed: disk gone")},
		"endpoint": errors.New("storage verification failed: disk gone"),
	}}
	result, err := stopAgentboxForResult(t.Context(), f.steps(), false)
	if err != nil || result.Running || len(result.Notes) != 0 || countCalls(f.calls, "stop") != 1 ||
		!slices.ContainsFunc(result.Leftovers, func(l string) bool { return strings.HasPrefix(l, "network namespace retirement: storage") }) ||
		!slices.ContainsFunc(result.Leftovers, func(l string) bool { return strings.HasPrefix(l, "endpoint socket: storage") }) {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestBoxStopReadFailures(t *testing.T) {
	// The first read fails: nothing is changed.
	f := &fakeStop{readErr: errors.New("engine down")}
	if _, err := stopAgentboxForResult(t.Context(), f.steps(), false); err == nil || len(f.calls) != 0 {
		t.Fatalf("first read: %v calls %v", err, f.calls)
	}
	// The final read fails: the command fails with it and never says stopped.
	f = &fakeStop{present: true, running: true, finalErr: errors.New("engine down")}
	err := runBoxStop(t.Context(), "coder", f.steps())
	if err == nil || !strings.Contains(err.Error(), "engine down") || strings.Contains(err.Error(), "stopped") {
		t.Fatalf("final read: %v", err)
	}
}

func TestBoxStopOfAnAbsentOrExitedContainer(t *testing.T) {
	// Absent: the unit and session cleanup still run; nothing is stopped
	// and the endpoint, which nothing owns without a container, is kept.
	f := &fakeStop{present: false, native: true}
	result, err := stopAgentboxForResult(t.Context(), f.steps(), false)
	if err != nil || result.Running || slices.Contains(f.calls, "stop") || slices.Contains(f.calls, "endpoint") ||
		!slices.Equal(f.calls, []string{"holder", "forwards", "sessionState"}) {
		t.Fatalf("absent: %+v %v %v", result, err, f.calls)
	}
	// Exited: no engine stop; the endpoint socket is removed under its checks.
	f = &fakeStop{present: true, running: false, native: true}
	result, err = stopAgentboxForResult(t.Context(), f.steps(), false)
	if err != nil || result.Running || slices.Contains(f.calls, "stop") || !slices.Contains(f.calls, "endpoint") {
		t.Fatalf("exited: %+v %v %v", result, err, f.calls)
	}
}

func TestBoxStartRemovesLeftoversOfAStoppedBoxOnly(t *testing.T) {
	// Stopped native box: the listed holder and the endpoint socket go first.
	f := &fakeStop{present: true, native: true}
	if err := removeStartLeftovers(t.Context(), f.steps()); err != nil || !slices.Equal(f.calls, []string{"listedHolder", "endpoint"}) {
		t.Fatalf("stopped: %v %v", err, f.calls)
	}
	// Generic route: no endpoint step.
	f = &fakeStop{present: true}
	if err := removeStartLeftovers(t.Context(), f.steps()); err != nil || !slices.Equal(f.calls, []string{"listedHolder"}) {
		t.Fatalf("generic: %v %v", err, f.calls)
	}
	// An unremovable leftover fails the start before any engine start.
	for _, failing := range []string{"listedHolder", "endpoint"} {
		f = &fakeStop{present: true, native: true, fail: map[string]error{failing: errors.New("busy")}}
		if err := removeStartLeftovers(t.Context(), f.steps()); err == nil || !strings.HasPrefix(err.Error(), "start failed: ") || !strings.Contains(err.Error(), "busy") {
			t.Fatalf("%s: %v", failing, err)
		}
	}
	// Running or absent: the start's own checks decide; nothing is removed.
	for _, f := range []*fakeStop{{present: true, running: true, native: true}, {native: true}} {
		if err := removeStartLeftovers(t.Context(), f.steps()); err != nil || len(f.calls) != 0 {
			t.Fatalf("%+v: %v %v", f, err, f.calls)
		}
	}
	f = &fakeStop{readErr: errors.New("engine down")}
	if err := removeStartLeftovers(t.Context(), f.steps()); err == nil || len(f.calls) != 0 {
		t.Fatalf("read: %v %v", err, f.calls)
	}
}

// attemptCalling returns a start attempt that marks the given engine calls,
// in order, as the client does at its engine boundary (covered with the real
// client in internal/container and internal/agentbox), then returns err.
func attemptCalling(err error, calls ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, call := range calls {
			recorder := container.EngineCallsFrom(ctx)
			switch call {
			case "start":
				recorder.MarkStart()
			case "stop":
				recorder.MarkStop()
			}
		}
		return err
	}
}

func TestBoxStartSettlesFromAReadAndRollsBackWithBoxStop(t *testing.T) {
	marker := &agentbox.NativeEffectsUncertain{Cause: errors.New("no ack")}
	// No engine start invoked (a pre-engine native refusal, a refused
	// creation): the box is as it was; the error stands, no read, no stop.
	refused := errors.New("native Start requires an independently retired SSH endpoint")
	f := &fakeStop{present: true}
	if err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(refused, "stop")); !errors.Is(err, refused) || f.reads != 0 {
		t.Fatalf("not started: %v reads %d", err, f.reads)
	}
	// Started and running at the read: success, whatever replies were lost.
	f = &fakeStop{present: true, running: true}
	if err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(nil, "start")); err != nil || len(f.calls) != 0 {
		t.Fatalf("running: %v %v", err, f.calls)
	}
	// Engine start, activation or readiness failed, or a missing container's
	// creation failed after its start, or autoupdate failed: one rollback
	// stop of the first read's container, then stopped.
	for _, cause := range []error{marker, errors.New("readiness"), errors.New("autoupdate coder: npm failed")} {
		f = &fakeStop{present: true, running: true}
		err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(cause, "start"))
		var got *agentbox.NativeEffectsUncertain
		if err == nil || !strings.HasPrefix(err.Error(), "start failed: ") || strings.Contains(err.Error(), "still running") ||
			errors.As(err, &got) || !slices.Equal(f.stopped, []string{firstID}) {
			t.Fatalf("%v: %v stopped %v", cause, err, f.stopped)
		}
	}
	// The start's own cleanup already invoked an engine stop and the box
	// still runs: no second stop; the still-running form, with its notes.
	f = &fakeStop{present: true, running: true, after: true}
	err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(errors.New("readiness"), "stop", "start", "stop"))
	if err == nil || !strings.Contains(err.Error(), "start failed: readiness; box coder is still running") ||
		len(f.stopped) != 0 || !strings.Contains(err.Error(), "already attempted") {
		t.Fatalf("inner cleanup stopped: %v stopped %v", err, f.stopped)
	}
	// A stop before the start (a previous generation's) does not count.
	f = &fakeStop{present: true, running: true}
	if err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(errors.New("readiness"), "stop", "start")); err == nil || len(f.stopped) != 1 {
		t.Fatalf("previous stop counted: %v stopped %v", err, f.stopped)
	}
	// No error but not running at the read: rolled back too, without a stop.
	f = &fakeStop{present: true}
	if err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(nil, "start")); err == nil ||
		!strings.Contains(err.Error(), "not running after the start") || slices.Contains(f.calls, "stop") || !slices.Contains(f.calls, "holder") {
		t.Fatalf("not running: %v %v", err, f.calls)
	}
	// The rollback leaves something behind: named in the failed start.
	f = &fakeStop{present: true, running: true, fail: map[string]error{"forwards": errors.New("unit busy")}}
	if err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(errors.New("boom"), "start")); err == nil ||
		!strings.Contains(err.Error(), "port forwards: unit busy") {
		t.Fatalf("rollback leftovers: %v", err)
	}
	// The rollback cannot read the box at its end: said so, never "stopped".
	f = &fakeStop{present: true, running: true, finalErr: errors.New("engine down")}
	if err := runStartForResult(t.Context(), "coder", f.steps(), attemptCalling(errors.New("boom"), "start")); err == nil ||
		!strings.Contains(err.Error(), "the rollback could not read the box") {
		t.Fatalf("rollback read: %v", err)
	}
}

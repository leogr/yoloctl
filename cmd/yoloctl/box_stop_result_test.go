package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

// fakeStop is a scripted box: its container's state before and after, the
// steps that fail, and the calls made.
type fakeStop struct {
	present, running, native bool
	after                    bool // running at the final read
	readErr, finalErr        error
	fail                     map[string]error
	calls                    []string
	reads                    int
}

func (f *fakeStop) steps() boxStopSteps {
	step := func(name string) func(context.Context) error {
		return func(context.Context) error { f.calls = append(f.calls, name); return f.fail[name] }
	}
	return boxStopSteps{
		read: func(context.Context) (bool, bool, bool, error) {
			f.reads++
			if f.reads == 1 {
				return f.present, f.running, f.native, f.readErr
			}
			return f.present, f.after, f.native, f.finalErr
		},
		holder:         func(ctx context.Context, _ bool) error { return step("holder")(ctx) },
		listedHolder:   step("listedHolder"),
		forwards:       func(ctx context.Context, _ bool) error { return step("forwards")(ctx) },
		stop:           step("stop"),
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
				result, err := stopAgentboxForResult(t.Context(), f.steps())
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
	result, err := stopAgentboxForResult(t.Context(), f.steps())
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

func TestBoxStopReadFailures(t *testing.T) {
	// The first read fails: nothing is changed.
	f := &fakeStop{readErr: errors.New("engine down")}
	if _, err := stopAgentboxForResult(t.Context(), f.steps()); err == nil || len(f.calls) != 0 {
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
	result, err := stopAgentboxForResult(t.Context(), f.steps())
	if err != nil || result.Running || slices.Contains(f.calls, "stop") || slices.Contains(f.calls, "endpoint") ||
		!slices.Equal(f.calls, []string{"holder", "forwards", "sessionState"}) {
		t.Fatalf("absent: %+v %v %v", result, err, f.calls)
	}
	// Exited: no engine stop; the endpoint socket is removed under its checks.
	f = &fakeStop{present: true, running: false, native: true}
	result, err = stopAgentboxForResult(t.Context(), f.steps())
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

func TestBoxStartSettlesFromAReadAndRollsBackWithBoxStop(t *testing.T) {
	marker := &agentbox.NativeEffectsUncertain{Cause: errors.New("no ack")}
	// No engine start submitted: the box is still stopped; the error stands.
	refused := errors.New("requires recreation")
	f := &fakeStop{present: true}
	if err := settleStartForResult(t.Context(), "coder", refused, false, f.steps()); !errors.Is(err, refused) || f.reads != 0 {
		t.Fatalf("not submitted: %v reads %d", err, f.reads)
	}
	// Started and running at the read: success, whatever replies were lost.
	f = &fakeStop{present: true, running: true}
	if err := settleStartForResult(t.Context(), "coder", nil, true, f.steps()); err != nil || len(f.calls) != 0 {
		t.Fatalf("running: %v %v", err, f.calls)
	}
	// Engine start or activation failed: one rollback stop, then stopped.
	f = &fakeStop{present: true, running: true}
	err := settleStartForResult(t.Context(), "coder", marker, true, f.steps())
	var got *agentbox.NativeEffectsUncertain
	if err == nil || !strings.HasPrefix(err.Error(), "start failed: ") || strings.Contains(err.Error(), "still running") ||
		errors.As(err, &got) || countCalls(f.calls, "stop") != 1 {
		t.Fatalf("rollback: %v %v", err, f.calls)
	}
	// The rollback leaves it running: said so.
	f = &fakeStop{present: true, running: true, after: true}
	if err := settleStartForResult(t.Context(), "coder", errors.New("readiness"), true, f.steps()); err == nil ||
		!strings.Contains(err.Error(), "start failed: readiness; box coder is still running") {
		t.Fatalf("still running: %v", err)
	}
	// No error but not running at the read: rolled back too, without a stop.
	f = &fakeStop{present: true}
	if err := settleStartForResult(t.Context(), "coder", nil, true, f.steps()); err == nil ||
		!strings.Contains(err.Error(), "not running after the start") || slices.Contains(f.calls, "stop") || !slices.Contains(f.calls, "holder") {
		t.Fatalf("not running: %v %v", err, f.calls)
	}
	// The rollback cannot read the box at its end: said so, never "stopped".
	f = &fakeStop{present: true, running: true, finalErr: errors.New("engine down")}
	if err := settleStartForResult(t.Context(), "coder", errors.New("boom"), true, f.steps()); err == nil ||
		!strings.Contains(err.Error(), "the rollback could not read the box") {
		t.Fatalf("rollback read: %v", err)
	}
}

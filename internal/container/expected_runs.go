package container

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Run identifies one run of a container: its full ID and the run's own
// marker. A stopped container started again keeps its ID but gets a new
// start time (and PID), so the ID alone is not the run.
type Run struct {
	ID        string
	StartedAt string
	PID       int
}

// RunOf is the run a read of the container shows.
func RunOf(d *Details) Run {
	if d == nil {
		return Run{}
	}
	return Run{ID: d.ID, StartedAt: d.StartedAt, PID: d.PID}
}

// ExpectedRuns is, for one operation, the run it expects of each box it
// works on: the run it read or started itself, or none after its own stop.
// The client records the runs it starts and the stops it makes; the
// operation records what it read. A box without an entry has no expectation
// and is checked by nothing.
type ExpectedRuns struct {
	mu   sync.Mutex
	runs map[string]*Run // nil value: none expected (after the operation's own stop)
	// started is the last run of each box the operation started itself,
	// kept across its own stops (which clear runs): the identity a cleanup
	// of that attempt is bound to.
	started map[string]Run
}

type expectedRunsKey struct{}

// WithExpectedRuns gives the operation under ctx its record; an existing
// record is kept.
func WithExpectedRuns(ctx context.Context) context.Context {
	if ExpectedRunsFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, expectedRunsKey{}, &ExpectedRuns{runs: map[string]*Run{}, started: map[string]Run{}})
}

// ExpectedRunsFrom returns the operation's record, or nil.
func ExpectedRunsFrom(ctx context.Context) *ExpectedRuns {
	runs, _ := ctx.Value(expectedRunsKey{}).(*ExpectedRuns)
	return runs
}

// Expect records the run of box the operation read or started.
func (e *ExpectedRuns) Expect(box string, run Run) {
	if e == nil || box == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runs[box] = &run
}

// ExpectIfUnset records a read run only when the operation has no
// expectation for box yet: a run it started itself, or its own stop, wins.
func (e *ExpectedRuns) ExpectIfUnset(box string, run Run) {
	if e == nil || box == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.runs[box]; !ok {
		e.runs[box] = &run
	}
}

// stopped records the operation's own stop of container id: its box expects
// no run until the operation starts one.
func (e *ExpectedRuns) stopped(id string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for box, run := range e.runs {
		if run != nil && run.ID == id {
			e.runs[box] = nil
		}
	}
}

// Started returns the last run of box the operation started itself, as read
// back after that start (incomplete when it could not be read), whether or
// not the operation stopped it since; ok is false when it started none.
func (e *ExpectedRuns) Started(box string) (Run, bool) {
	if e == nil {
		return Run{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, ok := e.started[box]
	return run, ok
}

// SameRun reports whether a read run is the identified run: the same
// container ID and start time, and the same PID when both are known; an
// identity without its ID or start time matches nothing (Check's rule).
func SameRun(identified, read Run) bool {
	return identified.ID != "" && identified.StartedAt != "" && read.ID == identified.ID && read.StartedAt == identified.StartedAt &&
		(identified.PID == 0 || read.PID == 0 || read.PID == identified.PID)
}

func (e *ExpectedRuns) recordStart(box string, run Run) {
	e.Expect(box, run)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started == nil {
		e.started = map[string]Run{}
	}
	e.started[box] = run
}

// Has reports an expectation for box.
func (e *ExpectedRuns) Has(box string) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.runs[box]
	return ok
}

// StoppedDuringOperation is the plain failure of an operation whose box's
// expected run is gone: absent, not running, another container, or the same
// container in a new run. Next names the effect it did not perform. It
// carries no uncertainty.
type StoppedDuringOperation struct {
	Box, Next string
}

func (e *StoppedDuringOperation) Error() string {
	return fmt.Sprintf("box %s stopped during the operation, after its last start or read, before %s", e.Box, e.Next)
}

// RunUnknown is the plain failure of an operation that started or read a run
// of box but could not read that run's identity (ID and start time): no later
// effect that assumes the run goes ahead unbound.
type RunUnknown struct {
	Box, Next string
}

func (e *RunUnknown) Error() string {
	return fmt.Sprintf("the run of box %s could not be read after its start or read; stopping before %s", e.Box, e.Next)
}

// Check compares a read of box's container with the expected run. Without
// an expectation it passes. A nil read is an absent container. An expected
// run without its full identity (unread after a start, or without a start
// time) never passes.
func (e *ExpectedRuns) Check(box string, d *Details, next string) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	expected, ok := e.runs[box]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	if expected != nil && (expected.ID == "" || expected.StartedAt == "") {
		return &RunUnknown{Box: box, Next: next}
	}
	if expected == nil || d == nil || !d.Running || d.ID != expected.ID || d.StartedAt != expected.StartedAt ||
		expected.PID != 0 && d.PID != 0 && d.PID != expected.PID {
		return &StoppedDuringOperation{Box: box, Next: next}
	}
	return nil
}

// StartAllowed reports whether the operation may start box: it expects no
// run of it yet (a first start) or none after its own stop. A box whose
// expected run stopped or vanished beside the operation is not started again;
// StartAllowed then returns the operation's plain failure.
func (e *ExpectedRuns) StartAllowed(box, next string) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	expected, ok := e.runs[box]
	e.mu.Unlock()
	if !ok || expected == nil {
		return nil
	}
	return &StoppedDuringOperation{Box: box, Next: next}
}

// CheckExpectedRun reads box's container and compares it with the
// operation's expected run, right before an effect that assumes that run.
// The read is not atomic with the effect. Without an expectation it reads
// nothing and returns nil details.
func (c *Client) CheckExpectedRun(ctx context.Context, box, next string) (*Details, error) {
	runs := ExpectedRunsFrom(ctx)
	if !runs.Has(box) {
		return nil, nil
	}
	d, err := c.Inspect(ctx, box)
	if err != nil {
		if isNoSuchContainer(err) {
			return nil, runs.Check(box, nil, next)
		}
		return nil, err
	}
	return d, runs.Check(box, d, next)
}

// startedBox is, for an operation that keeps a record, the box a container
// start is for: read before the engine start (no effect yet when the read
// fails), so a run that cannot be read back afterwards is still bound to it.
func (c *Client) startedBox(ctx context.Context, target string) (string, error) {
	if ExpectedRunsFrom(ctx) == nil || len(target) != 64 {
		return target, nil
	}
	d, err := c.Inspect(ctx, target)
	if err != nil {
		return "", fmt.Errorf("read container %s before its start: %w", target, err)
	}
	return d.Name, nil
}

// recordStarted records the run the client just started for box. A run that
// cannot be read back is recorded without its start time, so every later
// check of that box fails instead of passing unbound.
func (c *Client) recordStarted(ctx context.Context, target, box string) {
	runs := ExpectedRunsFrom(ctx)
	if runs == nil {
		return
	}
	d, err := c.Inspect(ctx, target)
	if err != nil || d.Name != box {
		runs.recordStart(box, Run{ID: target})
		return
	}
	runs.recordStart(box, RunOf(d))
}

func recordStopped(ctx context.Context, id string) {
	ExpectedRunsFrom(ctx).stopped(id)
}

// isNoSuchContainer matches the engine's and Inspect's own "no such
// container" answers.
func isNoSuchContainer(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") || strings.Contains(msg, "no such object") || strings.Contains(msg, "not found")
}

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
}

type expectedRunsKey struct{}

// WithExpectedRuns gives the operation under ctx its record; an existing
// record is kept.
func WithExpectedRuns(ctx context.Context) context.Context {
	if ExpectedRunsFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, expectedRunsKey{}, &ExpectedRuns{runs: map[string]*Run{}})
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

// Check compares a read of box's container with the expected run. Without
// an expectation it passes. A nil read is an absent container.
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
	if expected == nil || d == nil || !d.Running || d.ID != expected.ID ||
		expected.StartedAt != "" && d.StartedAt != expected.StartedAt ||
		expected.PID != 0 && d.PID != 0 && d.PID != expected.PID {
		return &StoppedDuringOperation{Box: box, Next: next}
	}
	return nil
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

// recordStarted records the run the client just started for its box.
func (c *Client) recordStarted(ctx context.Context, target string) {
	runs := ExpectedRunsFrom(ctx)
	if runs == nil {
		return
	}
	d, err := c.Inspect(ctx, target)
	if err != nil {
		return
	}
	runs.Expect(d.Name, RunOf(d))
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

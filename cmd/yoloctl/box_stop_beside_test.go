package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

const (
	besideT = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	besideC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// fakeBox is the box's container as reads see it over time: a list of
// (id, running) observations; each read takes the next, the last repeats.
type fakeBox struct {
	mu      sync.Mutex
	reads   []boxRead
	read    int
	readErr map[int]error
	stopErr error
	stops   []string
}

type boxRead struct {
	id      string
	running bool
}

func (f *fakeBox) steps() besideSteps {
	return besideSteps{
		read: func(context.Context) (string, bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			i := f.read
			f.read++
			if err := f.readErr[i]; err != nil {
				return "", false, err
			}
			if i >= len(f.reads) {
				i = len(f.reads) - 1
			}
			return f.reads[i].id, f.reads[i].running, nil
		},
		stop: func(_ context.Context, id string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.stops = append(f.stops, id)
			return f.stopErr
		},
	}
}

// Section 3's table, with astra-architect2's wording: the final observation
// decides, the engine's reply never does, and a replacement is never stopped.
func TestBusyStopResultIsTheFinalObservation(t *testing.T) {
	engineFailed := errors.New("nerdctl stop: exit status 1")
	for _, c := range []struct {
		name    string
		reads   []boxRead
		stopErr error
		stops   []string
		fail    string // the error's text, or "" for exit 0
	}{
		{"no container", []boxRead{{"", false}}, nil, nil, ""},
		{"stopped", []boxRead{{besideT, true}, {besideT, false}}, nil, []string{besideT}, ""},
		{"already exited", []boxRead{{besideT, false}}, nil, nil, ""},
		{"removed meanwhile", []boxRead{{besideT, true}, {"", false}}, nil, []string{besideT}, ""},
		{"still running", []boxRead{{besideT, true}, {besideT, true}}, nil, []string{besideT}, "stop failed: box coder is still running (container aaaaaaaaaaaa)"},
		{"replacement runs", []boxRead{{besideT, true}, {besideC, true}}, nil, []string{besideT}, "box coder is running as replacement container cccccccccccc; this stop targeted container aaaaaaaaaaaa"},
		{"replacement runs, nothing to stop", []boxRead{{"", false}, {besideC, true}}, nil, nil, "this stop found no running container to stop"},
		{"replaced, not running", []boxRead{{besideT, true}, {besideC, false}}, nil, []string{besideT}, ""},
		// The engine's error never decides: the final observation does, and
		// the diagnostic is kept.
		{"engine error, stopped", []boxRead{{besideT, true}, {besideT, false}}, engineFailed, []string{besideT}, ""},
		{"engine error, running", []boxRead{{besideT, true}, {besideT, true}}, engineFailed, []string{besideT}, "container stop of aaaaaaaaaaaa: nerdctl stop: exit status 1"},
	} {
		f := &fakeBox{reads: c.reads, stopErr: c.stopErr}
		err := stopTargetBeside(t.Context(), "coder", f.steps())
		if !slices.Equal(f.stops, c.stops) || (c.fail == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.fail) {
			t.Fatalf("%s: stops %v err %v", c.name, f.stops, err)
		}
	}
	// A failed read: the command fails with it and reports no state.
	for _, at := range []int{0, 1} {
		f := &fakeBox{reads: []boxRead{{besideT, true}}, readErr: map[int]error{at: errors.New("engine down")}, stopErr: engineFailed}
		err := stopTargetBeside(t.Context(), "coder", f.steps())
		if err == nil || !strings.Contains(err.Error(), "engine down") || strings.Contains(err.Error(), "stopped") || len(f.stops) > at {
			t.Fatalf("read %d: %v stops %v", at, err, f.stops)
		}
	}
}

// A replacement that appears after the stop's own engine call, before or
// after its final observation: at most one stop, of T; C is never stopped.
func TestBusyStopNeverRetargetsAReplacement(t *testing.T) {
	f := &fakeBox{reads: []boxRead{{besideT, true}, {besideC, true}, {besideC, true}}}
	_ = stopTargetBeside(t.Context(), "coder", f.steps())
	if !slices.Equal(f.stops, []string{besideT}) || f.read != 2 {
		t.Fatalf("stops %v reads %d", f.stops, f.read)
	}
}

// Two stops of one box at once: each resolves its own target and makes at
// most one engine stop; both report stopped.
func TestTwoBusyStopsOfOneBox(t *testing.T) {
	var mu sync.Mutex
	running := true
	shared := besideSteps{
		read: func(context.Context) (string, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return besideT, running, nil
		},
	}
	stops := 0
	shared.stop = func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		stops++
		running = false
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = stopTargetBeside(context.Background(), "coder", shared) }()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || stops < 1 || stops > 2 {
		t.Fatalf("errors %v stops %d", errs, stops)
	}
}

// besideWrapper runs `box stop coder` through the real wrapper in a worker
// context, with the real lock held by another operation when held is set.
type besideWrapper struct {
	events    []string
	intents   []controller.EffectIntent
	ranStop   bool
	principal controller.Principal
}

func (w *besideWrapper) run(t *testing.T, held bool, lock func(context.Context, string) (func(), error), box *fakeBox) (time.Duration, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousSync, previousTry, previousBeside, previousRevalidate := syncNativeChatRolesFn, trySpaceOperationLockFn, stopBoxBesideFn, revalidateOperatorFn
	t.Cleanup(func() {
		syncNativeChatRolesFn, trySpaceOperationLockFn, stopBoxBesideFn, revalidateOperatorFn = previousSync, previousTry, previousBeside, previousRevalidate
	})
	syncNativeChatRolesFn = func(context.Context, string) error { w.events = append(w.events, "role-sync"); return nil }
	if lock != nil {
		trySpaceOperationLockFn = lock
	}
	stopBoxBesideFn = func(cmd *cobra.Command, name string, _ besideSteps) (bool, error) {
		w.events = append(w.events, "busy")
		return stopBoxBeside(cmd, name, box.steps())
	}
	revalidateOperatorFn = func(context.Context) error { w.events = append(w.events, "revalidate"); return nil }
	if held {
		unlock, err := spacelock.Acquire(t.Context(), "work", true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unlock)
	}
	root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
	group := &cobra.Command{Use: "box"}
	stop := &cobra.Command{Use: "stop", Args: cobra.ExactArgs(1), RunE: func(*cobra.Command, []string) error {
		w.ranStop = true
		w.events = append(w.events, "slice2-stop")
		return nil
	}}
	stop.Flags().String("space", "work", "")
	group.AddCommand(stop)
	root.AddCommand(group)
	registerSpaceOperations(root)
	ctx := context.WithValue(t.Context(), controllerRequestKey{}, "request-0123456789abcdef")
	ctx = withControllerPrincipal(ctx, w.principal)
	ctx = controller.WithEffectRecorder(ctx, &completionRecorder{events: &w.events, locked: func() bool { return true }})
	ctx = controller.WithEffectGate(ctx, func(_ context.Context, intent controller.EffectIntent) error {
		w.events = append(w.events, "gate")
		w.intents = append(w.intents, intent)
		return nil
	})
	ctx = context.WithValue(ctx, workerCompletionKey{}, &workerCompletion{})
	root.SetArgs([]string{"box", "stop", "coder", "--space", "work"})
	started := time.Now()
	err := root.ExecuteContext(ctx)
	return time.Since(started), err
}

func TestBoxStopLockFreeTakesSlice2Path(t *testing.T) {
	w := &besideWrapper{}
	box := &fakeBox{reads: []boxRead{{besideT, true}}}
	if _, err := w.run(t, false, nil, box); err != nil || !w.ranStop || len(box.stops) != 0 ||
		!slices.Equal(w.events, []string{"gate", "slice2-stop", "role-sync", "complete"}) {
		t.Fatalf("lock free: %v events %v", err, w.events)
	}
}

// The lock is held by another operation (on this box or another): the stop
// returns without waiting, makes one engine stop of T, records its own
// completion, and runs none of the wrapper's suffix (no role publication,
// no cleanup, no declaration, session or port step: slice 2's stop never
// runs). The holder keeps its lock.
func TestBoxStopBesideAHeldLockDoesNotWait(t *testing.T) {
	w := &besideWrapper{}
	box := &fakeBox{reads: []boxRead{{besideT, true}, {besideT, false}}}
	took, err := w.run(t, true, nil, box)
	if err != nil || took > 2*time.Second || w.ranStop || !slices.Equal(box.stops, []string{besideT}) ||
		!slices.Equal(w.events, []string{"busy", "gate", "complete"}) {
		t.Fatalf("busy: %v after %v events %v stops %v", err, took, w.events, box.stops)
	}
	// Its footprint is the named box only.
	scope := w.intents[0].Scope
	if scope == nil || scope.Global || len(scope.Writes) != 1 || scope.Writes[0] != boxResource("coder") || len(scope.Reads) != 0 {
		t.Fatalf("footprint %+v", scope)
	}
	if _, err := spacelock.TryAcquire(t.Context(), "work", true); !errors.Is(err, spacelock.ErrBusy) {
		t.Fatalf("the other operation lost its lock: %v", err)
	}
}

// Taking the lock fails for another reason than contention: the command
// fails with that error and stops nothing.
func TestBoxStopLockFailureIsNotTheBusyPath(t *testing.T) {
	w := &besideWrapper{}
	box := &fakeBox{reads: []boxRead{{besideT, true}}}
	failure := errors.New("open space lock: permission denied")
	_, err := w.run(t, false, func(context.Context, string) (func(), error) { return nil, failure }, box)
	if !errors.Is(err, failure) || len(box.stops) != 0 || w.ranStop || slices.Contains(w.events, "complete") {
		t.Fatalf("%v events %v", err, w.events)
	}
}

// An operator caller on the busy path gets the same fresh validation as the
// wrapper; a rejected one is unauthorized, stops nothing and records nothing.
func TestBoxStopBesideRevalidatesAnOperator(t *testing.T) {
	w := &besideWrapper{principal: controller.Principal{Box: "ops", Incarnation: "i1"}}
	box := &fakeBox{reads: []boxRead{{besideT, true}, {besideT, false}}}
	if _, err := w.run(t, true, nil, box); err != nil || !slices.Equal(w.events, []string{"busy", "revalidate", "gate", "complete"}) {
		t.Fatalf("authorized operator: %v %v", err, w.events)
	}
	w = &besideWrapper{principal: controller.Principal{Box: "ops", Incarnation: "stale"}}
	box = &fakeBox{reads: []boxRead{{besideT, true}}}
	revoked := errors.New("box operator grant was revoked")
	previous := revalidateOperatorFn
	_, err := w.runWith(t, box, func(context.Context) error { return revoked })
	revalidateOperatorFn = previous
	if !errors.Is(err, revoked) || len(box.stops) != 0 || slices.Contains(w.events, "gate") || slices.Contains(w.events, "complete") {
		t.Fatalf("revoked operator: %v %v", err, w.events)
	}
}

// runWith is run with the lock held and a given operator validation.
func (w *besideWrapper) runWith(t *testing.T, box *fakeBox, revalidate func(context.Context) error) (time.Duration, error) {
	t.Helper()
	previousTry := trySpaceOperationLockFn
	defer func() { trySpaceOperationLockFn = previousTry }()
	trySpaceOperationLockFn = func(context.Context, string) (func(), error) {
		revalidateOperatorFn = revalidate // after run installed its own
		return nil, spacelock.ErrBusy
	}
	return w.run(t, false, trySpaceOperationLockFn, box)
}

// A busy-path stop that found its target not running made no engine stop
// and says so, instead of "stopped".
func TestBusyStopOfANotRunningTargetSaysSo(t *testing.T) {
	var out strings.Builder
	t.Cleanup(bindCLITestLogger(t, &out))
	f := &fakeBox{reads: []boxRead{{besideT, false}}}
	if err := stopTargetBeside(t.Context(), "coder", f.steps()); err != nil || len(f.stops) != 0 ||
		!strings.Contains(out.String(), "box coder was not running (container aaaaaaaaaaaa)") || strings.Contains(out.String(), "coder stopped") {
		t.Fatalf("%v stops %v\n%s", err, f.stops, out.String())
	}
}

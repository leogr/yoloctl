package main

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/features"
)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Viewers' inventory reads wake one run per interval, however many arrive;
// other reads never wake it.
func TestCodexObserveCoalescesAnInventoryStorm(t *testing.T) {
	s := &controller.Server{Receipts: controller.Receipts{Dir: t.TempDir()}}
	var runs atomic.Int32
	stop := scheduleCodexObserve(t.Context(), s, func(context.Context) error { runs.Add(1); return nil }, 300*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()
	s.ReadFinished("chat.status")
	time.Sleep(50 * time.Millisecond)
	if runs.Load() != 0 {
		t.Fatal("a non-inventory read woke the read")
	}
	for range 20 {
		s.ReadFinished("box.inventory")
		s.ReadFinished("box.list")
	}
	waitFor(t, "the first run", func() bool { return runs.Load() == 1 })
	for range 20 {
		s.ReadFinished("box.inventory")
	}
	time.Sleep(100 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatalf("reads within the interval ran again: %d runs", runs.Load())
	}
	waitFor(t, "the next interval's run", func() bool { return runs.Load() == 2 })
	time.Sleep(400 * time.Millisecond)
	if runs.Load() != 2 {
		t.Fatalf("without new reads it kept running: %d runs", runs.Load())
	}
}

// An uncertain or draining controller admits no maintenance, so no read runs.
func TestCodexObserveSkipsAnUncertainController(t *testing.T) {
	s := &controller.Server{Receipts: controller.Receipts{Dir: t.TempDir()}, RecoveryRequired: true}
	var runs atomic.Int32
	stop := scheduleCodexObserve(t.Context(), s, func(context.Context) error { runs.Add(1); return nil }, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()
	s.ReadFinished("box.inventory")
	time.Sleep(100 * time.Millisecond)
	if runs.Load() != 0 {
		t.Fatal("a read ran under RecoveryRequired")
	}
}

// The inventory reply never waits for the read: ReadFinished returns at once
// while a run is in progress.
func TestCodexObserveNeverBlocksTheRead(t *testing.T) {
	s := &controller.Server{Receipts: controller.Receipts{Dir: t.TempDir()}}
	started, release := make(chan struct{}), make(chan struct{})
	stop := scheduleCodexObserve(t.Context(), s, func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()
	s.ReadFinished("box.inventory")
	<-started
	begin := time.Now()
	for range 100 {
		s.ReadFinished("box.inventory")
	}
	if elapsed := time.Since(begin); elapsed > 50*time.Millisecond {
		t.Fatalf("ReadFinished blocked for %v while a run was in progress", elapsed)
	}
	close(release)
}

// Targets are the tracked Codex launches whose holder runs with the launch's
// token, read at the conversation box session shows after folding the live
// reports: a learn launch's first report, then a switch, are both followed;
// an exited holder and a Claude box are never read. (The three scenarios are
// archyAstra's review repro.)
func TestCodexObserveTargetsTheLiveFoldedConversation(t *testing.T) {
	const first = "4f9a2c1e-8b3d-4e7a-9c5f-1d2e3f4a5b6c"
	const second = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	for _, scenario := range []string{"fresh-report", "observed-switch", "exited-holder"} {
		t.Run(scenario, func(t *testing.T) {
			f := newObserveFixture(t)
			if _, err := f.planObserved(observedBox(), sessionStartOptions{}); err != nil {
				t.Fatal(err)
			}
			cfg := &agentbox.Config{Name: "cx", Space: "work", Agent: "codex", Account: "main", ID: agentbox.NewIdentity(),
				Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("cx")}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			f.names = append(f.names, "cx")
			rec, err := f.planObserved(cfg, sessionStartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			gen := rec.Launch.Generation
			f.reports[gen] = []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: first, Source: "startup", AgentTS: time.Now().UTC()}}
			want := first
			if scenario != "fresh-report" {
				foldLiveLaunch(held(t.Context()), "work", "cx")
				if persisted, _, err := agentsession.Load("work", "cx"); err != nil || persisted.Current == nil || persisted.Current.ID != first {
					t.Fatalf("fixture fold: %+v %v", persisted, err)
				}
			}
			switch scenario {
			case "observed-switch":
				f.reports[gen] = append(f.reports[gen], observe.Observation{Offset: 20, Event: "SessionStart", SessionID: second, Source: "resume", AgentTS: time.Now().UTC()})
				want = second
			case "exited-holder":
				f.exit("cx", 0)
				want = ""
			}
			targets, err := codexObserveTargets(t.Context(), nil, "work")
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if len(targets) != 0 {
					t.Fatalf("an exited session is still targeted: %+v", targets)
				}
				return
			}
			if len(targets) != 1 || targets[0] != (codexObserveTarget{box: "cx", gen: gen, thread: want}) {
				t.Fatalf("targets = %+v, want cx at generation %d on %s", targets, gen, want)
			}
			// One read per target through the confined exec, with validated argv.
			var calls [][]string
			saved := codexObserveExec
			t.Cleanup(func() { codexObserveExec = saved })
			codexObserveExec = func(ctx context.Context, space, box string, argv []string) error {
				if _, ok := ctx.Deadline(); !ok || space != "work" || box != "cx" {
					t.Errorf("exec %s/%s without a budget", space, box)
				}
				calls = append(calls, argv)
				return nil
			}
			if err := readCodexTargets(t.Context(), "work", targets); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || !slices.Equal(calls[0], []string{features.AgentObserveCodexQuery, strconv.FormatUint(gen, 10), want}) {
				t.Fatalf("argv = %v", calls)
			}
		})
	}
}

// Each box's fold gets its own live-read budget: a run that has spent one
// box's budget still folds the next box's reports.
func TestCodexObserveFoldsEachBoxWithinItsOwnBudget(t *testing.T) {
	f := newObserveFixture(t)
	const first = "4f9a2c1e-8b3d-4e7a-9c5f-1d2e3f4a5b6c"
	cfg := &agentbox.Config{Name: "cx", Space: "work", Agent: "codex", Account: "main", ID: agentbox.NewIdentity(),
		Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("cx")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	f.names = append(f.names, "cx")
	rec, err := f.planObserved(cfg, sessionStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.reports[rec.Launch.Generation] = []observe.Observation{{Offset: 10, Event: "SessionStart", SessionID: first, Source: "startup", AgentTS: time.Now().UTC()}}
	viewReads.Lock()
	viewReads.start = time.Now().Add(-time.Hour)
	viewReads.Unlock()
	t.Cleanup(resetObservationViewBudget)
	if targets, err := codexObserveTargets(t.Context(), nil, "work"); err != nil || len(targets) != 1 || targets[0].thread != first {
		t.Fatalf("a spent budget hid the box's reports: %+v %v", targets, err)
	}
}

// The fold never outlives the box's budget: with less left than a view's
// live-read budget, its read ends at the box's deadline.
func TestCodexObserveFoldEndsWithinTheBoxBudget(t *testing.T) {
	f := newObserveFixture(t)
	cfg := &agentbox.Config{Name: "cx", Space: "work", Agent: "codex", Account: "main", ID: agentbox.NewIdentity(),
		Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("cx")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	f.names = append(f.names, "cx")
	if _, err := f.planObserved(cfg, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	savedBudget, savedRead := codexObserveBoxBudget, readObservationFn
	t.Cleanup(func() {
		codexObserveBoxBudget, readObservationFn = savedBudget, savedRead
		resetObservationViewBudget()
	})
	codexObserveBoxBudget = observationViewBudget / 2
	var deadlines []time.Duration
	begin := time.Now()
	readObservationFn = func(ctx context.Context, dir string, gen uint64, cur observe.Cursor, uid int, at time.Time) (observe.Result, error) {
		if d, ok := ctx.Deadline(); ok {
			deadlines = append(deadlines, d.Sub(begin))
		}
		return savedRead(ctx, dir, gen, cur, uid, at)
	}
	if _, err := codexObserveTargets(t.Context(), nil, "work"); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) == 0 || slices.Max(deadlines) > codexObserveBoxBudget+50*time.Millisecond {
		t.Fatalf("fold deadlines %v exceed the box budget %v", deadlines, codexObserveBoxBudget)
	}
}

// A worker's cancellation (a mutation preempting the run) ends the fold's
// read too.
func TestCodexObserveFoldIsCanceledWithItsWorker(t *testing.T) {
	saved := readObservationFn
	t.Cleanup(func() { readObservationFn = saved; resetObservationViewBudget() })
	newObserveFixture(t) // the report owner and directory seams
	var canceled bool
	readObservationFn = func(ctx context.Context, _ string, gen uint64, _ observe.Cursor, _ int, _ time.Time) (observe.Result, error) {
		canceled = ctx.Err() != nil
		return observe.Result{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	beginObservationViewBudget(ctx)
	cancel()
	_, _ = agentsession.ReadCurrentReports("cx", 1, observe.Cursor{Generation: 1})
	if !canceled {
		t.Fatal("the fold's read outlived its worker's cancellation")
	}
}

// A box that does not answer within its budget is abandoned, and the run
// moves on to the next one.
func TestCodexObserveBoundsEachBox(t *testing.T) {
	saved, savedBudget := codexObserveExec, codexObserveBoxBudget
	t.Cleanup(func() { codexObserveExec, codexObserveBoxBudget = saved, savedBudget })
	codexObserveBoxBudget = 50 * time.Millisecond
	var read []string
	codexObserveExec = func(ctx context.Context, _, box string, _ []string) error {
		if box == "silent" {
			<-ctx.Done()
			return ctx.Err()
		}
		read = append(read, box)
		return nil
	}
	begin := time.Now()
	targets := []codexObserveTarget{{box: "silent", gen: 1, thread: "4f9a2c1e-8b3d-4e7a-9c5f-1d2e3f4a5b6c"}, {box: "next", gen: 2, thread: "4f9a2c1e-8b3d-4e7a-9c5f-1d2e3f4a5b6c"}}
	if err := readCodexTargets(t.Context(), "work", targets); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second || !slices.Equal(read, []string{"next"}) {
		t.Fatalf("a silent box held the read for %v; read %v", elapsed, read)
	}
}

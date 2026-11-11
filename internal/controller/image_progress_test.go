package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	bk "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	digest "github.com/opencontainers/go-digest"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

type progressBuffer struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (b *progressBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buffer.Write(p)
}
func (b *progressBuffer) text() string { b.Lock(); defer b.Unlock(); return b.buffer.String() }

// recordBuffer receives one encoded record per Write (an imageProgress
// sender's output) and renders them as plain Trace lines for assertions.
type recordBuffer struct {
	sync.Mutex
	records []slog.Record
}

func (b *recordBuffer) Write(p []byte) (int, error) {
	r, err := prettylog.UnmarshalRecord(p)
	if err != nil {
		return 0, err
	}
	b.Lock()
	defer b.Unlock()
	b.records = append(b.records, r)
	return len(p), nil
}
func (b *recordBuffer) text() string {
	b.Lock()
	records := append([]slog.Record(nil), b.records...)
	b.Unlock()
	var out bytes.Buffer
	l := plainTraceLogger(&out)
	for _, r := range records {
		_ = l.Handle(context.Background(), r)
	}
	return out.String()
}
func plainTraceLogger(w io.Writer) *prettylog.Logger {
	return prettylog.New(w, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
}
func detailLine(s *prettylog.Step, text string) { _, _ = io.WriteString(s.Detail(), text+"\n") }

func TestImageProgressImmediateAndQuietFeedback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out recordBuffer
		p := newImageProgress(t.Context(), &out)
		p.preparing("local/test:latest for box worker", "local/test:latest", "Image for box worker", "")
		synctest.Wait()
		if !strings.Contains(out.text(), "builder: Image for box worker") {
			t.Fatal(out.text())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		text := out.text()
		if n := strings.Count(text, "debug: builder: Still preparing image"); n != 2 {
			t.Fatalf("quiet notices=%d: %s", n, text)
		}
		p.note("actual build output")
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		p.finish()
		text = out.text()
		time.Sleep(5 * time.Second)
		if out.text() != text {
			t.Fatal("progress after finish")
		}
	})
}

func TestImageProgressJoinSnapshotAndLiveAreOrdered(t *testing.T) {
	var log imageBuildLog
	step := log.Logger().StartStep("Building image test-job", prettylog.Actor(imageActor))
	log.Logger().Slog().Warn("before join warning")
	detailLine(step, "before join")
	var out recordBuffer
	p := newImageProgress(t.Context(), &out)
	stop := log.subscribe(p)
	detailLine(step, "after join")
	stop()
	detailLine(step, "after unsubscribe")
	p.finish()
	got := out.text()
	for _, want := range []string{"warning: before join warning", "Building image test-job", "before join", "after join"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "after unsubscribe") || strings.Index(got, "before join warning") > strings.Index(got, "Building image") || strings.Index(got, "before join\n") > strings.Index(got, "after join") {
		t.Fatalf("order or leak:\n%s", got)
	}
	// The follower's own Tracker reproduces the job's step view.
	follower := prettylog.NewTracker(prettylog.TrackerLimits{})
	for _, r := range out.records {
		follower.Apply(r)
	}
	if steps := follower.Steps(); len(steps) != 1 || steps[0].State != prettylog.StepRunning || strings.Join(steps[0].Tail, "|") != "before join|after join" {
		t.Fatalf("%+v", steps)
	}
}

type blockedProgressWriter struct {
	ctx     context.Context
	entered chan struct{}
	once    sync.Once
}

func (w *blockedProgressWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.ctx.Done()
	return 0, w.ctx.Err()
}

func TestImageProgressSlowClientCannotBlockJobOrFollower(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	blocked := &blockedProgressWriter{ctx: ctx, entered: make(chan struct{})}
	slow := newImageProgress(ctx, blocked)
	var log imageBuildLog
	stopSlow := log.subscribe(slow)
	step := log.Logger().StartStep("Building image test-job")
	take(t, blocked.entered)
	line := strings.Repeat("x", 900)
	for range 3 * imageProgressLimit / len(line) {
		detailLine(step, line)
	}
	var out recordBuffer
	fast := newImageProgress(t.Context(), &out)
	stopFast := log.subscribe(fast)
	log.Logger().Slog().Info("final live log")
	step.Done()
	stopFast()
	fast.finish()
	if got := out.text(); !strings.Contains(got, "final live log") || !strings.Contains(got, "Building image test-job: done") {
		t.Fatalf("missing live log or step end: %s", got)
	}
	slow.mu.Lock()
	size, skipped := slow.bytes, slow.skipped
	slow.mu.Unlock()
	if size > imageProgressLimit || skipped == 0 {
		t.Fatal(size, skipped)
	}
	stopSlow()
	cancel()
	slow.finish()
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.subscribers) != 0 {
		t.Fatal("retained subscriber")
	}
}

// Step transitions are never dropped for a slow client, only detail lines.
func TestImageProgressOverflowKeepsTransitions(t *testing.T) {
	var log imageBuildLog
	p := &imageProgress{wake: make(chan struct{}, 1), sources: map[*imageBuildLog]*progressSource{}}
	stop := log.subscribe(p)
	steps := make([]*prettylog.Step, 0, 50)
	for i := range 50 {
		s := log.Logger().StartStep(fmt.Sprintf("vertex %d", i))
		steps = append(steps, s)
		for range 50 {
			detailLine(s, strings.Repeat("y", 900))
		}
	}
	for _, s := range steps {
		s.Done()
	}
	stop()
	transitions := 0
	for _, it := range p.items {
		r, err := prettylog.UnmarshalRecord(it.raw)
		if err != nil {
			t.Fatal(err)
		}
		if prettylog.StructureOf(r).State != "" {
			transitions++
		}
	}
	if transitions != 100 || p.skipped == 0 {
		t.Fatalf("transitions=%d skipped=%d", transitions, p.skipped)
	}
}

func TestImageProgressOverflowIsVisibleWhenReaderResumes(t *testing.T) {
	var log imageBuildLog
	p := &imageProgress{wake: make(chan struct{}, 1), sources: map[*imageBuildLog]*progressSource{}}
	stop := log.subscribe(p)
	step := log.Logger().StartStep("Building image test-job")
	for range 2 * imageProgressLimit / 900 {
		detailLine(step, strings.Repeat("z", 900))
	}
	stop()
	data, _ := p.next()
	r, err := prettylog.UnmarshalRecord(data)
	if err != nil || !strings.Contains(r.Message, "skipped") {
		t.Fatal("silent overflow", err)
	}
}

func TestImageProgressJoinAndPublishRace(t *testing.T) {
	var log imageBuildLog
	var writers sync.WaitGroup
	writers.Go(func() {
		step := log.Logger().StartStep("Building image race")
		for range 200 {
			log.Logger().Slog().Info("event")
			detailLine(step, "detail")
		}
		step.Done()
	})
	for range 30 {
		p := newImageProgress(t.Context(), io.Discard)
		stop := log.subscribe(p)
		stop()
		p.finish()
	}
	writers.Wait()
}

func TestImageSolveProgressDrainsStepsLogsWarningsAndKeepsResult(t *testing.T) {
	for _, cause := range []error{nil, errors.New("solve failed"), context.Canceled} {
		t.Run("result-"+fmtError(cause), func(t *testing.T) {
			var log imageBuildLog
			root := log.Logger().StartStep("Building image test")
			ctx := context.WithValue(t.Context(), imageBuildStepKey{}, imageProgressTarget{step: root, log: log.Logger().Slog()})
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			if errors.Is(cause, context.Canceled) {
				cancel()
			}
			want := &bk.SolveResponse{ExporterResponse: map[string]string{"result": "original"}}
			got, err := solveImageWithProgress(ctx, imageSolveFunc(func(_ context.Context, _ *llb.Definition, _ bk.SolveOpt, status chan *bk.SolveStatus) (*bk.SolveResponse, error) {
				if status == nil {
					t.Fatal("Solve status discarded")
				}
				defer close(status)
				start := time.Now()
				end := start.Add(time.Second)
				id := digest.FromString("run")
				open := digest.FromString("open")
				pending := digest.FromString("pending")
				status <- &bk.SolveStatus{Vertexes: []*bk.Vertex{{Digest: id, Name: "RUN echo output", Started: &start}, {Digest: pending, Name: "never started"}}}
				status <- &bk.SolveStatus{Logs: []*bk.VertexLog{{Vertex: id, Stream: 1, Data: []byte("stdout actual\n"), Timestamp: start}, {Vertex: id, Stream: 2, Data: []byte("progress 1%\rprogress 99%\r\nstderr actual\n"), Timestamp: start}}}
				status <- &bk.SolveStatus{Vertexes: []*bk.Vertex{{Digest: id, Name: "RUN echo output", Started: &start, Completed: &end, Cached: true}, {Digest: open, Name: "RUN still running", Started: &start}}, Warnings: []*bk.VertexWarning{{Short: []byte("test warning"), Detail: [][]byte{[]byte("warning detail")}}}}
				return want, cause
			}), bk.SolveOpt{})
			if got != want || !errors.Is(err, cause) {
				t.Fatal(got, err)
			}
			log.mu.Lock()
			steps, recent := log.tracker.Steps(), append([]slog.Record(nil), log.recent...)
			log.mu.Unlock()
			if len(steps) != 3 {
				t.Fatalf("%+v", steps)
			}
			run, open := steps[1], steps[2]
			if run.Title != "RUN echo output" || run.Parent != steps[0].ID || run.State != prettylog.StepOK || strings.Join(run.Tail, "|") != "stdout actual|progress 1%|progress 99%|stderr actual" {
				t.Fatalf("%+v", run)
			}
			cached := false
			for _, a := range run.Attrs {
				cached = cached || a.Key == "cached" && a.Value.Bool()
			}
			wantOpen := prettylog.StepOK
			if cause != nil {
				wantOpen = prettylog.StepSkipped
			}
			if !cached || open.Title != "RUN still running" || open.State != wantOpen {
				t.Fatalf("cached=%v %+v", cached, open)
			}
			if len(recent) != 1 || recent[0].Level != slog.LevelWarn || recent[0].Message != "test warning\nwarning detail" {
				t.Fatalf("%+v", recent)
			}
		})
	}
}
func fmtError(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}

type progressJobBackend struct{ *jobBackend }

func (b *progressJobBackend) Build(ctx context.Context, r ImageRecipe, p ImagePhase) (PinnedImage, error) {
	detailLine(imageBuildProgress(ctx).step, "actual step output")
	return b.jobBackend.Build(ctx, r, p)
}
func TestImageProgressFollowerKeepsLogsAfterLeaderLeaves(t *testing.T) {
	m, b := newJobs(t, 2)
	m.backend = &progressJobBackend{b}
	firstCtx, cancel := context.WithCancel(t.Context())
	var one, two recordBuffer
	leader := newImageProgress(firstCtx, &one)
	follower := newImageProgress(t.Context(), &two)
	a := make(chan acquireResult, 1)
	go func() {
		pin, err := m.acquireWithProgress(firstCtx, recipe("logs"), BlockerSummary{}, nil, leader)
		a <- acquireResult{pin, err}
	}()
	take(t, b.started)
	joined := make(chan struct{})
	c := make(chan acquireResult, 1)
	go func() {
		pin, err := m.acquireWithProgress(t.Context(), recipe("logs"), BlockerSummary{}, func(ImageWait) { close(joined) }, follower)
		c <- acquireResult{pin, err}
	}()
	take(t, joined)
	cancel()
	if got := take(t, a); !errors.Is(got.err, context.Canceled) {
		t.Fatal(got.err)
	}
	leader.finish()
	m.mu.Lock()
	var job *imageJob
	for _, j := range m.jobs {
		job = j
	}
	m.mu.Unlock()
	job.log.Logger().Slog().Info("log after leader cancellation")
	b.release <- struct{}{}
	got := take(t, c)
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.image.Release()
	follower.finish()
	text := two.text()
	for _, want := range []string{"Joining image build", "actual step output", "log after leader cancellation", "Building image " + shortImageJob(job.phase.JobID) + ": done"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
}

// Log frames must finish before the exit frame; neither machine stdout nor
// the private managed-view path carries any build diagnostics.
func TestImageProgressWireDrainsBeforeFinalResponse(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "returned failure"}[fail], func(t *testing.T) {
			s := storeServer(t)
			s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintImages}
			backend := &wireProgressBackend{imageServiceFixture: &imageServiceFixture{jobBackend: &jobBackend{started: make(chan ImagePhase, 2), release: make(chan struct{}, 2)}}, fail: fail}
			var err error
			s.Images, err = NewImageJobs(t.Context(), s, backend, "test-vm", 2)
			if err != nil {
				t.Fatal(err)
			}
			s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
				for _, name := range []string{"local/base:latest", "local/composed:latest"} {
					if _, err := streams.Image(ctx, ImageRequest{Action: "preparing", Reference: name, Box: "worker"}); err != nil {
						return 2, err
					}
					if _, err := streams.Image(ctx, ImageRequest{Action: "resolve", Reference: name}); err != nil {
						return 2, err
					}
					if _, err := streams.Image(ctx, ImageRequest{Action: "acquire", Recipe: recipe(name)}); err != nil {
						return 2, err
					}
				}
				if _, err := streams.Image(ctx, ImageRequest{Action: "prepared"}); err != nil {
					return 2, err
				}
				if err := streams.BeginEffects(ctx, EffectIntent{Scope: &Scope{Writes: []Resource{{Kind: ResourceImages, Name: "local/composed:latest"}}}}); err != nil {
					return 2, err
				}
				_, _ = io.WriteString(streams.Out, "{\"ok\":true}\n")
				return 0, streams.RecordEvidence.CompleteEffects(ctx, 0)
			}
			var stdout, stderr bytes.Buffer
			// This checks frame ordering across durable image preparation, not
			// its wall-clock duration. Use the test's cancellation/deadline
			// rather than invoke's short protocol-fixture timeout.
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
			err = Call(t.Context(), client, s.Hello, request("box.start"), ClientIO{Out: &stdout, Err: &stderr, Log: plainTraceLogger(&stderr)})
			<-done
			if fail {
				if err == nil || !strings.Contains(err.Error(), "actual solve failure") {
					t.Fatal(err)
				}
				if stdout.Len() != 0 {
					t.Fatal(stdout.String())
				}
				if !strings.Contains(stderr.String(), "failed") || OutcomeRendered(err) {
					t.Fatalf("a failed step is not a rendered command outcome: %v\n%s", err, stderr.String())
				}
			} else if err != nil || stdout.String() != "{\"ok\":true}\n" {
				t.Fatal(err, stdout.String())
			}
			for _, want := range []string{"builder: Image for box worker", "RUN actual", "last build log"} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("missing %q: %s", want, stderr.String())
				}
			}
			if !fail && !strings.Contains(stderr.String(), "builder: Image for box worker ready: done") {
				t.Fatal(stderr.String())
			}
		})
	}
}

type wireProgressBackend struct {
	*imageServiceFixture
	fail bool
}

func (b *wireProgressBackend) Build(ctx context.Context, _ ImageRecipe, p ImagePhase) (PinnedImage, error) {
	_, _ = io.WriteString(imageBuildProgress(ctx).step.Detail(), "RUN actual\nlast build log\n")
	if b.fail {
		return PinnedImage{}, &ImageBuildFailure{Cause: errors.New("actual solve failure")}
	}
	return PinnedImage{JobID: p.JobID, Reference: p.PreparedRef, Digest: "sha256:" + strings.Repeat("a", 64), Lease: "test-lease"}, nil
}

type imageSolveFunc func(context.Context, *llb.Definition, bk.SolveOpt, chan *bk.SolveStatus) (*bk.SolveResponse, error)

func (f imageSolveFunc) Solve(ctx context.Context, def *llb.Definition, opt bk.SolveOpt, status chan *bk.SolveStatus) (*bk.SolveResponse, error) {
	return f(ctx, def, opt, status)
}

func TestImageProgressWireQuietResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := storeServer(t)
		b := &quietResolveBackend{imageServiceFixture: &imageServiceFixture{jobBackend: &jobBackend{}}, entered: make(chan struct{}), release: make(chan struct{})}
		var err error
		s.Images, err = NewImageJobs(t.Context(), s, b, "vm", 2)
		if err != nil {
			t.Fatal(err)
		}
		s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
			if _, err := streams.Image(ctx, ImageRequest{Action: "preparing", Reference: "local/base:latest", Box: "worker"}); err != nil {
				return 2, err
			}
			_, err := streams.Image(ctx, ImageRequest{Action: "resolve", Reference: "local/base:latest"})
			return 2, err
		}
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		serverDone := make(chan struct{})
		go func() { defer close(serverDone); s.ServeConn(t.Context(), server, Principal{}) }()
		var stderr progressBuffer
		done := make(chan error, 1)
		go func() {
			done <- Call(t.Context(), client, s.Hello, request("box.start"), ClientIO{Err: &stderr, Log: plainTraceLogger(&stderr)})
		}()
		<-b.entered
		synctest.Wait()
		if !strings.Contains(stderr.text(), "builder: Image for box worker") {
			t.Fatal(stderr.text())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := strings.Count(stderr.text(), "Still preparing image"); n != 2 {
			t.Fatalf("quiet resolution feedback=%d: %s", n, stderr.text())
		}
		close(b.release)
		if err := <-done; err == nil || !strings.Contains(err.Error(), "resolution stopped") {
			t.Fatal(err)
		}
		<-serverDone
	})
}

type quietResolveBackend struct {
	*imageServiceFixture
	entered, release chan struct{}
}

func (b *quietResolveBackend) Resolve(context.Context, string) (string, string, error) {
	close(b.entered)
	<-b.release
	return "", "", errors.New("resolution stopped")
}

func TestImageProgressBlockedWireSenderEndsOnClientDisconnect(t *testing.T) {
	s := storeServer(t)
	var err error
	s.Images, err = NewImageJobs(t.Context(), s, &imageServiceFixture{jobBackend: &jobBackend{}}, "vm", 2)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		if _, err := streams.Image(ctx, ImageRequest{Action: "preparing", Reference: "local/test:latest"}); err != nil {
			return 2, err
		}
		close(entered)
		<-ctx.Done()
		return 2, ctx.Err()
	}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
	wire := NewWire(client, client)
	if err := wire.Write(Frame{Type: "hello", Hello: &s.Hello}); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Read(); err != nil {
		t.Fatal(err)
	}
	req := request("box.start")
	if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
		t.Fatal(err)
	}
	take(t, entered)
	// No read of log frames: the sole sender is blocked on this public connection.
	_ = client.Close()
	take(t, done)
}

// gatedWriter blocks the sender's first write until released, then records.
type gatedWriter struct {
	entered, release chan struct{}
	once             sync.Once
	out              recordBuffer
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.out.Write(p)
}

// A client that falls behind never holds more than the queue limit, even
// for records that are not lossy; when it catches up it receives a notice
// and the jobs' current state, which reproduces every step.
func TestImageProgressStructuralOverflowStaysBoundedAndResyncs(t *testing.T) {
	for _, burst := range []string{"step transitions", "non-detail records"} {
		t.Run(burst, func(t *testing.T) {
			w := &gatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
			p := newImageProgress(t.Context(), w)
			var log imageBuildLog
			stop := log.subscribe(p)
			take(t, w.entered)
			for i := range 30000 {
				if burst == "step transitions" {
					log.Logger().StartStep(fmt.Sprintf("vertex %d", i)).Done()
				} else {
					log.Logger().Slog().Info(fmt.Sprintf("notice %d", i))
				}
				p.mu.Lock()
				size := p.bytes
				p.mu.Unlock()
				if size > imageProgressLimit {
					t.Fatalf("queue %d bytes after %d", size, i)
				}
			}
			last := log.Logger().StartStep("final vertex")
			close(w.release)
			last.Done()
			stop()
			p.finish()
			text := w.out.text()
			if !strings.Contains(text, "this client fell behind") {
				t.Fatalf("no resync notice:\n%.2000s", text)
			}
			follower := prettylog.NewTracker(prettylog.TrackerLimits{})
			for _, r := range w.out.records {
				follower.Apply(r)
			}
			log.mu.Lock()
			want := log.tracker.Steps()
			log.mu.Unlock()
			got := follower.Steps()
			if len(got) != len(want) {
				t.Fatalf("steps %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].ID != want[i].ID || got[i].State != want[i].State {
					t.Fatalf("step %d: %+v want %+v", i, got[i], want[i])
				}
			}
			if burst == "non-detail records" && !strings.Contains(text, "notice 29999") {
				t.Fatal("recent records not replayed")
			}
		})
	}
}

// A status that carries a vertex's completion with its final output, and
// output that arrives after the completion, keep that output.
func TestImageSolveProgressKeepsOutputAtAndAfterCompletion(t *testing.T) {
	var log imageBuildLog
	root := log.Logger().StartStep("Building image test")
	steps := newSolveSteps(imageProgressTarget{step: root, log: log.Logger().Slog()})
	start := time.Now()
	end := start.Add(time.Second)
	id := digest.FromString("make")
	steps.apply(&bk.SolveStatus{Vertexes: []*bk.Vertex{{Digest: id, Name: "RUN make", Started: &start}}})
	steps.apply(&bk.SolveStatus{
		Vertexes: []*bk.Vertex{{Digest: id, Name: "RUN make", Started: &start, Completed: &end, Error: "exit code: 2"}},
		Logs:     []*bk.VertexLog{{Vertex: id, Data: []byte("FINAL COMPILER DETAIL\n")}},
	})
	steps.apply(&bk.SolveStatus{Logs: []*bk.VertexLog{{Vertex: id, Data: []byte("late line\n")}}})
	log.mu.Lock()
	all := log.tracker.Steps()
	log.mu.Unlock()
	if len(all) != 2 || all[1].State != prettylog.StepFailed || strings.Join(all[1].Tail, "|") != "FINAL COMPILER DETAIL|late line" {
		t.Fatalf("%+v", all)
	}
}

// drainProgress pops every queued record (running the sender's resync) into
// a fresh Tracker and returns it with the records.
func drainProgress(t *testing.T, p *imageProgress) (*prettylog.Tracker, []slog.Record) {
	t.Helper()
	tracker := prettylog.NewTracker(prettylog.TrackerLimits{})
	var records []slog.Record
	for range 100000 {
		raw, _ := p.next()
		if raw == nil {
			p.mu.Lock()
			pending := len(p.items)
			p.mu.Unlock()
			if pending == 0 {
				return tracker, records
			}
			continue
		}
		r, err := prettylog.UnmarshalRecord(raw)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
		tracker.Apply(r)
	}
	t.Fatal("queue never drained")
	return nil, nil
}

func detailCost(s prettylog.StepView) int64 {
	n := s.Omitted
	for _, line := range s.Tail {
		n += int64(len(line) + 1)
	}
	return n
}

// The replay sent after an overflow stays within the queue limit even for a
// detail-rich job, and accounts every detail byte it leaves out.
func TestImageProgressResyncReplayFitsTheLimit(t *testing.T) {
	var log imageBuildLog
	line := strings.Repeat("d", 255)
	for i := range 200 {
		s := log.Logger().StartStep(fmt.Sprintf("vertex %d", i))
		for range 20 {
			detailLine(s, line)
		}
		s.Done()
	}
	// A late follower's own replay is over the limit.
	p := &imageProgress{wake: make(chan struct{}, 1), sources: map[*imageBuildLog]*progressSource{}}
	stop := log.subscribe(p)
	defer stop()
	p.mu.Lock()
	dropping := p.sources[&log].dropping
	p.mu.Unlock()
	if !dropping {
		t.Fatal("no structural overflow in the fixture")
	}
	p.resyncOne()
	p.mu.Lock()
	queued := p.bytes
	p.mu.Unlock()
	if queued > imageProgressLimit {
		t.Fatalf("replay queued %d bytes", queued)
	}
	follower, _ := drainProgress(t, p)
	log.mu.Lock()
	want := log.tracker.Steps()
	log.mu.Unlock()
	got := follower.Steps()
	if len(got) != len(want) {
		t.Fatalf("%d steps, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].State != want[i].State || detailCost(got[i]) != detailCost(want[i]) {
			t.Fatalf("step %d: state %s/%s detail bytes %d/%d", i, got[i].State, want[i].State, detailCost(got[i]), detailCost(want[i]))
		}
	}
}

// Records published between the overflow and the replay's capture are
// covered by the replay exactly once; records published after it follow it.
func TestImageProgressResyncCutsSnapshotAndLiveOnce(t *testing.T) {
	var a, b imageBuildLog
	p := &imageProgress{wake: make(chan struct{}, 1), sources: map[*imageBuildLog]*progressSource{}}
	stopA, stopB := a.subscribe(p), b.subscribe(p)
	defer stopA()
	defer stopB()
	sa := a.Logger().StartStep("job a")
	sb := b.Logger().StartStep("job b")
	for i := 0; ; i++ {
		a.Logger().Slog().Info(fmt.Sprintf("filler %d %s", i, strings.Repeat("f", 200)))
		p.mu.Lock()
		over := p.sources[&a].dropping
		p.mu.Unlock()
		if over {
			break
		}
	}
	detailLine(sa, "exactly once")
	detailLine(sb, "b before capture")
	p.resyncOne() // captures a
	detailLine(sa, "after capture")
	follower, _ := drainProgress(t, p) // captures b on the way
	detailLine(sb, "b after capture")
	_, later := drainProgress(t, p)
	if len(later) != 1 || later[0].Message != "b after capture" {
		t.Fatalf("live record after the cut: %+v", later)
	}
	steps := follower.Steps()
	var gotA, gotB prettylog.StepView
	for _, s := range steps {
		switch s.Title {
		case "job a":
			gotA = s
		case "job b":
			gotB = s
		}
	}
	if strings.Join(gotA.Tail, "|") != "exactly once|after capture" || gotA.Omitted != 0 {
		t.Fatalf("job a: %+v", gotA)
	}
	if strings.Join(gotB.Tail, "|") != "b before capture" {
		t.Fatalf("job b: %+v", gotB)
	}
}

type slowWriter struct{ out recordBuffer }

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(50 * time.Microsecond)
	return w.out.Write(p)
}

// Two jobs publish concurrently into a slow client that overflows and
// resyncs repeatedly: it ends with every tracked step's final state and no
// detail line twice.
func TestImageProgressConcurrentResyncKeepsStatesWithoutDuplicates(t *testing.T) {
	w := &slowWriter{}
	p := newImageProgress(t.Context(), w)
	var a, b imageBuildLog
	stopA, stopB := a.subscribe(p), b.subscribe(p)
	var producers sync.WaitGroup
	for name, log := range map[string]*imageBuildLog{"a": &a, "b": &b} {
		producers.Go(func() {
			for i := range 90 { // both jobs fit one host Tracker (200 steps)
				s := log.Logger().StartStep(fmt.Sprintf("%s vertex %d", name, i))
				for j := range 40 {
					detailLine(s, fmt.Sprintf("%s-%d-%d %s", name, i, j, strings.Repeat("x", 300)))
				}
				for k := range 30 {
					log.Logger().Slog().Info(fmt.Sprintf("%s note %d.%d %s", name, i, k, strings.Repeat("n", 300)))
				}
				s.Done()
			}
		})
	}
	producers.Wait()
	stopA()
	stopB()
	p.finish()
	follower := prettylog.NewTracker(prettylog.TrackerLimits{})
	resyncs := 0
	for _, r := range w.out.records {
		follower.Apply(r)
		if strings.Contains(r.Message, "fell behind") {
			resyncs++
		}
	}
	if resyncs == 0 {
		t.Fatal("the fixture never overflowed with structural records")
	}
	got := map[string]prettylog.StepView{}
	for _, s := range follower.Steps() {
		got[s.Title] = s
		seen := map[string]bool{}
		for _, line := range s.Tail {
			if seen[line] {
				t.Fatalf("%s: line %.12s twice", s.Title, line)
			}
			seen[line] = true
		}
	}
	for _, log := range []*imageBuildLog{&a, &b} {
		log.mu.Lock()
		want := log.tracker.Steps()
		log.mu.Unlock()
		for _, s := range want {
			if got[s.Title].State != s.State {
				t.Fatalf("%s: %s, want %s", s.Title, got[s.Title].State, s.State)
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sources) != 0 {
		t.Fatalf("%d sources retained after their records were delivered", len(p.sources))
	}
}

// Step states with very long titles still fit: the replay compacts them,
// keeps every tracked step's state, and counts what it shortened.
func TestImageProgressResyncCompactsLongStates(t *testing.T) {
	var log imageBuildLog
	title := strings.Repeat("t", 6000)
	for i := range 200 {
		s := log.Logger().StartStep(fmt.Sprintf("%d %s", i, title))
		if i%2 == 0 {
			s.Done()
		} else {
			s.Fail(errors.New(strings.Repeat("e", 6000)))
		}
	}
	p := &imageProgress{wake: make(chan struct{}, 1), sources: map[*imageBuildLog]*progressSource{}}
	stop := log.subscribe(p)
	defer stop()
	p.resyncOne()
	p.mu.Lock()
	queued, skipped := p.bytes, p.skipped
	p.mu.Unlock()
	if queued > imageProgressLimit || skipped == 0 {
		t.Fatalf("queued %d, skipped %d", queued, skipped)
	}
	follower, _ := drainProgress(t, p)
	log.mu.Lock()
	want := log.tracker.Steps()
	log.mu.Unlock()
	got := follower.Steps()
	if len(got) != len(want) {
		t.Fatalf("%d steps, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].State != want[i].State || !strings.HasSuffix(got[i].Title, "…") || len(got[i].Title) > 256 {
			t.Fatalf("step %d: %+v", i, got[i])
		}
	}
	// With too little room even for compact states, nothing is captured:
	// the source keeps dropping and is captured once the queue is empty.
	log.mu.Lock()
	replay := log.replayLocked()
	log.mu.Unlock()
	if _, ok := encodeReplay(replay, nil, 4096, p); ok {
		t.Fatal("encoded 200 states into 4 KiB")
	}
}

// A composed image's preparing notice and its build step name the image
// and its features (not the job ID); invalid names are refused.
func TestImageProgressNamesTheImageAndItsFeatures(t *testing.T) {
	s := storeServer(t)
	s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintImages}
	backend := &wireProgressBackend{imageServiceFixture: &imageServiceFixture{jobBackend: &jobBackend{started: make(chan ImagePhase, 2), release: make(chan struct{}, 2)}}}
	var err error
	s.Images, err = NewImageJobs(t.Context(), s, backend, "test-vm", 2)
	if err != nil {
		t.Fatal(err)
	}
	refused := make(chan error, 2)
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		// Over the display bound: refused (the sender never sends this).
		_, bad := streams.Image(ctx, ImageRequest{Action: "preparing", Reference: "local/composed:latest", Features: make([]string, ProgressFeatures+1)})
		refused <- bad
		refused <- nil
		features := []string{"qa-one", "qa-two"}
		if _, err := streams.Image(ctx, ImageRequest{Action: "preparing", Reference: "local/composed:latest", Box: "worker", Features: features}); err != nil {
			return 2, err
		}
		if _, err := streams.Image(ctx, ImageRequest{Action: "acquire", Recipe: recipe("local/composed:latest")}); err != nil {
			return 2, err
		}
		_, err := streams.Image(ctx, ImageRequest{Action: "prepared"})
		return 2, err
	}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeConn(t.Context(), server, Principal{}) }()
	var stderr bytes.Buffer
	_ = Call(t.Context(), client, s.Hello, request("box.start"), ClientIO{Err: &stderr, Log: plainTraceLogger(&stderr)})
	<-done
	if e := <-refused; e == nil {
		t.Fatal("more names than the display bound were accepted")
	}
	<-refused
	// A name that is not plain text is counted, never a reason to stop.
	if text, err := progressFeatures([]string{"two words"}, 3); err != nil || text != " (features: +4 more)" {
		t.Fatalf("%q %v", text, err)
	}
	got := stderr.String()
	for _, want := range []string{
		"builder: Image for box worker (features: qa-one, qa-two)\n",
		"  Building image local/composed:latest (features: qa-one, qa-two): done",
		"builder: Image for box worker ready (features: qa-one, qa-two): done",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// A long feature list never rejects a valid recipe: the sender keeps the
// first names and counts the rest, and the notice says so.
func TestProgressFeatureNamesAbbreviateALongList(t *testing.T) {
	var names []string
	for i := range 65 {
		names = append(names, fmt.Sprintf("feature-%02d", i))
	}
	shown, more := ProgressFeatureNames(names)
	if len(shown) != ProgressFeatures || more != 49 {
		t.Fatal(len(shown), more)
	}
	text, err := progressFeatures(shown, more)
	if err != nil || !strings.HasSuffix(text, "feature-15, +49 more)") || strings.Contains(text, "feature-16") {
		t.Fatalf("%q %v", text, err)
	}
}

// Trial-2 target: a request's builds hang under one root step that ends as
// "Image for box X ready (features: …)"; no "Preparing image" lines and no
// slot-wait step reach the client, and a joining request shows the same
// steps under its own root.
func TestImageProgressBuildsEndAsOneRootStep(t *testing.T) {
	var log imageBuildLog
	var leaderOut, followerOut recordBuffer
	leader := newImageProgress(t.Context(), &leaderOut)
	follower := newImageProgress(t.Context(), &followerOut)
	leader.preparing("local/c:1 for box ok1 (features: f1)", "local/c:1 (features: f1)", "Image for box ok1", " (features: f1)")
	leader.preparing("local/base:1 for box ok1", "local/base:1", "Image for box ok1", "") // the base: no second root
	stopLeader := log.subscribe(leader)
	build := log.Logger().StartStep("Building image local/c:1 (features: f1)", prettylog.Actor(imageActor), prettylog.Executor(imageExecutor))
	run := build.Step("[1/1] RUN install")
	detailLine(run, "installing")
	follower.preparing("local/c:1 for box ok2 (features: f1)", "local/c:1 (features: f1)", "Image for box ok2", " (features: f1)")
	follower.note("Joining image build 0123456789ab")
	stopFollower := log.subscribe(follower)
	run.Done()
	build.Done()
	stopLeader()
	stopFollower()
	leader.prepared()
	follower.prepared()
	for name, out := range map[string]*recordBuffer{"leader": &leaderOut, "follower": &followerOut} {
		tracker := prettylog.NewTracker(prettylog.TrackerLimits{})
		var lines []string
		for _, r := range out.records {
			tracker.Apply(r)
			st := prettylog.StructureOf(r)
			if st.Step == "" && r.Level >= prettylog.LevelInfo {
				lines = append(lines, r.Message) // a line outside any step
			}
		}
		if len(lines) != 0 {
			t.Fatalf("%s: lines outside the root step: %q", name, lines)
		}
		steps := tracker.Steps()
		if len(steps) != 3 || steps[0].Parent != "" || steps[1].Parent != steps[0].ID || steps[2].Parent != steps[1].ID {
			t.Fatalf("%s: tree %+v", name, steps)
		}
		box := map[string]string{"leader": "ok1", "follower": "ok2"}[name]
		if root := steps[0]; root.State != prettylog.StepOK || root.Title != "Image for box "+box+" ready (features: f1)" {
			t.Fatalf("%s: root %+v", name, root)
		}
		for _, s := range steps {
			if strings.Contains(s.Title, "Waiting for an image build slot") {
				t.Fatalf("%s: slot-wait step shown", name)
			}
		}
	}
}

// A request that ends before its images are prepared leaves its root failed
// with the build's error, or uncertain when nothing failed.
func TestImageProgressRootEndsFailedOrUncertain(t *testing.T) {
	for _, fail := range []bool{true, false} {
		var log imageBuildLog
		var out recordBuffer
		p := newImageProgress(t.Context(), &out)
		p.preparing("local/c:1 for box ok1", "local/c:1", "Image for box ok1", "")
		stop := log.subscribe(p)
		build := log.Logger().StartStep("Building image local/c:1", prettylog.Actor(imageActor))
		if fail {
			build.Fail(errors.New("exit code: 3"))
		}
		stop()
		p.finish()
		tracker := prettylog.NewTracker(prettylog.TrackerLimits{})
		for _, r := range out.records {
			tracker.Apply(r)
		}
		root := tracker.Steps()[0]
		want, err := prettylog.StepUnknown, "did not finish before the command ended"
		if fail {
			want, err = prettylog.StepFailed, "exit code: 3"
		}
		if root.State != want || root.Err != err {
			t.Fatalf("fail=%v: %+v", fail, root)
		}
	}
}

// A structural overflow drops the request root's running record with
// everything else, and the request may end while a job's replay is still
// owed: the root is resent before the replay, the replayed job keeps the root
// as its parent, and the root's end comes last (failed with the job's error
// from the replay, or ready), so the build still ends as one root line.
// (After astra-architect's paused-sender probe, review-astra-10 F1.)
func TestImageProgressRootSurvivesOverflowAndOwedReplay(t *testing.T) {
	for _, end := range []string{"running", "finish", "prepared"} {
		t.Run(end, func(t *testing.T) {
			var log imageBuildLog
			build := log.Logger().StartStep("Building image local/test:1", prettylog.Actor(imageActor))
			for i := range 179 {
				s := build.Step(fmt.Sprintf("vertex%d %s", i, strings.Repeat("t", 6000)))
				s.Fail(errors.New(strings.Repeat("e", 6000)))
			}
			build.Fail(errors.New("build failed"))
			p := &imageProgress{wake: make(chan struct{}, 1), sources: map[*imageBuildLog]*progressSource{}}
			p.preparing("local/test:1 for box x", "local/test:1", "Image for box x", "")
			root := p.root
			stop := log.subscribe(p) // the queued replay overflows it
			defer stop()
			p.mu.Lock()
			overflowed := p.sources[&log].dropping && p.rootResend && len(p.items) == 0
			if end != "running" {
				p.endLocked(end == "prepared") // the request ends with the replay owed
			}
			p.mu.Unlock()
			if !overflowed {
				t.Fatal("fixture did not overflow structurally")
			}
			tracker, records := drainProgress(t, p)
			var rootAt, buildAt = -1, -1
			for i, r := range records {
				st := prettylog.StructureOf(r)
				if st.Step == root && rootAt < 0 {
					rootAt = i
				}
				if st.Step == build.ID() && buildAt < 0 {
					buildAt = i
				}
			}
			if rootAt < 0 || buildAt < rootAt {
				t.Fatalf("root resent at %d, build replayed at %d", rootAt, buildAt)
			}
			steps := tracker.Steps()
			if len(steps) != 181 {
				t.Fatalf("recovered %d steps, want root + build + 179 vertices", len(steps))
			}
			for _, s := range steps {
				switch {
				case s.ID == root:
					if s.Parent != "" {
						t.Fatalf("root has parent %q", s.Parent)
					}
				case s.ID == build.ID():
					if s.Parent != root {
						t.Fatalf("build parent %q, want the request root %q", s.Parent, root)
					}
				case s.Parent != build.ID():
					t.Fatalf("vertex %s parent %q", s.ID, s.Parent)
				}
			}
			view := steps[0]
			last := prettylog.StructureOf(records[len(records)-1])
			switch end {
			case "running":
				if view.ID != root || view.State != prettylog.StepRunning {
					t.Fatalf("root %+v", view)
				}
			case "finish":
				if view.State != prettylog.StepFailed || view.Err != "build failed" || last.Step != root {
					t.Fatalf("root %+v, last record %+v", view, last)
				}
			case "prepared":
				if view.State != prettylog.StepOK || view.Title != "Image for box x ready" || last.Step != root {
					t.Fatalf("root %+v, last record %+v", view, last)
				}
			}
		})
	}
}

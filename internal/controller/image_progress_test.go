package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	bk "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	digest "github.com/opencontainers/go-digest"
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

func TestImageProgressImmediateAndQuietFeedback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out progressBuffer
		p := newImageProgress(t.Context(), &out)
		p.preparing("local/test:latest for box worker")
		synctest.Wait()
		if !strings.Contains(out.text(), "Preparing image local/test:latest for box worker") {
			t.Fatal(out.text())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := strings.Count(out.text(), "Still preparing image"); n != 2 {
			t.Fatalf("quiet notices=%d: %s", n, out.text())
		}
		_, _ = io.WriteString(p, "actual build output\n")
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		p.finish()
		text := out.text()
		time.Sleep(5 * time.Second)
		if out.text() != text {
			t.Fatal("progress after finish")
		}
	})
}

func TestImageProgressJoinSnapshotAndLiveAreOrdered(t *testing.T) {
	var log imageBuildLog
	log.setPhase("Building image test-job")
	_, _ = io.WriteString(&log, "before join\n")
	var out progressBuffer
	p := newImageProgress(t.Context(), &out)
	stop := log.subscribe(p)
	_, _ = io.WriteString(&log, "after join\n")
	stop()
	_, _ = io.WriteString(&log, "after unsubscribe\n")
	p.finish()
	if got := out.text(); got != "Building image test-job\nbefore join\nafter join\n" {
		t.Fatal(got)
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
	_, _ = io.WriteString(&log, "blocked write\n")
	take(t, blocked.entered)
	for range 3 {
		_, _ = log.Write(bytes.Repeat([]byte("x"), imageProgressLimit))
	}
	var out progressBuffer
	fast := newImageProgress(t.Context(), &out)
	stopFast := log.subscribe(fast)
	_, _ = io.WriteString(&log, "final live log\n")
	stopFast()
	fast.finish()
	if got := out.text(); !strings.Contains(got, "final live log") || !strings.Contains(got, "omitted") && !strings.Contains(got, "skipped") {
		t.Fatal("missing live log or truncation notice")
	}
	slow.mu.Lock()
	size := slow.bytes
	slow.mu.Unlock()
	if size > imageProgressLimit {
		t.Fatal(size)
	}
	stopSlow()
	cancel()
	slow.finish()
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.tail) > imageProgressLimit || len(log.subscribers) != 0 {
		t.Fatal("retained unbounded output or subscriber")
	}
}

func TestImageProgressOverflowIsVisibleWhenReaderResumes(t *testing.T) {
	var log imageBuildLog
	p := &imageProgress{wake: make(chan struct{}, 1)}
	stop := log.subscribe(p)
	for range 4 {
		_, _ = log.Write(bytes.Repeat([]byte("z"), imageProgressLimit))
	}
	stop()
	data, _ := p.next()
	if !strings.Contains(string(data), "skipped") {
		t.Fatal("silent overflow")
	}
}

func TestImageProgressJoinAndPublishRace(t *testing.T) {
	var log imageBuildLog
	var writers sync.WaitGroup
	writers.Go(func() {
		for range 200 {
			_, _ = io.WriteString(&log, "event\n")
		}
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
			ctx := context.WithValue(t.Context(), imageBuildLogKey{}, &log)
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
				status <- &bk.SolveStatus{Vertexes: []*bk.Vertex{{Digest: id, Name: "RUN echo output", Started: &start}}}
				status <- &bk.SolveStatus{Logs: []*bk.VertexLog{{Vertex: id, Stream: 1, Data: []byte("stdout actual\n"), Timestamp: start}, {Vertex: id, Stream: 2, Data: []byte("stderr actual\n"), Timestamp: start}}}
				status <- &bk.SolveStatus{Vertexes: []*bk.Vertex{{Digest: id, Name: "RUN echo output", Started: &start, Completed: &end, Cached: true}}, Warnings: []*bk.VertexWarning{{Short: []byte("test warning"), Detail: [][]byte{[]byte("warning detail")}}}}
				return want, cause
			}), bk.SolveOpt{})
			if got != want || !errors.Is(err, cause) {
				t.Fatal(got, err)
			}
			text := string(log.tail)
			for _, want := range []string{"RUN echo output", "stdout actual", "stderr actual", "CACHED", "test warning", "warning detail"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q: %s", want, text)
				}
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
	_, _ = io.WriteString(imageBuildOutput(ctx), "actual step output\n")
	return b.jobBackend.Build(ctx, r, p)
}
func TestImageProgressFollowerKeepsLogsAfterLeaderLeaves(t *testing.T) {
	m, b := newJobs(t, 2)
	m.backend = &progressJobBackend{b}
	firstCtx, cancel := context.WithCancel(t.Context())
	var one, two progressBuffer
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
	_, _ = io.WriteString(&job.log, "log after leader cancellation\n")
	b.release <- struct{}{}
	got := take(t, c)
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.image.Release()
	follower.finish()
	for _, want := range []string{"Joining image build", "actual step output", "log after leader cancellation"} {
		if !strings.Contains(two.text(), want) {
			t.Fatalf("missing %q: %s", want, two.text())
		}
	}
}

// Ordinary stderr frames must finish before the exit frame; neither machine
// stdout nor the private managed-view path carries any build diagnostics.
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
			err = invoke(t, s, request("box.start"), nil, &stdout, &stderr)
			if fail {
				if err == nil || !strings.Contains(err.Error(), "actual solve failure") {
					t.Fatal(err)
				}
				if stdout.Len() != 0 {
					t.Fatal(stdout.String())
				}
			} else if err != nil || stdout.String() != "{\"ok\":true}\n" {
				t.Fatal(err, stdout.String())
			}
			for _, want := range []string{"Preparing image local/base:latest for box worker", "RUN actual", "last build log"} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("missing %q: %s", want, stderr.String())
				}
			}
			if !fail && !strings.Contains(stderr.String(), "Preparing image local/composed:latest") {
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
	_, _ = io.WriteString(imageBuildOutput(ctx), "RUN actual\nlast build log\n")
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
		go func() { done <- Call(t.Context(), client, s.Hello, request("box.start"), ClientIO{Err: &stderr}) }()
		<-b.entered
		synctest.Wait()
		if !strings.Contains(stderr.text(), "Preparing image local/base:latest for box worker") {
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
	// No read of stderr: the sole sender is blocked on this public connection.
	_ = client.Close()
	take(t, done)
}

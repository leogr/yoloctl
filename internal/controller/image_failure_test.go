package controller

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type failedImageBackend struct {
	*imageServiceFixture
	failure error
	calls   int
}

func (b *failedImageBackend) Build(ctx context.Context, _ ImageRecipe, _ ImagePhase) (PinnedImage, error) {
	b.calls++
	return PinnedImage{}, classifyImageSolveError(ctx, b.failure)
}
func TestReturnedSolveFailureKeepsRealErrorAndFailedReceipt(t *testing.T) {
	m, b := newJobs(t, 2)
	backend := &failedImageBackend{imageServiceFixture: &imageServiceFixture{jobBackend: b}, failure: status.Error(codes.Unknown, "Dockerfile RUN failed with exit code 23")}
	m.backend = backend
	for range 2 {
		_, err := m.Acquire(t.Context(), recipe("failed"))
		var interrupted *ImageInterrupted
		var failed *ImageBuildFailure
		if err == nil || !strings.Contains(err.Error(), "exit code 23") || errors.As(err, &interrupted) || !errors.As(err, &failed) {
			t.Fatal("failure was hidden as an interruption", err)
		}
	}
	if backend.calls != 2 {
		t.Fatal("failure retained capacity or same-input admission", backend.calls)
	}
	records, err := m.server.Receipts.List()
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
	for _, r := range records {
		if r.State != "complete" || r.Status != 2 || r.Completion == nil || r.Completion.Status != 2 || r.abandonedImage() || r.Image.Residual || r.unresolved() {
			t.Fatal("known failure recorded as uncertain/abandoned", r)
		}
	}
	facts, err := m.server.Receipts.ImageOrphans()
	if err != nil || len(facts) != 0 {
		t.Fatal("known failure invented an orphan", facts, err)
	}
}
func TestSolveCancellationAndLostTransportDoNotBecomeKnownFailures(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, status.Error(codes.Unavailable, "transport lost"), status.Error(codes.DeadlineExceeded, "reply lost"), status.Error(codes.Canceled, "solve cancelled"), status.Error(codes.DataLoss, "reply corrupt"), status.Error(codes.Internal, "stream lost without trailers")} {
		var failed *ImageBuildFailure
		if errors.As(classifyImageSolveError(t.Context(), err), &failed) {
			t.Fatal("unknown outcome declared failed", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var failed *ImageBuildFailure
	if errors.As(classifyImageSolveError(ctx, errors.New("cancelled local session")), &failed) {
		t.Fatal("caller cancellation became failure")
	}
}
func TestSolveFailureReachesRequestWithoutRetryShortcut(t *testing.T) {
	s := storeServer(t)
	s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintImages}
	backend := &failedImageBackend{imageServiceFixture: &imageServiceFixture{jobBackend: &jobBackend{}}, failure: status.Error(codes.Unknown, "syntax frontend denied by policy")}
	var err error
	s.Images, err = NewImageJobs(t.Context(), s, backend, "vm", 2)
	if err != nil {
		t.Fatal(err)
	}
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		_, err := streams.Image(ctx, ImageRequest{Action: "acquire", Recipe: recipe("failure")})
		return 2, err
	}
	req := request("box.start")
	var output strings.Builder
	err = invoke(t, s, req, nil, io.Discard, &output)
	var interrupted *ImageInterrupted
	if err == nil || errors.As(err, &interrupted) || !strings.Contains(output.String()+err.Error(), "syntax frontend denied by policy") {
		t.Fatal("request hid failure or offered retry shortcut", output.String(), err)
	}
	r, err := s.Receipts.Read(req.ID)
	if err != nil || r.EffectPhase != EffectWaiting || r.State == "uncertain" {
		t.Fatal("failure changed the unpermitted request outcome", r, err)
	}
}

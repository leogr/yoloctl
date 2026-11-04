package controller

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAstraTransportInternalMustNotBecomeFailedCompletion(t *testing.T) {
	for _, message := range []string{
		"server closed the stream without sending trailers",
		"stream terminated by RST_STREAM with error code: INTERNAL_ERROR",
	} {
		t.Run(message, func(t *testing.T) {
			m, b := newJobs(t, 2)
			m.backend = &failedImageBackend{imageServiceFixture: &imageServiceFixture{jobBackend: b}, failure: status.Error(codes.Internal, message)}
			_, err := m.Acquire(t.Context(), recipe("transport"))
			var failed *ImageBuildFailure
			records, readErr := m.server.Receipts.List()
			if readErr != nil || len(records) != 1 {
				t.Fatal(records, readErr)
			}
			if errors.As(err, &failed) || records[0].Completion != nil {
				t.Fatalf("lost transport falsely became known failure: err=%v state=%s completion=%+v", err, records[0].State, records[0].Completion)
			}
			if !strings.Contains(err.Error(), message) {
				t.Fatal("lost transport cause hidden", err)
			}
			r := records[0]
			if r.State != "uncertain" || !r.abandonedImage() || !r.Image.Residual {
				t.Fatal("lost transport lacks abandonment/orphan accounting", r)
			}
			// Unknown execution never reclaims admission via a made-up failure.
			// Option A, not a stop proof, allows this new explicit attempt now.
			_, again := m.Acquire(t.Context(), recipe("transport"))
			if again == nil || !strings.Contains(again.Error(), message) {
				t.Fatal("same-input admission did not reopen", again)
			}

		})
	}
}

func TestImageInterruptionPreservesCauseWithoutDuplicatingWireMessage(t *testing.T) {
	first := &ImageInterrupted{JobID: "private-job", Retryable: true, Cause: status.Error(codes.Internal, "server closed the stream without sending trailers")}
	wire := &ImageInterrupted{JobID: first.JobID, Retryable: first.Retryable, Cause: errors.New(first.Error())}
	if wire.Error() != first.Error() {
		t.Fatal("wire reconstruction duplicated the diagnostic", wire.Error())
	}
}

package controller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

type orphanLogSink struct {
	records chan slog.Record
	failure error
}

func (s *orphanLogSink) Emit(e prettylog.Emission) error {
	if e.Kind == prettylog.EmissionRecord {
		s.records <- e.Record
	}
	return s.failure
}

func TestImageOrphanWarningDoesNotChangeEvidenceOrResult(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "failed-log-sink"}[broken], func(t *testing.T) {
			sink := &orphanLogSink{records: make(chan slog.Record, 4)}
			if broken {
				sink.failure = errors.New("diagnostic sink failed")
			}
			logger := prettylog.New(io.Discard, prettylog.Options{Verbosity: prettylog.Trace, Emit: sink})
			restore := logging.SetLogger(logger)
			defer restore()
			defer func() { _ = logger.Close() }()
			jobs, backend := newJobs(t, 1)
			backend.ignoreCancellation = true
			defer func() { backend.release <- struct{}{} }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			pending := acquire(jobs, ctx, recipe("logged-orphan"))
			phase := take(t, backend.started)
			cancel()
			result := take(t, pending)
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("cancellation result changed: %v", result.err)
			}
			record := take(t, sink.records)
			st := prettylog.StructureOf(record)
			if record.Level != slog.LevelWarn || st.Actor != logging.Builder || st.Outcome != "" || !strings.Contains(record.Message, phase.JobID) || !strings.Contains(record.Message, "no acknowledgment required") {
				t.Fatalf("record=%+v structure=%+v", record, st)
			}
			facts, err := jobs.server.Receipts.ImageOrphans()
			if err != nil || len(facts) != 1 || facts[0].Phase.JobID != phase.JobID || facts[0].Outcome != "unknown" || !facts[0].Phase.OrphanRecorded {
				t.Fatalf("orphan evidence changed: %+v %v", facts, err)
			}
			if !errors.Is(logger.Err(), sink.failure) {
				t.Fatalf("sink error %v want %v", logger.Err(), sink.failure)
			}
		})
	}
}

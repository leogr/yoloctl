package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

type diagnosticRecorder struct {
	statuses []int
	failure  error
}

func (*diagnosticRecorder) ExpandEffects(context.Context, []controller.Resource) error { return nil }
func (*diagnosticRecorder) RecordDocument(context.Context, controller.DocumentIntent) error {
	return nil
}
func (r *diagnosticRecorder) CompleteEffects(_ context.Context, status int) error {
	r.statuses = append(r.statuses, status)
	return r.failure
}

func TestWorkerCompletionDiagnosticIsNotAnOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result error
		status int
	}{
		{"success", nil, 0}, {"failure", errors.New("operation failed"), 2}, {"exit", &session.ExitError{Status: 7}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, _ := captureCLIRecords(t)
			r := &diagnosticRecorder{failure: errors.New("receipt completion denied")}
			ctx := controller.WithEffectRecorder(t.Context(), r)
			recordWorkerCompletion(ctx, tc.result)
			records := readCLIRecords(t, wire)
			if len(r.statuses) != 1 || r.statuses[0] != tc.status || len(records) != 1 {
				t.Fatalf("statuses=%v records=%v", r.statuses, records)
			}
			st := prettylog.StructureOf(records[0])
			if records[0].Level != slog.LevelError || st.Actor != logging.Controller || st.Err != r.failure.Error() || st.Outcome != "" {
				t.Fatalf("diagnostic %+v", st)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		result error
		cancel bool
	}{
		{"uncertain", &controller.EffectsUncertain{Cause: errors.New("lost reply")}, false}, {"canceled", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, _ := captureCLIRecords(t)
			r := &diagnosticRecorder{failure: errors.New("must not record")}
			ctx, cancel := context.WithCancel(controller.WithEffectRecorder(t.Context(), r))
			defer cancel()
			if tc.cancel {
				cancel()
			}
			recordWorkerCompletion(ctx, tc.result)
			if len(r.statuses) != 0 || len(readCLIRecords(t, wire)) != 0 {
				t.Fatal("unknown/canceled completion was recorded or logged")
			}
		})
	}
}

type diagnosticRecordSink chan slog.Record

func (c diagnosticRecordSink) Emit(e prettylog.Emission) error {
	if e.Kind == prettylog.EmissionRecord {
		c <- e.Record
	}
	return nil
}

func TestAuditReconciliationDeferredUsesFileLogger(t *testing.T) {
	records := make(diagnosticRecordSink, 8)
	l := prettylog.New(io.Discard, prettylog.Options{Verbosity: prettylog.Trace, Emit: records})
	restore := logging.SetLogger(l)
	defer restore()
	defer func() { _ = l.Close() }()
	// Without a native context the real reconciliation returns its existing
	// generic failure. No service, VM, filesystem repair or retry is invoked.
	stop := startAuditRetention(t.Context(), &controller.Server{})
	defer stop()
	select {
	case record := <-records:
		st := prettylog.StructureOf(record)
		if record.Message != "Reconciliation deferred" || record.Level != slog.LevelWarn || st.Actor != logging.Audit || st.Outcome != "" || st.Err != "" {
			t.Fatalf("record=%+v structure=%+v", record, st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup reconciliation diagnostic was not emitted")
	}
}

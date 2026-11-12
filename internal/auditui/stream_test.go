package auditui

import (
	"context"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func streamEvent(rule, user string) audit.Event {
	return audit.Event{Time: time.Unix(100, 0), Rule: rule, Severity: "Notice",
		Container: "host", Executable: "/bin/touch", Result: "SUCCESS", Path: "/tmp/test",
		OutputFields: map[string]string{"proc.name": "touch", "user.name": user}}
}

func TestAuditRowsRepeatOwnsFirstEventAcrossNextArrivalAndEviction(t *testing.T) {
	sink := newRowSink(3)
	stream := newEventStream(t.Context(), prettylog.Normal, sink, sink.close)
	t.Cleanup(func() { _ = stream.close() })
	for _, e := range []audit.Event{streamEvent("A", "alice"), streamEvent("A", "bob"), streamEvent("B", "carol")} {
		if err := stream.add(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	rows := sink.snapshot()
	if len(rows) != 3 || rows[1].kind != prettylog.EmissionRepeatSummary || rows[1].repeat != 1 ||
		rows[1].owner != rows[0].owner || rows[1].event != rows[0].event || rows[1].event.OutputFields["user.name"] != "alice" ||
		rows[2].event.Rule != "B" || rows[2].event.OutputFields["user.name"] != "carol" {
		t.Fatalf("wrong selected summary source: %+v", rows)
	}
	// Evict the original record but retain the summary's immutable snapshot.
	if err := stream.add(t.Context(), streamEvent("C", "dave")); err != nil {
		t.Fatal(err)
	}
	rows = sink.snapshot()
	if rows[0].kind != prettylog.EmissionRepeatSummary || rows[0].event.OutputFields["user.name"] != "alice" {
		t.Fatal("summary retargeted after owner record eviction")
	}
	if err := stream.add(t.Context(), streamEvent("D", "eve")); err != nil {
		t.Fatal(err)
	}
	rows = sink.snapshot()
	sink.mu.Lock()
	owners := len(sink.owners)
	sink.mu.Unlock()
	if owners > len(rows) {
		t.Fatal("owner map escaped row bound")
	}
	// A delayed/foreign summary cannot attach to any newly retained row.
	if err := sink.Emit(prettylog.Emission{Kind: prettylog.EmissionRepeatSummary, Owner: 1, Repeat: 99}); err != nil {
		t.Fatal(err)
	}
	if len(sink.snapshot()) != len(rows) || sink.snapshot()[0].event.Rule != "B" {
		t.Fatal("evicted owner resolved to a new row")
	}
	if err := sink.Emit(prettylog.Emission{Kind: prettylog.EmissionRecord, Owner: 1, Record: eventRecord(streamEvent("fake", "mallory"))}); err == nil {
		t.Fatal("reused owner admitted")
	}
}

func TestAuditRowRetentionAndWakeAreBounded(t *testing.T) {
	sink := newRowSink(7)
	stream := newEventStream(t.Context(), prettylog.Normal, sink, sink.close)
	t.Cleanup(func() { _ = stream.close() })
	for i := range 1000 {
		e := streamEvent("A", "user")
		e.Path = time.Unix(int64(i), 0).String()
		if err := stream.add(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	sink.mu.Lock()
	owners := len(sink.owners)
	sink.mu.Unlock()
	if len(sink.snapshot()) != 7 || owners != 7 || len(sink.wake) != 1 {
		t.Fatal("unbounded display retention/wake")
	}
}

func TestAuditCloseDrainsOnceBeforeDetachingAndCannotDeadlock(t *testing.T) {
	sink := newRowSink(10)
	ctx, cancel := context.WithCancel(t.Context())
	stream := newEventStream(ctx, prettylog.Normal, sink, sink.close)
	_ = stream.add(ctx, streamEvent("A", "first"))
	_ = stream.add(ctx, streamEvent("A", "repeat"))
	closed := make(chan error, 1)
	go func() { closed <- stream.close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close blocked on a UI callback")
	}
	_ = stream.close()
	cancel()
	rows := sink.snapshot()
	if len(rows) != 2 || rows[1].kind != prettylog.EmissionRepeatSummary || rows[1].repeat != 1 || rows[1].event.OutputFields["user.name"] != "first" {
		t.Fatal("close dropped/duplicated/retargeted pending summary")
	}
	if err := stream.add(t.Context(), streamEvent("B", "late")); err == nil {
		t.Fatal("closed view accepted a late event")
	}
	select {
	case <-sink.done:
	default:
		t.Fatal("sink did not detach")
	}
}

func TestAuditParentCancellationSettlesPendingTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	sink := newRowSink(10)
	stream := newEventStream(ctx, prettylog.Normal, sink, sink.close)
	_ = stream.add(ctx, streamEvent("A", "first"))
	_ = stream.add(ctx, streamEvent("A", "repeat"))
	cancel()
	select {
	case <-stream.closedDone:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled view retained its fold timer")
	}
	rows := sink.snapshot()
	if len(rows) != 2 || rows[1].owner != rows[0].owner {
		t.Fatal("cancel lost source ownership")
	}
}

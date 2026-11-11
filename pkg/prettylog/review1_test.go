package prettylog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Review 1 F1: empty and short executor lines are bounded like any other.
func TestTrackerBoundsEmptyAndShortLines(t *testing.T) {
	for _, stream := range []string{"empty", "mixed"} {
		lim := TrackerLimits{Steps: 2, StepDetail: 16, TotalDetail: 24}
		tr := NewTracker(lim)
		var wire bytes.Buffer
		l := New(&wire, Options{Format: FormatWire, Verbosity: Quiet})
		s := l.StartStep("work")
		for i := 0; i < 10000; i++ {
			line := "\n"
			if stream == "mixed" && i%2 == 0 {
				line = "ab\n"
			}
			_, _ = io.WriteString(s.Detail(), line)
		}
		s.Done()
		rd := NewWireReader(&wire)
		for {
			r, err := rd.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			tr.Apply(r)
		}
		ts := tr.steps[s.ID()]
		if ts.bytes > lim.StepDetail || len(ts.lines) > lim.StepDetail || tr.total > lim.TotalDetail {
			t.Fatalf("%s: retained %d lines, %d bytes, total %d", stream, len(ts.lines), ts.bytes, tr.total)
		}
		v := tr.Steps()[0]
		var all int64 = 10000
		if stream == "mixed" {
			all = 5000*3 + 5000*1
		}
		shown := int64(0)
		for _, line := range v.Tail {
			shown += int64(len(line) + 1)
		}
		if v.Omitted+shown != all {
			t.Fatalf("%s: omitted %d + shown %d != %d", stream, v.Omitted, shown, all)
		}
		again := NewTracker(lim)
		for _, r := range tr.Snapshot() {
			again.Apply(r)
		}
		if !reflect.DeepEqual(again.Steps(), tr.Steps()) {
			t.Fatalf("%s: snapshot differs", stream)
		}
	}
}

// Review 1 F2: the last occurrence of a reserved key decides, even when its
// type is wrong, in every consumer and in both duplicate orders.
func TestLastReservedOccurrenceDecidesEverywhere(t *testing.T) {
	invalidLast := recAt(t0, LevelError, "failure", KeyOutcome, "ok", KeyOutcome, 7)
	validLast := recAt(t0, LevelError, "failure", KeyOutcome, 7, KeyOutcome, "ok")
	check := func(name string, r slog.Record) {
		t.Helper()
		st := StructureOf(r)
		b, err := MarshalRecord(r, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := UnmarshalRecord(b)
		if err != nil {
			t.Fatal(err)
		}
		if got := StructureOf(decoded); got != st {
			t.Fatalf("%s: wire changed structure %+v -> %+v", name, st, got)
		}
		if strings.Count(string(b), `"outcome"`) != 1 {
			t.Fatalf("%s: encoded %s", name, b)
		}
	}
	if st := StructureOf(invalidLast); st.Outcome != "" || st.Kind != KindError {
		t.Fatalf("invalid last: %+v", st)
	}
	if st := StructureOf(validLast); st.Outcome != "ok" || st.Kind != KindOK {
		t.Fatalf("valid last: %+v", st)
	}
	check("invalid last", invalidLast)
	check("valid last", validLast)

	// The invalid last value is an ordinary field; earlier duplicates vanish.
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Verbose})
	_ = l.Handle(context.Background(), invalidLast)
	if b.String() != "error: failure outcome=7\n" {
		t.Fatalf("display %q", b.String())
	}
	// With-derived attrs come first, so the record's value wins either way.
	for _, c := range []struct {
		with, rec any
		want      string
	}{
		{"ok", 7, ""}, {7, "ok", "ok"},
	} {
		var out bytes.Buffer
		lg := New(&out, Options{Format: FormatWire, Verbosity: Trace})
		lg.Slog().With(KeyOutcome, c.with).Error("x", KeyOutcome, c.rec)
		r, err := NewWireReader(&out).Next()
		if err != nil {
			t.Fatal(err)
		}
		if got := StructureOf(r).Outcome; got != c.want {
			t.Fatalf("with %v record %v: outcome %q", c.with, c.rec, got)
		}
	}
	// A stale detail flag cannot make a transition lossy.
	tr := recAt(t0, LevelInfo, "s", KeyDetail, true, KeyStep, "a.1", KeyState, "running", KeyDetail, "no")
	if IsLossy(tr) || StructureOf(tr).Detail {
		t.Fatalf("stale detail flag kept: %+v", StructureOf(tr))
	}
}

// Review 1 F3: exactly one JSON object, optionally surrounded by whitespace.
func TestUnmarshalRecordRejectsTrailingData(t *testing.T) {
	b, err := MarshalRecord(recAt(t0, LevelInfo, "ok", "d", time.Second), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"}", "]", "{}", "1", `"x"`, "junk", " }"} {
		if _, err := UnmarshalRecord(append(append([]byte{}, b...), tail...)); err == nil {
			t.Errorf("accepted trailing %q", tail)
		}
	}
	if _, err := UnmarshalRecord(append(append([]byte(" \n"), b...), " \n\t"...)); err != nil {
		t.Errorf("rejected surrounding whitespace: %v", err)
	}
	bad := bytes.Replace(b, []byte(`{"d":1000000000}`), []byte(`{"d":1000000000}}`), 1)
	if _, err := UnmarshalRecord(bad); err == nil {
		t.Error("accepted a malformed typed value")
	}
}

// Review 1 F4: the default fold key distinguishes every different record.
func TestDefaultFoldKeyIsUnambiguous(t *testing.T) {
	pairs := [][2]slog.Record{
		{recAt(t0, LevelWarn, "message\x00key=value"), recAt(t0, LevelWarn, "message", "key", "value")},
		{recAt(t0, LevelWarn, "same", "value", 1), recAt(t0, LevelWarn, "same", "value", "1")},
		{recAt(t0, LevelWarn, "a:b"), recAt(t0, LevelWarn, "a", ":b", "")},
		{recAt(t0, LevelWarn, "m", "k", "1:v"), recAt(t0, LevelWarn, "m", "k", "1", "v", "")},
		{recAt(t0, LevelWarn, "m", "e", errors.New("x")), recAt(t0, LevelWarn, "m", "e", fmt.Stringer(time.Duration(0)))},
		{recAt(t0, LevelWarn, "m"), recAt(t0, LevelError, "m")},
	}
	for i, p := range pairs {
		a, _ := DefaultFoldKey(p[0])
		b, _ := DefaultFoldKey(p[1])
		if a == b {
			t.Errorf("pair %d shares key %q", i, a)
		}
	}
	same1, _ := DefaultFoldKey(recAt(t0, LevelWarn, "m", "k", 1))
	same2, _ := DefaultFoldKey(recAt(t0.Add(time.Second), LevelWarn, "m", "k", 1))
	if same1 != same2 {
		t.Error("identical records at different times differ")
	}
	var out bytes.Buffer
	l := New(&out, Options{Format: FormatPlain, Verbosity: Verbose, Fold: FoldOptions{Window: time.Hour}})
	_ = l.Handle(context.Background(), pairs[0][0])
	_ = l.Handle(context.Background(), pairs[0][1])
	_ = l.Close()
	if strings.Count(out.String(), "warning:") != 2 {
		t.Fatalf("different warnings folded: %q", out.String())
	}
}

// Review 2 F2: a non-string value under a reserved key stays an ordinary
// field across the wire, for direct and derived records.
func TestWrongTypedReservedValueStaysFieldOnWire(t *testing.T) {
	direct := recAt(t0, LevelError, "failure", KeyOutcome, "failed", slog.Any(KeyOutcome, errors.New("ok")))
	var derived bytes.Buffer
	New(&derived, Options{Format: FormatWire, Verbosity: Trace}).Slog().
		With(KeyState, "running").Error("failure", KeyOutcome, errors.New("ok"), KeyStep, fmt.Stringer(Opaque("a.1")))
	fromDerived, err := NewWireReader(&derived).Next()
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]slog.Record{"direct": direct, "derived": fromDerived} {
		before := StructureOf(r)
		b, err := MarshalRecord(r, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		after, err := UnmarshalRecord(b)
		if err != nil {
			t.Fatal(err)
		}
		if got := StructureOf(after); got != before || got.Outcome != "" || got.Step != "" {
			t.Fatalf("%s: structure %+v became %+v (%s)", name, before, got, b)
		}
		var out bytes.Buffer
		_ = New(&out, Options{Format: FormatPlain, Verbosity: Verbose}).Handle(context.Background(), after)
		if !strings.Contains(out.String(), "outcome=ok") {
			t.Fatalf("%s: field not shown: %q", name, out.String())
		}
	}
}

// Review 3 F2: non-finite floats also stay non-strings across the wire.
func TestNonFiniteFloatStaysFieldOnWire(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		r := recAt(t0, LevelInfo, "x", slog.Float64(KeyStep, f), slog.Float64("ratio", f))
		b, err := MarshalRecord(r, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalRecord(b)
		if err != nil {
			t.Fatal(err)
		}
		if st := StructureOf(got); st.Step != "" || st != StructureOf(r) {
			t.Fatalf("%v: structure %+v (%s)", f, st, b)
		}
		var out bytes.Buffer
		_ = New(&out, Options{Format: FormatPlain, Verbosity: Verbose}).Handle(context.Background(), got)
		if !strings.Contains(out.String(), "step="+fmt.Sprint(f)) {
			t.Fatalf("%v: %q", f, out.String())
		}
	}
}

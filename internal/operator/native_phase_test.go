package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func nativePhaseTestBegin(t *testing.T) (*NativePhaseCollector, context.Context) {
	t.Helper()
	c := NewNativePhaseCollector()
	ctx := WithNativePhaseCollector(t.Context(), c)
	if NativePhaseActive(ctx) {
		t.Fatal("unbegun collector exposed a span")
	}
	ctx = BeginNativePhase(ctx, "coder", strings.Repeat("a", 64), "g"+strings.Repeat("b", 48))
	if !NativePhaseActive(ctx) {
		t.Fatal("original span was not installed")
	}
	return c, ctx
}

func nativePhaseTestRecord(ctx context.Context, kind NativePhaseKind, outcome NativePhaseOutcome, generation string) {
	RecordNativePhase(ctx, NativePhaseEvent{Kind: kind, Outcome: outcome, Generation: generation})
}

func nativePhaseTestLaunch(ctx context.Context, role NativePhaseRole, generation string, through NativePhaseKind) context.Context {
	ctx = WithNativePhaseRole(ctx, role)
	nativePhaseTestRecord(ctx, NativePhaseLaunchAttempt, NativePhasePending, "")
	for _, kind := range []NativePhaseKind{NativePhasePrepareReturn, NativePhaseEngineStartReturn, NativePhaseActivationReturn, NativePhaseSSHReturn} {
		nativePhaseTestRecord(ctx, kind, NativePhaseOK, generation)
		if kind == through {
			break
		}
	}
	return ctx
}

func nativePhaseTestDecode(t *testing.T, c *NativePhaseCollector) (NativePhaseTranscript, []byte) {
	t.Helper()
	raw, err := c.MarshalFinal()
	if err != nil {
		t.Fatal(err)
	}
	trace, err := DecodeNativePhaseTranscript(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > NativePhaseMaxBytes || len(trace.Events) > NativePhaseMaxEvents {
		t.Fatal("capture exceeded its fixed bound")
	}
	return trace, raw
}

func nativePhaseTestSuccess(t *testing.T) NativePhaseTranscript {
	t.Helper()
	c, ctx := nativePhaseTestBegin(t)
	nativePhaseTestRecord(ctx, NativePhaseOldStopReturn, NativePhaseOK, "")
	desired := nativePhaseTestLaunch(ctx, NativePhaseDesired, "g"+strings.Repeat("c", 48), NativePhaseSSHReturn)
	nativePhaseTestRecord(desired, NativePhaseLaunchReturn, NativePhaseOK, "")
	FinishNativePhase(ctx, NativePhaseOK)
	trace, _ := nativePhaseTestDecode(t, c)
	if !trace.Terminal.Complete || trace.Terminal.Disposition != NativePhaseOK || NativePhaseActive(ctx) {
		t.Fatal("successful callback coverage was not complete")
	}
	return trace
}

func TestNativePhaseClosedFailedPrefixesAndActualLaunchReturns(t *testing.T) {
	_ = nativePhaseTestSuccess(t)
	for _, mode := range []string{"preflight", "prepare-failed", "before-start-failed", "start-uncertain", "ssh-failed", "cleanup-failed", "rollback-prepare-failed"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx := nativePhaseTestBegin(t)
			final := NativePhaseFailed
			if mode != "preflight" {
				nativePhaseTestRecord(ctx, NativePhaseOldStopReturn, NativePhaseOK, "")
				desired := WithNativePhaseRole(ctx, NativePhaseDesired)
				generation := "g" + strings.Repeat("c", 48)
				if mode == "prepare-failed" || mode == "rollback-prepare-failed" {
					nativePhaseTestRecord(desired, NativePhaseLaunchAttempt, NativePhasePending, "")
					nativePhaseTestRecord(desired, NativePhasePrepareReturn, NativePhaseFailed, "")
					nativePhaseTestRecord(desired, NativePhaseLaunchReturn, NativePhaseFailed, "")
				} else {
					desired = nativePhaseTestLaunch(ctx, NativePhaseDesired, generation, NativePhasePrepareReturn)
					if mode == "start-uncertain" {
						nativePhaseTestRecord(desired, NativePhaseEngineStartReturn, NativePhaseUncertain, generation)
						final = NativePhaseUncertain
					} else {
						if mode != "before-start-failed" {
							nativePhaseTestRecord(desired, NativePhaseEngineStartReturn, NativePhaseOK, generation)
							nativePhaseTestRecord(desired, NativePhaseActivationReturn, NativePhaseOK, generation)
							nativePhaseTestRecord(desired, NativePhaseSSHReturn, NativePhaseFailed, generation)
						}
						cleanup := NativePhaseOK
						if mode == "cleanup-failed" {
							cleanup, final = NativePhaseFailed, NativePhaseUncertain
						}
						nativePhaseTestRecord(desired, NativePhaseGenerationCleanupReturn, cleanup, generation)
					}
					nativePhaseTestRecord(desired, NativePhaseLaunchReturn, final, "")
				}
				if final != NativePhaseUncertain {
					if mode == "rollback-prepare-failed" {
						rollback := WithNativePhaseRole(ctx, NativePhaseRollback)
						nativePhaseTestRecord(rollback, NativePhaseLaunchAttempt, NativePhasePending, "")
						nativePhaseTestRecord(rollback, NativePhasePrepareReturn, NativePhaseFailed, "")
						nativePhaseTestRecord(rollback, NativePhaseLaunchReturn, NativePhaseFailed, "")
						final = NativePhaseUncertain
					} else {
						rollback := nativePhaseTestLaunch(ctx, NativePhaseRollback, "g"+strings.Repeat("d", 48), NativePhaseSSHReturn)
						nativePhaseTestRecord(rollback, NativePhaseLaunchReturn, NativePhaseOK, "")
					}
				}
			}
			FinishNativePhase(ctx, final)
			trace, _ := nativePhaseTestDecode(t, c)
			if !trace.Terminal.Complete || trace.Terminal.Disposition != final {
				t.Fatal("closed failed prefix or restoration result was lost", trace.Terminal)
			}
			attempts := 0
			for _, event := range trace.Events {
				if event.Kind == NativePhaseLaunchAttempt && event.Role == NativePhaseRollback {
					attempts++
				}
			}
			want := 1
			if mode == "preflight" || mode == "start-uncertain" || mode == "cleanup-failed" {
				want = 0
			}
			if attempts != want {
				t.Fatal("failed rollback invocation or uncertainty changed attempt coverage", attempts, want)
			}
		})
	}
}

func TestNativePhaseForwardIdentityAndCleanupPrecedeFreshRollback(t *testing.T) {
	c, ctx := nativePhaseTestBegin(t)
	nativePhaseTestRecord(ctx, NativePhaseOldStopReturn, NativePhaseOK, "")
	generation := "g" + strings.Repeat("c", 48)
	desired := nativePhaseTestLaunch(ctx, NativePhaseDesired, generation, NativePhaseSSHReturn)
	forward := NativePhaseForward{Unit: "yoloctl-openshell-port-coder-8080-80.service", HostPort: 8080, TargetPort: 80, Attempt: strings.Repeat("1", 32)}
	for _, phase := range []struct {
		kind    NativePhaseKind
		outcome NativePhaseOutcome
	}{
		{NativePhaseForwardStartReturn, NativePhaseOK},
		{NativePhaseForwardReadyReturn, NativePhaseFailed},
		{NativePhaseForwardCleanupReturn, NativePhaseOK},
	} {
		RecordNativePhase(desired, NativePhaseEvent{Kind: phase.kind, Outcome: phase.outcome, Generation: generation, Forward: &forward})
	}
	forward.Unit = "SECRET_MUST_NOT_REACH_CAPTURE"
	nativePhaseTestRecord(desired, NativePhaseGenerationCleanupReturn, NativePhaseOK, generation)
	nativePhaseTestRecord(desired, NativePhaseLaunchReturn, NativePhaseFailed, "")
	rollback := nativePhaseTestLaunch(ctx, NativePhaseRollback, "g"+strings.Repeat("d", 48), NativePhaseSSHReturn)
	nativePhaseTestRecord(rollback, NativePhaseLaunchReturn, NativePhaseOK, "")
	FinishNativePhase(ctx, NativePhaseFailed)
	trace, raw := nativePhaseTestDecode(t, c)
	if !trace.Terminal.Complete || bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("bind-conflict")) {
		t.Fatal("forward return acquired an arbitrary body or invented bind cause", trace.Terminal)
	}
}

func TestNativePhaseDecoderRejectsCanonicalButSemanticallyWrongCoverage(t *testing.T) {
	base := nativePhaseTestSuccess(t)
	for _, mode := range []string{"missing-engine", "reordered", "missing-launch-return", "duplicate-stop", "foreign-cid", "old-generation", "forward-on-ssh", "pending-generation", "wrong-role", "count", "complete-with-loss", "invented-bind-class"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var trace NativePhaseTranscript
			if err := json.Unmarshal(raw, &trace); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-engine":
				trace.Events = append(trace.Events[:4], trace.Events[5:]...)
			case "reordered":
				trace.Events[4], trace.Events[5] = trace.Events[5], trace.Events[4]
			case "missing-launch-return":
				trace.Events = append(trace.Events[:7], trace.Events[8:]...)
			case "duplicate-stop":
				trace.Events = append(trace.Events[:2], append([]NativePhaseEvent{trace.Events[1]}, trace.Events[2:]...)...)
			case "foreign-cid":
				trace.Events[4].CID = strings.Repeat("f", 64)
			case "old-generation":
				trace.Events[3].Generation = trace.OriginalGeneration
			case "forward-on-ssh":
				trace.Events[6].Forward = &NativePhaseForward{}
			case "pending-generation":
				trace.Events[2].Generation = trace.OriginalGeneration
			case "wrong-role":
				trace.Events[4].Role, trace.Events[4].Attempt = NativePhaseRollback, 2
			case "complete-with-loss":
				trace.Terminal.Loss = NativePhaseLossUnavailable
			case "invented-bind-class":
				trace.Events[6].Outcome = "bind-conflict"
			}
			for i := range trace.Events {
				trace.Events[i].Sequence = i + 1
			}
			trace.Terminal.Count, trace.Terminal.LastSequence = len(trace.Events), len(trace.Events)
			if mode == "count" {
				trace.Terminal.Count++
			}
			raw, _ = json.Marshal(trace)
			if _, err := DecodeNativePhaseTranscript(raw); err == nil {
				t.Fatal("semantically wrong transcript was accepted", mode)
			}
		})
	}
}

func TestNativePhaseStrictJSONAndMissingEnd(t *testing.T) {
	base := nativePhaseTestSuccess(t)
	raw, _ := json.Marshal(base)
	for _, bad := range [][]byte{
		append(append([]byte(nil), raw...), '\n'),
		append(append([]byte(nil), raw...), []byte("{}")...),
		[]byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"policy":"secret"`, 1)),
		raw[:len(raw)-1],
		bytes.Repeat([]byte("x"), NativePhaseMaxBytes+1),
	} {
		if _, err := DecodeNativePhaseTranscript(bad); err == nil {
			t.Fatal("noncanonical or malformed JSON was accepted")
		}
	}
	c, _ := nativePhaseTestBegin(t)
	trace, _ := nativePhaseTestDecode(t, c)
	if trace.Terminal.Complete || trace.Terminal.Loss != NativePhaseLossMissingEnd {
		t.Fatal("missing end became complete", trace.Terminal)
	}
	trace.Terminal.Complete, trace.Terminal.Loss = true, NativePhaseLossNone
	raw, _ = json.Marshal(trace)
	if _, err := DecodeNativePhaseTranscript(raw); err == nil {
		t.Fatal("terminal flag manufactured missing callback coverage")
	}
}

func TestNativePhaseLossIsBoundedAndNeverResumesOrGrantsAuthority(t *testing.T) {
	for _, mode := range []string{"unavailable", "foreign-cid", "second-span", "overflow", "uncertain-rollback"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx := nativePhaseTestBegin(t)
			switch mode {
			case "unavailable":
				MarkNativePhaseIncomplete(ctx)
			case "foreign-cid":
				RecordNativePhase(ctx, NativePhaseEvent{Kind: NativePhaseOldStopReturn, Outcome: NativePhaseOK, CID: strings.Repeat("f", 64)})
			case "second-span":
				_ = BeginNativePhase(ctx, "other", strings.Repeat("f", 64), "g"+strings.Repeat("e", 48))
			case "overflow":
				nativePhaseTestRecord(ctx, NativePhaseOldStopReturn, NativePhaseOK, "")
				generation := "g" + strings.Repeat("c", 48)
				desired := nativePhaseTestLaunch(ctx, NativePhaseDesired, generation, NativePhaseSSHReturn)
				for n := 1; n <= NativePhaseMaxEvents; n++ {
					f := NativePhaseForward{Unit: fmt.Sprintf("yoloctl-openshell-port-coder-%d-80.service", 10000+n), HostPort: 10000 + n, TargetPort: 80, Attempt: fmt.Sprintf("%032x", n)}
					RecordNativePhase(desired, NativePhaseEvent{Kind: NativePhaseForwardStartReturn, Outcome: NativePhaseOK, Generation: generation, Forward: &f})
					RecordNativePhase(desired, NativePhaseEvent{Kind: NativePhaseForwardReadyReturn, Outcome: NativePhaseOK, Generation: generation, Forward: &f})
				}
			case "uncertain-rollback":
				nativePhaseTestRecord(ctx, NativePhaseOldStopReturn, NativePhaseOK, "")
				desired := nativePhaseTestLaunch(ctx, NativePhaseDesired, "g"+strings.Repeat("c", 48), NativePhasePrepareReturn)
				nativePhaseTestRecord(desired, NativePhaseEngineStartReturn, NativePhaseUncertain, "g"+strings.Repeat("c", 48))
				nativePhaseTestRecord(desired, NativePhaseLaunchReturn, NativePhaseUncertain, "")
				nativePhaseTestRecord(WithNativePhaseRole(ctx, NativePhaseRollback), NativePhaseLaunchAttempt, NativePhasePending, "")
			}
			FinishNativePhase(ctx, NativePhaseFailed)
			trace, _ := nativePhaseTestDecode(t, c)
			if trace.Terminal.Complete || trace.Terminal.Loss == NativePhaseLossNone {
				t.Fatal("collection loss became complete coverage")
			}
			if mode == "overflow" && trace.Terminal.Loss != NativePhaseLossOverflow {
				t.Fatal("overflow was silently replaced", trace.Terminal)
			}
		})
	}
	if _, err := NewNativePhaseCollector().MarshalFinal(); err == nil {
		t.Fatal("absent span became zero successful events")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := NewNativePhaseCollector()
	ctx = BeginNativePhase(WithNativePhaseCollector(ctx, c), "coder", strings.Repeat("a", 64), "g"+strings.Repeat("b", 48))
	nativePhaseTestRecord(ctx, NativePhaseOldStopReturn, NativePhaseCanceled, "")
	FinishNativePhase(ctx, NativePhaseCanceled)
	trace, _ := nativePhaseTestDecode(t, c)
	if !trace.Terminal.Complete || trace.Terminal.Disposition != NativePhaseCanceled {
		t.Fatal("observation changed original cancellation")
	}
}

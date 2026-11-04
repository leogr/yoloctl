package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/operator"
)

func nativePhaseTestParent(n int) NativePhaseParent {
	return NativePhaseParent{Hello: Hello{Protocol: ProtocolVersion, Bundle: "exact-build", Space: "work", Generation: "generation"},
		DiskID: "native-disk", VMIncarnation: "vm-incarnation", ExecutionID: fmt.Sprintf("native-phase-execution-%04d", n), Method: "box.reconcile",
		Scope: &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "coder"}}}}
}

func nativePhaseTestBody(box string, incomplete bool) []byte {
	c := operator.NewNativePhaseCollector()
	ctx := operator.WithNativePhaseCollector(context.Background(), c)
	ctx = operator.BeginNativePhase(ctx, box, strings.Repeat("a", 64), "g"+strings.Repeat("b", 48))
	if incomplete {
		operator.MarkNativePhaseIncomplete(ctx)
	}
	operator.FinishNativePhase(ctx, operator.NativePhaseFailed)
	raw, _ := c.MarshalFinal()
	return raw
}

func nativePhaseTestAccept(o *WorkerObservation, raw []byte, scope *Scope) {
	o.store.mu.Lock()
	o.entryLocked().capture = true // synthetic already-owned transport, not launch proof
	o.store.mu.Unlock()
	o.Started(42)
	o.Exited()
	o.Result(7, NativePhaseOutcomeFailed, scope)
	o.accept(raw, "none")
}

func TestNativePhaseStoreRequiresParentCustodyAndOwnsReadCopies(t *testing.T) {
	s := NewNativePhaseStore()
	parent := nativePhaseTestParent(1)
	o := s.Reserve(parent)
	if o == nil {
		t.Fatal("valid original parent was not reserved")
	}
	parent.Scope.Writes[0].Name = "changed"
	raw := nativePhaseTestBody("coder", false)
	nativePhaseTestAccept(o, raw, &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "coder"}}})
	value := s.Read(parent.ExecutionID)
	if value.State != NativePhaseStateComplete || value.WorkerPID != 42 || value.Result == nil || value.Result.Status != 7 || value.Result.Outcome != NativePhaseOutcomeFailed || value.ControllerInstance == "" || !bytes.Equal(value.Transcript, raw) {
		t.Fatal("observation lost parent custody or rewrote failed command", value)
	}
	value.Parent.Scope.Writes[0].Name = "other"
	value.Transcript[0] = '!'
	value.Result.Status = 0
	again := s.Read(parent.ExecutionID)
	if again.Parent.Scope.Writes[0].Name != "coder" || again.Result.Status != 7 || !bytes.Equal(again.Transcript, raw) {
		t.Fatal("read response aliases stored diagnostic data")
	}
	if s.Read("unknown-execution").State != NativePhaseStateUnavailable || (*NativePhaseStore)(nil).Read(parent.ExecutionID).State != NativePhaseStateUnavailable || len(s.entries) != 1 {
		t.Fatal("unavailable read allocated or invented a trace")
	}
}

func TestNativePhaseStoreSaturationCompletedEvictionAndLateDrainToken(t *testing.T) {
	s := NewNativePhaseStore()
	owners := make([]*WorkerObservation, NativePhaseSlots)
	for i := range owners {
		owners[i] = s.Reserve(nativePhaseTestParent(i))
		if owners[i] == nil {
			t.Fatal("slot unavailable before capacity", i)
		}
	}
	if s.Reserve(nativePhaseTestParent(NativePhaseSlots)) != nil || len(s.entries) != NativePhaseSlots {
		t.Fatal("active saturation allocated another capture")
	}
	old := owners[0]
	old.Exited()
	old.Result(2, NativePhaseOutcomeFailed, nativePhaseTestParent(0).Scope)
	newOwner := s.Reserve(nativePhaseTestParent(NativePhaseSlots))
	if newOwner == nil || s.Read(old.id).State != NativePhaseStateUnavailable || len(s.entries) != NativePhaseSlots {
		t.Fatal("completed oldest slot was not evicted")
	}
	owners[1].Exited()
	owners[1].Result(2, NativePhaseOutcomeFailed, nativePhaseTestParent(1).Scope)
	reused := s.Reserve(nativePhaseTestParent(0))
	if reused == nil || reused.token == old.token {
		t.Fatal("reused execution did not receive independent slot custody")
	}
	old.accept(nativePhaseTestBody("coder", false), "none")
	if value := s.Read(old.id); value.State != NativePhaseStateCollecting || value.WorkerPID != 0 || value.Transcript != nil {
		t.Fatal("late drain replaced a reused slot", value)
	}
}

func TestNativePhaseStoreRejectsForeignMalformedMissingAndOversizedEvidence(t *testing.T) {
	for _, mode := range []string{"foreign-box", "missing-scope", "malformed", "missing-end", "incomplete", "wire-overflow", "envelope-overflow"} {
		t.Run(mode, func(t *testing.T) {
			s := NewNativePhaseStore()
			parent := nativePhaseTestParent(1)
			o := s.Reserve(parent)
			raw, scope := nativePhaseTestBody("coder", false), parent.Scope
			switch mode {
			case "foreign-box":
				raw = nativePhaseTestBody("foreign", false)
			case "missing-scope":
				scope = nil
			case "malformed":
				raw = []byte(`{"secret":"must-never-be-exported"}`)
			case "missing-end":
				raw = nil
			case "incomplete":
				raw = nativePhaseTestBody("coder", true)
			case "wire-overflow":
				raw = bytes.Repeat([]byte("x"), operator.NativePhaseMaxBytes+1)
			case "envelope-overflow":
				// A byte-complete invalid body is still never exposed as raw JSON.
				raw = bytes.Repeat([]byte(" "), operator.NativePhaseMaxBytes)
			}
			nativePhaseTestAccept(o, raw, scope)
			value := s.Read(parent.ExecutionID)
			if value.State == NativePhaseStateComplete || value.Result.Status != 7 {
				t.Fatal("incomplete observation invented command success", value)
			}
			if mode != "incomplete" && value.Transcript != nil {
				t.Fatal("unvalidated or foreign payload was exposed", string(value.Transcript))
			}
			encoded, _ := json.Marshal(value)
			if len(encoded) > operator.NativePhaseMaxBytes || bytes.Contains(encoded, []byte("must-never-be-exported")) {
				t.Fatal("readout exceeded its bound or exposed unvalidated text")
			}
		})
	}
}

func TestNativePhaseMetadataAndExternalReadersStayBounded(t *testing.T) {
	s := NewNativePhaseStore()
	parent := nativePhaseTestParent(1)
	parent.Method = strings.Repeat("x", 129)
	if s.Reserve(parent) != nil {
		t.Fatal("oversized parent metadata enabled capture")
	}
	parent = nativePhaseTestParent(1)
	for range maxScopeResources {
		parent.Scope.Reads = append(parent.Scope.Reads, Resource{Kind: ResourceImages, Name: "registry.example/" + strings.Repeat("x", 240) + ":tag"})
	}
	if s.Reserve(parent) != nil || len(s.entries) != 0 {
		t.Fatal("oversized scope metadata allocated capture")
	}
	var releases []func()
	for range 4 {
		release, ok := s.TryRead()
		if !ok {
			t.Fatal("response capacity missing before limit")
		}
		releases = append(releases, release)
	}
	if release, ok := s.TryRead(); ok || release != nil {
		t.Fatal("stalled responses exceeded copied-data capacity")
	}
	releases[0]()
	releases[0]()
	release, ok := s.TryRead()
	if !ok {
		t.Fatal("response capacity was not released exactly once")
	}
	release()
	for _, release := range releases[1:] {
		release()
	}
}

func waitNativePhaseRead(t *testing.T, s *NativePhaseStore, id string) NativePhaseReadout {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		value := s.Read(id)
		if value.State != NativePhaseStateCollecting {
			return value
		}
		if time.Now().After(deadline) {
			t.Fatal("diagnostic drain did not reach its independent bound")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNativePhaseIndependentDrainClosesOnlyAfterExitBound(t *testing.T) {
	s := NewNativePhaseStore()
	parent := nativePhaseTestParent(1)
	o := s.Reserve(parent)
	_, write := o.pipe()
	if write == nil {
		t.Fatal("diagnostic pipe unavailable")
	}
	defer func() { _ = write.Close() }()
	o.Started(os.Getpid())
	start := time.Now()
	o.Exited()
	o.Result(7, NativePhaseOutcomeFailed, parent.Scope)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("command-result notifications waited for blocked diagnostics")
	}
	value := waitNativePhaseRead(t, s, parent.ExecutionID)
	if value.State != NativePhaseStateIncomplete || value.Reason != "missing_end" || value.Result.Status != 7 {
		t.Fatal("open diagnostic writer changed original result", value)
	}
}

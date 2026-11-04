package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/termio"
)

func nativeNonceOwnerFixture(t *testing.T) (*Server, *Receipt, *nativeNonceOwner) {
	t.Helper()
	s := &Server{Hello: hello(), DiskID: testDisk, Receipts: Receipts{t.TempDir()}, VerifyStorage: func(context.Context) error { return nil }}
	req := request("test.native-nonce")
	r, err := s.Receipts.begin(req, Principal{}, s.Hello.Generation, s.DiskID, true)
	if err != nil {
		t.Fatal(err)
	}
	r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "box"}}}
	r.EffectPhase = EffectStarted
	if err := s.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	o := s.newNativeNonceOwner(t.Context(), Principal{}, req, &r, Operation{Mutates: true, EffectGated: true})
	t.Cleanup(o.close)
	return s, &r, o
}

func TestNativeNonceOwnerAllocatesFreshIdentityBoundOnlyToOriginalExecution(t *testing.T) {
	s, r, o := nativeNonceOwnerFixture(t)
	first, err := o.allocate(t.Context(), "box")
	if err != nil || !validNativeIncarnation(first) {
		t.Fatal("original scoped owner could not allocate", err)
	}
	second, err := o.allocate(t.Context(), "box")
	if err != nil || first == second {
		t.Fatal("fresh original identities collided/adopted", err)
	}
	if err := o.match(t.Context(), "box", first); err != nil {
		t.Fatal(err)
	}
	if err := o.match(t.Context(), "other", first); err == nil {
		t.Fatal("nonce transferred to another box")
	}
	// Matching passive request/receipt fields cannot copy the original map.
	copyOwner := s.newNativeNonceOwner(t.Context(), Principal{}, request("test.native-nonce"), r, Operation{Mutates: true, EffectGated: true})
	defer copyOwner.close()
	if err := copyOwner.match(t.Context(), "box", first); err == nil {
		t.Fatal("copied receipt metadata adopted original incarnation")
	}
	o.close()
	if value, err := o.allocate(t.Context(), "box"); err == nil || value != "" {
		t.Fatal("expired original execution allocated incarnation")
	}
	if err := o.match(t.Context(), "box", first); err == nil {
		t.Fatal("closed original owner revived allocation")
	}
}

func TestNativeNonceOwnerRequiresActualScopedDurablePermission(t *testing.T) {
	for _, change := range []string{"waiting", "complete", "state-complete", "setup-complete", "wrong-scope", "generation", "disk", "principal", "nil-scope", "proof-zero", "storage", "already-fenced", "held", "receipt-unavailable", "receipt-alias"} {
		t.Run(change, func(t *testing.T) {
			s, r, o := nativeNonceOwnerFixture(t)
			save := true
			switch change {
			case "waiting":
				r.EffectPhase = EffectWaiting
			case "complete":
				r.Completion = &Completion{}
			case "state-complete":
				r.State = "complete"
			case "setup-complete":
				r.SetupComplete = true
			case "wrong-scope":
				r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "other"}}}
			case "generation":
				r.Generation = "other"
			case "disk":
				r.DiskID = "other"
			case "principal":
				r.Principal = Principal{Box: "other", Incarnation: "actor"}
			case "nil-scope":
				r.Scope = nil
			case "proof-zero":
				r.ProofVersion = 0
			case "storage":
				s.VerifyStorage = func(context.Context) error { return errors.New("actual storage unavailable") }
			case "already-fenced":
				s.receiptFailure = errors.New("unreconciled receipt failure")
			case "held":
				seedReceipt(t, s, "other-receipt-0123456789", "uncertain", writes(box("box")), nil)
			case "receipt-unavailable":
				save = false
				if err := os.Remove(filepath.Join(s.Receipts.Dir, r.ID+".json")); err != nil {
					t.Fatal(err)
				}
			case "receipt-alias":
				save = false
				raw, err := os.ReadFile(filepath.Join(s.Receipts.Dir, r.ID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte(`"state":"pending"`), []byte(`"State":"complete","state":"pending"`), 1)
				if err := os.WriteFile(filepath.Join(s.Receipts.Dir, r.ID+".json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if save {
				if err := s.Receipts.Save(*r); err != nil {
					t.Fatal(err)
				}
			}
			if value, err := o.allocate(t.Context(), "box"); err == nil || value != "" || len(o.allocated) != 0 {
				t.Fatal("invalid original receipt/storage scope allocated native identity", err)
			}
			if change == "receipt-unavailable" || change == "receipt-alias" || change == "storage" {
				if s.receiptFailure == nil {
					t.Fatal("unproved native storage/receipt did not close admission")
				}
			}
		})
	}
}

func TestNativeNonceOwnerBoundContextCannotBeReplaced(t *testing.T) {
	s, r, _ := nativeNonceOwnerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	o := s.newNativeNonceOwner(ctx, Principal{}, request("test.native-nonce"), r, Operation{Mutates: true, EffectGated: true})
	defer o.close()
	value, err := o.allocate(t.Context(), "box")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if got, err := o.allocate(context.Background(), "box"); err == nil || got != "" {
		t.Fatal("fresh caller context revived original execution")
	}
	if err := o.match(context.Background(), "box", value); err == nil {
		t.Fatal("fresh context revived original nonce binding")
	}
	if s.newNativeNonceOwner(t.Context(), Principal{}, request("box.list"), nil, Operation{}) != nil {
		t.Fatal("read acquired original native allocator")
	}
}

func TestNativeNoncePrivateCodecRefusesAmbiguityAndInvalidIdentities(t *testing.T) {
	for _, raw := range []string{`{"Box":"box"}`, `{"box":"other","box":"box"}`, `{"Box":"other","box":"box"}`, `{"box":"box","unknown":1}`, ` {"box":"box"}`, `{"box":null}`, `{}`, strings.Repeat("x", nativeNonceFrameLimit+1)} {
		var r nativeNonceRequest
		if err := canonicalNativeNonce([]byte(raw), &r); err == nil {
			t.Fatal("ambiguous native nonce request accepted")
		}
	}
	for _, value := range []string{"", strings.Repeat("a", 31), strings.Repeat("A", 32), "../" + strings.Repeat("a", 29)} {
		if validNativeIncarnation(value) {
			t.Fatal("invalid native incarnation accepted")
		}
	}
}

// Invoked only by TestMain's actual fixed RunWorker subprocess route. No VM,
// container, native kernel or publisher is created by these transport tests.
func nativeNonceTestWorker(released *os.File, req Request) {
	finish := func(code int) { _, _ = released.Write([]byte{checkpointFinish}); os.Exit(code) }
	c := NewWorkerCheckpoints(released)
	c.Gate()
	ctx := operator.WithNativeIncarnationAllocator(context.Background(), c.NativeIncarnation)
	action := req.Args[0]
	if action == "before-permission" {
		if _, err := operator.AllocateNativeIncarnation(ctx, "box"); err == nil {
			_, _ = fmt.Fprintln(os.Stdout, "unpermitted nonce returned")
			finish(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, "refused-before-permission")
		finish(0)
	}
	if action == "child-fd" {
		if err := exec.Command("/bin/sh", "-c", "test ! -e /dev/fd/4").Run(); err != nil {
			finish(2)
		}
	}
	if err := c.BeginEffects(ctx, EffectIntent{Scope: writes(box("box"))}); err != nil {
		finish(2)
	}
	if action == "wrong-box" {
		if _, err := operator.AllocateNativeIncarnation(ctx, "other"); err == nil {
			_, _ = fmt.Fprintln(os.Stdout, "out-of-scope nonce was returned")
			finish(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, "refused-outside-scope")
	} else {
		value, err := operator.AllocateNativeIncarnation(ctx, "box")
		if err != nil {
			_, _ = fmt.Fprintln(os.Stdout, "nonce-error:", err)
			finish(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, value)
	}
	if err := c.CompleteEffects(ctx, 0); err != nil {
		finish(2)
	}
	if _, err := operator.AllocateNativeIncarnation(ctx, "box"); err == nil {
		_, _ = fmt.Fprintln(os.Stdout, "completed worker received another nonce")
		finish(2)
	}
	finish(0)
}

func TestNativeNonceActualServerWorkerPrivateChannelAndExpiry(t *testing.T) {
	for _, action := range []string{"normal", "wrong-box", "before-permission", "child-fd"} {
		t.Run(action, func(t *testing.T) {
			s := &Server{Hello: hello(), DiskID: testDisk, Receipts: Receipts{t.TempDir()}, Operations: map[string]Operation{"test.native-nonce": {Mutates: true, EffectGated: true, Footprint: FootprintBox}}, VerifyStorage: func(context.Context) error { return nil }}
			calls := 0
			var captured NativeNonceCall
			s.Execute = func(ctx context.Context, p Principal, r Request, streams Streams) (int, error) {
				captured = streams.NativeNonce
				if captured == nil {
					t.Error("actual admitted worker lacks original allocator")
					return 2, errors.New("allocator unavailable")
				}
				streams.NativeNonce = func(ctx context.Context, box string) (string, error) { calls++; return captured(ctx, box) }
				return RunWorker(ctx, p, r, streams)
			}
			req := request("test.native-nonce")
			req.Args = []string{action}
			var out bytes.Buffer
			if err := invoke(t, s, req, nil, &out, io.Discard); err != nil {
				t.Fatal("original worker private native nonce flow failed:", out.String(), err)
			}
			switch action {
			case "before-permission":
				if calls != 0 || !strings.Contains(out.String(), "refused-before-permission") {
					t.Fatal("unpermitted original worker invoked allocator", calls, out.String())
				}
			case "wrong-box":
				if calls != 1 || !strings.Contains(out.String(), "refused-outside-scope") {
					t.Fatal("actual worker allocated outside original box scope", calls, out.String())
				}
			default:
				if calls != 1 || !validNativeIncarnation(strings.TrimSpace(out.String())) {
					t.Fatal("actual worker lacked one original parent nonce", calls, out.String())
				}
			}
			if got, err := captured(t.Context(), "box"); err == nil || got != "" {
				t.Fatal("finished actual worker's callback revived original nonce allocation")
			}
		})
	}
}

func TestNativeNonceWorkerRefusesContradictoryAndMalformedPrivateReplies(t *testing.T) {
	for _, kind := range []string{"error-plus-identity", "upper-identity", "uncertain-without-error", "alias", "known-denial", "uncertain-denial"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			parent, child, err := termio.ViewPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close(); _ = child.Close() }()
			c := NewWorkerCheckpoints(child)
			c.Gate()
			checkpointFixturePhase(t, c, parent, checkpointBegin) // wire ACK only, NOT permission evidence
			done := make(chan error, 1)
			go func() {
				var message [1]byte
				if _, err := io.ReadFull(parent, message[:]); err != nil {
					done <- err
					return
				}
				if message[0] != checkpointNativeNonce {
					done <- errors.New("fixture read wrong private kind")
					return
				}
				var request nativeNonceRequest
				if err := readNativeNonceBody(parent, &request); err != nil {
					done <- err
					return
				}
				reply := nativeNonceReply{Incarnation: strings.Repeat("a", 32)}
				switch kind {
				case "error-plus-identity":
					reply.Error = "contradiction"
				case "upper-identity":
					reply.Incarnation = strings.Repeat("A", 32)
				case "uncertain-without-error":
					reply.Uncertain = true
				case "known-denial":
					reply = nativeNonceReply{Error: "denied"}
				case "uncertain-denial":
					reply = nativeNonceReply{Error: "storage uncertain", Uncertain: true}
				}
				raw, err := json.Marshal(reply)
				if err != nil {
					done <- err
					return
				}
				if kind == "alias" {
					raw = bytes.Replace(raw, []byte(`"incarnation"`), []byte(`"Incarnation"`), 1)
				}
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
				done <- writeAll(parent, append(header[:], raw...))
			}()
			value, callErr := c.NativeIncarnation(ctx, "box")
			if callErr == nil || value != "" {
				t.Fatal("invalid private reply yielded native incarnation", kind)
			}
			var uncertain *EffectsUncertain
			if errors.As(callErr, &uncertain) != (kind == "uncertain-denial") {
				t.Fatal("private nonce uncertainty marker lost/invented", kind, callErr)
			}
			if kind != "known-denial" && kind != "uncertain-denial" {
				if _, err := child.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatal("invalid private nonce reply left channel reusable", kind, err)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("fixture did not deliver intended complete reply", err)
				}
			case <-ctx.Done():
				t.Fatal("fixture private reply did not settle")
			}
		})
	}
}

type noncePhaseEvidence struct{}

func (noncePhaseEvidence) ExpandEffects(context.Context, []Resource) error      { return nil }
func (noncePhaseEvidence) RecordDocument(context.Context, DocumentIntent) error { return nil }
func (noncePhaseEvidence) CompleteEffects(context.Context, int) error           { return nil }

func TestNativeNonceParentRejectsRawRequestsOutsideOriginalCheckpointPhase(t *testing.T) {
	for _, phase := range []string{"before-permission", "after-completion", "after-release"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			parent, child, err := termio.ViewPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close(); _ = child.Close() }()
			calls := 0
			invoked := make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				defer func() { _ = parent.Close() }()
				done <- serveWorkerCheckpoints(ctx, parent, Streams{NativeNonce: func(context.Context, string) (string, error) {
					calls++
					invoked <- struct{}{}
					return strings.Repeat("a", 32), nil
				}, BeginEffects: func(context.Context, EffectIntent) error { return nil }, RecordEvidence: noncePhaseEvidence{}})
			}()
			c := NewWorkerCheckpoints(child)
			c.Gate()
			if phase != "before-permission" {
				if err := c.BeginEffects(ctx, scopeIntent(writes(box("box")))); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "after-completion" {
				if err := c.CompleteEffects(ctx, 0); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "after-release" {
				if err := c.Release(); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately bypass the child's own phase check. The actual parent
			// must independently reject this complete canonical private request.
			raw, _ := json.Marshal(nativeNonceRequest{"box"})
			var header [5]byte
			header[0] = checkpointNativeNonce
			binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
			if err := writeAll(child, append(header[:], raw...)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-invoked:
				t.Fatal("parent invoked native allocator outside original phase", phase)
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "invalid original native incarnation checkpoint phase") || calls != 0 {
					t.Fatal("parent accepted nonce outside original phase", phase, calls, err)
				}
			case <-ctx.Done():
				t.Fatal("parent did not refuse raw nonce phase")
			}
		})
	}
}

func TestNativeNonceStorageLossCannotHideBehindCancellation(t *testing.T) {
	for _, kind := range []string{"pure", "joined-pure", "mixed"} {
		t.Run(kind, func(t *testing.T) {
			s, r, _ := nativeNonceOwnerFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			o := s.newNativeNonceOwner(ctx, Principal{}, request("test.native-nonce"), r, Operation{Mutates: true, EffectGated: true})
			defer o.close()
			s.VerifyStorage = func(context.Context) error {
				cancel()
				switch kind {
				case "pure":
					return fmt.Errorf("verification canceled: %w", context.Canceled)
				case "joined-pure":
					return errors.Join(context.Canceled, context.DeadlineExceeded)
				default:
					return errors.Join(context.Canceled, errors.New("native identity unavailable"))
				}
			}
			value, err := o.allocate(t.Context(), "box")
			if err == nil || value != "" {
				t.Fatal("canceled verification allocated identity")
			}
			if (s.receiptFailure != nil) != (kind == "mixed") {
				t.Fatal("mixed storage failure hidden or pure cancellation fenced unrelated work", kind, err)
			}
		})
	}
}

func TestNativeNonceWorkerRefusesInvalidLocalPhaseBeforeAnyPrivateBytes(t *testing.T) {
	for _, phase := range []string{"ungated", "before-permission", "after-completion", "after-release"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			parent, child, err := termio.ViewPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close(); _ = child.Close() }()
			c := NewWorkerCheckpoints(child)
			if phase != "ungated" {
				c.Gate()
			}
			if phase != "before-permission" {
				checkpointFixturePhase(t, c, parent, checkpointBegin)
			}
			switch phase {
			case "after-completion":
				checkpointFixturePhase(t, c, parent, checkpointComplete)
			case "after-release":
				checkpointFixturePhase(t, c, parent, checkpointSetup)
			}

			type observation struct {
				n   int
				err error
			}
			seen := make(chan observation, 1)
			go func() { var b [1]byte; n, err := parent.Read(b[:]); seen <- observation{n, err}; _ = parent.Close() }()
			if value, err := c.NativeIncarnation(ctx, "box"); err == nil || value != "" {
				t.Fatal("invalid local phase returned nonce")
			}
			if err := child.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Fatal(err)
			}
			select {
			case got := <-seen:
				if got.n != 0 || !errors.Is(got.err, io.EOF) {
					t.Fatal("invalid local nonce phase wrote private bytes", phase, got.n, got.err)
				}
			case <-ctx.Done():
				t.Fatal("local nonce no-write proof did not settle with EOF")
			}
		})
	}
}

// Ordinary transport fixture only: drive phase through actual bytes/ACK instead
// of mutating private shared-owner state. It supplies no original-parent grant.
func checkpointFixturePhase(t *testing.T, c *WorkerCheckpoints, parent *os.File, kind byte) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		var opcode [1]byte
		if _, err := io.ReadFull(parent, opcode[:]); err != nil {
			done <- err
			return
		}
		if opcode[0] != kind {
			done <- errors.New("wrong phase fixture opcode")
			return
		}
		ack := checkpointRecorded
		switch kind {
		case checkpointBegin:
			var body EffectIntent
			if err := readCheckpointBody(parent, &body); err != nil {
				done <- err
				return
			}
			ack = checkpointPermit
		case checkpointComplete:
			var body int
			if err := readCheckpointBody(parent, &body); err != nil {
				done <- err
				return
			}
		case checkpointSetup:
			ack = checkpointAttached
		}
		_, err := parent.Write([]byte{ack})
		done <- err
	}()
	var err error
	switch kind {
	case checkpointBegin:
		err = c.BeginEffects(t.Context(), EffectIntent{})
	case checkpointComplete:
		err = c.CompleteEffects(t.Context(), 0)
	case checkpointSetup:
		err = c.Release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

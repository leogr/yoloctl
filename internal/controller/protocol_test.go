package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

func TestRecoveryRequiresReviewWithoutReplayingOrBlockingReads(t *testing.T) {
	receipts := Receipts{t.TempDir()}
	for _, id := range []string{"pending-request-123456", "attached-request-123456"} {
		r := request("box.create")
		r.ID = id
		item, err := receipts.Begin(r, Principal{}, "generation-1")
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(id, "attached") {
			item.SetupComplete = true
			if err := receipts.Save(item); err != nil {
				t.Fatal(err)
			}
		}
	}
	required, err := receipts.Recover()
	if err != nil || !required {
		t.Fatalf("recovery: %v %v", required, err)
	}
	attached, err := receipts.Read("attached-request-123456")
	if err != nil || attached.State != "setup-complete" {
		t.Fatalf("attachment outcome: %+v %v", attached, err)
	}
	s := &Server{Hello: hello(), Receipts: receipts, RecoveryRequired: required,
		Operations: map[string]Operation{"box.create": {Mutates: true}, "box.list": {}},
		Execute:    func(context.Context, Principal, Request, Streams) (int, error) { return 0, nil },
	}
	if err := invoke(t, s, request("box.list"), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := invoke(t, s, request("box.create"), nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "`yoloctl space operation work ID --acknowledge`") {
		t.Fatal("mutation admitted before review, or review path missing", err)
	}
	r := request("control.acknowledge")
	r.Payload, _ = json.Marshal(map[string]string{"id": "pending-request-123456"})
	if err := invoke(t, s, r, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := invoke(t, s, request("box.create"), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if required, err := receipts.Recover(); required || err != nil {
		t.Fatalf("review lost on restart: %v %v", required, err)
	}
	old := request("box.create")
	old.ID = "pending-request-123456"
	if err := invoke(t, s, old, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("acknowledged request replayed")
	}
}

func TestReceiptWriteFailureClosesMutationAdmissionUntilReconciled(t *testing.T) {
	for _, duringSetup := range []bool{false, true} {
		name := "final-result"
		if duringSetup {
			name = "setup-proof"
		}
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir, saved := filepath.Join(parent, "receipts"), filepath.Join(parent, "saved")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int32
			s := &Server{Hello: hello(), Receipts: Receipts{dir},
				Operations: map[string]Operation{"box.create": {Mutates: true}, "box.list": {}},
				Execute: func(_ context.Context, _ Principal, req Request, streams Streams) (int, error) {
					if req.Method == "box.list" {
						return 0, nil
					}
					if writes.Add(1) > 1 {
						return 0, nil
					}
					// Keep the pending record, but make its next replacement fail
					// even when the test runner is root (no chmod-based simulation).
					if err := os.Rename(dir, saved); err != nil {
						return 2, err
					}
					if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
						return 2, err
					}
					if duringSetup {
						_ = streams.Release()
					}
					return 0, nil
				},
			}
			req := request("box.create")
			if err := invoke(t, s, req, nil, io.Discard, io.Discard); err == nil {
				t.Fatal("unflushed outcome reported as success")
			}
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(saved, dir); err != nil {
				t.Fatal(err)
			}
			// Repair alone must not authorize a new write. The controller
			// keeps serving reads and says it is read-only.
			if err := invoke(t, s, request("box.list"), nil, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			var notice strings.Builder
			if err := invoke(t, s, request("control.ping"), nil, io.Discard, &notice); err != nil || !strings.Contains(notice.String(), "read-only") {
				t.Fatal("controller hid failed receipt storage", err, notice.String())
			}
			next := request("box.create")
			next.ID += "-next"
			if err := invoke(t, s, next, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "receipt storage failed") {
				t.Fatalf("mutation after receipt failure: %v", err)
			}
			if writes.Load() != 1 {
				t.Fatal("a second mutation ran")
			}
			// A recovery pass reconciles the original execution's own outcome
			// and reopens admission without a restart or replay.
			if s.recoveryPass(t.Context()) || s.receiptFailure != nil {
				t.Fatal("healthy storage stayed held", s.receiptFailure)
			}
			receipt, err := s.Receipts.Read(req.ID)
			// A failed setup flush cancels the execution after its setup.
			want := map[bool]string{false: "complete", true: "setup-complete"}[duringSetup]
			if err != nil || receipt.State != want || receipt.SetupComplete != duringSetup {
				t.Fatalf("reconciled outcome: %+v, %v", receipt, err)
			}
			if err := invoke(t, s, next, nil, io.Discard, io.Discard); err != nil || writes.Load() != 2 {
				t.Fatal("reconciled controller refused a new mutation", err)
			}
		})
	}
}

func TestCatalogSnapshotFreezesAtomicReplacements(t *testing.T) {
	source, parent := t.TempDir(), t.TempDir()
	for _, kind := range []string{"agents", "features", "capabilities"} {
		if err := os.Mkdir(filepath.Join(source, kind), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := safepath.WriteFile(source, "agents/test.yaml", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, digest, cleanup, err := CatalogSnapshot(source, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := safepath.WriteFile(source, "agents/test.yaml", []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := safepath.ReadFile(first, "agents/test.yaml", MaxDocument)
	if err != nil || string(raw) != "old" {
		t.Fatalf("operation snapshot changed: %q %v", raw, err)
	}
	_, next, cleanup2, err := CatalogSnapshot(source, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if next == digest {
		t.Fatal("catalog revision unchanged")
	}
	if err := os.Symlink(filepath.Join(source, "agents/test.yaml"), filepath.Join(source, "features/unsafe.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := CatalogSnapshot(source, parent); err == nil {
		t.Fatal("accepted symlink catalog")
	}
}

func request(method string) Request {
	return Request{ID: "test-request-0123456789", Method: method, Space: "work", Dash: -1}
}

func hello() Hello { return Hello{ProtocolVersion, "build-1", "work", "generation-1"} }

func TestWireRejectsMalformedFrames(t *testing.T) {
	for _, raw := range [][]byte{
		{0, 0, 0, 0},
		binary.BigEndian.AppendUint32(nil, MaxFrame+1),
		append(binary.BigEndian.AppendUint32(nil, 4), '{'),
		append(binary.BigEndian.AppendUint32(nil, 12), []byte(`{"oops":1234}`)...),
	} {
		if _, err := NewWire(bytes.NewReader(raw), io.Discard).Read(); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestScopeAndCompatibilityBeforeExecution(t *testing.T) {
	ops := map[string]Operation{"box.list": {BoxAllowed: true}, "account.show": {}}
	p := Principal{"operator", "container-1"}
	for _, req := range []Request{
		func() Request { r := request("box.list"); r.Space = "other"; return r }(),
		request("account.show"), request("box.future-command"),
		func() Request {
			r := request("box.list")
			r.Flags = map[string][]string{"all-spaces": {"true"}}
			return r
		}(),
		func() Request {
			r := request("box.list")
			r.Flags = map[string][]string{"from-space": {"other"}}
			return r
		}(),
	} {
		if err := Authorize(p, req, "work", ops); err == nil {
			t.Fatalf("authorized %+v", req)
		}
	}
	if err := Authorize(p, request("box.list"), "work", ops); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Hello){
		func(h *Hello) { h.Bundle = "old" },
		func(h *Hello) { h.Protocol++ },
		func(h *Hello) { h.Space = "other" },
		func(h *Hello) { h.Generation = "old" },
	} {
		h := hello()
		mutate(&h)
		if err := MatchHello(h, hello()); err == nil {
			t.Fatal("accepted incompatible hello")
		}
	}
}

func invoke(t *testing.T, s *Server, req Request, in io.Reader, out, stderr io.Writer) error {
	t.Helper()
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.ServeConn(ctx, right, Principal{}); close(done) }()
	err := Call(ctx, left, hello(), req, ClientIO{In: in, Out: out, Err: stderr})
	<-done
	return err
}

func TestControlCallsDoNotSendUnreadStreamFrames(t *testing.T) {
	// Control methods return directly, without the worker's input pump. Bytes
	// left unread at close make a Unix bridge report ECONNRESET after success.
	for _, method := range []string{"control.ping", "control.drain", "control.resume", "control.receipts", "control.status", "control.acknowledge", "control.acknowledge-all"} {
		t.Run(method, func(t *testing.T) {
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			defer func() { _ = client.Close(); _ = server.Close() }()
			result := make(chan error, 1)
			go func() { result <- Call(ctx, client, hello(), request(method), ClientIO{}) }()
			wire := NewWire(server, server)
			if _, err := wire.Read(); err != nil {
				t.Fatal(err)
			}
			h := hello()
			if err := wire.Write(Frame{Type: "hello", Hello: &h}); err != nil {
				t.Fatal(err)
			}
			if _, err := wire.Read(); err != nil {
				t.Fatal(err)
			}
			_ = server.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			f, err := wire.Read()
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("unexpected stream frame for control request: %+v, %v", f, err)
			}
			_ = server.SetReadDeadline(time.Time{})
			if err := wire.Write(Frame{Type: "exit"}); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamPreservesBytesEOFAndStatus(t *testing.T) {
	s := &Server{Hello: hello(), Operations: map[string]Operation{"box.exec": {}},
		Execute: func(_ context.Context, _ Principal, _ Request, s Streams) (int, error) {
			raw, err := io.ReadAll(s.In)
			if err != nil {
				return 2, err
			}
			_, _ = s.Out.Write(raw)
			_, _ = s.Err.Write([]byte("separate\x00stderr"))
			return 17, nil
		},
	}
	var out, stderr bytes.Buffer
	err := invoke(t, s, request("box.exec"), strings.NewReader("exact\r\n\x00bytes"), &out, &stderr)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Status != 17 {
		t.Fatalf("exit: %v", err)
	}
	if out.String() != "exact\r\n\x00bytes" || stderr.String() != "separate\x00stderr" {
		t.Fatalf("changed stream: %q / %q", out.String(), stderr.String())
	}
}

func TestStreamInputFlowControlBeyondWindow(t *testing.T) {
	raw := bytes.Repeat([]byte("large\x00input\n"), 100000)
	s := &Server{Hello: hello(), Operations: map[string]Operation{"box.exec": {}},
		Execute: func(_ context.Context, _ Principal, _ Request, s Streams) (int, error) {
			_, err := io.Copy(s.Out, s.In)
			return 0, err
		},
	}
	var out bytes.Buffer
	if err := invoke(t, s, request("box.exec"), bytes.NewReader(raw), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("stream truncated: %d/%d", out.Len(), len(raw))
	}
}

func TestOperatorRevocationCancelsExistingConnection(t *testing.T) {
	var revoked atomic.Bool
	s := &Server{Hello: hello(), Operations: map[string]Operation{"box.logs": {BoxAllowed: true}},
		CheckPrincipal: func(_ context.Context, _ Principal) error {
			if revoked.Load() {
				return errors.New("revoked")
			}
			return nil
		},
		Execute: func(ctx context.Context, _ Principal, _ Request, _ Streams) (int, error) {
			revoked.Store(true)
			<-ctx.Done()
			return 2, ctx.Err()
		},
	}
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.ServeConn(ctx, right, Principal{Box: "operator", Incarnation: "incarnation"}); close(done) }()
	err := Call(ctx, left, hello(), request("box.logs"), ClientIO{Out: io.Discard, Err: io.Discard})
	<-done
	if err == nil || ctx.Err() != nil {
		t.Fatalf("revocation did not promptly close connection: %v / %v", err, ctx.Err())
	}
}

func TestReceiptsPreventReplayAndDrainClosesAdmission(t *testing.T) {
	var called atomic.Int32
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()},
		Operations: map[string]Operation{"box.create": {Mutates: true}},
		Execute: func(_ context.Context, _ Principal, _ Request, _ Streams) (int, error) {
			called.Add(1)
			return 0, nil
		},
	}
	r := request("box.create")
	if err := invoke(t, s, r, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := invoke(t, s, r, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("replayed mutation")
	}
	if called.Load() != 1 {
		t.Fatal("worker replayed")
	}
	receipt, err := s.Receipts.Read(r.ID)
	if err != nil || receipt.State != "complete" {
		t.Fatalf("%+v %v", receipt, err)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.ID += "-next"
	if err := invoke(t, s, r, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("admitted after drain")
	}
}

func TestDrainWaitsForSetupButNotAttachment(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}}
	release, _, err := s.admit(request("box.attach"), Principal{}, true)
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- s.Drain(context.Background()) }()
	select {
	case <-waited:
		t.Fatal("drained active setup")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	release()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}
}

func TestStorePublicationRecoveryAndNativeStatePreserved(t *testing.T) {
	disk := t.TempDir()
	native := filepath.Join(disk, "account-state")
	if err := os.Mkdir(native, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "history"), []byte("native history"), 0o600); err != nil {
		t.Fatal(err)
	}
	boot := &spaceconfig.Bootstrap{Name: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid", Mounts: []spaceconfig.Mount{{Source: "/work", Writable: true}}}
	bundle := StoreBundle{Schema: 1, Settings: spaceconfig.Settings{SchemaVersion: 1, Name: "work", DefaultAccount: "personal", DefaultWorkdir: "/work"},
		Accounts: map[string][]byte{"personal": []byte("schema_version: 1\nname: personal\n")}}
	first, err := PublishStore(disk, boot, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.RemoveFile(filepath.Join(disk, ".controller"), "ready"); err != nil {
		t.Fatal(err)
	}
	second, err := PublishStore(disk, boot, bundle)
	if err != nil || second != first {
		t.Fatalf("recovery: %s %v", second, err)
	}
	raw, _ := os.ReadFile(filepath.Join(native, "history"))
	if string(raw) != "native history" {
		t.Fatal("native state changed")
	}
	if err := ActivateStore(disk); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishStore(disk, boot, bundle); err == nil {
		t.Fatal("reinitialized active store")
	}
}

func TestStoreRefusesSubstitutedConfig(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "config")); err != nil {
		t.Fatal(err)
	}
	if err := verifyDocuments(filepath.Join(root, "config"), map[string][]byte{"space.yaml": {}}); err == nil {
		t.Fatal("accepted symlink store")
	}
}

func TestResumeDoesNotClearUncertainOutcomesOrBypassFlushFailure(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, RecoveryRequired: true, Operations: map[string]Operation{"box.create": {Mutates: true}}}
	// A legacy unknown outcome holds the whole space.
	earlier := request("box.create")
	earlier.ID = "earlier-0123456789abcdef"
	held, err := s.Receipts.Begin(earlier, Principal{}, "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	held.State = "uncertain"
	if err := s.Receipts.Save(held); err != nil {
		t.Fatal(err)
	}
	if err := s.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.ResumeAdmission = func() error { return errors.New("flush failed") }
	if err := s.Resume(); err == nil || !s.draining {
		t.Fatal("resumed after failed maintenance flush")
	}
	s.ResumeAdmission = func() error { return nil }
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	if !s.RecoveryRequired {
		t.Fatal("resume cleared uncertain outcomes")
	}
	if _, _, err := s.admit(request("box.create"), Principal{}, true); err == nil {
		t.Fatal("resume authorized unreviewed mutation")
	}
	if err := s.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.receiptFailure = errors.New("durability unknown")
	if err := s.Resume(); err == nil {
		t.Fatal("resume hid receipt failure")
	}
}

func TestOperatorCannotResumeHostMaintenance(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, draining: true}
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.ServeConn(ctx, right, Principal{Box: "operator", Incarnation: "current"}); close(done) }()
	err := Call(ctx, left, hello(), request("control.resume"), ClientIO{Out: io.Discard, Err: io.Discard})
	<-done
	if err == nil || !s.draining {
		t.Fatal("operator resumed maintenance", err)
	}
}

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
)

func TestLiveReceiptStorageFailurePreservesExecutionAndFlushesBeforeWorkerEnds(t *testing.T) {
	s := storeServer(t)
	s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintBox}
	original := writeReceipt
	var reject atomic.Bool
	reject.Store(true)
	writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
		var r Receipt
		if json.Unmarshal(raw, &r) == nil && r.EffectPhase == EffectStarted && reject.Load() {
			return errors.New("record fsync unavailable")
		}
		return original(dir, name, raw, mode)
	}
	t.Cleanup(func() { writeReceipt = original })
	armed, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		calls.Add(1)
		if err := streams.BeginEffects(ctx, EffectIntent{Scope: writes(box("web"))}); err != nil {
			return 2, err
		}
		close(armed)
		select {
		case <-finish:
		case <-ctx.Done():
			return 2, ctx.Err()
		}
		return 0, streams.RecordEvidence.CompleteEffects(ctx, 0)
	}
	req := request("box.start")
	done := make(chan error, 1)
	go func() { done <- invoke(t, s, req, nil, io.Discard, io.Discard) }()
	select {
	case <-armed:
	case err := <-done:
		t.Fatalf("record failure refused execution: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("worker was not armed")
	}
	if _, err := s.RecoverReceipts(); err != nil {
		t.Fatal(err)
	}
	before, err := s.Receipts.Read(req.ID)
	if err != nil || before.EffectPhase != EffectWaiting {
		t.Fatal("expected the older waiting copy", before, err)
	}
	reject.Store(false)
	if err := s.repairStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Receipts.Read(req.ID)
	if err != nil || saved.State != "pending" || saved.EffectPhase != EffectStarted || !saved.RecordedLate {
		t.Fatal(saved, err)
	}
	s.mu.Lock()
	live := s.executions[req.ID]
	s.mu.Unlock()
	if live == nil || live.settled {
		t.Fatal("late publication released the running execution")
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := s.awaitReservations(waitCtx, "other", writes(box("web"))); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("late publication lost running exclusion", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal("caller lost its actual successful result", err)
	}
	saved, err = s.Receipts.Read(req.ID)
	if err != nil || saved.State != "complete" || saved.Status != 0 || !saved.RecordedLate || calls.Load() != 1 {
		t.Fatal(saved, err, calls.Load())
	}
}

func TestUnpublishedReceiptNeverOverwritesMalformedPriorID(t *testing.T) {
	for _, settleFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "repair while running", true: "repair after completion"}[settleFirst], func(t *testing.T) {
			s := storeServer(t)
			req := request("box.start")
			path := s.Receipts.Dir + "/" + req.ID + ".json"
			raw := []byte("malformed earlier record")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			r, err := s.beginExecution(req, Principal{}, true)
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			r.EffectPhase, r.Scope = EffectStarted, writes(box("web"))
			if settleFirst {
				r.State, r.Status, r.Finished = "complete", 7, time.Now().UTC()
				r.Completion = &Completion{Status: 7, At: r.Finished}
			}
			s.mu.Lock()
			err = s.persistExecution(*r, settleFirst)
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := os.ReadFile(path)
			if err != nil || string(bytes) != string(raw) {
				t.Fatal("unverified file was overwritten", string(bytes), err)
			}
			old := Receipt{ID: req.ID, Method: "account.import", Principal: Principal{Box: "earlier", Incarnation: "old"}, State: "pending", Started: r.Started.Add(-time.Hour), Generation: "earlier", DiskID: s.DiskID, ProofVersion: documentProofVersion, EffectPhase: EffectWaiting}
			if err := s.Receipts.Save(old); err != nil {
				t.Fatal(err)
			}
			if err := s.repairStorage(t.Context()); err != nil {
				t.Fatal(err)
			}
			saved, err := s.Receipts.Read(req.ID)
			if err != nil || !sameAdmission(saved, old) || saved.State != "interrupted" {
				t.Fatal(saved, err)
			}
			s.mu.Lock()
			view, err := s.operationView(req.ID, Principal{})
			live := s.executions[req.ID]
			note, known := s.collisions[req.ID]
			s.mu.Unlock()
			if err != nil || !view.NotStored || view.Historical == nil || !known || !sameAdmission(note.historical, old) {
				t.Fatal(view, err)
			}
			if settleFirst {
				if live != nil || view.State != "not-recorded" || view.Completion != nil || view.Scope != nil {
					t.Fatal("completed collision was not released honestly", view)
				}
			} else {
				if live == nil || live.receipt.EffectPhase != EffectStarted || live.receipt.Scope.Writes[0] != box("web") || view.State != "pending" {
					t.Fatal("historical recovery changed the live execution", view)
				}
				finished := *live.receipt
				finished.State = "complete"
				finished.Finished = time.Now().UTC()
				s.mu.Lock()
				err = s.persistExecution(finished, true)
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			saved, err = s.Receipts.Read(req.ID)
			if err != nil || !sameAdmission(saved, old) {
				t.Fatal("later result replaced earlier record", saved, err)
			}
			s.mu.Lock()
			_, err = s.beginExecution(req, Principal{}, true)
			s.mu.Unlock()
			if err == nil {
				t.Fatal("process-local collision note was forgotten")
			}
			// Another principal sees neither the current method nor its principal.
			s.mu.Lock()
			other, err := s.operationView(req.ID, old.Principal)
			s.mu.Unlock()
			if err != nil || other.Method != "" || other.Historical == nil {
				t.Fatal("visibility was not independent", other, err)
			}
			encoded, _ := json.Marshal(other)
			if strings.Contains(string(encoded), `"method":"box.start"`) {
				t.Fatal("collision disclosed another principal's method", string(encoded))
			}
		})
	}
}

func TestUnpublishedReceiptPublishesOnlyAfterAbsentOrSameAdmission(t *testing.T) {
	for _, repair := range []string{"absent", "same"} {
		for _, ended := range []bool{false, true} {
			t.Run(repair+map[bool]string{false: "-running", true: "-ended"}[ended], func(t *testing.T) {
				s := storeServer(t)
				req := request("box.start")
				path := s.Receipts.Dir + "/" + req.ID + ".json"
				if err := os.WriteFile(path, []byte("unreadable record"), 0600); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				r, err := s.beginExecution(req, Principal{}, true)
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				initial := *r
				r.EffectPhase, r.Scope = EffectStarted, writes(box("web"))
				if ended {
					r.State = "complete"
					r.Finished = time.Now().UTC()
				}
				s.mu.Lock()
				err = s.persistExecution(*r, ended)
				_, duplicate := s.beginExecution(req, Principal{}, true)
				s.mu.Unlock()
				if err != nil || duplicate == nil {
					t.Fatal("unpublished registration lost", err, duplicate)
				}
				if repair == "absent" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else if err := s.Receipts.Save(initial); err != nil {
					t.Fatal(err)
				}
				original := writeReceipt
				writes := 0
				writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
					if name == req.ID+".json" {
						writes++
					}
					return original(dir, name, raw, mode)
				}
				t.Cleanup(func() { writeReceipt = original })
				if err := s.repairStorage(t.Context()); err != nil {
					t.Fatal(err)
				}
				saved, err := s.Receipts.Read(req.ID)
				if err != nil || saved.EffectPhase != EffectStarted || !saved.RecordedLate || writes != 1 {
					t.Fatal(saved, err, writes)
				}
				s.mu.Lock()
				entry := s.executions[req.ID]
				s.mu.Unlock()
				if (entry == nil) != ended {
					t.Fatal("flush changed worker settlement", ended, entry)
				}
				if !ended && saved.State != "pending" || ended && saved.State != "complete" {
					t.Fatal(saved)
				}
			})
		}
	}
}

func TestRecordFailureKeepsTheCommandsOwnFailureResult(t *testing.T) {
	s := storeServer(t)
	s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintBox}
	original := writeReceipt
	writeReceipt = func(string, string, []byte, os.FileMode) error { return errors.New("receipt write unavailable") }
	t.Cleanup(func() { writeReceipt = original })
	calls := 0
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		calls++
		if err := streams.BeginEffects(ctx, EffectIntent{Scope: writes(box("web"))}); err != nil {
			return 2, err
		}
		if err := streams.RecordEvidence.CompleteEffects(ctx, 7); err != nil {
			return 2, err
		}
		return 7, errors.New("the actual operation failed")
	}
	req := request("box.start")
	err := invoke(t, s, req, nil, io.Discard, io.Discard)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Status != 7 || exit.Message != "the actual operation failed" || calls != 1 {
		t.Fatal(err, calls)
	}
	s.mu.Lock()
	view, viewErr := s.operationView(req.ID, Principal{})
	s.mu.Unlock()
	if viewErr != nil || view.Status != 7 || view.State != "complete" || view.Source != "memory" {
		t.Fatal(view, viewErr)
	}
}

func TestActualNativeStorageFailureRemainsDistinctFromRecordFailure(t *testing.T) {
	s := storeServer(t)
	s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintBox}
	s.VerifyStorage = func(context.Context) error { return nil }
	calls := 0
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		calls++
		if err := streams.BeginEffects(ctx, EffectIntent{Scope: writes(box("web"))}); err != nil {
			return 2, err
		}
		return 0, streams.RecordEvidence.CompleteEffects(ctx, 0)
	}
	s.receiptFailure = errors.New("record store unavailable")
	req := request("box.start")
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil || calls != 1 {
		t.Fatal("record health became permission", err, calls)
	}
	s.mu.Lock()
	s.noteNativeStorageFailure(errors.New("native UUID verification failed"))
	s.mu.Unlock()
	req.ID += "-native"
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err == nil || calls != 1 {
		t.Fatal("actual storage identity was bypassed", err, calls)
	}
	if err := s.repairStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	req.ID += "-verified"
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil || calls != 2 {
		t.Fatal("verified identity did not restore normal operation", err, calls)
	}
}

func TestPostRenameRecordFailureKeepsLatestCompletionDirty(t *testing.T) {
	s := storeServer(t)
	s.Operations["box.start"] = Operation{Mutates: true, EffectGated: true, Footprint: FootprintBox}
	original := writeReceipt
	var fail atomic.Bool
	fail.Store(true)
	writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
		if err := original(dir, name, raw, mode); err != nil {
			return err
		}
		if fail.Load() {
			return &atomicfile.CommitError{Err: errors.New("directory fsync failed after rename")}
		}
		return nil
	}
	t.Cleanup(func() { writeReceipt = original })
	calls := 0
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		calls++
		if err := streams.BeginEffects(ctx, EffectIntent{Scope: writes(box("web"))}); err != nil {
			return 2, err
		}
		return 0, streams.RecordEvidence.CompleteEffects(ctx, 0)
	}
	req := request("box.start")
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	entry := s.executions[req.ID]
	s.mu.Unlock()
	if entry == nil || !entry.settled || entry.written == entry.revision {
		t.Fatal("unclear sync was treated as durable")
	}
	published, err := s.Receipts.Read(req.ID)
	if err != nil || published.State != "complete" || published.Completion == nil {
		t.Fatal(published, err)
	}
	fail.Store(false)
	if err := s.repairStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Receipts.Read(req.ID)
	if err != nil || saved.State != "complete" || saved.Status != 0 || !saved.RecordedLate || calls != 1 {
		t.Fatal(saved, err, calls)
	}
	s.mu.Lock()
	entry = s.executions[req.ID]
	s.mu.Unlock()
	if entry != nil {
		t.Fatal("durable final revision was not released")
	}
}

func TestNativeVerificationCannotClearANewerIdentityFailure(t *testing.T) {
	s := storeServer(t)
	s.mu.Lock()
	s.noteNativeStorageFailure(errors.New("first native failure"))
	s.mu.Unlock()
	verifying, finish := make(chan struct{}), make(chan struct{})
	s.VerifyStorage = func(context.Context) error { close(verifying); <-finish; return nil }
	done := make(chan error, 1)
	go func() { done <- s.repairStorage(t.Context()) }()
	<-verifying
	s.mu.Lock()
	s.noteNativeStorageFailure(errors.New("newer native failure"))
	s.mu.Unlock()
	close(finish)
	if err := <-done; err == nil {
		t.Fatal("old verification cleared a newer native failure")
	}
	s.mu.Lock()
	failure := s.nativeStorageFailure
	s.mu.Unlock()
	if failure == nil || !strings.Contains(failure.Error(), "newer") {
		t.Fatal(failure)
	}
	s.VerifyStorage = func(context.Context) error { return nil }
	if err := s.repairStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadableReceiptSymlinkNeverBecomesPermissionOrReplacement(t *testing.T) {
	s := storeServer(t)
	req := request("box.start")
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.Receipts.Dir, req.ID+".json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	calls := 0
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		if err := streams.BeginEffects(ctx, scopeIntent(writes(box("web")))); err != nil {
			return 2, err
		}
		calls++
		return 0, nil
	}
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "keep" {
		t.Fatal("unreadable path target overwritten", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unknown ID entry replaced", err)
	}
	if calls != 1 {
		t.Fatal("operation did not execute exactly once", calls)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := s.repairStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Receipts.Read(req.ID)
	if err != nil || saved.State != "complete" || !saved.RecordedLate {
		t.Fatal("absence did not permit late publication", saved, err)
	}
}

func TestLatePublicationSerializesWithLaterCompletion(t *testing.T) {
	s := storeServer(t)
	r := seedLiveExecution(t, s, "ordered-publication-012345", writes(box("web")), nil)
	original := writeReceipt
	t.Cleanup(func() { writeReceipt = original })
	writeReceipt = func(string, string, []byte, os.FileMode) error { return errors.New("record unavailable") }
	s.mu.Lock()
	_ = s.persistExecution(*r, false)
	s.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
		if blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return original(dir, name, raw, mode)
	}
	flushed := make(chan error, 1)
	go func() { s.mu.Lock(); defer s.mu.Unlock(); flushed <- s.flushExecutions() }()
	<-entered
	finished := make(chan error, 1)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		latest := *s.executions[r.ID].receipt
		latest.State = "complete"
		latest.Status = 7
		latest.Finished = time.Now().UTC()
		finished <- s.persistExecution(latest, true)
	}()
	close(release)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	saved, err := s.Receipts.Read(r.ID)
	if err != nil || saved.State != "complete" || saved.Status != 7 || !saved.RecordedLate {
		t.Fatal("late snapshot replaced completion", saved, err)
	}
	s.mu.Lock()
	entry := s.executions[r.ID]
	s.mu.Unlock()
	if entry != nil {
		t.Fatal("settled current revision retained", entry)
	}
}

func TestImageJobFirstFactIsStrictCreateOnly(t *testing.T) {
	t.Run("write-error", func(t *testing.T) {
		s := storeServer(t)
		original := createReceipt
		t.Cleanup(func() { createReceipt = original })
		failure := errors.New("first strict stage write failed")
		calls := 0
		createReceipt = func(string, string, []byte, os.FileMode) error { calls++; return failure }
		s.receiptFailure = errors.New("ordinary recording failed")
		_, err := s.beginImageJob("strict-first-fact-012345")
		if !errors.Is(err, failure) || calls != 1 || len(s.executions) != 0 {
			t.Fatal("stage failure lost or kept as late state", calls, err)
		}
	})
	t.Run("same-id-preserved", func(t *testing.T) {
		s := storeServer(t)
		id := "strict-same-id-012345"
		path := filepath.Join(s.Receipts.Dir, id+".json")
		if err := os.WriteFile(path, []byte("unreadable old record"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.beginImageJob(id); !errors.Is(err, os.ErrExist) {
			t.Fatal("unknown old ID replaced", err)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "unreadable old record" {
			t.Fatal("old record changed", err)
		}
	})
	t.Run("directory-unavailable", func(t *testing.T) {
		s := storeServer(t)
		restore := breakStore(t, s.Receipts.Dir)
		defer restore()
		original := createReceipt
		t.Cleanup(func() { createReceipt = original })
		calls := 0
		createReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
			calls++
			return original(dir, name, raw, mode)
		}
		_, err := s.beginImageJob("strict-unavailable-012345")
		if err == nil || calls != 1 || len(s.executions) != 0 {
			t.Fatal("build refused before first write or retained a late stage", calls, err)
		}
	})
}

// This uses real inherited checkpoint dispatch with representative operation
// classes; the child is a controlled worker fixture, not a VM/product smoke.
func TestOrdinaryRecordingFailureAtEveryCheckpointKeepsWorkerResult(t *testing.T) {
	for _, method := range []string{"box.stop", "box.start", "account.secret.set", "chat.stream", "image.pull", "box.exec", "box.set"} {
		for _, point := range []string{"begin", "gate", "expand", "proof", "setup", "completion", "final"} {
			if point == "setup" && !setupThenAttach(method) {
				continue
			}
			t.Run(method+"/"+point, func(t *testing.T) {
				s := storeServer(t)
				s.Operations = map[string]Operation{method: {Mutates: true, EffectGated: true}}
				s.Execute = RunWorker
				original := writeReceipt
				t.Cleanup(func() { writeReceipt = original })
				var failed atomic.Bool
				fault := errors.New("injected " + point + " record write failure")
				writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
					var r Receipt
					if err := json.Unmarshal(raw, &r); err != nil {
						return err
					}
					matches := false
					switch point {
					case "begin":
						matches = r.EffectPhase == EffectWaiting
					case "gate":
						matches = r.EffectPhase == EffectStarted && r.Scope != nil && len(r.Scope.Writes) == 1 && len(r.Proofs) == 0
					case "expand":
						matches = r.Scope != nil && len(r.Scope.Writes) == 2 && len(r.Proofs) == 0
					case "proof":
						matches = len(r.Proofs) == 1 && !r.SetupComplete && r.Completion == nil
					case "setup":
						matches = r.SetupComplete && r.Completion == nil
					case "completion":
						matches = r.State == "pending" && r.Completion != nil
					case "final":
						matches = r.State != "pending"
					}
					if matches && failed.CompareAndSwap(false, true) {
						return fault
					}
					return original(dir, name, raw, mode)
				}
				req := request(method)
				path := filepath.Join(t.TempDir(), "effects")
				req.Args = []string{"recording-matrix", path}
				if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
					t.Fatal("record error replaced result", err)
				}
				writeReceipt = original
				if !failed.Load() {
					t.Fatal("write point not exercised")
				}
				body, err := os.ReadFile(path)
				if err != nil || string(body) != "one\n" {
					t.Fatal("effect missing or repeated", string(body), err)
				}
				if err := s.repairStorage(t.Context()); err != nil {
					t.Fatal(err)
				}
				r, err := s.Receipts.Read(req.ID)
				if err != nil || r.State != "complete" || r.Status != 0 || !r.RecordedLate || r.Completion == nil {
					t.Fatal("latest result not recorded", r, err)
				}
			})
		}
	}
}

func TestUnreadableInventoryRunsOrdinaryMethodClasses(t *testing.T) {
	for _, method := range []string{"box.stop", "box.start", "account.secret.set", "box.exec"} {
		t.Run(method, func(t *testing.T) {
			s := storeServer(t)
			s.Operations = map[string]Operation{method: {Mutates: true, EffectGated: true}}
			s.Execute = RunWorker
			restore := breakStore(t, s.Receipts.Dir)
			restored := false
			t.Cleanup(func() {
				if !restored {
					restore()
				}
			})
			req := request(method)
			path := filepath.Join(t.TempDir(), "effects")
			req.Args = []string{"recording-matrix", path}
			if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
				t.Fatal("broken inventory refused command", err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "one\n" {
				t.Fatal("effect missing or replayed", string(body), err)
			}
			s.mu.Lock()
			e := s.executions[req.ID]
			s.mu.Unlock()
			if e == nil || !e.settled || !e.unpublished {
				t.Fatal("unstored result not retained", e)
			}
			restore()
			restored = true
			if err := s.repairStorage(t.Context()); err != nil {
				t.Fatal(err)
			}
			saved, err := s.Receipts.Read(req.ID)
			if err != nil || saved.State != "complete" || !saved.RecordedLate {
				t.Fatal(saved, err)
			}
		})
	}
}

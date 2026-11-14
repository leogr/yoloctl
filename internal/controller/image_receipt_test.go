package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/leogr/yoloctl/internal/spacelock"
)

type checkedPreflight struct {
	*jobBackend
	check func() error
}

func (b *checkedPreflight) Preflight(context.Context, ImageRecipe) error { return b.check() }
func TestPrivatePreflightAndCleanupHaveIndependentDurablePermission(t *testing.T) {
	m, b := newJobs(t, 2)
	called := false
	m.backend = &checkedPreflight{jobBackend: b, check: func() error {
		records, err := m.server.Receipts.List()
		if err != nil {
			return err
		}
		permitted := false
		for _, r := range records {
			permitted = permitted || r.Method == "image.job" && r.EffectPhase == EffectStarted && r.Image.Stage == "preflight"
		}
		if !permitted {
			t.Error("preflight ran before its durable permit")
		}
		return m.Cleanup(t.Context(), func(ctx context.Context, permit func() error) error {
			if err := permit(); err != nil {
				return err
			}
			r, err := m.server.Receipts.Read(ImageCleanupID(ctx))
			if err != nil {
				return err
			}
			if r.Method != "image.cleanup" || r.EffectPhase != EffectStarted || !containsResource(r.Scope.Writes, Resource{Kind: ResourceImages}) {
				t.Error("deletion has no durable domain permit")
			}
			called = true
			return nil
		})
	}}
	b.release <- struct{}{}
	result, err := m.Acquire(t.Context(), recipe("cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	result.Release()
	if !called {
		t.Fatal("cleanup never ran")
	}
}
func TestImageReceiptOwnersSettleFailuresWithoutReplay(t *testing.T) {
	for _, point := range []string{"permit", "completion", "abandonment", "last-interest"} {
		t.Run(point, func(t *testing.T) {
			m, b := newJobs(t, 2)
			original, orphanWriter := writeReceipt, writeImageOrphan
			t.Cleanup(func() { writeReceipt = original; writeImageOrphan = orphanWriter })
			injected := false
			failure := errors.New("simulated image metadata ENOSPC")
			if point == "abandonment" {
				writeImageOrphan = func(dir, name string, raw []byte, mode os.FileMode) error {
					if !injected {
						injected = true
						return failure
					}
					return orphanWriter(dir, name, raw, mode)
				}
			} else {
				writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
					match := bytes.Contains(raw, []byte(`"method":"image.job"`))
					switch point {
					case "permit":
						match = match && bytes.Contains(raw, []byte(`"stage":"preflight"`)) && !bytes.Contains(raw, []byte(`"completion":`))
					case "completion":
						match = match && bytes.Contains(raw, []byte(`"completion":`))
					case "last-interest":
						match = match && bytes.Contains(raw, []byte(`"released_at":`))
					}
					if match && !injected {
						injected = true
						return failure
					}
					return original(dir, name, raw, mode)
				}
			}
			var old *imageJob
			if point == "abandonment" {
				b.ignoreCancellation = true
				ctx, cancel := context.WithCancel(t.Context())
				pending := acquire(m, ctx, recipe("abandon"))
				phase := take(t, b.started)
				m.mu.Lock()
				old = m.jobs[phase.JobID]
				m.mu.Unlock()
				cancel()
				take(t, pending)
			} else {
				b.release <- struct{}{}
				result, err := m.Acquire(t.Context(), recipe(point))
				if point == "last-interest" {
					if err != nil {
						t.Fatal(err)
					}
					result.Release()
				} else if err == nil {
					result.Release()
					t.Fatal("failure did not reach caller")
				}
			}
			if !injected {
				t.Fatal("failure point never reached")
			}
			writeReceipt, writeImageOrphan = original, orphanWriter
			if err := m.server.repairStorage(t.Context()); err != nil {
				t.Fatal("finished metadata owner blocked recovery", err)
			}
			b.mu.Lock()
			running := b.running
			b.mu.Unlock()
			if point == "abandonment" && running != 1 {
				t.Fatal("test did not retain an unresponsive solve")
			}
			if old != nil {
				before, err := m.server.Receipts.Read(old.phase.JobID)
				if err != nil {
					t.Fatal(err)
				}
				b.release <- struct{}{}
				take(t, old.done)
				after, err := m.server.Receipts.Read(old.phase.JobID)
				if err != nil || !sameReceipt(before, after) {
					t.Fatal("late solve rewrote abandoned metadata", err)
				}
			}
		})
	}
}
func TestCleanupCancellationKeepsItsPublicEffectUnknown(t *testing.T) {
	m, _ := newJobs(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	var id string
	err := m.Cleanup(ctx, func(ctx context.Context, permit func() error) error {
		if err := permit(); err != nil {
			return err
		}
		id = ImageCleanupID(ctx)
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r, err := m.server.Receipts.Read(id)
	if err != nil || r.State != "interrupted" || r.unresolved() || r.Completion != nil {
		t.Fatal("cleanup claimed an unproved outcome", r, err)
	}
}
func TestLegacyInputLookupSurvivesExpandedImageGraphIdentity(t *testing.T) {
	m, backend := newJobs(t, 2)
	input := recipe("legacy")
	input.BaseReference = "ubuntu:24.04"
	legacy, _ := input.Identity()
	id := strings.Repeat("d", 64)
	phase := ImagePhase{JobID: id, SolveRef: id, PreparedRef: "yoloctl/prepared/" + id, StableInput: legacy, Attempt: legacy, Incarnation: "legacy"}
	r := Receipt{ID: id, Method: "image.job", State: "uncertain", ProofVersion: 2, DiskID: m.server.DiskID, EffectPhase: EffectStarted, Image: &phase, Scope: &Scope{Writes: []Resource{inputResource(legacy), {Kind: ResourceImages, Name: phase.PreparedRef}}}}
	if err := m.server.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := m.server.Receipts.Recover(); err != nil {
		t.Fatal(err)
	}
	input.Images = []imagejob.ImageInput{{Reference: "docker.io/library/ubuntu:24.04", Source: "ubuntu:24.04", Digest: input.BaseDigest, Platform: input.Platform}}
	pending := acquire(m, t.Context(), input)
	take(t, backend.started)
	backend.release <- struct{}{}
	result := take(t, pending)
	if result.err != nil {
		t.Fatal("legacy ended attempt fenced new build", result.err)
	}
	result.image.Release()
}

func TestStorageRepairCannotSettleActiveImageMetadataOwner(t *testing.T) {
	m, b := newJobs(t, 2)
	waiting := acquire(m, t.Context(), recipe("active-owner"))
	phase := take(t, b.started)
	m.server.mu.Lock()
	r, err := m.server.Receipts.Read(phase.JobID)
	if err != nil {
		m.server.mu.Unlock()
		t.Fatal(err)
	}
	m.server.record(r, errors.New("unrelated transient write failure"), false, false, "")
	m.server.mu.Unlock()
	if err := m.server.repairStorage(t.Context()); err == nil {
		t.Fatal("active metadata owner was prematurely settled")
	}
	b.release <- struct{}{}
	got := take(t, waiting)
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.image.Release()
	if err := m.server.repairStorage(t.Context()); err != nil {
		t.Fatal("finished owner cannot repair", err)
	}
}
func TestImageCleanupRefusesIntersectingHoldDrainAndStorageFailure(t *testing.T) {
	for _, mode := range []string{"hold", "drain", "storage", "native-storage"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := newJobs(t, 2)
			switch mode {
			case "hold":
				seedReceipt(t, m.server, "cleanup-held-receipt", "uncertain", writes(Resource{Kind: ResourceImages, Name: "example/held:latest"}), func(r *Receipt) { r.ProofVersion = 2 })
			case "drain":
				m.server.draining = true
			case "storage":
				m.server.receiptFailure = errors.New("unavailable receipt store")
			case "native-storage":
				m.server.nativeStorageFailure = errors.New("native disk unavailable")
			}
			called := false
			err := m.Cleanup(t.Context(), func(context.Context, func() error) error { called = true; return nil })
			if mode == "hold" || mode == "storage" {
				if err != nil || !called {
					t.Fatal("finished outcome fenced cleanup", err)
				}
			} else if err == nil || called {
				t.Fatal("unpermitted deleting helper ran", err)
			}
			if mode != "storage" && m.server.receiptFailure != nil {
				t.Fatal("ordinary admission refusal created a storage failure", m.server.receiptFailure)
			}
		})
	}
}

func TestCleanupReceiptFailuresRecoverWithoutRepeatingDeletion(t *testing.T) {
	for _, point := range []string{"permit", "completion"} {
		t.Run(point, func(t *testing.T) {
			m, _ := newJobs(t, 2)
			original := writeReceipt
			t.Cleanup(func() { writeReceipt = original })
			injected := false
			calls := 0
			writeReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
				match := bytes.Contains(raw, []byte(`"method":"image.cleanup"`)) && bytes.Contains(raw, []byte(`"effect_phase":"started"`))
				complete := bytes.Contains(raw, []byte(`"completion":`))
				if !injected && match && (point == "completion" && complete || point == "permit" && !complete) {
					injected = true
					return errors.New("cleanup receipt ENOSPC")
				}
				return original(dir, name, raw, mode)
			}
			err := m.Cleanup(t.Context(), func(_ context.Context, permit func() error) error {
				if err := permit(); err != nil {
					return err
				}
				calls++
				return nil
			})
			if err != nil || !injected {
				t.Fatal("cleanup record failure replaced result or was not exercised", err)
			}
			want := 1
			if calls != want {
				t.Fatal("helper crossed missing permission", calls, want)
			}
			writeReceipt = original
			if err := m.server.repairStorage(t.Context()); err != nil {
				t.Fatal("cleanup owner did not settle", err)
			}
			if calls != want {
				t.Fatal("storage repair repeated cleanup")
			}
		})
	}
}

func TestCleanupReservesImageDomainOnlyAfterNativeLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m, _ := newJobs(t, 2)
	unlock, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.Cleanup(t.Context(), func(ctx context.Context, permit func() error) error {
			close(waiting)
			release, err := spacelock.Acquire(ctx, "work", true)
			if err != nil {
				return err
			}
			defer release()
			return permit()
		})
	}()
	take(t, waiting)
	// Model a normal effect gate already holding the native lock. Cleanup is
	// still waiting for that lock, so this gate must not wait on cleanup.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := m.server.awaitReservations(ctx, "normal-worker", writes(Resource{Kind: ResourceImages})); err != nil {
		t.Fatal("cleanup reserved ahead of its native lock", err)
	}
	unlock()
	if err := take(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestImageCreateCollisionSurvivesAcquireLeaveAndCleanup(t *testing.T) {
	for _, kind := range []string{"malformed", "unrelated-completion", "unrelated-image-job"} {
		t.Run(kind, func(t *testing.T) {
			m, b := newJobs(t, 2)
			original := createReceipt
			t.Cleanup(func() { createReceipt = original })
			var target string
			var before []byte
			createReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
				target = filepath.Join(dir, name)
				before = []byte("old unreadable record")
				if kind != "malformed" {
					var current Receipt
					if err := json.Unmarshal(raw, &current); err != nil {
						return err
					}
					old := Receipt{ID: current.ID, Method: "account.secret.set", Principal: Principal{}, State: "complete", Status: 7,
						Started: current.Started.Add(-time.Hour), Finished: current.Started.Add(-time.Minute), Generation: "older-controller",
						ProofVersion: documentProofVersion, DiskID: testDisk, EffectPhase: EffectStarted,
						Scope: writes(acct("old-account")), Completion: &Completion{At: current.Started.Add(-time.Minute), Status: 7}}
					if kind == "unrelated-image-job" {
						old.Method = "image.job"
						old.ProofVersion = 2
						old.Status = 0
						old.Completion.Status = 0
						old.Scope = writes(Resource{Kind: ResourceImages, Name: "yoloctl/prepared/" + old.ID})
						old.Image = &ImagePhase{PrivateVersion: 1, JobID: old.ID, StableInput: strings.Repeat("a", 64), Attempt: strings.Repeat("b", 64), SolveRef: old.ID,
							Incarnation: "old-vm", PreparedRef: "yoloctl/prepared/" + old.ID, Stage: "build", Output: "sha256:" + strings.Repeat("c", 64), Lease: "old-lease"}
					}
					var err error
					before, err = json.Marshal(old)
					if err != nil {
						return err
					}
				}
				if err := os.WriteFile(target, before, 0600); err != nil {
					return err
				}
				if kind != "malformed" {
					id := strings.TrimSuffix(name, ".json")
					if _, err := m.server.Receipts.Read(id); err != nil {
						return err
					}
				}
				return original(dir, name, raw, mode)
			}
			_, err := m.Acquire(t.Context(), recipe("first-create-collision"))
			createReceipt = original
			if !errors.Is(err, os.ErrExist) {
				t.Fatal("expected create-only refusal", err)
			}
			check := func() {
				t.Helper()
				raw, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(raw, before) {
					t.Fatalf("refused job replaced unrelated record: %s (%v)", raw, err)
				}
			}
			check() // Acquire has already taken its failed-job leave path.
			b.mu.Lock()
			preflights, running, releases := b.preflights, b.running, b.released
			b.mu.Unlock()
			if preflights != 0 || running != 0 || releases != 0 {
				t.Fatal("refused first fact acquired backend ownership", preflights, running, releases)
			}
			m.mu.Lock()
			jobs := len(m.jobs)
			m.mu.Unlock()
			m.server.mu.Lock()
			entries, ledger := len(m.server.executions), len(m.server.ledger)
			m.server.mu.Unlock()
			if jobs != 0 || entries != 0 || ledger != 0 {
				t.Fatal("refused first fact retained replacement ownership", jobs, entries, ledger)
			}
			if err := m.Cleanup(t.Context(), func(_ context.Context, permit func() error) error { return permit() }); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

func TestPossiblyPublishedFirstImageFactRetainsOnlyItsOwnOwnership(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-back-proves-own", true: "committed-write"}[typed], func(t *testing.T) {
			m, b := newJobs(t, 2)
			original := createReceipt
			t.Cleanup(func() { createReceipt = original })
			failure := errors.New("first fact sync failed")
			var id string
			createReceipt = func(dir, name string, raw []byte, mode os.FileMode) error {
				id = strings.TrimSuffix(name, ".json")
				if err := original(dir, name, raw, mode); err != nil {
					return err
				}
				if typed {
					return &atomicfile.CommitError{Err: failure}
				}
				return failure
			}
			_, err := m.Acquire(t.Context(), recipe("first-own-sync-error"))
			createReceipt = original
			if !errors.Is(err, failure) {
				t.Fatal("lost strict first-write error", err)
			}
			b.mu.Lock()
			preflights := b.preflights
			b.mu.Unlock()
			if preflights != 0 {
				t.Fatal("effect crossed failed first write")
			}
			own, err := m.server.Receipts.Read(id)
			if err != nil || own.Method != "image.job" || own.State != "complete" || own.Status != 2 || own.Completion == nil {
				t.Fatal("own published fact not finalized", own, err)
			}
			if err := m.server.repairStorage(t.Context()); err != nil {
				t.Fatal("strict own metadata not reconciled", err)
			}
			if own.RecordedLate {
				t.Fatal("strict fact converted to ordinary late receipt")
			}
		})
	}
}

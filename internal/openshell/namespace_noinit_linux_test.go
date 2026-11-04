package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Ordinary owned-file + actual generation preparation/readback protocol fixture.
// Kernel mount inventories and engine ACK callbacks below are scripted; these
// tests do not claim VM root, namespace, engine or lifecycle qualification.
func noInitializationFixture(t *testing.T) (*NativeGeneration, *NativePreparedGeneration, *namespacePinLock, namespaceIdentity, string) {
	t.Helper()
	f := newMaterialFixture(t)
	f.record.Launch.UnconfinedNetwork = true
	f.writeRecords(t)
	raw, _ := json.Marshal(generationOrigin(f.record))
	if err := os.WriteFile(filepath.Join(f.root.Name(), "origin.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := snapshotGenerationAt(f.root, f.record, f.release, strings.Repeat("a", 64), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	prepared, err := g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), generationTarget(g), generationEffectFixture(t, g))
	if err != nil {
		t.Fatal(err)
	}
	token := newNativePreparedGeneration(prepared, g.cid)
	t.Cleanup(func() { _ = token.Close() })
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	lock, err := lockNamespacePinsAt(t.Context(), control, prepared.Record.Launch.Binding.Incarnation, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return g, token, lock, namespaceIdentity{Device: 7, Inode: 91}, "11111111-2222-4333-8444-555555555555"
}

func TestNativeGenerationNoInitializationCompletionComposesOriginalCleanupColdStopAndFreshPreparation(t *testing.T) {
	for _, prefix := range []string{"no-start", "start-ack-no-reservation"} {
		t.Run(prefix, func(t *testing.T) {
			g, token, lock, mount, boot := noInitializationFixture(t)
			if prefix == "start-ack-no-reservation" {
				// Script the exact known no-reservation result; production stores the
				// actual return through token.Activate only after the caller's Start ACK.
				token.activationAttempted = true
				token.activation = &NativeActivation{prepared: token.Prepared(), cid: token.cid}
				handled, err := token.activation.StopStartedGeneration(t.Context(), NativeNamespaceStop{})
				if err != nil || handled {
					t.Fatal("known no-reservation prefix changed", handled, err)
				}
			}
			record, cid := token.Prepared().Record, token.cid
			if _, found, err := readNoInitializationAt(lock, record, cid); err != nil || found {
				t.Fatal("missing marker became completion", found, err)
			}
			stops := 0
			owner := NativeNamespaceStop{
				VerifyOwned: func(context.Context) error { return nil },
				Stop:        func(context.Context) error { stops++; return nil },
				VerifyInactive: func(c context.Context) error {
					deadline, ok := c.Deadline()
					if c.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
						t.Fatal("cleanup lost bounded ACK context")
					}
					return nil
				},
			}
			token.cleanupAttempted = true
			err := token.stopUninitialized(t.Context(), owner, func(context.Context) error { return verifyNoInitializationEntries(lock, record) }, func(_ context.Context, p pinRetirementProgress) error {
				return publishNoInitializationAt(lock, record, cid, boot, mount, p)
			})
			if err != nil || stops != 1 {
				t.Fatal("original no-initialization cleanup failed", err, stops)
			}
			if err := token.stopUninitialized(t.Context(), owner, func(context.Context) error { return nil }, func(context.Context, pinRetirementProgress) error { return nil }); err == nil || stops != 1 {
				t.Fatal("original no-init cleanup was replayed", err, stops)
			}
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			// Cold independent reader has no original token, old PID or origin file.
			cold, found, err := readNoInitializationAt(lock, record, cid)
			if err != nil || !found {
				t.Fatal("known no-initialization cleanup left cold Stop unprovable", found, err)
			}
			defer func() { _ = cold.Close() }()
			raw := append([]byte(nil), cold.raw...)
			if err := rejectNoInitializationAt(lock, record); err == nil {
				t.Fatal("completed generation could reserve/activate again")
			}
			verify := func(context.Context) error { return verifyNoInitializationEntries(lock, record) }
			for _, currentBoot := range []string{boot, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"} {
				progress, err := completeUninitializedNativeStop(t.Context(), nativePinStopHooks{owner.VerifyOwned, owner.Stop, owner.VerifyInactive}, cold, record, cid, currentBoot, mount, verify)
				if err != nil || !progress.completed || !progress.stopAcknowledged || progress.policyAttempted || progress.pinUnmountAttempted || progress.directoryUnmountAttempted {
					t.Fatal("cold no-init Stop did not use Stop-only completion", progress, err)
				}
			}
			if stops != 3 || cold.check(record, cid) != nil || string(cold.raw) != string(raw) {
				t.Fatal("cold Stop replayed old ACK or replaced completion", stops)
			}
			next, err := g.prepareNext(t.Context(), "next-controller", g.OriginalPolicy(), generationTarget(g), generationEffectFixture(t, g))
			if err != nil || next.Record.Generation == record.Generation || next.Record.Files["status/activation.key"] == record.Files["status/activation.key"] {
				t.Fatal("next start could not prepare fresh authority after known no-init cleanup", err)
			}
			if err := rejectNoInitializationAt(lock, next.Record); err != nil {
				t.Fatal("prior completion fenced a different fresh generation", err)
			}
		})
	}
}

func TestNativeGenerationNoInitializationFailureStopsPublicationAndColdEffects(t *testing.T) {
	for _, stage := range []string{"owner", "stop", "inactive", "publish"} {
		t.Run(stage, func(t *testing.T) {
			_, token, lock, mount, boot := noInitializationFixture(t)
			record, cid := token.Prepared().Record, token.cid
			token.cleanupAttempted = true
			cause := errors.New("lost " + stage + " reply")
			stops, publishes := 0, 0
			step := func(name string) error {
				if name == stage {
					return cause
				}
				return nil
			}
			err := token.stopUninitialized(t.Context(), NativeNamespaceStop{
				VerifyOwned:    func(context.Context) error { return step("owner") },
				Stop:           func(context.Context) error { stops++; return step("stop") },
				VerifyInactive: func(context.Context) error { return step("inactive") },
			}, func(context.Context) error { return verifyNoInitializationEntries(lock, record) }, func(_ context.Context, p pinRetirementProgress) error {
				publishes++
				if err := step("publish"); err != nil {
					return err
				}
				return publishNoInitializationAt(lock, record, cid, boot, mount, p)
			})
			if !errors.Is(err, cause) {
				t.Fatal("failure lost", err)
			}
			var uncertain *NativeEffectsUncertain
			if stage != "owner" && !errors.As(err, &uncertain) {
				t.Fatal("failed effect lost uncertainty", err)
			}
			if stage == "owner" && stops != 0 || stage != "publish" && publishes != 0 {
				t.Fatal("failure ran suffix", stops, publishes)
			}
			if _, found, err := readNoInitializationAt(lock, record, cid); err != nil || found {
				t.Fatal("failed cleanup fabricated independent completion", found, err)
			}
		})
	}
}

func TestNativeGenerationNoInitializationColdSelectionRejectsContradictoryOrChangedProof(t *testing.T) {
	for _, change := range []string{"cid", "record", "same-boot-mount", "origin", "pin-directory", "marker-change", "marker-link", "marker-mode", "closed-reader"} {
		t.Run(change, func(t *testing.T) {
			_, token, lock, mount, boot := noInitializationFixture(t)
			record, cid := token.Prepared().Record, token.cid
			p := pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}
			if err := publishNoInitializationAt(lock, record, cid, boot, mount, p); err != nil {
				t.Fatal(err)
			}
			cold, found, err := readNoInitializationAt(lock, record, cid)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			defer func() { _ = cold.Close() }()
			switch change {
			case "cid":
				cid = strings.Repeat("b", 64)
			case "record":
				record.Launch.Workdir = "/other"
			case "same-boot-mount":
				mount.Inode++
			case "origin":
				if err := writePinFileAt(int(lock.directories[3].file.Fd()), record.Generation+".json", []byte("{}"), false, lock.uid, lock.gid); err != nil {
					t.Fatal(err)
				}
			case "pin-directory":
				if err := unix.Mkdirat(int(lock.directories[4].file.Fd()), record.Generation, 0o700); err != nil {
					t.Fatal(err)
				}
			case "marker-change":
				// Deliberately corrupt only this fixture's owned file. The production
				// immutable publisher must refuse replacement, even in this test.
				fd, err := unix.Openat(int(cold.directory.Fd()), record.Generation+".json", unix.O_WRONLY|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				file := os.NewFile(uintptr(fd), "adversarial-no-init-fixture")
				_, err = file.Write([]byte("{}"))
				if err := errors.Join(err, file.Close()); err != nil {
					t.Fatal(err)
				}
			case "marker-link":
				if err := unix.Linkat(int(cold.directory.Fd()), record.Generation+".json", int(cold.directory.Fd()), "extra", 0); err != nil {
					t.Fatal(err)
				}
			case "marker-mode":
				if err := unix.Fchmodat(int(cold.directory.Fd()), record.Generation+".json", 0o644, 0); err != nil {
					t.Fatal(err)
				}
			case "closed-reader":
				_ = cold.Close()
			}
			stops := 0
			progress, err := completeUninitializedNativeStop(t.Context(), nativePinStopHooks{func(context.Context) error { return nil }, func(context.Context) error { stops++; return nil }, func(context.Context) error { return nil }}, cold, record, cid, boot, mount, func(context.Context) error { return verifyNoInitializationEntries(lock, record) })
			if err == nil || stops != 0 || progress.stopAttempted {
				t.Fatal("contradictory no-init proof reached fresh effects", change, progress, err, stops)
			}
		})
	}
}

func TestNativeGenerationNoInitializationMountInventoryRefusesAnyCurrentProjection(t *testing.T) {
	target, mounts, dir, _ := nativePinMountViewFixture(t)
	dir.mount = 10
	if err := validateNoInitializationMounts(mounts[:1], target, dir); err != nil {
		t.Fatal("bare native inventory refused", err)
	}
	for _, variant := range []string{"directory", "namespace", "read-only", "foreign-device", "missing-base"} {
		inventory := append([]nativePinMount(nil), mounts[:1]...)
		switch variant {
		case "directory":
			inventory = append(inventory, mounts[1])
		case "namespace":
			inventory = append(inventory, mounts[2])
		case "read-only":
			inventory[0].readOnly = true
		case "foreign-device":
			inventory[0].device++
		case "missing-base":
			inventory = nil
		}
		if err := validateNoInitializationMounts(inventory, target, dir); err == nil {
			t.Fatal("unsafe no-init inventory accepted", variant)
		}
	}
}

func TestNativeGenerationPreparedTokenRefusesMetadataAdoptionAndCopiesRecord(t *testing.T) {
	_, token, _, _, _ := noInitializationFixture(t)
	prepared := token.Prepared()
	if !token.Matches(prepared, token.cid) || !token.ActivationMatches(false, nil) {
		t.Fatal("original token binding rejected")
	}
	prepared.Record.Files["status/activation.key"] = "changed"
	if token.Matches(prepared, token.cid) || token.Prepared().Record.Files["status/activation.key"] == "changed" {
		t.Fatal("metadata copy changed original token")
	}
	for _, missing := range []*NativePreparedGeneration{nil, {}, {prepared: token.Prepared(), cid: token.cid}} {
		if missing.Matches(token.Prepared(), token.cid) {
			t.Fatal("cold metadata minted original preparation")
		}
		if err := missing.StopGeneration(t.Context(), NativeNamespaceStop{}); err == nil {
			t.Fatal("missing original token permitted cleanup")
		}
	}
	if text := fmt.Sprintf("%v %#v", token, token); !strings.Contains(text, "redacted") || strings.Contains(text, token.cid) {
		t.Fatal("token formatting exposed private identity", text)
	}
}

func TestNativeGenerationNoInitializationPublisherRejectsMissingACKsAndReplacement(t *testing.T) {
	for _, change := range []string{"no-attempt", "no-ack", "already-complete", "policy", "pin-unmount", "directory-unmount", "foreign-cid", "foreign-boot"} {
		t.Run(change, func(t *testing.T) {
			_, token, lock, mount, boot := noInitializationFixture(t)
			p := pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}
			cid := token.cid
			switch change {
			case "no-attempt":
				p.stopAttempted = false
			case "no-ack":
				p.stopAcknowledged = false
			case "already-complete":
				p.completed = true
			case "policy":
				p.policyAttempted = true
			case "pin-unmount":
				p.pinUnmountAttempted = true
			case "directory-unmount":
				p.directoryUnmountAttempted = true
			case "foreign-cid":
				cid = "incomplete"
			case "foreign-boot":
				boot = "unverified"
			}
			if err := publishNoInitializationAt(lock, token.Prepared().Record, cid, boot, mount, p); err == nil {
				t.Fatal("invalid no-init completion published", change)
			}
		})
	}
	_, token, lock, mount, boot := noInitializationFixture(t)
	record, cid := token.Prepared().Record, token.cid
	p := pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}
	if err := publishNoInitializationAt(lock, record, cid, boot, mount, p); err != nil {
		t.Fatal(err)
	}
	cold, found, err := readNoInitializationAt(lock, record, cid)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	defer func() { _ = cold.Close() }()
	if err := publishNoInitializationAt(lock, record, cid, boot, mount, p); err == nil {
		t.Fatal("no-init completion was replaced")
	}
	if err := cold.check(record, cid); err != nil {
		t.Fatal("refused replacement changed original completion", err)
	}
}

func TestNativeGenerationNoInitializationStopLostACKNeverRunsSuffix(t *testing.T) {
	for _, cancelAfterACK := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cause := errors.New("lost stop reply")
		inactive, publishes := 0, 0
		p, err := runNoInitializationStop(ctx, noInitializationStopHooks{
			before: func(context.Context) error { return nil },
			stop: func(context.Context) error {
				if cancelAfterACK {
					cancel()
					return nil
				}
				return cause
			},
			inactive: func(c context.Context) error {
				inactive++
				if c.Err() != nil {
					t.Fatal("ACK completion inherited cancelled transport")
				}
				return nil
			},
			complete: func(context.Context, pinRetirementProgress) error { publishes++; return nil },
		})
		if cancelAfterACK {
			if err != nil || !p.completed || inactive != 1 || publishes != 1 {
				t.Fatal("known ACK did not complete independent bounded suffix", p, err, inactive, publishes)
			}
		} else {
			var uncertain *NativeEffectsUncertain
			if !errors.As(err, &uncertain) || !errors.Is(err, cause) || inactive != 0 || publishes != 0 || p.completed {
				t.Fatal("lost Stop ACK reached completion", p, err, inactive, publishes)
			}
		}
	}
}

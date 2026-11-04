package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ordinary private directories and explicit
// injected storage/historical checks do NOT prove native disk/VM admission.
func nativeOriginCatalogRootFixture(t *testing.T) (string, nativeOriginCatalogSelection) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "containers"), 0700); err != nil {
		t.Fatal(err)
	}
	return root, nativeOriginCatalogSelection{"fixture", "disk-id", "disk-uuid"}
}
func nativeOriginCatalogEntryFixture(t *testing.T, root string, s nativeOriginCatalogSelection, box, inc, qualification, helper string) containerOrigin {
	t.Helper()
	origin := containerOrigin{s.space, s.diskID, s.diskUUID, box, inc, qualification, helper}
	directory := filepath.Join(root, "containers", inc)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "origin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return origin
}
func acceptNativeOriginCatalogFixture(context.Context) error { return nil }

func TestNativeOriginCatalogHistoricalPairAndAllSameBoxIncarnations(t *testing.T) {
	root, s := nativeOriginCatalogRootFixture(t)
	oldQ, oldH := strings.Repeat("1", 64), strings.Repeat("2", 64)
	first := nativeOriginCatalogEntryFixture(t, root, s, "selected", strings.Repeat("a", 32), oldQ, oldH)
	nativeOriginCatalogEntryFixture(t, root, s, "selected", strings.Repeat("b", 32), oldQ, oldH)
	nativeOriginCatalogEntryFixture(t, root, s, "other", strings.Repeat("c", 32), oldQ, oldH)
	historicalCalls := 0
	historical := func(origin containerOrigin) error {
		historicalCalls++
		if origin.Qualification != oldQ || origin.HelperSHA256 != oldH {
			return errors.New("unsupported recorded pair")
		}
		return nil
	}
	catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), acceptNativeOriginCatalogFixture, historical)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	found, err := catalog.ForBox("selected")
	if err != nil || len(found) != 2 || found[0] != first {
		t.Fatal("same-box/keep incarnation omitted", err)
	}
	found[0].Box = "mutated-copy"
	again, _ := catalog.ForBox("selected")
	if again[0].Box != "selected" {
		t.Fatal("caller mutated original")
	}
	if historicalCalls < 6 {
		t.Fatal("historical qualification not rechecked for every origin")
	}
	if err := catalog.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	// There is intentionally no current-helper/source qualification call and
	// no keep parameter, legacy classification or cleanup capability here.
}

func TestNativeOriginCatalogRejectsUnknownPartialAndUnqualifiedMembers(t *testing.T) {
	for _, kind := range []string{"missing-origin", "unknown-member", "unsupported-history", "storage-error", "malformed-origin"} {
		t.Run(kind, func(t *testing.T) {
			root, s := nativeOriginCatalogRootFixture(t)
			verify := acceptNativeOriginCatalogFixture
			historical := func(containerOrigin) error { return nil }
			name := strings.Repeat("a", 32)
			switch kind {
			case "missing-origin":
				if err := os.Mkdir(filepath.Join(root, "containers", name), 0700); err != nil {
					t.Fatal(err)
				}
			case "unknown-member":
				if err := os.WriteFile(filepath.Join(root, "containers", "orphan-fragment"), []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				nativeOriginCatalogEntryFixture(t, root, s, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64))
				switch kind {
				case "unsupported-history":
					historical = func(containerOrigin) error { return errors.New("unknown historical qualification") }
				case "storage-error":
					verify = func(context.Context) error { return errors.New("actual storage unavailable") }
				case "malformed-origin":
					if err := os.WriteFile(filepath.Join(root, "containers", name, "origin.json"), []byte(`{}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), verify, historical); err == nil || catalog != nil {
				t.Fatal("unknown native state became a catalog")
			}
		})
	}
}

func TestNativeOriginCatalogStrictClosedCanonicalDecoder(t *testing.T) {
	s := nativeOriginCatalogSelection{"fixture", "disk-id", "disk-uuid"}
	name := strings.Repeat("a", 32)
	origin := containerOrigin{s.space, s.diskID, s.diskUUID, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64)}
	good, err := json.Marshal(origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeCatalogOrigin(good, name, s); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`null`, `{}`, string(good) + "\n", string(good) + ` {}`,
		strings.Replace(string(good), `"space":"fixture"`, `"Space":"fixture","space":"fixture"`, 1),
		strings.Replace(string(good), `"space":"fixture"`, `"space":"fixture","Space":"fixture"`, 1),
		strings.Replace(string(good), `"space":"fixture"`, `"space":"fixture","space":"fixture"`, 1),
		strings.Replace(string(good), `"space":"fixture"`, `"space":null`, 1),
		strings.Replace(string(good), `"space":"fixture"`, `"space":"foreign"`, 1),
		strings.Replace(string(good), `"space":"fixture"`, `"space":"fixture","extra":true`, 1),
		strings.Replace(string(good), strings.Repeat("1", 64), strings.Repeat("A", 64), 1),
		strings.Replace(string(good), name, strings.Repeat("b", 32), 1),
	} {
		if _, err := decodeNativeCatalogOrigin([]byte(raw), name, s); err == nil {
			t.Fatal("malformed/foreign origin accepted")
		}
	}
}

func TestNativeOriginCatalogByteIdenticalReplacementAndExpiryRefuse(t *testing.T) {
	root, s := nativeOriginCatalogRootFixture(t)
	name := strings.Repeat("a", 32)
	nativeOriginCatalogEntryFixture(t, root, s, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64))
	catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), acceptNativeOriginCatalogFixture, func(containerOrigin) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	originalDirectory, originalOrigin := catalog.entries[0].directory, catalog.entries[0].file
	raw := append([]byte(nil), catalog.entries[0].raw...)
	fresh := filepath.Join(root, "containers", name, "fresh-fixture")
	if err := os.WriteFile(fresh, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fresh, filepath.Join(root, "containers", name, "origin.json")); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Check(t.Context()); err == nil {
		t.Fatal("matching bytes repaired replaced origin identity")
	}
	if _, err := catalog.ForBox("selected"); err == nil {
		t.Fatal("failed catalog yielded classification input")
	}
	for pass := 0; pass < 2; pass++ {
		if err := catalog.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []*os.File{originalDirectory, originalOrigin} {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("owned original not closed", err)
		}
	}
	if err := catalog.Check(t.Context()); err == nil {
		t.Fatal("expired observation repaired")
	}
}

func TestNativeOriginCatalogEmptyPresentCollectionAndMissingParentsDiffer(t *testing.T) {
	root, s := nativeOriginCatalogRootFixture(t)
	historical := func(containerOrigin) error { return nil }
	catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), acceptNativeOriginCatalogFixture, historical)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	if found, err := catalog.ForBox("selected"); err != nil || len(found) != 0 {
		t.Fatal("actual empty collection failed", err)
	}
	// This says no origin in this collection ONLY, not total native absence.
	missing := filepath.Join(root, "missing-control")
	if value, err := observeNativeOriginCatalogAt(t.Context(), missing, s, os.Geteuid(), os.Getegid(), acceptNativeOriginCatalogFixture, historical); !errors.Is(err, os.ErrNotExist) || value != nil {
		t.Fatal("unverified missing control became absent catalog", err)
	}
	for _, value := range []*nativeOriginCatalog{nil, {}} {
		if _, err := value.ForBox("selected"); err == nil {
			t.Fatal("uncaptured catalog became empty")
		}
	}
}

func TestNativeOriginCatalogCurrentStorageFailureIsSticky(t *testing.T) {
	root, s := nativeOriginCatalogRootFixture(t)
	var failure error
	verify := func(context.Context) error { return failure }
	catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), verify, func(containerOrigin) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	failure = errors.New("original storage identity lost")
	if err := catalog.Check(t.Context()); !errors.Is(err, failure) {
		t.Fatal("storage failure lost", err)
	}
	failure = nil
	if err := catalog.Check(t.Context()); err == nil {
		t.Fatal("matching current state repaired failed observation")
	}
	if _, err := catalog.ForBox("selected"); err == nil {
		t.Fatal("failed scope yielded absence")
	}
}

func TestNativeOriginCatalogMatchingEmptyCollectionReplacementRefuses(t *testing.T) {
	root, s := nativeOriginCatalogRootFixture(t)
	catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), acceptNativeOriginCatalogFixture, func(containerOrigin) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	path := filepath.Join(root, "containers")
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Check(t.Context()); err == nil {
		t.Fatal("replacement empty collection became original absence")
	}
	if _, err := catalog.ForBox("selected"); err == nil {
		t.Fatal("replaced collection yielded absence")
	}
}

func TestNativeOriginCatalogBracketsSameBytesMemberReplacement(t *testing.T) {
	for _, phase := range []string{"construct", "check"} {
		for _, stage := range []string{"historical", "final-storage"} {
			t.Run(phase+"/"+stage, func(t *testing.T) {
				root, s := nativeOriginCatalogRootFixture(t)
				name := strings.Repeat("a", 32)
				original := nativeOriginCatalogEntryFixture(t, root, s, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64))
				raw, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				historicalCalls, storageCalls := 0, 0
				historicalAt, storageAt := 0, 0
				if phase == "construct" {
					if stage == "historical" {
						historicalAt = 2
					} else {
						storageAt = 3
					}
				}
				replaced := false
				replace := func() {
					if replaced {
						t.Fatal("fixture replacement repeated")
					}
					replaced = true
					directory := filepath.Join(root, "containers", name)
					if err := os.Rename(directory, filepath.Join(root, "retained-original")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(directory, "origin.json"), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				historical := func(containerOrigin) error {
					historicalCalls++
					if historicalCalls == historicalAt {
						replace()
					}
					return nil
				}
				storage := func(context.Context) error {
					storageCalls++
					if storageCalls == storageAt {
						replace()
					}
					return nil
				}
				catalog, err := observeNativeOriginCatalogAt(t.Context(), root, s, os.Geteuid(), os.Getegid(), storage, historical)
				if phase == "construct" {
					if err == nil || catalog != nil {
						if catalog != nil {
							_ = catalog.Close()
						}
						t.Fatal("replaced original member became usable", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = catalog.Close() }()
					if stage == "historical" {
						historicalAt = historicalCalls + 1
					} else {
						storageAt = storageCalls + 2
					}
					if err := catalog.Check(t.Context()); err == nil {
						t.Fatal("same-name identical replacement accepted")
					}
					if err := catalog.Check(t.Context()); err == nil {
						t.Fatal("replacement failure was repaired")
					}
					if _, err := catalog.ForBox("selected"); err == nil {
						t.Fatal("replaced member yielded classification input")
					}
				}
				if !replaced {
					t.Fatal("fixture did not reach replacement interval")
				}
			})
		}
	}
}

func TestNativeOriginCatalogFinalVerifierCancellationRefuses(t *testing.T) {
	for _, phase := range []string{"construct", "check"} {
		t.Run(phase, func(t *testing.T) {
			root, s := nativeOriginCatalogRootFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls, cancelAt := 0, 0
			if phase == "construct" {
				cancelAt = 3
			}
			verify := func(context.Context) error {
				calls++
				if calls == cancelAt {
					cancel()
				}
				return nil
			}
			catalog, err := observeNativeOriginCatalogAt(ctx, root, s, os.Geteuid(), os.Getegid(), verify, func(containerOrigin) error { return nil })
			if phase == "construct" {
				if !errors.Is(err, context.Canceled) || catalog != nil {
					if catalog != nil {
						_ = catalog.Close()
					}
					t.Fatal("final constructor cancellation became success", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = catalog.Close() }()
				cancelAt = calls + 2
				if err := catalog.Check(ctx); !errors.Is(err, context.Canceled) {
					t.Fatal("final Check cancellation lost", err)
				}
				if err := catalog.Check(t.Context()); err == nil {
					t.Fatal("cancelled check regained validity")
				}
				if _, err := catalog.ForBox("selected"); err == nil {
					t.Fatal("cancelled check yielded absence")
				}
			}
			if calls != cancelAt {
				t.Fatal("fixture missed final verification", calls, cancelAt)
			}
		})
	}
}

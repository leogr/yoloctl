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

// Reuses source fixtures with injected
// storage/history facts; no actual native disk/VM/default/authority proof.
func nativeOriginScopeSelectionFixture() nativeOriginCatalogSelection {
	return nativeOriginCatalogSelection{"fixture", "disk-id", "disk-uuid"}
}
func nativeOriginScopeHistoryFixture(containerOrigin) error { return nil }

func TestNativeOriginScopeMissingAndPresentBranchesPreserveExplicitState(t *testing.T) {
	states := []nativeOriginAncestryState{nativeOriginAncestryMissingRuntime, nativeOriginAncestryMissingControl, nativeOriginAncestryMissingContainers, nativeOriginAncestryPresent}
	for count := 0; count <= 3; count++ {
		for _, mode := range []os.FileMode{0700, 0755} {
			t.Run(string(rune('a'+count))+"-"+mode.String(), func(t *testing.T) {
				disk := nativeOriginAncestryRootFixture(t, count, mode)
				selection := nativeOriginScopeSelectionFixture()
				var escaped *nativeOriginScope
				var owned []*os.File
				historyCalls := 0
				history := func(containerOrigin) error {
					historyCalls++
					return errors.New("missing prefix must not query catalog/history")
				}
				if count == 3 {
					history = nativeOriginScopeHistoryFixture
				}
				err := withNativeOriginScopeAt(t.Context(), disk, selection, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, history, func(scope *nativeOriginScope) error {
					escaped = scope
					for _, node := range scope.ancestry.nodes {
						owned = append(owned, node.file)
					}
					if scope.catalog != nil {
						owned = append(owned, scope.catalog.root, scope.catalog.collection)
					}
					observation, err := scope.Observation("selected")
					if err != nil {
						return err
					}
					if observation.prefix.state != states[count] || len(observation.origins) != 0 || observation.namespacePinsObserved || observation.prefix.namespacePinsObserved || observation.selection != selection || observation.box != "selected" {
						t.Fatal("prefix/empty origin metadata was replaced with generic absence")
					}
					if (scope.catalog != nil) != (count == 3) {
						t.Fatal("missing-prefix branch opened/adopted a catalog")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if count < 3 && historyCalls != 0 {
					t.Fatal("missing prefix queried historical records")
				}
				if _, err := escaped.Observation("selected"); err == nil {
					t.Fatal("escaped original scope remained usable")
				}
				if err := escaped.Check(t.Context()); err == nil {
					t.Fatal("expired scope repaired")
				}
				for _, file := range owned {
					if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatal("original catalog/ancestor owner leaked", err)
					}
				}
			})
		}
	}
}

func TestNativeOriginScopeAllSameBoxAndCopiedMetadata(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 3, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	selection := nativeOriginScopeSelectionFixture()
	q, h := strings.Repeat("1", 64), strings.Repeat("2", 64)
	first := nativeOriginCatalogEntryFixture(t, control, selection, "selected", strings.Repeat("a", 32), q, h)
	nativeOriginCatalogEntryFixture(t, control, selection, "selected", strings.Repeat("b", 32), q, h)
	nativeOriginCatalogEntryFixture(t, control, selection, "other", strings.Repeat("c", 32), q, h)
	err := withNativeOriginScopeAt(t.Context(), disk, selection, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
		observation, err := scope.Observation("selected")
		if err != nil {
			return err
		}
		if len(observation.origins) != 2 || observation.origins[0] != first {
			t.Fatal("same-box keep/other incarnation omitted")
		}
		if observation.prefix.control != scope.catalog.rootID || observation.prefix.containers != scope.catalog.collectionID {
			t.Fatal("catalog was not bound to original ancestry")
		}
		observation.origins[0].Box = "mutated-copy"
		observation.prefix.presentPrefix[0] = "changed-prefix"
		observation.selection.space = "foreign"
		again, err := scope.Observation("selected")
		if err != nil {
			return err
		}
		if again.origins[0] != first || again.prefix.presentPrefix[0] != "runtime" || again.selection != selection {
			t.Fatal("caller changed original proof inputs")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeOriginScopeOriginalPrefixAndCatalogSubstitutionRefuse(t *testing.T) {
	for _, which := range []string{"appeared-runtime", "replaced-containers", "matching-origin-file"} {
		t.Run(which, func(t *testing.T) {
			count := 3
			if which == "appeared-runtime" {
				count = 0
			}
			disk := nativeOriginAncestryRootFixture(t, count, 0755)
			selection := nativeOriginScopeSelectionFixture()
			control := filepath.Join(disk, "runtime", "openshell")
			name := strings.Repeat("a", 32)
			if which == "matching-origin-file" {
				nativeOriginCatalogEntryFixture(t, control, selection, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64))
			}
			err := withNativeOriginScopeAt(t.Context(), disk, selection, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
				original := scope.original
				switch which {
				case "appeared-runtime":
					if err := os.Mkdir(filepath.Join(disk, "runtime"), 0700); err != nil {
						t.Fatal(err)
					}
				case "replaced-containers":
					if err := os.Rename(filepath.Join(control, "containers"), filepath.Join(control, "old-containers")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(control, "containers"), 0700); err != nil {
						t.Fatal(err)
					}
				case "matching-origin-file":
					raw, err := json.Marshal(scope.catalog.entries[0].origin)
					if err != nil {
						t.Fatal(err)
					}
					fresh := filepath.Join(control, "containers", name, "new-fixture-origin")
					if err := os.WriteFile(fresh, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(fresh, filepath.Join(control, "containers", name, "origin.json")); err != nil {
						t.Fatal(err)
					}
				}
				if err := scope.Check(t.Context()); err == nil {
					t.Fatal("later same-name source adopted")
				}
				if !sameNativeOriginAncestryView(original, scope.original) {
					t.Fatal("original baseline rewritten")
				}
				if _, err := scope.Observation("selected"); err == nil {
					t.Fatal("failed original scope yielded metadata")
				}
				return nil
			})
			if err == nil {
				t.Fatal("callback outcome repaired original substitution")
			}
		})
	}
}

func TestNativeOriginScopeOriginalCancellationAndZeroScopeRefuse(t *testing.T) {
	for _, count := range []int{0, 3} {
		disk := nativeOriginAncestryRootFixture(t, count, 0755)
		ctx, cancel := context.WithCancel(t.Context())
		err := withNativeOriginScopeAt(ctx, disk, nativeOriginScopeSelectionFixture(), os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
			cancel()
			if err := scope.Check(context.WithoutCancel(ctx)); err == nil {
				t.Fatal("replacement context revived original execution")
			}
			if _, err := scope.Observation("selected"); err == nil {
				t.Fatal("expired original context yielded metadata")
			}
			return nil
		})
		cancel()
		if err == nil {
			t.Fatal("cancelled original callback became success")
		}
	}
	for _, scope := range []*nativeOriginScope{nil, {}} {
		if _, err := scope.Observation("selected"); err == nil {
			t.Fatal("zero scope became explicit absence")
		}
	}
}

func TestNativeOriginScopeCatalogCloseAndCallbackFailuresRemainSeparate(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 3, 0755)
	original := errors.New("original callback failed")
	var escaped *nativeOriginScope
	err := withNativeOriginScopeAt(t.Context(), disk, nativeOriginScopeSelectionFixture(), os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
		escaped = scope
		if err := scope.catalog.root.Close(); err != nil {
			t.Fatal(err)
		} // only the fixture's owned catalog FD
		return original
	})
	if !errors.Is(err, original) || escaped.closeErr == nil || !errors.Is(err, escaped.closeErr) {
		t.Fatal("original callback/catalog close result lost", err)
	}
	if repeated := escaped.close(); repeated != escaped.closeErr {
		t.Fatal("catalog Close was retried or result changed")
	}
	if !escaped.ancestry.closed {
		t.Fatal("outer owner did not expire/close its own ancestry")
	}
}

func TestNativeOriginScopeFinalAncestryVerificationCannotHideMemberReplacement(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 3, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	selection := nativeOriginScopeSelectionFixture()
	name := strings.Repeat("a", 32)
	original := nativeOriginCatalogEntryFixture(t, control, selection, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64))
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	calls, replaceAt := 0, 0
	replaced := false
	verify := func(context.Context) error {
		calls++
		if calls == replaceAt {
			replaced = true
			directory := filepath.Join(control, "containers", name)
			if err := os.Rename(directory, filepath.Join(control, "retained-original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "origin.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	err = withNativeOriginScopeAt(t.Context(), disk, selection, os.Geteuid(), os.Getegid(), verify, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
		// Check brackets a catalog check with two ancestry checks. Schedule the
		// replacement in the last storage callback of that trailing ancestry check,
		// after the catalog's own member/path validation has already completed.
		replaceAt = calls + 8
		if err := scope.Check(t.Context()); err == nil {
			t.Fatal("final ancestry callback hid original member replacement")
		}
		if !replaced {
			t.Fatal("fixture missed final ancestry verification interval")
		}
		if _, err := scope.Observation("selected"); err == nil {
			t.Fatal("failed composition yielded metadata")
		}
		return nil
	})
	if err == nil {
		t.Fatal("callback return repaired composition failure")
	}
}

func TestNativeOriginScopeNoExternalVerifierAfterCatalogClose(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 3, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	selection := nativeOriginScopeSelectionFixture()
	name := strings.Repeat("a", 32)
	original := nativeOriginCatalogEntryFixture(t, control, selection, "selected", name, strings.Repeat("1", 64), strings.Repeat("2", 64))
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var escaped *nativeOriginScope
	postCloseCalls := 0
	replaced := false
	verify := func(context.Context) error {
		// The source callbacks run synchronously on this fixture goroutine. A
		// verifier scheduled after inner finalization must never be permitted to
		// invalidate the just-closed catalog while the wrapper still reports success.
		if escaped != nil && escaped.closed {
			postCloseCalls++
			if !replaced {
				replaced = true
				directory := filepath.Join(control, "containers", name)
				if err := os.Rename(directory, filepath.Join(control, "retained-after-close")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "origin.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return nil
	}
	err = withNativeOriginScopeAt(t.Context(), disk, selection, os.Geteuid(), os.Getegid(), verify, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
		escaped = scope
		_, err := scope.Observation("selected")
		return err
	})
	if err != nil {
		t.Fatal("ordinary original scope failed", err)
	}
	if postCloseCalls != 0 || replaced {
		t.Fatal("outer ancestry callback hid member replacement after catalog Close", postCloseCalls)
	}
	if escaped == nil || !escaped.closed || !escaped.catalog.closed || !escaped.ancestry.closed {
		t.Fatal("original close owners did not settle")
	}
}

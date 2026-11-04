package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Ordinary directories test the passive source profile, never genuine native
// mounts, historical joins, full namespace/ref census or caller classification.
func namespaceScaffoldFixture(t *testing.T, control, inc string, noinit bool) string {
	t.Helper()
	path := filepath.Join(control, "namespace-pins", inc)
	for _, p := range []string{filepath.Dir(path), path, filepath.Join(path, "origins"), filepath.Join(path, "pins")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if noinit {
		if err := os.Mkdir(filepath.Join(path, "no-init"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, ".lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func withNamespaceScaffoldFixture(t *testing.T, disk string, current func(context.Context) error, use func(*nativeOriginScope) error) error {
	t.Helper()
	return withNativeOriginScopeAt(t.Context(), disk, nativeOriginScopeSelectionFixture(), os.Geteuid(), os.Getegid(), current, nativeOriginScopeHistoryFixture, use)
}
func TestNativeNamespaceScaffoldsMissingContainersStillReadsSibling(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 2, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	first := strings.Repeat("a", 32)
	second := strings.Repeat("b", 32)
	namespaceScaffoldFixture(t, control, first, false)
	namespaceScaffoldFixture(t, control, second, true)
	err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
		p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		got, err := p.Observation(t.Context())
		if err != nil {
			return err
		}
		if got.prefix != nativeOriginAncestryMissingContainers || got.collectionMissing || len(got.entries) != 2 || got.entries[0].incarnation != first || !got.entries[0].bareMetadata || !got.entries[1].bareMetadata {
			t.Fatal("sibling skipped or bare metadata inferred as total absence", got)
		}
		got.entries[0].incarnation = "changed-copy"
		got.entries[0].bareMetadata = false
		again, err := p.Observation(t.Context())
		if err != nil {
			return err
		}
		if again.entries[0].incarnation != first || !again.entries[0].bareMetadata {
			t.Fatal("caller changed original observation")
		}
		// Passive reader never acquires even a shared flock or replaces the inode.
		fd, err := unix.Open(filepath.Join(control, "namespace-pins", first, ".lock"), unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unix.Close(fd) }()
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal("observer acquired lock", err)
		}
		return unix.Flock(fd, unix.LOCK_UN)
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestNativeNamespaceScaffoldsExplicitPrefixAndCollectionAbsence(t *testing.T) {
	for count := 0; count <= 3; count++ {
		t.Run(string(rune('a'+count)), func(t *testing.T) {
			disk := nativeOriginAncestryRootFixture(t, count, 0755)
			err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
				p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
				if err != nil {
					return err
				}
				defer func() { _ = p.Close() }()
				v, err := p.Observation(t.Context())
				if err != nil {
					return err
				}
				if len(v.entries) != 0 || v.prefix != scope.original.state || v.collectionMissing != (count >= 2) {
					t.Fatal("prefix and exact collection absence conflated")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestNativeNamespaceScaffoldsPayloadRemainsUnexamined(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 3, 0700)
	control := filepath.Join(disk, "runtime", "openshell")
	inc := strings.Repeat("a", 32)
	path := namespaceScaffoldFixture(t, control, inc, true)
	gen := "g" + strings.Repeat("1", 48)
	// This intentionally is NOT a qualified record/origin group. The reader
	// must report payload pending instead of claiming foreign/target/absence.
	if err := os.WriteFile(filepath.Join(path, "origins", gen+".json"), []byte("unexamined"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "pins", gen), 0700); err != nil {
		t.Fatal(err)
	}
	err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
		p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		v, err := p.Observation(t.Context())
		if err != nil {
			return err
		}
		if len(v.entries) != 1 || v.entries[0].bareMetadata {
			t.Fatal("payload treated as bare")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestNativeNamespaceScaffoldsRefusePartialUnknownProfiles(t *testing.T) {
	for _, which := range []string{"unknown", "missing-lock", "nonempty-lock", "missing-pins", "wrong-mode", "symlink-inc", "symlink-child", "noncanonical-inc", "bad-generation", "generation-symlink", "lock-hardlink"} {
		t.Run(which, func(t *testing.T) {
			disk := nativeOriginAncestryRootFixture(t, 2, 0755)
			control := filepath.Join(disk, "runtime", "openshell")
			inc := strings.Repeat("a", 32)
			path := namespaceScaffoldFixture(t, control, inc, false)
			var err error
			switch which {
			case "unknown":
				err = os.WriteFile(filepath.Join(path, "unexpected"), nil, 0600)
			case "missing-lock":
				err = os.Rename(filepath.Join(path, ".lock"), filepath.Join(control, "saved-lock"))
			case "nonempty-lock":
				err = os.WriteFile(filepath.Join(path, ".lock"), []byte("owner?"), 0600)
			case "missing-pins":
				err = os.Rename(filepath.Join(path, "pins"), filepath.Join(control, "saved-pins"))
			case "wrong-mode":
				err = os.Chmod(filepath.Join(path, "origins"), 0755)
			case "symlink-inc":
				err = os.Rename(path, path+"-old")
				if err == nil {
					err = os.Symlink(path+"-old", path)
				}
			case "symlink-child":
				err = os.Rename(filepath.Join(path, "pins"), filepath.Join(control, "saved-pins"))
				if err == nil {
					err = os.Symlink(filepath.Join(control, "saved-pins"), filepath.Join(path, "pins"))
				}
			case "noncanonical-inc":
				err = os.Rename(path, filepath.Join(filepath.Dir(path), "not-an-incarnation"))
			case "bad-generation":
				err = os.WriteFile(filepath.Join(path, "origins", "bad.json"), nil, 0600)
			case "generation-symlink":
				err = os.Symlink(filepath.Join(path, "origins"), filepath.Join(path, "pins", "g"+strings.Repeat("1", 48)))
			case "lock-hardlink":
				err = os.Link(filepath.Join(path, ".lock"), filepath.Join(control, "other-link"))
			}
			if err != nil {
				t.Fatal(err)
			}
			err = withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
				p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
				if p != nil {
					_ = p.Close()
				}
				if err == nil {
					t.Fatal("unknown/partial profile was accepted")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestNativeNamespaceScaffoldsSubstitutionAndFailureStaySticky(t *testing.T) {
	for _, which := range []string{"inc", "lock", "origins", "appeared-payload", "appeared-collection"} {
		t.Run(which, func(t *testing.T) {
			disk := nativeOriginAncestryRootFixture(t, 2, 0755)
			control := filepath.Join(disk, "runtime", "openshell")
			inc := strings.Repeat("a", 32)
			path := ""
			if which != "appeared-collection" {
				path = namespaceScaffoldFixture(t, control, inc, false)
			}
			err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
				p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
				if err != nil {
					return err
				}
				defer func() { _ = p.Close() }()
				var old, new string
				switch which {
				case "inc":
					old = path
					new = path + "-saved"
					err = os.Rename(old, new)
					if err == nil {
						namespaceScaffoldFixture(t, control, inc, false)
					}
				case "lock":
					old = filepath.Join(path, ".lock")
					new = filepath.Join(control, "saved-lock")
					err = os.Rename(old, new)
					if err == nil {
						err = os.WriteFile(old, nil, 0600)
					}
				case "origins":
					old = filepath.Join(path, "origins")
					new = filepath.Join(control, "saved-origins")
					err = os.Rename(old, new)
					if err == nil {
						err = os.Mkdir(old, 0700)
					}
				case "appeared-payload":
					err = os.WriteFile(filepath.Join(path, "origins", "g"+strings.Repeat("1", 48)+".json"), nil, 0600)
				case "appeared-collection":
					namespaceScaffoldFixture(t, control, inc, false)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := p.Check(t.Context()); err == nil {
					t.Fatal("original mapping/roster loss hidden")
				}
				if old != "" {
					if err := os.Rename(old, filepath.Join(control, "replacement-outside-collection")); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(new, old); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := p.Observation(t.Context()); err == nil {
					t.Fatal("matching restoration repaired sticky loss")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestNativeNamespaceScaffoldsFinalStorageCallbackCannotHideReplacement(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 2, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	path := namespaceScaffoldFixture(t, control, strings.Repeat("a", 32), false)
	armed, changed := false, false
	current := func(context.Context) error {
		if armed && !changed {
			changed = true
			if err := os.Rename(filepath.Join(path, "origins"), filepath.Join(control, "saved-origins")); err != nil {
				return err
			}
			return os.Mkdir(filepath.Join(path, "origins"), 0700)
		}
		return nil
	}
	err := withNamespaceScaffoldFixture(t, disk, current, func(scope *nativeOriginScope) error {
		p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		armed = true
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("external storage callback replacement escaped final live scan")
		}
		armed = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("schedule did not run")
	}
}
func TestNativeNamespaceScaffoldsExpiryCancelAndOwnedClose(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 2, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	namespaceScaffoldFixture(t, control, strings.Repeat("a", 32), false)
	var escaped *nativeNamespaceScaffolds
	var files []*os.File
	err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
		p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
		if err != nil {
			return err
		}
		escaped = p
		for _, n := range p.nodes {
			files = append(files, n.file)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if !errors.Is(p.Check(ctx), context.Canceled) {
			t.Fatal("cancellation not retained")
		}
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("replacement context repaired failure")
		}
		if err := p.Close(); err != nil {
			return err
		}
		if err := p.Close(); err != nil {
			return err
		}
		if err := scope.Check(t.Context()); err != nil {
			t.Fatal("borrowed scope was closed", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := escaped.Check(context.WithoutCancel(t.Context())); err == nil {
		t.Fatal("escaped scope revived")
	}
	for _, file := range files {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("owned descriptor not closed", err)
		}
	}
}
func TestNativeNamespaceScaffoldsBoundedCompleteRoster(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 2, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	path := namespaceScaffoldFixture(t, control, strings.Repeat("a", 32), false)
	for i := 0; i < nativeOriginCatalogLimit+1; i++ {
		// Names need not be valid; the count bound must refuse before interpretation.
		if err := os.WriteFile(filepath.Join(path, "origins", strings.Repeat("a", i/100+1)+string(rune(0x100+i))), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
		p, err := observeNativeNamespaceScaffolds(t.Context(), scope)
		if p != nil {
			_ = p.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "member limit") {
			t.Fatal("complete count bound missed", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeNamespaceScaffoldsBorrowedScopeExpiryAndOriginalCancellation(t *testing.T) {
	for _, cancelOriginal := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "canceled-original"}[cancelOriginal], func(t *testing.T) {
			disk := nativeOriginAncestryRootFixture(t, 2, 0700)
			control := filepath.Join(disk, "runtime", "openshell")
			namespaceScaffoldFixture(t, control, strings.Repeat("a", 32), false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var p *nativeNamespaceScaffolds
			err := withNativeOriginScopeAt(ctx, disk, nativeOriginScopeSelectionFixture(), os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
				var err error
				p, err = observeNativeNamespaceScaffolds(ctx, scope)
				if err != nil {
					return err
				}
				if cancelOriginal {
					cancel()
					if err := p.Check(context.WithoutCancel(ctx)); !errors.Is(err, context.Canceled) {
						t.Fatal("original cancellation hidden by replacement context", err)
					}
				}
				return nil
			})
			if cancelOriginal {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := p.Check(context.WithoutCancel(ctx)); err == nil {
				t.Fatal("expired borrowed scope remained usable")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestNativeNamespaceScaffoldsFinalStorageCancellationAndAggregateBudget(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 2, 0700)
	control := filepath.Join(disk, "runtime", "openshell")
	namespaceScaffoldFixture(t, control, strings.Repeat("a", 32), false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	armed := false
	current := func(context.Context) error {
		if armed {
			cancel()
		}
		return nil
	}
	err := withNativeOriginScopeAt(ctx, disk, nativeOriginScopeSelectionFixture(), os.Geteuid(), os.Getegid(), current, nativeOriginScopeHistoryFixture, func(scope *nativeOriginScope) error {
		p, err := observeNativeNamespaceScaffolds(ctx, scope)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		used := nativeNamespaceScaffoldMembers
		if _, _, err := nativeNamespaceScaffoldNames(ctx, p.nodes[0].file, &used); err == nil {
			t.Fatal("aggregate budget ignored")
		}
		armed = true
		if err := p.Check(context.WithoutCancel(ctx)); !errors.Is(err, context.Canceled) {
			t.Fatal("final callback cancellation hidden", err)
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNativeNamespaceScaffoldsConcurrentChecksAndOuterExpiry(t *testing.T) {
	disk := nativeOriginAncestryRootFixture(t, 2, 0700)
	control := filepath.Join(disk, "runtime", "openshell")
	namespaceScaffoldFixture(t, control, strings.Repeat("a", 32), false)
	for attempt := 0; attempt < 20; attempt++ {
		var p *nativeNamespaceScaffolds
		started, done := make(chan struct{}), make(chan struct{})
		err := withNamespaceScaffoldFixture(t, disk, nativeOriginAncestryCurrentFixture, func(scope *nativeOriginScope) error {
			var err error
			p, err = observeNativeNamespaceScaffolds(t.Context(), scope)
			if err != nil {
				return err
			}
			go func() {
				defer close(done)
				close(started)
				for i := 0; i < 20; i++ {
					if err := p.Check(t.Context()); err != nil {
						return
					}
				}
			}()
			<-started
			return nil // original scope closes while checks may still be running
		})
		if err != nil {
			t.Fatal(err)
		}
		<-done
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("outer expiry left borrowed source usable")
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

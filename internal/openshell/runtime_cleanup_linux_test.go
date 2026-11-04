package openshell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Actual initializer/immutable multi-generation files and the original private
// ACK protocol; engine/kernel/storage/remaining predicates remain scripted.
// No admitted native constructor, VM, engine or root effect is exercised.
func runtimeCleanupFixture(t *testing.T) (string, *nativeRuntimeCleanup, nativeContainerRemovalScope) {
	t.Helper()
	f, g, _, scope := containerRefreshFixture(t)
	parentPath := t.TempDir()
	if err := os.Chmod(parentPath, 0700); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(parentPath, g.current.Launch.Binding.Incarnation)
	if err := os.Rename(f.root.Name(), rootPath); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	full, err := inventoryNativeContainerAt(t.Context(), root, g.current, f.release, f.owners)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = full.Close() })
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	id, err := pinIdentity(int(parent.Fd()), f.owners.rootUID, f.owners.rootGID, true)
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeNamespaceCleanup{retirement: &nativeContainerRetirement{full: full, retired: true}, records: full.Records(), cid: scope.cid}
	p, err := snapshotRuntimeCleanupAt(t.Context(), n, parent, id, filepath.Base(rootPath))
	if err != nil {
		t.Fatal(err)
	}
	return rootPath, p, scope
}

func runtimeCleanupActions(t *testing.T, p *nativeRuntimeCleanup, scope nativeContainerRemovalScope, current func(context.Context) error) (nativeContainerRemovalActions, *int) {
	t.Helper()
	a, removes := refreshProtocolActions(t, scope, func(context.Context, *nativeContainerStopACK) error { return nil })
	a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
		return p.namespace.removeWithCurrentVerifier(ctx, ack, func(context.Context) error { return nil })
	}
	a.runtimeMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
		return p.removeWithCurrentVerifier(ctx, ack, current)
	}
	return a, removes
}

func TestRuntimeCleanupOriginalACKRemovesRoleDataBeforeAllAnchorsAndOnlyOwnedRoot(t *testing.T) {
	path, p, scope := runtimeCleanupFixture(t)
	parentPath := filepath.Dir(path)
	sibling := filepath.Join(parentPath, "unrelated-incarnation")
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "retain"), []byte("other runtime"), 0600); err != nil {
		t.Fatal(err)
	}
	issuer := filepath.Join(parentPath, "issuer-outside-runtime")
	if err := os.WriteFile(issuer, []byte("retain global issuer"), 0600); err != nil {
		t.Fatal(err)
	}
	checks := 0
	current := func(context.Context) error {
		checks++
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		dataPresent := false
		for _, record := range scope.records {
			dir := filepath.Join(path, "generations", record.Generation)
			entries, err := os.ReadDir(dir)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Name() != "record.json" {
					dataPresent = true
				}
			}
		}
		if dataPresent {
			for _, record := range scope.records {
				if _, err := os.Lstat(filepath.Join(path, "generations", record.Generation, "record.json")); err != nil {
					t.Fatal("immutable record removed before all role data", err)
				}
			}
		}
		if _, err := os.Lstat(filepath.Join(path, "generations")); err == nil {
			for _, name := range []string{"current.json", "origin.json"} {
				if _, err := os.Lstat(filepath.Join(path, name)); err != nil {
					t.Fatal("global anchor removed before generations", name, err)
				}
			}
		}
		if _, err := os.Lstat(filepath.Join(path, "current.json")); err == nil {
			if _, err := os.Lstat(filepath.Join(path, "origin.json")); err != nil {
				t.Fatal("origin removed before current", err)
			}
		}
		return nil
	}
	a, removes := runtimeCleanupActions(t, p, scope, current)
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal("original ACK failed private cleanup", err)
	}
	if !p.completed || !p.rootRemoved || checks < len(p.steps) || *removes != 1 {
		t.Fatal("private cleanup omitted ACK/effects/completion", p.completed, p.rootRemoved, checks, *removes)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned private incarnation remains", err)
	}
	if err := p.checkExpected(t.Context()); err != nil {
		t.Fatal("post-ACK absence verification failed", err)
	}
	for name, want := range map[string]string{filepath.Join(sibling, "retain"): "other runtime", issuer: "retain global issuer"} {
		if raw, err := os.ReadFile(name); err != nil || string(raw) != want {
			t.Fatal("private cleanup changed unrelated storage", name, err)
		}
	}
}

func TestRuntimeCleanupRequiresOriginalRemoveAndNamespaceACKBeforeFiles(t *testing.T) {
	for _, change := range []string{"nil-ACK", "wrong-CID", "missing-namespace-completion", "unacknowledged-namespace", "subset-records", "duplicate-records", "closed-retirement"} {
		t.Run(change, func(t *testing.T) {
			path, p, scope := runtimeCleanupFixture(t)
			before, err := os.ReadFile(filepath.Join(path, "origin.json"))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			a, _ := runtimeCleanupActions(t, p, scope, func(context.Context) error { calls++; return nil })
			check := func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				switch change {
				case "nil-ACK":
					ack = nil
				case "wrong-CID":
					p.namespace.cid = strings.Repeat("b", 64)
				case "missing-namespace-completion":
					p.namespace.completed = false
				case "subset-records":
					p.records = p.records[:1]
				case "duplicate-records":
					p.records[1] = p.records[0]
				case "closed-retirement":
					p.namespace.retirement.closed = true
				}
				return p.removeWithCurrentVerifier(ctx, ack, func(context.Context) error { calls++; return nil })
			}
			if change == "unacknowledged-namespace" {
				a.namespaceMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
					p.namespace.completed = true
					return check(ctx, ack)
				}
			} else {
				a.runtimeMetadata = check
			}
			// After the rm ACK a refused cleanup is a named warning, not a
			// failed removal (decision #3); it still changes nothing.
			notices := captureNativeRemovalNotices(t)
			if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil || len(*notices) == 0 {
				t.Fatal("private cleanup accepted missing original authority", change, err, *notices)
			}
			if calls != 0 || p.attempted {
				t.Fatal("invalid ACK reached current/effect suffix", calls, p.attempted)
			}
			after, err := os.ReadFile(filepath.Join(path, "origin.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid ACK changed private anchors", err)
			}
		})
	}
}

func TestRuntimeCleanupChangedRemainingInventoryRefusesBeforeUnlink(t *testing.T) {
	for _, change := range []string{"unknown-member", "new-allowed-leaf", "record-same-inode-bytes", "record-replaced", "record-symlink", "record-hardlink", "root-replaced", "simulated-root-mount", "namespace-bookkeeping", "current-proof", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			path, p, scope := runtimeCleanupFixture(t)
			first := p.steps[0].key
			a, _ := runtimeCleanupActions(t, p, scope, func(context.Context) error { return nil })
			before := a.runtimeMetadata
			a.runtimeMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				var err error
				record := filepath.Join(path, "generations", scope.records[0].Generation, "record.json")
				switch change {
				case "unknown-member":
					err = os.WriteFile(filepath.Join(path, "unknown"), nil, 0600)
				case "new-allowed-leaf":
					err = os.WriteFile(filepath.Join(path, "generations", scope.records[0].Generation, "status", "activation.json"), []byte("{}"), 0600)
				case "record-same-inode-bytes":
					raw, e := os.ReadFile(record)
					if e != nil {
						t.Fatal(e)
					}
					raw[0] = '['
					err = os.WriteFile(record, raw, 0600)
				case "record-replaced":
					if err = os.Rename(record, record+"-outside"); err == nil {
						err = os.WriteFile(record, p.full.rawRecords[scope.records[0].Generation], 0600)
					}
					if err == nil {
						err = os.Rename(record+"-outside", filepath.Join(filepath.Dir(path), "old-record-retained"))
					}
				case "record-symlink":
					if err = os.Remove(record); err == nil {
						err = os.Symlink(filepath.Join(path, "current.json"), record)
					}
				case "record-hardlink":
					err = os.Link(record, filepath.Join(filepath.Dir(path), "outside-record-link"))
				case "root-replaced":
					if err = os.Rename(path, path+"-old"); err == nil {
						err = os.Mkdir(path, 0700)
					}
				case "simulated-root-mount":
					m := p.expected[""]
					m.identity.mount++
					p.expected[""] = m
				case "namespace-bookkeeping":
					p.namespace.expected = &namespaceCleanupInventory{}
				case "current-proof":
					return p.removeWithCurrentVerifier(ctx, ack, func(context.Context) error { return errors.New("remaining consumers unavailable") })
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				if err != nil {
					t.Fatal(err)
				}
				return before(ctx, ack)
			}
			if err := completeNativeContainerRemoval(t.Context(), scope, a); err == nil || !runtimeCleanupUncertain(err) {
				t.Fatal("changed private inventory did not stop with uncertainty", change, err)
			}
			base := path
			if change == "root-replaced" {
				base = path + "-old"
			}
			if _, err := os.Lstat(filepath.Join(base, first)); err != nil {
				t.Fatal("private cleanup unlinked before refusal", err)
			}
			if p.completed || p.rootRemoved {
				t.Fatal("refusal claimed private completion")
			}
		})
	}
}

func TestRuntimeCleanupUnlinkSyncFailureDoesNotAdvanceOrRetryEvenAfterRootGone(t *testing.T) {
	for _, where := range []string{"first-leaf", "root"} {
		t.Run(where, func(t *testing.T) {
			path, p, scope := runtimeCleanupFixture(t)
			index := 0
			if where == "root" {
				index = len(p.steps) - 1
			}
			step := p.steps[index]
			fd, err := unix.Openat(int(step.parent.Fd()), ".", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			broken := os.NewFile(uintptr(fd), "opaque-sync-failure-parent")
			defer func() { _ = broken.Close() }()
			p.steps[index].parent = broken
			if where == "root" {
				p.parent = broken
			}
			a, _ := runtimeCleanupActions(t, p, scope, func(context.Context) error { return nil })
			var again error
			before := a.runtimeMetadata
			a.runtimeMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				err := before(ctx, ack)
				if err == nil || !errors.Is(err, unix.EBADF) {
					t.Fatal("fixture did not reach actual parent fsync failure", err)
				}
				again = p.removeWithCurrentVerifier(ctx, ack, func(context.Context) error { t.Fatal("poisoned cleanup rechecked/retried"); return nil })
				if again != p.failure {
					t.Fatal("retry lost original uncertain error")
				}
				return err
			}
			if err := completeNativeContainerRemoval(t.Context(), scope, a); err == nil || !runtimeCleanupUncertain(err) {
				t.Fatal("lost sync acknowledged metadata removal", err)
			}
			if p.completed || p.rootRemoved || again == nil {
				t.Fatal("failed sync advanced original progress")
			}
			if _, exists := p.expected[step.key]; !exists {
				t.Fatal("failed sync advanced expected remaining set")
			}
			if where == "root" {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("root fixture did not unlink before sync error", err)
				}
				if len(p.expected) != 1 {
					t.Fatal("root failure lost expected root")
				}
			} else if _, err := os.Lstat(filepath.Join(path, "origin.json")); err != nil {
				t.Fatal("leaf sync failure removed origin anchor", err)
			}
		})
	}
}

func TestRuntimeCleanupForeignStepParentAndEarlyAnchorsRefuseBeforeEffects(t *testing.T) {
	for _, change := range []string{"foreign-parent", "early-record", "early-current", "early-origin"} {
		t.Run(change, func(t *testing.T) {
			path, p, scope := runtimeCleanupFixture(t)
			first := p.steps[0].key
			var foreign string
			if change == "foreign-parent" {
				foreign = t.TempDir()
				if err := os.Chmod(foreign, 0700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(foreign)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				actual, err := runtimeCleanupFDMember(file)
				if err != nil {
					t.Fatal(err)
				}
				original, err := runtimeCleanupFDMember(p.steps[0].parent)
				if err != nil || actual.identity == original.identity {
					t.Fatal("foreign parent must be valid and distinct", err)
				}
				if p.steps[0].directory {
					t.Fatal("foreign parent fixture requires first step to be a leaf")
				}
				if err := os.WriteFile(filepath.Join(foreign, p.steps[0].name), []byte("retain foreign leaf"), 0600); err != nil {
					t.Fatal(err)
				}
				p.steps[0].parent = file
			} else {
				index := -1
				for n, s := range p.steps {
					if change == "early-record" && runtimeCleanupRecord(s.key) || change == "early-current" && s.key == "current.json" || change == "early-origin" && s.key == "origin.json" {
						index = n
						break
					}
				}
				if index < 0 {
					t.Fatal("missing intended anchor step")
				}
				p.steps[0], p.steps[index] = p.steps[index], p.steps[0]
			}
			a, _ := runtimeCleanupActions(t, p, scope, func(context.Context) error { return nil })
			if err := completeNativeContainerRemoval(t.Context(), scope, a); err == nil || !runtimeCleanupUncertain(err) {
				t.Fatal("foreign step/early anchor accepted", change, err)
			}
			if _, err := os.Lstat(filepath.Join(path, first)); err != nil {
				t.Fatal("refusal unlinked original role data", err)
			}
			if foreign != "" {
				if _, err := os.Lstat(filepath.Join(foreign, p.steps[0].name)); err != nil {
					t.Fatal("foreign parent descriptor was used for private unlink", err)
				}
			}
			for _, name := range []string{"current.json", "origin.json"} {
				if _, err := os.Lstat(filepath.Join(path, name)); err != nil {
					t.Fatal("refusal removed private anchor", name, err)
				}
			}
			if change == "early-record" {
				for _, record := range scope.records {
					if _, err := os.Lstat(filepath.Join(path, "generations", record.Generation, "record.json")); err != nil {
						t.Fatal("refusal removed immutable record before role data", err)
					}
				}
			}
		})
	}
}

func TestRuntimeCleanupExpiredACKCannotReplayObservedPrivateFiles(t *testing.T) {
	path, p, scope := runtimeCleanupFixture(t)
	a, _ := runtimeCleanupActions(t, p, scope, func(context.Context) error { return nil })
	var saved *nativeContainerRemoveACK
	a.runtimeMetadata = func(_ context.Context, ack *nativeContainerRemoveACK) error { saved = ack; return nil }
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := p.removeWithCurrentVerifier(t.Context(), saved, func(context.Context) error { calls++; return nil })
	if err == nil || calls != 0 || p.attempted {
		t.Fatal("expired remove token reached private runtime files", err, calls, p.attempted)
	}
	if _, err := os.Lstat(filepath.Join(path, "origin.json")); err != nil {
		t.Fatal("expired remove token changed private origin", err)
	}
}

func TestRuntimeCleanupNativeBoundaryRequiresActualScope(t *testing.T) {
	if _, err := prepareNativeRuntimeCleanup(t.Context(), nil, nil); err == nil {
		t.Fatal("empty native cleanup gained authority")
	}
	_, p, _ := runtimeCleanupFixture(t)
	if _, err := prepareNativeRuntimeCleanup(t.Context(), p.namespace, nil); err == nil {
		t.Fatal("owned-file fixture became actual native scope")
	}
	if err := p.removeAfterACK(t.Context(), nil, func(context.Context) error { t.Fatal("non-native cleanup reached native callback"); return nil }); err == nil {
		t.Fatal("owned-file fixture gained native runtime effect authority")
	}
}

func runtimeCleanupUncertain(err error) bool {
	var uncertain *NativeEffectsUncertain
	return errors.As(err, &uncertain)
}

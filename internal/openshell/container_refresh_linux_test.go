package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

// The real PID1 shared initializer and atomic observation writer operate on
// current-user private files. Engine Stop/inactivity/rm are scripted below;
// this is not native root or shutdown/consumer proof.
func containerRefreshFixture(t *testing.T) (materialFixture, *NativeGeneration, *NativeContainerInventory, nativeContainerRemovalScope) {
	t.Helper()
	f, g, records := containerInventoryFixture(t)
	generation, err := runtimeDirectoryAt(f.root, "generations", f.owners.rootUID, f.owners.rootGID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = generation.Close() }()
	current, err := runtimeDirectoryAt(generation, g.current.Generation, f.owners.rootUID, f.owners.rootGID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = current.Close() }()
	// Match openActivationDirectory's readable FD, not runtimeDirectoryAt's
	// O_PATH lookup handle: the real shared initializer fsyncs this parent.
	statusFD, err := unix.Openat(int(current.Fd()), "status", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	status := os.NewFile(uintptr(statusFD), "fixture-runtime-status")
	defer func() { _ = status.Close() }()
	if err := runtimeDirectoryOwner(status, f.owners.rootUID, f.owners.rootGID); err != nil {
		t.Fatal(err)
	}
	shared, err := prepareRuntimeSharedAt(status, f.owners.supervisorUID, f.owners.supervisorGID)
	if err != nil {
		t.Fatal(err)
	}
	_ = shared.close()
	path := "generations/" + g.current.Generation + "/status/runtime.json"
	if err := safepath.WriteFileOwned(f.root.Name(), path, []byte(`{"fixture":"ready"}`), 0600, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(f.root.Name(), "generations", g.current.Generation, "status/runtime-supervisor/proxy-tls/.tmpABC123")
	if err := os.WriteFile(tmp, []byte("fixture temporary public CA"), 0600); err != nil {
		t.Fatal(err)
	}
	forwards := filepath.Join(f.root.Name(), "generations", g.current.Generation, "status/forwards")
	if err := os.Mkdir(forwards, 0700); err != nil {
		t.Fatal(err)
	}
	for _, nonce := range []string{strings.Repeat("e", 32), strings.Repeat("f", 32)} {
		for _, suffix := range []string{".lock", ".json"} {
			body := []byte(nil)
			if suffix == ".json" {
				body = []byte(`{"fixture":"published"}`)
			}
			if err := os.WriteFile(filepath.Join(forwards, nonce+suffix), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	inventory, err := inventoryNativeContainerAt(t.Context(), f.root, g.current, f.release, f.owners)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inventory.Close() })
	return f, g, inventory, nativeContainerRemovalScope{cid: g.cid, currentGeneration: g.current.Generation, records: records}
}
func refreshProtocolActions(t *testing.T, scope nativeContainerRemovalScope, refresh func(context.Context, *nativeContainerStopACK) error) (nativeContainerRemovalActions, *int) {
	t.Helper()
	removes := new(int)
	noop := func(context.Context) error { return nil }
	a := nativeContainerRemovalActions{qualify: noop, stop: noop, inactive: noop, refresh: refresh, retire: func(context.Context, *nativeContainerStopACK) error { return nil }, remove: func(context.Context) error { (*removes)++; return nil }, remaining: noop,
		namespaceMetadata: func(_ context.Context, ack *nativeContainerRemoveACK) error {
			return ack.permitsMetadata(scope.records[0], scope.cid, false)
		},
		runtimeMetadata: func(_ context.Context, ack *nativeContainerRemoveACK) error {
			return ack.permitsMetadata(scope.records[0], scope.cid, true)
		}}
	return a, removes
}
func TestNativeContainerRefreshPermitsKnownStatusLifetimeOnlyAfterOriginalACK(t *testing.T) {
	f, g, original, scope := containerRefreshFixture(t)
	var next *NativeContainerInventory
	a, removes := refreshProtocolActions(t, scope, func(ctx context.Context, ack *nativeContainerStopACK) error {
		var err error
		next, err = refreshNativeContainerAfterStop(ctx, original, f.release, g.cid, ack)
		return err
	})
	a.stop = func(context.Context) error {
		path := "generations/" + g.current.Generation + "/status/runtime.json"
		if err := safepath.WriteFileOwned(f.root.Name(), path, []byte(`{"fixture":"stopped"}`), 0600, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
		return os.Remove(filepath.Join(f.root.Name(), "generations", g.current.Generation, "status/runtime-supervisor/proxy-tls/.tmpABC123"))
	}
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal("acknowledged known status changes rejected", err)
	}
	if next == nil {
		t.Fatal("refresh returned no new observation")
	}
	defer func() { _ = next.Close() }()
	if err := next.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := original.Check(t.Context()); err == nil {
		t.Fatal("ordinary strict inventory check was weakened")
	}
	if *removes != 1 {
		t.Fatal("protocol lost original removal order", *removes)
	}
	if _, err := os.Lstat(filepath.Join(f.root.Name(), "origin.json")); err != nil {
		t.Fatal("read-only refresh removed runtime metadata", err)
	}
}
func TestNativeContainerRefreshRefusesImmutableChangesBeforeRetirementOrRemoval(t *testing.T) {
	for _, change := range []string{"immutable-record-inode", "immutable-origin-inode", "immutable-key-inode", "immutable-record-bytes", "unknown-status", "status-symlink", "surviving-runtime-directory", "subset-ACK", "foreign-CID-ACK", "forward-lock-replaced", "forward-lock-missing", "forward-parent-missing"} {
		t.Run(change, func(t *testing.T) {
			f, g, original, scope := containerRefreshFixture(t)
			actualCID := g.cid
			if change == "subset-ACK" {
				scope.records = scope.records[1:]
			}
			if change == "foreign-CID-ACK" {
				scope.cid = strings.Repeat("b", 64)
			}
			retired := 0
			a, removes := refreshProtocolActions(t, scope, func(ctx context.Context, ack *nativeContainerStopACK) error {
				next, err := refreshNativeContainerAfterStop(ctx, original, f.release, actualCID, ack)
				if next != nil {
					_ = next.Close()
				}
				return err
			})
			a.retire = func(context.Context, *nativeContainerStopACK) error { retired++; return nil }
			a.stop = func(context.Context) error {
				prefix := "generations/" + g.current.Generation + "/"
				path := ""
				switch change {
				case "forward-lock-replaced":
					path = prefix + "status/forwards/" + strings.Repeat("e", 32) + ".lock"
				case "forward-lock-missing":
					return os.Remove(filepath.Join(f.root.Name(), prefix, "status/forwards", strings.Repeat("e", 32)+".lock"))
				case "forward-parent-missing":
					return os.Rename(filepath.Join(f.root.Name(), prefix, "status/forwards"), filepath.Join(t.TempDir(), "retained-forwards"))
				case "immutable-record-inode":
					path = prefix + "record.json"
				case "immutable-origin-inode":
					path = "origin.json"
				case "immutable-key-inode":
					path = prefix + "supervisor/auth.json"
				case "immutable-record-bytes":
					return os.WriteFile(filepath.Join(f.root.Name(), prefix, "record.json"), []byte("{}"), 0600)
				case "unknown-status":
					return os.WriteFile(filepath.Join(f.root.Name(), prefix, "status/unexpected"), []byte("retain"), 0600)
				case "status-symlink":
					target := filepath.Join(f.root.Name(), prefix, "status/runtime.json")
					if err := os.Remove(target); err != nil {
						return err
					}
					return os.Symlink(filepath.Join(f.root.Name(), "current.json"), target)
				case "surviving-runtime-directory":
					// Move an empty allowed directory outside the runtime tree, then replace
					// it at the same allowed name. Schema alone must not adopt the new inode.
					dir := filepath.Join(f.root.Name(), prefix, "status/runtime-supervisor/ssh")
					if err := os.Rename(dir, filepath.Join(t.TempDir(), "retained-ssh")); err != nil {
						return err
					}
					return os.Mkdir(dir, 0700)
				}
				if path != "" {
					raw, err := os.ReadFile(filepath.Join(f.root.Name(), path))
					if err != nil {
						return err
					}
					return safepath.WriteFileOwned(f.root.Name(), path, raw, 0600, os.Geteuid(), os.Getegid())
				}
				return nil
			}
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			var uncertain *NativeEffectsUncertain
			if err == nil || errors.As(err, &uncertain) || retired != 0 || *removes != 0 { // plain step error before rm (decision #3)
				t.Fatal("status refresh accepted unproved immutable/scope change", change, err, retired, *removes)
			}
			if _, err := os.Lstat(filepath.Join(f.root.Name(), "current.json")); err != nil {
				t.Fatal("refresh refusal removed current metadata", err)
			}
		})
	}
}
func TestNativeContainerRefreshRequiresOriginalInactivityAndLiveToken(t *testing.T) {
	f, g, inventory, scope := containerRefreshFixture(t)
	attempt, err := newNativeContainerRemovalAttempt(scope)
	if err != nil {
		t.Fatal(err)
	}
	// Private data seam only: a Stop flag without the protocol's independent
	// inactive callback can never permit this reader. No real ACK is minted.
	attempt.stopAcknowledged = true
	ack := &nativeContainerStopACK{attempt}
	next, err := refreshNativeContainerAfterStop(t.Context(), inventory, f.release, g.cid, ack)
	if next != nil {
		_ = next.Close()
	}
	if err == nil {
		t.Fatal("status refresh accepted Stop ACK without inactivity proof")
	}
	attempt.inactiveVerified = true
	attempt.active = false
	next, err = refreshNativeContainerAfterStop(t.Context(), inventory, f.release, g.cid, ack)
	if next != nil {
		_ = next.Close()
	}
	if err == nil {
		t.Fatal("status refresh accepted expired original ACK")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := refreshNativeContainerAfterStop(ctx, inventory, f.release, g.cid, ack); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled refresh ignored context", err)
	}
}

func TestNativeContainerRefreshForwardJSONChurnRetainsOriginalLockAnchors(t *testing.T) {
	f, g, original, scope := containerRefreshFixture(t)
	dir := filepath.Join(f.root.Name(), "generations", g.current.Generation, "status/forwards")
	first, second := strings.Repeat("e", 32), strings.Repeat("f", 32)
	prior := map[string]os.FileInfo{}
	for _, nonce := range []string{first, second} {
		info, err := os.Stat(filepath.Join(dir, nonce+".lock"))
		if err != nil {
			t.Fatal(err)
		}
		prior[nonce] = info
	}
	var next *NativeContainerInventory
	a, removes := refreshProtocolActions(t, scope, func(ctx context.Context, ack *nativeContainerStopACK) error {
		var err error
		next, err = refreshNativeContainerAfterStop(ctx, original, f.release, g.cid, ack)
		return err
	})
	a.stop = func(context.Context) error {
		if err := os.Remove(filepath.Join(dir, first+".json")); err != nil {
			return err
		}
		return safepath.WriteFileOwned(f.root.Name(), "generations/"+g.current.Generation+"/status/forwards/"+second+".json", []byte(`{"fixture":"withdrawn"}`), 0600, os.Geteuid(), os.Getegid())
	}
	if err := completeNativeContainerRemoval(t.Context(), scope, a); err != nil {
		t.Fatal("forward JSON churn rejected with original locks retained", err)
	}
	if next == nil || *removes != 1 {
		t.Fatal("forward JSON refresh skipped original protocol", next, *removes)
	}
	defer func() { _ = next.Close() }()
	for nonce, info := range prior {
		after, err := os.Stat(filepath.Join(dir, nonce+".lock"))
		if err != nil || !os.SameFile(info, after) {
			t.Fatal("refresh replaced/removed original forward lock", nonce, err)
		}
	}
	if err := original.Check(t.Context()); err == nil {
		t.Fatal("strict original inventory silently adopted JSON churn")
	}
	if err := next.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

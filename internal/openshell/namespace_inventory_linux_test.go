package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real immutable generation/bootstrap publication and ordinary protected files;
// namespace identities and marker ACKs are synthetic. No namespace, mount,
// engine Stop, history or native cleanup authority is proved by this fixture.
func namespaceInventoryFixture(t *testing.T) (string, *namespacePinLock, []RuntimeRecord, string) {
	t.Helper()
	f := newMaterialFixture(t)
	f.record.Launch.UnconfinedNetwork = true
	f.writeRecords(t)
	origin, _ := json.Marshal(generationOrigin(f.record))
	if err := os.WriteFile(filepath.Join(f.root.Name(), "origin.json"), origin, 0600); err != nil {
		t.Fatal(err)
	}
	cid := strings.Repeat("a", 64)
	g, err := snapshotGenerationAt(f.root, f.record, f.release, cid, f.owners)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	records := []RuntimeRecord{cloneRuntimeRecord(g.current)}
	effect := generationEffectFixture(t, g)
	for _, controller := range []string{"prior-controller", "current-controller"} {
		next, err := g.prepareNext(t.Context(), controller, g.OriginalPolicy(), generationTarget(g), effect)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, cloneRuntimeRecord(next.Record))
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	lock, err := lockNamespacePinsAt(t.Context(), control, records[0].Launch.Binding.Incarnation, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	boot := "11111111-2222-4333-8444-555555555555"
	mount := namespaceIdentity{Device: 7, Inode: 92}
	for n, record := range records[:2] {
		network := namespaceIdentity{Device: 7, Inode: uint64(100 + n)}
		target := ActivationTarget{ContainerID: cid, PID: 1_000_000_000 + n, StartedAt: time.Unix(1_800_000_000, 0)}
		scope, err := newNamespacePinScope(record, target, "12345", network, mount, boot)
		if err != nil {
			t.Fatal(err)
		}
		seed := EgressInputs{egressOwner(record), []netip.Addr{netip.MustParseAddr("192.168.127.1")}, []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24")}}
		plan, err := newNativeEgressPlan(network, seed, []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
		if err != nil {
			t.Fatal(err)
		}
		reservation, err := lock.reserve(scope, record, cid, plan)
		if err != nil {
			t.Fatal(err)
		}
		_ = reservation.Close()
	}
	if err := publishNoInitializationAt(lock, records[2], cid, boot, mount, pinRetirementProgress{stopAttempted: true, stopAcknowledged: true}); err != nil {
		t.Fatal(err)
	}
	return root, lock, records, cid
}
func TestNamespaceGenerationInventoryIncludesPinAndIndependentNoInitializationUnion(t *testing.T) {
	root, lock, records, cid := namespaceInventoryFixture(t)
	s, err := snapshotNamespaceGenerations(t.Context(), lock, records, cid)
	if err != nil {
		t.Fatal("namespace union construction failed", err)
	}
	defer s.Close()
	if len(s.pins) != 2 || len(s.noInitialization) != 1 || len(s.records) != 3 {
		t.Fatal("namespace inventory dropped pin/no-init generations", len(s.pins), len(s.noInitialization), len(s.records))
	}
	records[0].Files["boundary/bootstrap.json"] = "caller mutation"
	if err := s.check(t.Context()); err != nil {
		t.Fatal("namespace inventory borrowed caller metadata", err)
	}
	s.Close()
	if err := s.check(t.Context()); err == nil {
		t.Fatal("closed namespace inventory remained valid")
	}
	if err := lock.check(); err != nil {
		t.Fatal("inventory closed caller's stable lock", err)
	}
	inc := filepath.Join(root, "namespace-pins", lock.incarnation)
	for _, name := range []string{".lock", "origins/" + records[0].Generation + ".json", "pins/" + records[1].Generation + "/state.json", "no-init/" + records[2].Generation + ".json"} {
		if _, err := os.Lstat(filepath.Join(inc, name)); err != nil {
			t.Fatal("read-only inventory removed retained metadata", name, err)
		}
	}
}
func TestNamespaceGenerationInventoryRefusesOrphanConflictAndPartialCoverage(t *testing.T) {
	for _, change := range []string{"orphan-origin", "orphan-pin", "unknown-incarnation", "unknown-pin-leaf", "unknown-no-init", "missing-origin", "missing-pin", "missing-no-init", "missing-placeholder", "conflicting-no-init", "duplicate-record", "foreign-record", "wrong-cid", "symlink-origin", "state-mode", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			root, lock, records, cid := namespaceInventoryFixture(t)
			inc := filepath.Join(root, "namespace-pins", lock.incarnation)
			origin := filepath.Join(inc, "origins", records[0].Generation+".json")
			pin := filepath.Join(inc, "pins", records[0].Generation)
			ctx := t.Context()
			var err error
			before, err := os.ReadFile(filepath.Join(pin, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "orphan-origin":
				raw, e := os.ReadFile(origin)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(filepath.Join(inc, "origins", "g"+strings.Repeat("f", 48)+".json"), raw, 0600)
			case "orphan-pin":
				err = os.Mkdir(filepath.Join(inc, "pins", "g"+strings.Repeat("f", 48)), 0700)
			case "unknown-incarnation":
				err = os.WriteFile(filepath.Join(inc, "unexpected"), []byte("retain"), 0600)
			case "unknown-pin-leaf":
				err = os.WriteFile(filepath.Join(pin, "unexpected"), []byte("retain"), 0600)
			case "unknown-no-init":
				err = os.WriteFile(filepath.Join(inc, "no-init", "unexpected"), []byte("retain"), 0600)
			case "missing-origin":
				err = os.Remove(origin)
			case "missing-pin":
				err = os.Rename(pin, pin+"-retained")
			case "missing-no-init":
				err = os.Remove(filepath.Join(inc, "no-init", records[2].Generation+".json"))
			case "missing-placeholder":
				err = os.Remove(filepath.Join(pin, "namespace"))
			case "conflicting-no-init":
				// Adversarial setup intentionally bypasses publisher's conflict guard.
				raw, e := json.Marshal(namespaceNoInitialization{1, recordIdentity(records[0]), cid, "11111111-2222-4333-8444-555555555555", namespaceIdentity{7, 92}})
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(filepath.Join(inc, "no-init", records[0].Generation+".json"), raw, 0600)
			case "duplicate-record":
				records = append(records, records[0])
			case "foreign-record":
				records[0].Launch.Binding.Incarnation = strings.Repeat("f", 32)
			case "wrong-cid":
				cid = strings.Repeat("b", 64)
			case "symlink-origin":
				if err = os.Remove(origin); err == nil {
					err = os.Symlink(filepath.Join(inc, "origins", records[1].Generation+".json"), origin)
				}
			case "state-mode":
				err = os.Chmod(filepath.Join(pin, "state.json"), 0644)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err := snapshotNamespaceGenerations(ctx, lock, records, cid)
			if s != nil {
				s.Close()
			}
			if err == nil {
				t.Fatal("unsafe namespace union was accepted", change)
			}
			state := filepath.Join(pin, "state.json")
			if change == "missing-pin" {
				state = filepath.Join(pin+"-retained", "state.json")
			}
			after, e := os.ReadFile(state)
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("read-only refusal changed namespace state", e)
			}
		})
	}
}
func TestNamespaceGenerationInventoryRevalidatesWholeUnionAndImmutableBytes(t *testing.T) {
	for _, change := range []string{"new-orphan-origin", "origin-same-inode", "state-same-inode", "no-init-changed", "new-unknown-leaf", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			root, lock, records, cid := namespaceInventoryFixture(t)
			s, err := snapshotNamespaceGenerations(t.Context(), lock, records, cid)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			inc := filepath.Join(root, "namespace-pins", lock.incarnation)
			ctx := t.Context()
			var path string
			switch change {
			case "new-orphan-origin":
				path = filepath.Join(inc, "origins", "g"+strings.Repeat("f", 48)+".json")
				err = os.WriteFile(path, []byte("{}"), 0600)
			case "origin-same-inode":
				path = filepath.Join(inc, "origins", records[0].Generation+".json")
			case "state-same-inode":
				path = filepath.Join(inc, "pins", records[0].Generation, "state.json")
			case "no-init-changed":
				path = filepath.Join(inc, "no-init", records[2].Generation+".json")
			case "new-unknown-leaf":
				path = filepath.Join(inc, "pins", records[0].Generation, "unexpected")
				err = os.WriteFile(path, []byte("retain"), 0600)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if change == "origin-same-inode" || change == "state-same-inode" || change == "no-init-changed" {
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				next := append([]byte(" "), raw...)
				switch change {
				case "origin-same-inode":
					var fields map[string]json.RawMessage
					if e := json.Unmarshal(raw, &fields); e != nil {
						t.Fatal(e)
					}
					next = append([]byte(`{"record":`), fields["record"]...)
					next = append(next, []byte(`,"directory":`)...)
					next = append(next, fields["directory"]...)
					next = append(next, '}')
				case "state-same-inode":
					prefix := []byte(`{"schema":1,`)
					if !bytes.HasPrefix(raw, prefix) {
						t.Fatal("state fixture did not have canonical schema prefix")
					}
					next = append([]byte("{"), raw[len(prefix):len(raw)-1]...)
					next = append(next, []byte(`,"schema":1}`)...)
				}
				if change != "no-init-changed" && (len(raw) != len(next) || bytes.Equal(raw, next)) {
					t.Fatal("immutable byte mutation changed size or did nothing")
				}
				err = os.WriteFile(path, next, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.check(ctx); err == nil {
				t.Fatal("namespace union accepted changed membership/bytes", change)
			}
		})
	}
}
func TestNamespaceGenerationInventoryRequiresCompleteInputsBeforeReading(t *testing.T) {
	if _, err := snapshotNamespaceGenerations(t.Context(), nil, nil, ""); err == nil {
		t.Fatal("empty scope became empty inventory")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := snapshotNamespaceGenerations(ctx, nil, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled inventory ignored context", err)
	}
}

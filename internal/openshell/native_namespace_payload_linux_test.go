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

	"golang.org/x/sys/unix"
)

type nativeNamespacePayloadFixture struct {
	f               draftOriginRecordFixture
	path, cid, boot string
}

func nativeNamespacePayloadMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func newNativeNamespacePayloadFixture(t *testing.T) nativeNamespacePayloadFixture {
	t.Helper()
	f := newDraftOriginRecordFixture(t)
	f.current.Launch.UnconfinedNetwork = true
	f.older.Launch.UnconfinedNetwork = true
	for _, record := range []RuntimeRecord{f.current, f.older} {
		if err := record.Validate(f.release, f.origin.HelperSHA256); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.container, "generations", record.Generation, "record.json"), nativeNamespacePayloadMarshal(t, record), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.container, "current.json"), nativeNamespacePayloadMarshal(t, f.current), 0600); err != nil {
		t.Fatal(err)
	}
	path := namespaceScaffoldFixture(t, f.control, f.origin.Incarnation, true)
	control, err := os.Open(f.control)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	lock, err := lockNamespacePinsAt(t.Context(), control, f.origin.Incarnation, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	cid, boot := strings.Repeat("a", 64), "11111111-2222-4333-8444-555555555555"
	network, infrastructure := namespaceIdentity{Device: 7, Inode: 91}, namespaceIdentity{Device: 7, Inode: 92}
	target := ActivationTarget{ContainerID: cid, PID: 1_000_000_000, StartedAt: time.Unix(1_800_000_000, 0)}
	scope, err := newNamespacePinScope(f.current, target, "12345", network, infrastructure, boot)
	if err != nil {
		t.Fatal(err)
	}
	seed := EgressInputs{egressOwner(f.current), []netip.Addr{netip.MustParseAddr("192.168.127.1")}, []netip.Prefix{netip.MustParsePrefix("10.4.0.0/24")}}
	plan, err := newNativeEgressPlan(network, seed, []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := lock.reserve(scope, f.current, cid, plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reservation.Close() })
	var state namespacePinRecord
	if err := json.Unmarshal(reservation.files.current, &state); err != nil {
		t.Fatal(err)
	}
	// Synthetic STORED active phase only. No mount/policy/Create/Stop ACK is supplied.
	state.Phase, state.DirectoryMountID, state.PinMountID = pinPresent, 122, 123
	if err := reservation.files.writeObservation(state); err != nil {
		t.Fatal(err)
	}
	noinit := namespaceNoInitialization{Schema: 1, RecordSHA256: recordIdentity(f.older), ContainerID: cid, KernelBootID: boot, InfrastructureMount: infrastructure}
	if err := os.WriteFile(filepath.Join(path, "no-init", f.older.Generation+".json"), nativeNamespacePayloadMarshal(t, noinit), 0600); err != nil {
		t.Fatal(err)
	}
	return nativeNamespacePayloadFixture{f, path, cid, boot}
}
func (f nativeNamespacePayloadFixture) use(t *testing.T, current func(context.Context) error, resolve func(containerOrigin) (QualifiedRelease, error), use func(*nativeOriginScope, *nativeNamespaceScaffolds, *nativeOriginRecords) error) error {
	t.Helper()
	return withNativeOriginScopeAt(t.Context(), f.f.disk, f.f.selection, os.Geteuid(), os.Getegid(), current, func(origin containerOrigin) error {
		if origin != f.f.origin {
			return errors.New("foreign fixture origin")
		}
		return nil
	}, func(scope *nativeOriginScope) error {
		scaffolds, err := observeNativeNamespaceScaffolds(t.Context(), scope)
		if err != nil {
			return err
		}
		defer func() { _ = scaffolds.Close() }()
		records, err := captureNativeOriginRecords(t.Context(), scope, f.f.origin.Incarnation, resolve)
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		return use(scope, scaffolds, records)
	}) //Primary fixture only, NOT aggregate callback/Close installation.
}
func TestNativeNamespacePayloadCompleteMixedGroupsActiveForeignAndCopies(t *testing.T) {
	f := newNativeNamespacePayloadFixture(t)
	err := f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
		p, err := observeNativeNamespacePayload(t.Context(), scope, s, r)
		if err != nil {
			return err
		}
		borrowed := []*os.File{scope.catalog.root, r.entry.directory, r.entry.file, p.origins.file, p.pins.file, p.noinit.file}
		got, err := p.Observation(t.Context())
		if err != nil {
			return err
		}
		if got.origin != f.f.origin || len(got.generations) != 2 {
			t.Fatal("incomplete original metadata", got)
		}
		found := map[string]nativeNamespacePayloadGeneration{}
		for _, g := range got.generations {
			found[g.generation] = g
			if g.recordedCID != f.cid || g.kernelBootID != f.boot || g.retiredSchema != 0 {
				t.Fatal("stored metadata was dropped/turned into retirement", g)
			}
		}
		if found[f.f.current.Generation].kind != 3 || found[f.f.current.Generation].phase != pinPresent || found[f.f.older.Generation].kind != 4 {
			t.Fatal("active/no-init groups changed", found)
		}
		for _, n := range p.nodes {
			if n.name == "namespace" {
				t.Fatal("namespace leaf was opened")
			}
		}
		got.origin.Box = "mutated-copy"
		got.generations[0].recordedCID = "changed-copy"
		again, err := p.Observation(t.Context())
		if err != nil {
			return err
		}
		if again.origin.Box != f.f.origin.Box || again.generations[0].recordedCID != f.cid {
			t.Fatal("caller copy changed original")
		}
		if err := p.Close(); err != nil {
			return err
		}
		if err := p.Close(); err != nil {
			return err
		}
		for _, file := range borrowed {
			if _, err := file.Stat(); err != nil {
				t.Fatal("payload closed borrowed handle", err)
			}
		}
		if err := p.CheckOriginal(t.Context()); err == nil {
			t.Fatal("closed payload usable")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestNativeNamespacePayloadOriginalFinalPhaseHasNoExternalCallbackAndDetectsVerifierMutation(t *testing.T) {
	f := newNativeNamespacePayloadFixture(t)
	armed := false
	calls := 0
	statePath := filepath.Join(f.path, "pins", f.f.current.Generation, "state.json")
	original, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(origin containerOrigin) (QualifiedRelease, error) {
		calls++
		if armed {
			armed = false
			var state namespacePinRecord
			if err := json.Unmarshal(original, &state); err != nil {
				return QualifiedRelease{}, err
			}
			state.Phase = pinPolicyApplyAttempt
			if err := os.WriteFile(statePath, nativeNamespacePayloadMarshal(t, state), 0600); err != nil {
				return QualifiedRelease{}, err
			}
		}
		return f.f.resolve(origin)
	}
	current := func(ctx context.Context) error { calls++; return ctx.Err() }
	err = f.use(t, current, resolver, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
		p, err := observeNativeNamespacePayload(t.Context(), scope, s, r)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		before := calls
		armed = true
		if err := p.CheckOriginal(t.Context()); err != nil {
			return err
		}
		if calls != before {
			t.Fatal("local-only phase invoked external callback")
		}
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("resolver-time mutation escaped final original bytes")
		}
		if err := os.WriteFile(statePath, original, 0600); err != nil {
			return err
		}
		if err := p.CheckOriginal(t.Context()); err == nil {
			t.Fatal("original failure repaired by restored bytes")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestNativeNamespacePayloadOrphansPartialConflictsAndBadPayloadFail(t *testing.T) {
	for _, which := range []string{"origin-without-pin", "pin-without-origin", "noinit-conflict", "missing-record-group", "orphan-generation", "missing-namespace", "unknown-member", "folded-origin", "folded-state", "folded-noinit", "bad-state-mode", "unknown-leaf-mode", "invalid-retired-on-active"} {
		t.Run(which, func(t *testing.T) {
			f := newNativeNamespacePayloadFixture(t)
			gen := f.f.current.Generation
			originPath := filepath.Join(f.path, "origins", gen+".json")
			statePath := filepath.Join(f.path, "pins", gen, "state.json")
			noinitPath := filepath.Join(f.path, "no-init", f.f.older.Generation+".json")
			// Rename exact known fixture names aside, never delete. All operations below
			// are authored ordinary-FS test actions, never executed by this draft task.
			switch which {
			case "origin-without-pin":
				if err := os.Rename(filepath.Join(f.path, "pins", gen), filepath.Join(f.f.disk, "saved-pin")); err != nil {
					t.Fatal(err)
				}
			case "pin-without-origin":
				if err := os.Rename(originPath, filepath.Join(f.f.disk, "saved-origin")); err != nil {
					t.Fatal(err)
				}
			case "noinit-conflict":
				raw, err := os.ReadFile(noinitPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.path, "no-init", gen+".json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-record-group":
				if err := os.Rename(noinitPath, filepath.Join(f.f.disk, "saved-noinit")); err != nil {
					t.Fatal(err)
				}
			case "orphan-generation":
				if err := os.WriteFile(filepath.Join(f.path, "no-init", "g"+strings.Repeat("e", 48)+".json"), []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-namespace":
				if err := os.Rename(filepath.Join(f.path, "pins", gen, "namespace"), filepath.Join(f.f.disk, "saved-namespace")); err != nil {
					t.Fatal(err)
				}
			case "unknown-member":
				if err := os.WriteFile(filepath.Join(f.path, "pins", gen, "unknown"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "folded-origin", "folded-state", "folded-noinit":
				path, old, new := originPath, `"directory":`, `"Directory":`
				if which == "folded-state" {
					path, old, new = statePath, `"schema":`, `"Schema":`
				}
				if which == "folded-noinit" {
					path, old, new = noinitPath, `"schema":`, `"Schema":`
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				changed := []byte(strings.Replace(string(raw), old, new, 1))
				if bytes.Equal(changed, raw) {
					t.Fatal("fixture mutation absent")
				}
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-state-mode":
				if err := os.Chmod(statePath, 0644); err != nil {
					t.Fatal(err)
				}
			case "unknown-leaf-mode":
				if err := os.Chmod(filepath.Join(f.path, "pins", gen, "namespace"), 0666); err != nil {
					t.Fatal(err)
				}
			case "invalid-retired-on-active":
				if err := os.WriteFile(filepath.Join(f.path, "pins", gen, "retired.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
				p, err := observeNativeNamespacePayload(t.Context(), scope, s, r)
				if p != nil {
					_ = p.Close()
				}
				if err == nil {
					t.Fatal("partial/unknown metadata became usable", which)
				}
				return nil
			})
			if err != nil {
				t.Fatal("source scaffold should recognize names while payload refuses", err)
			}
		})
	}
}
func TestNativeNamespacePayloadStoredDifferentBootAndDeviceAreNotCurrentIdentityGates(t *testing.T) {
	f := newNativeNamespacePayloadFixture(t)
	path := filepath.Join(f.path, "origins", f.f.current.Generation+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var origin namespacePinOrigin
	if err := json.Unmarshal(raw, &origin); err != nil {
		t.Fatal(err)
	}
	origin.Directory.Device = 999999 //historical fields are metadata, not current st_dev
	if err := os.WriteFile(path, nativeNamespacePayloadMarshal(t, origin), 0600); err != nil {
		t.Fatal(err)
	}
	err = f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
		p, err := observeNativeNamespacePayload(t.Context(), scope, s, r)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		return p.CheckOriginal(t.Context()) //No current kernel-boot parameter/probe exists.
	})
	if err != nil {
		t.Fatal("old recorded identity incorrectly compared to current native device", err)
	}
	// Pure shape controls for namespace-like metadata, NOT real NSFS/mount tests.
	base := namespaceInventoryMember{identity: pinFileIdentity{device: 987, inode: 765, mount: 543}, mode: unix.S_IFREG | 0444, uid: 0, gid: 0, links: 1, size: 0}
	if err := nativeNamespacePayloadLeaf(base); err != nil {
		t.Fatal("namespace-like current mount metadata rejected", err)
	}
	base.uid, base.gid, base.identity.device, base.identity.mount = 555, 777, 123, 456
	if err := nativeNamespacePayloadLeaf(base); err != nil {
		t.Fatal("blanket UID/device/mount gate added", err)
	}
	base.mode = unix.S_IFREG | 0600
	if err := nativeNamespacePayloadLeaf(base); err != nil {
		t.Fatal("ordinary placeholder shape rejected", err)
	}
	base.mode = unix.S_IFDIR | 0555
	if err := nativeNamespacePayloadLeaf(base); err == nil {
		t.Fatal("unknown namespace leaf kind accepted")
	}
}
func TestNativeNamespacePayloadSameByteReplacementAndOriginalCancellationSticky(t *testing.T) {
	for _, which := range []string{"same-byte-replacement", "cancellation"} {
		t.Run(which, func(t *testing.T) {
			f := newNativeNamespacePayloadFixture(t)
			err := f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				p, err := observeNativeNamespacePayload(ctx, scope, s, r)
				if err != nil {
					return err
				}
				defer func() { _ = p.Close() }()
				cause := errors.New("original payload execution canceled")
				if which == "cancellation" {
					cancel(cause)
					err = p.CheckOriginal(context.WithoutCancel(ctx))
					if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
						t.Fatal("original custom cancellation cause lost", err)
					}
				} else {
					path := filepath.Join(f.path, "pins", f.f.current.Generation, "state.json")
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if err := os.Rename(path, filepath.Join(f.f.disk, "saved-state")); err != nil {
						return err
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						return err
					}
					if err := p.CheckOriginal(t.Context()); err == nil {
						t.Fatal("matching bytes repaired original mapping")
					}
				}
				if _, err := p.Observation(t.Context()); err == nil {
					t.Fatal("fresh context repaired failed observer")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestNativeNamespacePayloadOriginalScopeExpiryBeforeBorrowedAccess(t *testing.T) {
	f := newNativeNamespacePayloadFixture(t)
	var p *nativeNamespacePayload
	// Child stays live through the outer scope's final verifier, then source
	// expires. This is an expiry test, not a proposed production owner finalizer.
	err := f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
		var err error
		p, err = observeNativeNamespacePayload(t.Context(), scope, s, r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err := p.CheckOriginal(t.Context()); err == nil {
		t.Fatal("escaped observer accepted expired original sources")
	}
}
func TestNativeNamespacePayloadOwnedPartialCloseIsOnceAndSticky(t *testing.T) {
	first, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &nativeNamespacePayload{nodes: []nativeNamespacePayloadNode{{file: first}, {file: second}}}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	before := p.Close()
	if !errors.Is(before, os.ErrClosed) {
		t.Fatal("partial owned Close error lost", before)
	}
	if after := p.Close(); after != before {
		t.Fatal("Close retry changed sticky result", before, after)
	}
	if _, err := second.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("other partial owned handle leaked", err)
	}
}

func TestNativeNamespacePayloadStrictSourceAliasesInBothOrders(t *testing.T) {
	for _, which := range []string{"origin", "state", "noinit"} {
		for _, order := range []string{"first", "last", "duplicate"} {
			t.Run(which+"-"+order, func(t *testing.T) {
				f := newNativeNamespacePayloadFixture(t)
				path, field := filepath.Join(f.path, "origins", f.f.current.Generation+".json"), "directory"
				if which == "state" {
					path, field = filepath.Join(f.path, "pins", f.f.current.Generation, "state.json"), "schema"
				}
				if which == "noinit" {
					path, field = filepath.Join(f.path, "no-init", f.f.older.Generation+".json"), "schema"
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				key := strings.ToUpper(field[:1]) + field[1:]
				if order == "duplicate" {
					key = field
				}
				pair := `"` + key + `":` + string(fields[field])
				changed := append([]byte(`{`+pair+`,`), raw[1:]...)
				if order == "last" {
					changed = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,`+pair+`}`)...)
				}
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
				err = f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
					p, err := observeNativeNamespacePayload(t.Context(), scope, s, r)
					if p != nil {
						_ = p.Close()
					}
					if err == nil {
						t.Fatal("matching aliases/duplicates accepted")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestNativeNamespacePayloadOptionalRetiredMarkersPreserveStoredSchema1And2(t *testing.T) {
	for _, phase := range []namespacePinPhase{pinDirectoryUnmounted, pinPlanned, pinDirectoryBindAttempt, pinPartialUnmounted} {
		t.Run(string(phase), func(t *testing.T) {
			f := newNativeNamespacePayloadFixture(t)
			statePath := filepath.Join(f.path, "pins", f.f.current.Generation, "state.json")
			originRaw, err := os.ReadFile(filepath.Join(f.path, "origins", f.f.current.Generation+".json"))
			if err != nil {
				t.Fatal(err)
			}
			stateRaw, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var state namespacePinRecord
			if err := json.Unmarshal(stateRaw, &state); err != nil {
				t.Fatal(err)
			}
			state.Phase, state.ReadbackSHA256 = phase, ""
			if phase == pinDirectoryUnmounted {
				state.DirectoryMountID, state.PinMountID = 122, 123
			} else {
				state.DirectoryMountID, state.PinMountID = 0, 0
				if phase == pinPartialUnmounted {
					state.DirectoryMountID = 122
				}
			}
			stateRaw = nativeNamespacePayloadMarshal(t, state)
			if err := os.WriteFile(statePath, stateRaw, 0600); err != nil {
				t.Fatal(err)
			}
			expected, err := namespaceRetiredBindingFromMetadata(originRaw, stateRaw, f.f.current, f.cid)
			if err != nil {
				t.Fatal(err)
			}
			// Metadata-only fixture publication, never a successful effect-token path.
			if err := os.WriteFile(filepath.Join(f.path, "pins", f.f.current.Generation, "retired.json"), nativeNamespacePayloadMarshal(t, expected), 0600); err != nil {
				t.Fatal(err)
			}
			err = f.use(t, nativeOriginAncestryCurrentFixture, f.f.resolve, func(scope *nativeOriginScope, s *nativeNamespaceScaffolds, r *nativeOriginRecords) error {
				p, err := observeNativeNamespacePayload(t.Context(), scope, s, r)
				if err != nil {
					return err
				}
				defer func() { _ = p.Close() }()
				got, err := p.Observation(t.Context())
				if err != nil {
					return err
				}
				found := false
				for _, view := range got.generations {
					if view.generation == f.f.current.Generation {
						found = true
						if view.retiredSchema != expected.Schema {
							t.Fatal("stored retirement schema changed", view)
						}
					}
				}
				if !found {
					t.Fatal("original generation dropped")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Project metadata fixtures use the existing fictional qualified
// document + valid nonsecret record metadata, never a native context/engine.
// Only metadata is copied into the catalog; referenced private bodies need not
// exist and this reader does not check readiness/private credential integrity.
type draftOriginRecordFixture struct {
	disk, control, container string
	selection                nativeOriginCatalogSelection
	origin                   containerOrigin
	release                  QualifiedRelease
	current, older           RuntimeRecord
}

func newDraftOriginRecordFixture(t *testing.T) draftOriginRecordFixture {
	t.Helper()
	material := newMaterialFixture(t)
	helper, err := material.release.Helper(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	current := cloneRuntimeRecord(material.record)
	current.Launch.HelperSHA256 = helper.BinarySHA256
	older := cloneRuntimeRecord(current)
	older.SessionID = "11111111-2222-4333-8444-555555555555"
	older.Launch.Binding.ControllerGeneration = "older-controller"
	digest, err := older.Launch.Binding.digest()
	if err != nil {
		t.Fatal(err)
	}
	older.Generation, err = generationForSession(digest, older.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if older.Generation == current.Generation {
		t.Fatal("fixture generation collision")
	}
	for _, record := range []RuntimeRecord{current, older} {
		if err := record.Validate(material.release, helper.BinarySHA256); err != nil {
			t.Fatal(err)
		}
	}
	disk := nativeOriginAncestryRootFixture(t, 3, 0755)
	control := filepath.Join(disk, "runtime", "openshell")
	origin := generationOrigin(current)
	selection := nativeOriginCatalogSelection{origin.Space, origin.DiskID, origin.DiskUUID}
	container := filepath.Join(control, "containers", origin.Incarnation)
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	encode := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := os.WriteFile(filepath.Join(container, "origin.json"), encode(origin), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(container, "current.json"), encode(current), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(container, "generations"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, record := range []RuntimeRecord{current, older} {
		directory := filepath.Join(container, "generations", record.Generation)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "record.json"), encode(record), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return draftOriginRecordFixture{disk, control, container, selection, origin, material.release, current, older}
}
func (f draftOriginRecordFixture) use(t *testing.T, use func(*nativeOriginScope) error) error {
	t.Helper()
	return withNativeOriginScopeAt(t.Context(), f.disk, f.selection, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(origin containerOrigin) error {
		if origin != f.origin {
			return errors.New("foreign fixture origin")
		}
		return nil
	}, use)
}
func (f draftOriginRecordFixture) resolve(origin containerOrigin) (QualifiedRelease, error) {
	if origin != f.origin {
		return QualifiedRelease{}, errors.New("foreign original resolver selection")
	}
	return f.release, nil
}

func TestNativeOriginRecordsCompleteHistoricalMetadataAndNoVolatileBodies(t *testing.T) {
	f := newDraftOriginRecordFixture(t)
	err := f.use(t, func(scope *nativeOriginScope) error {
		records, err := captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, f.resolve)
		if err != nil {
			return err
		}
		borrowedDirectory, borrowedOrigin := records.entry.directory, records.entry.file
		owned := []*os.File{records.rootReader, records.generations, records.current.file}
		for _, value := range records.values {
			owned = append(owned, value.directory, value.value.file)
		}
		current, all, err := records.Records(t.Context())
		if err != nil {
			return err
		}
		if recordIdentity(current) != recordIdentity(f.current) || len(all) != 2 {
			t.Fatal("historical record/current scope incomplete")
		}
		found := map[string]bool{}
		for _, record := range all {
			found[record.Generation] = true
		}
		if !found[f.older.Generation] || !found[f.current.Generation] {
			t.Fatal("older controller generation dropped")
		}
		all[0].Files["boundary/bootstrap.json"] = "changed-copy"
		current.Files["boundary/bootstrap.json"] = "changed-current-copy"
		for _, record := range []RuntimeRecord{f.current, f.older} {
			directory := filepath.Join(f.container, "generations", record.Generation, "status")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "runtime.json"), []byte("changing volatile status, not a record"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		again, allAgain, err := records.Records(t.Context())
		if err != nil {
			return err
		}
		if again.Files["boundary/bootstrap.json"] == "changed-current-copy" || allAgain[0].Files["boundary/bootstrap.json"] == "changed-copy" {
			t.Fatal("copied records alias originals")
		}
		if err := records.Close(); err != nil {
			return err
		}
		for _, file := range owned {
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("owned observation handle leaked", err)
			}
		}
		for _, file := range []*os.File{borrowedDirectory, borrowedOrigin} {
			if _, err := file.Stat(); err != nil {
				t.Fatal("borrowed catalog handle closed", err)
			}
		}
		if err := scope.Check(t.Context()); err != nil {
			t.Fatal("reader expired its borrowed source", err)
		}
		if err := records.Check(t.Context()); err == nil {
			t.Fatal("closed observer repaired")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeOriginRecordsCanonicalClosedDecoderRejectsMatchingAliases(t *testing.T) {
	f := newDraftOriginRecordFixture(t)
	raw, _ := json.Marshal(f.current)
	launch, _ := json.Marshal(f.current.Launch)
	candidates := [][]byte{append(bytes.Clone(raw), '\n'), append(bytes.Clone(raw), []byte(" {}")...)}
	canonical := append([]byte(`"launch":`), launch...)
	for _, members := range []string{`"launch":` + string(launch) + `,"Launch":` + string(launch), `"Launch":` + string(launch) + `,"launch":` + string(launch)} {
		candidates = append(candidates, bytes.Replace(raw, canonical, []byte(members), 1))
	}
	box := []byte(`"box":"` + f.origin.Box + `"`)
	for _, members := range []string{string(box) + `,"Box":"` + f.origin.Box + `"`, `"Box":"` + f.origin.Box + `",` + string(box), string(box) + `,` + string(box)} {
		candidates = append(candidates, bytes.Replace(raw, box, []byte(members), 1))
	}
	for _, candidate := range candidates {
		record, err := decodeNativeOriginRuntimeRecord(candidate, f.origin, f.release)
		if err == nil || record.Generation != "" {
			t.Fatal("noncanonical/ambiguous row yielded decoded metadata")
		}
	}
	if _, err := decodeNativeOriginRuntimeRecord(raw, f.origin, QualifiedRelease{}); err == nil {
		t.Fatal("unqualified historical release accepted")
	}
}

func TestNativeOriginRecordsCaptureErrorsNeverBecomeEmptyOrCloseBorrowed(t *testing.T) {
	for _, kind := range []string{"malformed-current", "missing-record", "unknown-generation", "unknown-root", "bad-mode", "unsupported-resolver"} {
		t.Run(kind, func(t *testing.T) {
			f := newDraftOriginRecordFixture(t)
			switch kind {
			case "malformed-current":
				if err := os.WriteFile(filepath.Join(f.container, "current.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-record":
				if err := os.Rename(filepath.Join(f.container, "generations", f.older.Generation, "record.json"), filepath.Join(f.container, "generations", f.older.Generation, "not-record.json")); err != nil {
					t.Fatal(err)
				}
			case "unknown-generation":
				if err := os.Mkdir(filepath.Join(f.container, "generations", "orphan"), 0700); err != nil {
					t.Fatal(err)
				}
			case "unknown-root":
				if err := os.WriteFile(filepath.Join(f.container, "extra"), []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-mode":
				if err := os.Chmod(filepath.Join(f.container, "generations", f.older.Generation, "record.json"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := f.use(t, func(scope *nativeOriginScope) error {
				resolve := f.resolve
				if kind == "unsupported-resolver" {
					resolve = func(containerOrigin) (QualifiedRelease, error) {
						return QualifiedRelease{}, errors.New("supported historical release unavailable")
					}
				}
				result, err := captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, resolve)
				if err == nil || result != nil {
					t.Fatal("partial/unknown input became empty record inventory")
				}
				if err := scope.Check(t.Context()); err != nil {
					t.Fatal("failed capture closed borrowed original scope", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeOriginRecordsResolverMutationThenRestorationStaysFailed(t *testing.T) {
	f := newDraftOriginRecordFixture(t)
	armed := false
	calls := 0
	err := f.use(t, func(scope *nativeOriginScope) error {
		filename := filepath.Join(f.container, "generations", f.older.Generation, "record.json")
		original, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		resolver := func(origin containerOrigin) (QualifiedRelease, error) {
			calls++
			if armed {
				if err := os.WriteFile(filename, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			return f.resolve(origin)
		}
		records, err := captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, resolver)
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		armed = true
		if err := records.Check(t.Context()); err == nil {
			t.Fatal("external verifier interval hid changed record")
		}
		armed = false
		if err := os.WriteFile(filename, original, 0600); err != nil {
			t.Fatal(err)
		}
		before := calls
		if err := records.Check(t.Context()); err == nil {
			t.Fatal("restored record repaired original uncertainty")
		}
		if calls != before {
			t.Fatal("failed observer invoked historical resolver again")
		}
		if current, all, err := records.Records(t.Context()); err == nil || current.Generation != "" || all != nil {
			t.Fatal("failed observer yielded partial records")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeOriginRecordsSameBytesReplacementAndRosterGrowthRefuse(t *testing.T) {
	for _, kind := range []string{"same-bytes", "new-generation"} {
		t.Run(kind, func(t *testing.T) {
			f := newDraftOriginRecordFixture(t)
			err := f.use(t, func(scope *nativeOriginScope) error {
				records, err := captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, f.resolve)
				if err != nil {
					return err
				}
				defer func() { _ = records.Close() }()
				if kind == "same-bytes" {
					filename := filepath.Join(f.container, "generations", f.older.Generation, "record.json")
					raw, err := os.ReadFile(filename)
					if err != nil {
						t.Fatal(err)
					}
					fresh := filename + ".new"
					if err := os.WriteFile(fresh, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(fresh, filename); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(filepath.Join(f.container, "generations", "g"+strings.Repeat("f", 48)), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := records.Check(t.Context()); err == nil {
					t.Fatal("later matching row/new generation adopted")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeOriginRecordsOriginalContextAndBorrowedScopeExpiryRefuse(t *testing.T) {
	f := newDraftOriginRecordFixture(t)
	var escaped *nativeOriginRecords
	err := f.use(t, func(scope *nativeOriginScope) error {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		records, err := captureNativeOriginRecords(ctx, scope, f.origin.Incarnation, f.resolve)
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		cancel()
		if err := records.Check(context.WithoutCancel(ctx)); !errors.Is(err, context.Canceled) {
			t.Fatal("replacement context revived original lifetime", err)
		}
		escaped, err = captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, f.resolve)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = escaped.Close() }()
	if err := escaped.Check(t.Context()); err == nil {
		t.Fatal("escaped reader accessed closed borrowed scope")
	}
	for _, r := range []*nativeOriginRecords{nil, {}} {
		if _, all, err := r.Records(t.Context()); err == nil || all != nil {
			t.Fatal("zero observer became record inventory")
		}
	}
}

func TestNativeOriginRecordsOwnCloseErrorNeverClosesBorrowedOwner(t *testing.T) {
	f := newDraftOriginRecordFixture(t)
	err := f.use(t, func(scope *nativeOriginScope) error {
		records, err := captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, f.resolve)
		if err != nil {
			return err
		}
		if err := records.current.file.Close(); err != nil {
			t.Fatal(err)
		} // exact owned fixture FD
		closeErr := records.Close()
		if closeErr == nil {
			t.Fatal("owned Close failure lost")
		}
		if again := records.Close(); again != closeErr {
			t.Fatal("Close retried/changed original result")
		}
		if err := scope.Check(t.Context()); err != nil {
			t.Fatal("failed owned Close touched borrowed catalog", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeOriginRecordsEscapedConcurrentCheckRechecksLifetimeAfterResolver(t *testing.T) {
	f := newDraftOriginRecordFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	armed := false
	var escaped *nativeOriginRecords
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		if escaped != nil {
			_ = escaped.Close()
		}
	}()
	err := f.use(t, func(scope *nativeOriginScope) error {
		resolver := func(origin containerOrigin) (QualifiedRelease, error) {
			if armed {
				close(entered)
				<-release
			}
			return f.resolve(origin)
		}
		var err error
		escaped, err = captureNativeOriginRecords(t.Context(), scope, f.origin.Incarnation, resolver)
		if err != nil {
			return err
		}
		armed = true
		go func() { done <- escaped.Check(t.Context()) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("fixture resolver did not reach bounded cut")
		}
		return nil // outer source expires while resolver owns NO source lock
	})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = escaped.Close() }()
	select {
	case checkErr := <-done:
		if checkErr == nil {
			t.Fatal("escaped read accessed expired borrowed source")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("escaped lifetime check did not finish")
	}
}

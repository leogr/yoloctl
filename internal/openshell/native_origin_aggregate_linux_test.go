package openshell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Actual ordinary protected source files + fictional release documents ONLY.
// These fixtures do not Configure native context, mount NSFS, install/fetch a
// production artifact, issue kernel effects or imply any cleanup/receipt ACK.
type nativeOriginAggregateFixture struct {
	payload     nativeNamespacePayloadFixture
	historyRoot string
	captures    int
	documents   []*nativeHistoricalRelease
}

func newNativeOriginAggregateFixture(t *testing.T) *nativeOriginAggregateFixture {
	t.Helper()
	payload := newNativeNamespacePayloadFixture(t)
	historyRoot := t.TempDir()
	nativeHistoricalReleaseWriteFixture(t, historyRoot, payload.f.origin, payload.f.release.Manifest())
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(historyRoot, payload.f.origin.Qualification), 0700) })
	return &nativeOriginAggregateFixture{payload: payload, historyRoot: historyRoot}
}
func (f *nativeOriginAggregateFixture) run(t *testing.T, ctx context.Context, current func(context.Context) error, use func(*nativeOriginAggregateRead) error) error {
	t.Helper()
	selection := f.payload.f.selection
	factory := func(origin containerOrigin) (*nativeHistoricalRelease, error) {
		f.captures++
		doc, err := captureNativeHistoricalReleaseWith(ctx, origin, os.Geteuid(), os.Getegid(), current, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(f.historyRoot, origin))
		if doc != nil {
			f.documents = append(f.documents, doc)
		}
		return doc, err
	}
	return withNativeOriginAggregateAt(ctx, f.payload.f.disk, selection, os.Geteuid(), os.Getegid(), current, nativeHistoricalReleaseBoundaryFixture, factory, use)
}
func nativeOriginAggregateOwnedFixture(a *nativeOriginAggregate) []*os.File {
	var out []*os.File
	for _, doc := range a.history.order {
		for _, node := range doc.nodes {
			out = append(out, node.file)
		}
	}
	for _, p := range a.payloads {
		for _, node := range p.nodes {
			out = append(out, node.file)
		}
	}
	for _, r := range a.records {
		out = append(out, r.rootReader, r.current.file, r.generations)
		for _, value := range r.values {
			out = append(out, value.directory, value.value.file)
		}
	}
	if a.scaffold != nil {
		for _, node := range a.scaffold.nodes {
			out = append(out, node.file)
		}
	}
	return out
}
func nativeOriginAggregateAddOriginFixture(t *testing.T, f *nativeOriginAggregateFixture, inc, box string) {
	t.Helper()
	base := f.payload.f
	current, older := cloneRuntimeRecord(base.current), cloneRuntimeRecord(base.older)
	for _, record := range []*RuntimeRecord{&current, &older} {
		record.Launch.Binding.Incarnation, record.Launch.Binding.Box = inc, box
		record.Launch.UnconfinedNetwork = false //foreign confined Box needs no namespace ledger
		digest, err := record.Launch.Binding.digest()
		if err != nil {
			t.Fatal(err)
		}
		record.Generation, err = generationForSession(digest, record.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if err := record.Validate(base.release, base.origin.HelperSHA256); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(base.control, "containers", inc)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "origin.json"), nativeNamespacePayloadMarshal(t, generationOrigin(current)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "current.json"), nativeNamespacePayloadMarshal(t, current), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "generations"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, record := range []RuntimeRecord{current, older} {
		dir := filepath.Join(path, "generations", record.Generation)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "record.json"), nativeNamespacePayloadMarshal(t, record), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNativeOriginAggregateRetainsAllOriginsSharesExactDocumentAndCopiesMetadata(t *testing.T) {
	f := newNativeOriginAggregateFixture(t)
	inc := strings.Repeat("e", 32)
	if inc == f.payload.f.origin.Incarnation {
		inc = strings.Repeat("d", 32)
	}
	nativeOriginAggregateAddOriginFixture(t, f, inc, "foreign")
	var escaped *nativeOriginAggregateRead
	var owned, sourceOwned []*os.File
	err := f.run(t, t.Context(), nativeOriginAncestryCurrentFixture, func(read *nativeOriginAggregateRead) error {
		escaped = read
		a := read.owner
		owned = nativeOriginAggregateOwnedFixture(a)
		sourceOwned = append(sourceOwned, a.source.catalog.root, a.source.catalog.collection)
		for _, entry := range a.entries {
			sourceOwned = append(sourceOwned, entry.directory, entry.file)
		}
		for _, node := range a.source.ancestry.nodes {
			sourceOwned = append(sourceOwned, node.file)
		}
		got, err := read.Observation(t.Context())
		if err != nil {
			return err
		}
		if len(got.origins) != 2 || len(got.records) != 2 || len(got.payloads) != 1 || len(got.namespaces.entries) != 1 {
			t.Fatal("complete origin/record/namespace union missing", got)
		}
		if f.captures != 1 || len(a.history.bindings) != 2 || len(a.history.order) != 1 {
			t.Fatal("reopened same qualification or lost separate original origin binding")
		}
		first := a.history.bindings[a.entries[0].origin.Incarnation]
		second := a.history.bindings[a.entries[1].origin.Incarnation]
		if first.origin == second.origin || first.document != second.document {
			t.Fatal("document sharing replaced origin identity")
		}
		got.origins[0].Box = "changed-copy"
		got.records[0].generations[0].recordSHA256 = "changed-copy"
		got.payloads[0].generations[0].recordedCID = "changed-copy"
		got.namespaces.entries[0].incarnation = "changed-copy"
		again, err := read.Observation(t.Context())
		if err != nil {
			return err
		}
		if again.origins[0].Box == "changed-copy" || again.records[0].generations[0].recordSHA256 == "changed-copy" || again.payloads[0].generations[0].recordedCID == "changed-copy" || again.namespaces.entries[0].incarnation == "changed-copy" {
			t.Fatal("snapshot aliases aggregate")
		}
		for _, file := range owned {
			if _, err := file.Stat(); err != nil {
				t.Fatal("child closed before callback finished", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range append(owned, sourceOwned...) {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("original owned file leaked", err)
		}
	}
	if err := escaped.Check(t.Context()); err == nil {
		t.Fatal("escaped read survived original callback")
	}
	if got, err := escaped.Observation(t.Context()); err == nil || len(got.origins) != 0 {
		t.Fatal("expired read returned usable snapshot", got, err)
	}
	var zero nativeOriginAggregateRead
	if err := zero.Check(t.Context()); err == nil {
		t.Fatal("zero read usable")
	}
}
func TestNativeOriginAggregateFinalVerifierKeepsChildrenLiveAndExpiresEscapedRead(t *testing.T) {
	f := newNativeOriginAggregateFixture(t)
	afterUse := false
	calls := 0
	var owned []*os.File
	var escaped *nativeOriginAggregateRead
	current := func(ctx context.Context) error {
		if afterUse {
			calls++
			for _, file := range owned {
				if _, err := file.Stat(); err != nil {
					return errors.New("external verifier saw already-closed child")
				}
			}
			if err := escaped.Check(ctx); err == nil {
				return errors.New("finalizing read still started a callback")
			}
		}
		return ctx.Err()
	}
	if err := f.run(t, t.Context(), current, func(read *nativeOriginAggregateRead) error {
		escaped = read
		owned = nativeOriginAggregateOwnedFixture(read.owner)
		afterUse = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("mandatory final SOURCE external verification was skipped")
	}
	for _, file := range owned {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("child not closed after final original checks", err)
		}
	}
}
func nativeOriginAggregateFinalCallbackFixture(t *testing.T, mutate bool, last int) (int, bool, error) {
	t.Helper()
	f := newNativeOriginAggregateFixture(t)
	afterUse := false
	calls := 0
	changed := false
	var owned []*os.File
	current := func(ctx context.Context) error {
		if afterUse {
			calls++
			for _, file := range owned {
				if _, err := file.Stat(); err != nil {
					return errors.New("callback ran after child Close")
				}
			}
			if mutate && calls == last {
				path := filepath.Join(f.historyRoot, f.payload.f.origin.Qualification)
				raw := f.payload.f.release.Manifest()
				if err := os.Chmod(path, 0700); err != nil {
					return err
				}
				if err := os.Rename(filepath.Join(path, "qualification.json"), filepath.Join(f.historyRoot, "saved-original")); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(path, "qualification.json"), raw, 0444); err != nil {
					return err
				}
				if err := os.Chmod(path, 0555); err != nil {
					return err
				}
				changed = true
			}
		}
		return ctx.Err()
	}
	err := f.run(t, t.Context(), current, func(read *nativeOriginAggregateRead) error {
		owned = nativeOriginAggregateOwnedFixture(read.owner)
		afterUse = true
		return nil
	})
	return calls, changed, err
}
func TestNativeOriginAggregateLastExternalCallbackCannotReplaceEarlierDocumentBeforeClose(t *testing.T) {
	// Determine this exact source fixture's finite final-verifier sequence once;
	// not an asserted native/ACK/mock-count qualification or a magic count.
	count, _, err := nativeOriginAggregateFinalCallbackFixture(t, false, 0)
	if err != nil || count == 0 {
		t.Fatal("clean final phase unavailable", count, err)
	}
	observed, changed, err := nativeOriginAggregateFinalCallbackFixture(t, true, count)
	if !changed || observed != count {
		t.Fatal("did not mutate at actual last source callback", count, observed, changed, err)
	}
	if err == nil {
		t.Fatal("last verifier's same-byte historical replacement escaped LOCAL aggregate completion")
	}
}
func TestNativeOriginAggregateFinalSourceCallbackPayloadMutationRefuses(t *testing.T) {
	f := newNativeOriginAggregateFixture(t)
	afterUse := false
	changed := false
	path := filepath.Join(f.payload.path, "pins", f.payload.f.current.Generation, "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	current := func(ctx context.Context) error {
		if afterUse && !changed {
			changed = true
			// Same protected inode, altered bytes: origin-only last source check cannot
			// substitute for payload bytes AFTER every external verification callback.
			changedRaw := bytes.Replace(raw, []byte(`"phase":"pin-present"`), []byte(`"phase":"policy-apply-attempt"`), 1)
			if bytes.Equal(changedRaw, raw) {
				return errors.New("fixture phase mutation absent")
			}
			if err := os.WriteFile(path, changedRaw, 0600); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	err = f.run(t, t.Context(), current, func(read *nativeOriginAggregateRead) error { afterUse = true; return nil })
	if !changed || err == nil {
		t.Fatal("final external payload mutation became success", changed, err)
	}
}
func TestNativeOriginAggregateMissingPrefixesAndNeutralSiblingStayExplicit(t *testing.T) {
	for prefix := 0; prefix <= 2; prefix++ {
		t.Run(string(rune('a'+prefix)), func(t *testing.T) {
			disk := nativeOriginAncestryRootFixture(t, prefix, 0755)
			selection := nativeOriginScopeSelectionFixture()
			bare := strings.Repeat("b", 32)
			if prefix == 2 {
				namespaceScaffoldFixture(t, filepath.Join(disk, "runtime", "openshell"), bare, true)
			}
			captures := 0
			factory := func(containerOrigin) (*nativeHistoricalRelease, error) {
				captures++
				return nil, errors.New("missing-prefix fixture must not invent historical material")
			}
			err := withNativeOriginAggregateAt(t.Context(), disk, selection, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, nativeHistoricalReleaseBoundaryFixture, factory, func(read *nativeOriginAggregateRead) error {
				got, err := read.Observation(t.Context())
				if err != nil {
					return err
				}
				if len(got.origins) != 0 || len(got.records) != 0 || len(got.payloads) != 0 || got.prefix.state == nativeOriginAncestryPresent {
					t.Fatal("missing prefix fabricated native records or present source", got)
				}
				if prefix == 2 && (len(got.namespaces.entries) != 1 || !got.namespaces.entries[0].bareMetadata) {
					t.Fatal("missing containers skipped neutral sibling")
				}
				if prefix < 2 && len(got.namespaces.entries) != 0 {
					t.Fatal("invented sibling below missing parent")
				}
				return nil
			})
			if err != nil || captures != 0 {
				t.Fatal("explicit missing/neutral metadata unavailable", err, captures)
			}
		})
	}
}
func TestNativeOriginAggregateUnknownPayloadAndPartialNativeOriginNeverEmpty(t *testing.T) {
	for _, which := range []string{"orphan-payload", "partial-records"} {
		t.Run(which, func(t *testing.T) {
			f := newNativeOriginAggregateFixture(t)
			if which == "partial-records" {
				if err := os.Rename(filepath.Join(f.payload.f.container, "current.json"), filepath.Join(f.payload.f.disk, "saved-current")); err != nil {
					t.Fatal(err)
				}
			} else {
				inc := strings.Repeat("f", 32)
				if inc == f.payload.f.origin.Incarnation {
					inc = strings.Repeat("d", 32)
				}
				path := namespaceScaffoldFixture(t, f.payload.f.control, inc, false)
				if err := os.WriteFile(filepath.Join(path, "origins", "g"+strings.Repeat("a", 48)+".json"), []byte("orphan"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := f.run(t, t.Context(), nativeOriginAncestryCurrentFixture, func(*nativeOriginAggregateRead) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("unknown/partial became a readable empty union", err, called)
			}
			for _, doc := range f.documents {
				for _, node := range doc.nodes {
					if _, err := node.file.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatal("partial aggregate stranded historical child", err)
					}
				}
			}
		})
	}
}
func TestNativeOriginAggregateOriginalCancellationCauseAndBudgetFailureStaySticky(t *testing.T) {
	for _, which := range []string{"cancel", "budget"} {
		t.Run(which, func(t *testing.T) {
			f := newNativeOriginAggregateFixture(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("original aggregate expired")
			err := f.run(t, ctx, nativeOriginAncestryCurrentFixture, func(read *nativeOriginAggregateRead) error {
				if which == "cancel" {
					cancel(cause)
					if err := read.Check(context.WithoutCancel(ctx)); !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
						t.Fatal("original cause lost", err)
					}
				} else {
					// Private accounting fault, not a new source/authority seam. Underlying
					// actual retained children remain unchanged and must still all close.
					h := read.owner.history
					h.mu.Lock()
					saved := h.descriptors
					h.descriptors = nativeOriginAggregateDescriptors + 1
					h.mu.Unlock()
					if err := read.Check(t.Context()); err == nil {
						t.Fatal("aggregate descriptor budget omitted")
					}
					h.mu.Lock()
					h.descriptors = saved
					h.mu.Unlock()
				}
				if _, err := read.Observation(t.Context()); err == nil {
					t.Fatal("restoration repaired failed original aggregate")
				}
				return nil
			})
			if err == nil {
				t.Fatal("callback ignored failure yet wrapper finalized successfully")
			}
			if which == "cancel" && !errors.Is(err, cause) {
				t.Fatal("outer result lost original cause", err)
			}
		})
	}
}
func TestNativeOriginAggregateHistoryBudgetRefusesBeforeCaptureAndCannotRecaptureAfterFailure(t *testing.T) {
	f := newNativeOriginAggregateFixture(t)
	calls := 0
	c := &nativeOriginHistoryCache{lifetime: t.Context(), selection: f.payload.f.selection, rawBytes: nativeOriginAggregateHistoryBytes, bindings: map[string]nativeOriginHistoryBinding{}, documents: map[nativeOriginHistoryKey]*nativeHistoricalRelease{}, capture: func(containerOrigin) (*nativeHistoricalRelease, error) {
		calls++
		return nil, errors.New("capture should not run")
	}}
	defer func() { _ = c.closeOwned() }()
	if err := c.validate(f.payload.f.origin); err == nil || calls != 0 {
		t.Fatal("history budget opened another document", err, calls)
	}
	c.rawBytes = 0
	if err := c.validate(f.payload.f.origin); err == nil || calls != 0 {
		t.Fatal("budget failure recaptured a new original", err, calls)
	}
}

func TestNativeOriginAggregateFailedForeignAttachDoesNotCloseOriginalOwner(t *testing.T) {
	f := newNativeOriginAggregateFixture(t)
	otherDisk := nativeOriginAncestryRootFixture(t, 0, 0755)
	err := f.run(t, t.Context(), nativeOriginAncestryCurrentFixture, func(read *nativeOriginAggregateRead) error {
		owned := nativeOriginAggregateOwnedFixture(read.owner)
		err := withNativeOriginScopeRetainedAt(t.Context(), otherDisk, f.payload.f.selection, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(containerOrigin) error { return errors.New("foreign history must not be requested") }, read.owner, func(*nativeOriginScope) error { return errors.New("foreign use must not run") })
		if err == nil {
			t.Fatal("same owner attached to another source")
		}
		for _, file := range owned {
			if _, err := file.Stat(); err != nil {
				t.Fatal("failed foreign attachment closed original child", err)
			}
		}
		return read.Check(t.Context())
	})
	if err != nil {
		t.Fatal("foreign attach damaged original scope", err)
	}
}

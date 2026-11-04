package openshell

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	nativeOriginAggregateGenerations  = 4096
	nativeOriginAggregateDescriptors  = 32768
	nativeOriginAggregateBytes        = 128 << 20 //retained raw bytes; NOT total RSS
	nativeOriginAggregateHistoryBytes = 64 << 20
	nativeOriginAggregateTime         = 30 * time.Second //never extends original deadline
)

type nativeOriginHistoryKey struct{ qualification, helper string }
type nativeOriginHistoryBinding struct {
	origin   containerOrigin
	document *nativeHistoricalRelease
}
type nativeOriginHistoryCache struct {
	mu                    sync.Mutex
	lifetime              context.Context
	selection             nativeOriginCatalogSelection
	capture               func(containerOrigin) (*nativeHistoricalRelease, error) //actual wrapper fixes source/boundary
	bindings              map[string]nativeOriginHistoryBinding
	documents             map[nativeOriginHistoryKey]*nativeHistoricalRelease
	order                 []*nativeHistoricalRelease
	descriptors, rawBytes int
	sealed, closed        bool
	failure, closeErr     error
}

func (c *nativeOriginHistoryCache) usable() error {
	if c == nil || c.closed || c.capture == nil || c.lifetime == nil {
		return errors.New("original history cache expired/incomplete")
	}
	if c.failure != nil {
		return c.failure
	}
	return nativeHistoricalContextError(c.lifetime)
}
func (c *nativeOriginHistoryCache) validate(origin containerOrigin) (result error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		if result != nil {
			c.failure = result
		}
	}()
	if err := c.usable(); err != nil {
		return err
	}
	if origin.Space != c.selection.space || origin.DiskID != c.selection.diskID || origin.DiskUUID != c.selection.diskUUID {
		return errors.New("history origin differs from ORIGINAL native selection")
	}
	if binding, ok := c.bindings[origin.Incarnation]; ok {
		if binding.origin != origin {
			return errors.New("original incarnation changed its recorded history binding")
		}
		return binding.document.Check(c.lifetime)
	}
	if c.sealed || len(c.bindings) >= nativeOriginCatalogLimit {
		return errors.New("history cache refuses new/unbounded incarnation after original capture")
	}
	key := nativeOriginHistoryKey{origin.Qualification, origin.HelperSHA256}
	document, exists := c.documents[key]
	if !exists {
		// Reserve worst one-document raw+manifest bytes BEFORE acquisition. Source
		// has fixed six-FD production chain; no broad current mount restriction.
		if len(c.order) >= nativeOriginCatalogLimit || c.rawBytes > nativeOriginAggregateHistoryBytes-2*nativeHistoricalManifestLimit {
			return errors.New("aggregate historical-document budget")
		}
		var err error
		document, err = c.capture(origin)
		if document != nil {
			c.documents[key] = document
			c.order = append(c.order, document) //OWN before fallible checks
			c.descriptors += len(document.nodes)
			c.rawBytes += len(document.raw) + len(document.release.manifest)
		}
		if err != nil {
			return err
		}
		if document == nil {
			return errors.New("historical capture returned no original document")
		}
		if c.rawBytes > nativeOriginAggregateHistoryBytes || c.descriptors > nativeOriginAggregateDescriptors {
			return errors.New("aggregate historical source exceeds budget")
		}
	} else if err := document.Check(c.lifetime); err != nil {
		return err
	}
	if err := nativeOriginRecordRelease(origin, document.release); err != nil {
		return err
	}
	// Sharing is ONLY immutable document observation. Every actual origin keeps
	// its OWN exact seven-field binding; document.original origin is never edited.
	c.bindings[origin.Incarnation] = nativeOriginHistoryBinding{origin, document}
	return nil
}
func (c *nativeOriginHistoryCache) resolve(origin containerOrigin) (QualifiedRelease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return QualifiedRelease{}, err
	}
	binding, ok := c.bindings[origin.Incarnation]
	if !c.sealed || !ok || binding.origin != origin {
		c.failure = errors.New("resolver lacks SAME sealed original origin binding")
		return QualifiedRelease{}, c.failure
	}
	release, err := binding.document.Release(c.lifetime)
	if err != nil {
		c.failure = err
		return QualifiedRelease{}, err
	}
	if err := nativeOriginRecordRelease(origin, release); err != nil {
		c.failure = err
		return QualifiedRelease{}, err
	}
	return release, nil
}
func (c *nativeOriginHistoryCache) closeOwned() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	for i := len(c.order) - 1; i >= 0; i-- {
		c.closeErr = errors.Join(c.closeErr, c.order[i].Close())
	}
	return c.closeErr
}

type nativeOriginAggregateRecord struct {
	incarnation, currentGeneration string
	generations                    []nativeOriginAggregateGeneration
}
type nativeOriginAggregateGeneration struct{ generation, controllerGeneration, recordSHA256 string }
type nativeOriginAggregateView struct {
	selection  nativeOriginCatalogSelection
	prefix     nativeOriginAncestryView
	origins    []containerOrigin //ALL Boxes/incarnations; no keep/target filter
	records    []nativeOriginAggregateRecord
	namespaces nativeNamespaceScaffoldView  //bare remains neutral, missing explicit
	payloads   []nativeNamespacePayloadView //nonbare joined ORIGINAL origins+all records
}

func (nativeOriginAggregateView) String() string {
	return "native primary metadata union [not mode/ACK/reference authority]"
}

type nativeOriginAggregate struct {
	mu                                  sync.Mutex
	lifetime                            context.Context
	checkBoundary                       func() error //actual pure original boundary method; never storage callback
	history                             *nativeOriginHistoryCache
	source                              *nativeOriginScope         //BORROWED; scope wrapper alone closes
	catalog                             *nativeOriginCatalog       //same borrowed original or nil for qualified prefix
	entries                             []nativeOriginCatalogEntry //copied raw/scalars, same BORROWED original FDs
	scaffold                            *nativeNamespaceScaffolds  //OWN
	records                             []*nativeOriginRecords     //OWN
	payloads                            []*nativeNamespacePayload  //OWN
	namespaceView                       nativeNamespaceScaffoldView
	attached, ready, finalizing, closed bool
	failure, closeErr                   error
}
type nativeOriginAggregateRead struct{ owner *nativeOriginAggregate }

func (*nativeOriginAggregateRead) String() string {
	return "native aggregate read [borrowed metadata only]"
}
func (r *nativeOriginAggregateRead) Check(ctx context.Context) error {
	if r == nil || r.owner == nil {
		return errors.New("zero/expired aggregate read")
	}
	a := r.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.finalizing || !a.ready {
		return errors.New("aggregate read outside original callback")
	}
	_, err := a.check(ctx)
	if err != nil {
		err = errors.Join(err, nativeHistoricalContextError(a.lifetime), nativeHistoricalContextError(ctx))
		a.failure = err
	}
	return err
}
func (r *nativeOriginAggregateRead) Observation(ctx context.Context) (nativeOriginAggregateView, error) {
	if r == nil || r.owner == nil {
		return nativeOriginAggregateView{}, errors.New("zero/expired aggregate read")
	}
	a := r.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.finalizing || !a.ready {
		return nativeOriginAggregateView{}, errors.New("aggregate read outside original callback")
	}
	view, err := a.check(ctx)
	if err != nil {
		err = errors.Join(err, nativeHistoricalContextError(a.lifetime), nativeHistoricalContextError(ctx))
		a.failure = err
		return nativeOriginAggregateView{}, err
	}
	return view, nil
}

// Actual private wrapper. No public CLI/RPC, authority factory or positive
// Configure seam. The original receipt/box-effect/storage caller is still owed.
func withNativeOriginAggregate(ctx context.Context, verifyCurrent func(context.Context) error, use func(*nativeOriginAggregateRead) error) error {
	if ctx == nil || use == nil {
		return errors.New("aggregate lacks original lifetime/read callback")
	}
	boundary, err := captureNativeOriginObservationBoundary(verifyCurrent)
	if err != nil {
		return err
	}
	selected := boundary.snapshot
	return withNativeOriginAggregateAt(ctx, selected.DiskMount, nativeOriginCatalogSelection{selected.Space, selected.DiskID, selected.DiskUUID}, 0, 0, boundary.verify, boundary.check, func(origin containerOrigin) (*nativeHistoricalRelease, error) {
		return captureNativeHistoricalRelease(ctx, boundary, origin)
	}, use)
}

// Private ordinary-FS seam. Fixed production wrapper supplies disk, selection,
// original native boundary and installed-document capture; fixture roots do NOT
// create native authority. Missing sources are explicit, never empty fallback.
func withNativeOriginAggregateAt(ctx context.Context, disk string, selection nativeOriginCatalogSelection, uid, gid int, verifyCurrent func(context.Context) error, checkBoundary func() error, captureDocument func(containerOrigin) (*nativeHistoricalRelease, error), use func(*nativeOriginAggregateRead) error) (result error) {
	if ctx == nil || verifyCurrent == nil || checkBoundary == nil || captureDocument == nil || use == nil || selection.validate() != nil {
		return errors.New("aggregate lacks original scope/readers")
	}
	history := &nativeOriginHistoryCache{lifetime: ctx, selection: selection, capture: captureDocument, bindings: map[string]nativeOriginHistoryBinding{}, documents: map[nativeOriginHistoryKey]*nativeHistoricalRelease{}}
	a := &nativeOriginAggregate{lifetime: ctx, checkBoundary: checkBoundary, history: history}
	// Only pre-attach constructor failure uses this fallback. Attached child Close
	// belongs to scope's INNER defer, before catalog/ancestry Close, not this defer.
	defer func() {
		a.mu.Lock()
		unattached := !a.attached && !a.closed
		a.mu.Unlock()
		if unattached {
			result = errors.Join(result, a.closeOwned())
		}
		if result != nil {
			result = errors.Join(result, nativeHistoricalContextError(ctx))
		}
	}()
	return withNativeOriginScopeRetainedAt(ctx, disk, selection, uid, gid, verifyCurrent, history.validate, a, func(source *nativeOriginScope) error {
		if err := a.capture(source); err != nil {
			return err
		}
		return use(&nativeOriginAggregateRead{owner: a})
	})
}
func (a *nativeOriginAggregate) attachScope(source *nativeOriginScope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.attached || source == nil || a.source != nil {
		return errors.New("aggregate cannot replace original scope")
	}
	a.source = source
	a.attached = true
	return nil
}
func (a *nativeOriginAggregate) usable(ctx context.Context, reading bool) error {
	if a == nil || a.closed || !a.attached || a.source == nil || a.history == nil || a.checkBoundary == nil {
		return errors.New("aggregate expired/incomplete")
	}
	if a.failure != nil {
		return a.failure
	}
	if reading && (!a.ready || a.finalizing) {
		return errors.New("aggregate read outside original callback")
	}
	if err := nativeHistoricalContextError(a.lifetime); err != nil {
		return err
	}
	if err := nativeHistoricalContextError(ctx); err != nil {
		return err
	}
	return a.checkBoundary()
}
func (a *nativeOriginAggregate) capture(source *nativeOriginScope) (result error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if result != nil {
			a.failure = result
		}
	}()
	if source != a.source || a.ready || a.finalizing {
		return errors.New("aggregate original source already captured/replaced")
	}
	if err := a.usable(a.lifetime, false); err != nil {
		return err
	}
	source.mu.Lock()
	err := func() error {
		if err := source.usable(); err != nil {
			return err
		}
		a.catalog = source.catalog
		if a.catalog != nil {
			c := a.catalog
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed || !c.complete || c.failure != nil {
				return errors.Join(errors.New("aggregate original catalog unavailable"), c.failure)
			}
			for _, entry := range c.entries {
				copy := entry
				copy.raw = bytes.Clone(entry.raw)
				a.entries = append(a.entries, copy)
			}
		}
		return nil
	}()
	source.mu.Unlock()
	if err != nil {
		return err
	}
	a.history.mu.Lock()
	err = func() error {
		if err := a.history.usable(); err != nil {
			return err
		}
		if len(a.history.bindings) != len(a.entries) {
			return errors.New("installed history does not cover exact original origin collection")
		}
		for _, entry := range a.entries {
			binding, ok := a.history.bindings[entry.origin.Incarnation]
			if !ok || binding.origin != entry.origin || binding.document == nil {
				return errors.New("installed history lost exact original origin binding")
			}
		}
		a.history.sealed = true
		return nil
	}()
	a.history.mu.Unlock()
	if err != nil {
		return err
	}
	a.scaffold, err = observeNativeNamespaceScaffolds(a.lifetime, source)
	if err != nil {
		return err
	}
	a.scaffold.mu.Lock()
	a.namespaceView = a.scaffold.view
	a.namespaceView.entries = slices.Clone(a.scaffold.view.entries)
	a.scaffold.mu.Unlock()
	if err := a.budget(); err != nil {
		return err
	}
	byInc := map[string]*nativeOriginRecords{}
	for _, entry := range a.entries {
		records, e := captureNativeOriginRecords(a.lifetime, source, entry.origin.Incarnation, a.history.resolve)
		if e != nil {
			return e
		}
		a.records = append(a.records, records)
		byInc[entry.origin.Incarnation] = records
		if err := a.budget(); err != nil {
			return err
		}
	}
	for _, entry := range a.namespaceView.entries {
		if entry.bareMetadata {
			continue
		} //neutral ONLY; origin/receipt sources still dominate
		records := byInc[entry.incarnation]
		if records == nil {
			return errors.New("nonbare namespace payload lacks ORIGINAL native origin/records")
		}
		payload, e := observeNativeNamespacePayload(a.lifetime, source, a.scaffold, records)
		if e != nil {
			return e
		}
		a.payloads = append(a.payloads, payload)
		if err := a.budget(); err != nil {
			return err
		}
	}
	a.ready = true
	if err := a.external(a.lifetime); err != nil {
		return err
	}
	return a.local(a.lifetime)
}

// Owned children are private and a.mu serializes creation/check/Close; these
// static resource counts do not touch borrowed FDs or invoke any callback.
// Initial scaffold capture may retain 1+5*1024 nodes before this budget check.
// During later record/payload capture, one in-progress child can transiently
// add <=1024 rows/4096 FDs/32MiB before rejection. Cache reserves the
// worst document BEFORE acquisition.
func (a *nativeOriginAggregate) budget() error {
	descriptors, rawBytes, rows := 0, 0, 0
	a.history.mu.Lock()
	descriptors += a.history.descriptors
	rawBytes += a.history.rawBytes
	bindings := len(a.history.bindings)
	a.history.mu.Unlock()
	if len(a.entries) > nativeOriginCatalogLimit || bindings != len(a.entries) {
		return errors.New("aggregate origin/history collection budget/scope")
	}
	descriptors += len(a.source.ancestry.nodes)
	if a.catalog != nil {
		descriptors += 2 + 2*len(a.entries)
	}
	for _, entry := range a.entries {
		rawBytes += 2 * len(entry.raw)
	} //catalog retained+copied frozen raw
	if a.scaffold != nil {
		descriptors += len(a.scaffold.nodes)
	}
	for _, r := range a.records {
		rows += len(r.values)
		descriptors += 3 + 2*len(r.values)
		rawBytes += len(r.entry.raw) + len(r.current.raw)
		for _, value := range r.values {
			rawBytes += len(value.value.raw)
		}
	}
	for _, p := range a.payloads {
		descriptors += len(p.nodes)
		rawBytes += p.storedBytes
	}
	if rows > nativeOriginAggregateGenerations || descriptors > nativeOriginAggregateDescriptors || rawBytes > nativeOriginAggregateBytes {
		return errors.New("aggregate retained generation/descriptor/raw-byte budget exceeded")
	}
	return nil
}
func (a *nativeOriginAggregate) external(ctx context.Context) error {
	if err := a.usable(ctx, false); err != nil {
		return err
	}
	if err := a.source.Check(ctx); err != nil {
		return err
	}
	if err := a.scaffold.Check(ctx); err != nil {
		return err
	}
	for _, r := range a.records {
		if err := r.Check(ctx); err != nil {
			return err
		}
	}
	// Last external verifier remains REQUIRED and cannot be replaced by a local
	// success callback. It may revisit history/storage for earlier entries.
	return a.source.Check(ctx)
}

// Caller a.mu; source local access takes ONLY source.mu->catalog OR ancestry.
// Child Close is impossible until a.mu released. Individual record/scaffold/
// payload phases hold their own required source lifetime locks across FDs.
func (a *nativeOriginAggregate) sourceLocal(ctx context.Context) error {
	s := a.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return err
	}
	if s.catalog != a.catalog {
		return errors.New("aggregate original catalog object changed")
	}
	if err := s.finalAncestryMappings(ctx); err != nil {
		return err
	}
	if a.catalog != nil {
		c := a.catalog
		c.mu.Lock()
		err := func() error {
			if c.closed || !c.complete || c.failure != nil || len(c.entries) != len(a.entries) || c.selection != s.selection || c.rootID != s.original.control || c.collectionID != s.original.containers {
				return errors.Join(errors.New("aggregate catalog no longer bound original source"), c.failure)
			}
			if err := c.checkDirectories(); err != nil {
				return err
			}
			names, err := nativeOriginCatalogNames(ctx, c.collection)
			if err != nil || !slices.Equal(names, c.names) {
				return errors.Join(errors.New("aggregate origin roster changed"), err)
			}
			for i, expected := range a.entries {
				entry := &c.entries[i]
				if entry.origin != expected.origin || entry.file != expected.file || entry.directory != expected.directory || entry.fileID != expected.fileID || entry.directoryID != expected.directoryID || !bytes.Equal(entry.raw, expected.raw) {
					return errors.New("aggregate original origin entry replaced")
				}
				if err := c.checkMemberDirectory(entry); err != nil {
					return err
				}
				id, err := pinIdentity(int(entry.file.Fd()), c.uid, c.gid, false)
				if err != nil || id != entry.fileID {
					return errors.Join(errors.New("aggregate original origin FD changed"), err)
				}
				raw, err := readNativeCatalogOrigin(entry.file)
				if err != nil || !bytes.Equal(raw, expected.raw) {
					return errors.Join(errors.New("aggregate original origin bytes changed"), err)
				}
				if got, err := decodeNativeCatalogOrigin(raw, expected.origin.Incarnation, c.selection); err != nil || got != expected.origin {
					return errors.Join(errors.New("aggregate origin closed metadata changed"), err)
				}
				fd, err := unix.Openat2(int(entry.directory.Fd()), "origin.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
				if err != nil {
					return err
				}
				named, idErr := pinIdentity(fd, c.uid, c.gid, false)
				closeErr := unix.Close(fd)
				if idErr != nil || closeErr != nil || named != entry.fileID {
					return errors.Join(errors.New("aggregate original named origin changed"), idErr, closeErr)
				}
			}
			names, err = nativeOriginCatalogNames(ctx, c.collection)
			if err != nil || !slices.Equal(names, c.names) {
				return errors.Join(errors.New("aggregate origin roster changed across bytes"), err)
			}
			return c.checkDirectories()
		}()
		c.mu.Unlock()
		if err != nil {
			return err
		}
	} else if len(a.entries) != 0 {
		return errors.New("missing-prefix aggregate acquired origin entries")
	}
	return s.finalAncestryMappings(ctx)
}
func (a *nativeOriginAggregate) scaffoldLocal(ctx context.Context) error {
	p, s := a.scaffold, a.source
	p.mu.Lock()
	defer p.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return err
	}
	if p.closed || !p.complete || p.failure != nil || p.scope != s {
		return errors.Join(errors.New("aggregate original scaffold unavailable"), p.failure)
	}
	if p.view.prefix != a.namespaceView.prefix || p.view.collectionMissing != a.namespaceView.collectionMissing || !slices.Equal(p.view.entries, a.namespaceView.entries) {
		return errors.New("aggregate original scaffold view changed")
	}
	if p.control != nil {
		id, err := pinIdentity(int(p.control.Fd()), p.uid, p.gid, true)
		if err != nil || id != p.controlID {
			return errors.Join(errors.New("aggregate scaffold original control changed"), err)
		}
		if p.view.collectionMissing {
			var st unix.Stat_t
			if err := unix.Fstatat(int(p.control.Fd()), "namespace-pins", &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
				return errors.Join(errors.New("aggregate namespace collection absence changed/unavailable"), err)
			}
		}
	}
	for pass := 0; pass < 2; pass++ {
		for _, node := range p.nodes {
			if err := p.checkNode(node); err != nil {
				return err
			}
		}
		used := 0
		for _, roster := range p.rosters {
			if roster.node < 0 || roster.node >= len(p.nodes) {
				return errors.New("aggregate scaffold roster selector changed")
			}
			names, members, err := nativeNamespaceScaffoldNames(ctx, p.nodes[roster.node].file, &used)
			if err != nil {
				return err
			}
			if !slices.Equal(names, roster.names) || !nativeOriginAggregateMembersEqual(members, roster.members) {
				return errors.New("aggregate full original scaffold roster changed")
			}
		}
	}
	return s.finalAncestryMappings(ctx)
}
func nativeOriginAggregateMembersEqual(a, b map[string]namespaceInventoryMember) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || other != value {
			return false
		}
	}
	return true
}
func (a *nativeOriginAggregate) local(ctx context.Context) error {
	if err := a.usable(ctx, false); err != nil {
		return err
	}
	if !a.ready {
		return errors.New("aggregate incomplete")
	}
	if err := a.budget(); err != nil {
		return err
	}
	for pass := 0; pass < 2; pass++ {
		if err := a.sourceLocal(ctx); err != nil {
			return err
		}
		a.history.mu.Lock()
		err := func() error {
			if err := a.history.usable(); err != nil {
				return err
			}
			if !a.history.sealed || len(a.history.bindings) != len(a.entries) {
				return errors.New("aggregate original history binding set changed")
			}
			for _, entry := range a.entries {
				binding, ok := a.history.bindings[entry.origin.Incarnation]
				if !ok || binding.origin != entry.origin || binding.document == nil {
					return errors.New("aggregate original origin/history relation changed")
				}
			}
			for _, doc := range a.history.order {
				if err := doc.CheckOriginal(ctx); err != nil {
					return err
				}
			}
			return nil
		}()
		a.history.mu.Unlock()
		if err != nil {
			return err
		}
		for _, r := range a.records {
			r.mu.Lock()
			r.source.mu.Lock()
			err := r.checkLocal(ctx)
			r.source.mu.Unlock()
			r.mu.Unlock()
			if err != nil {
				return err
			}
		}
		if err := a.scaffoldLocal(ctx); err != nil {
			return err
		}
		for _, p := range a.payloads {
			if err := p.CheckOriginal(ctx); err != nil {
				return err
			}
		}
		if err := a.sourceLocal(ctx); err != nil {
			return err
		}
	}
	return a.usable(ctx, false) //pure original context/layout; NO external verifier
}
func (a *nativeOriginAggregate) check(ctx context.Context) (nativeOriginAggregateView, error) {
	if err := a.usable(ctx, true); err != nil {
		return nativeOriginAggregateView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginAggregateTime)
	defer cancel()
	if err := a.external(ctx); err != nil {
		return nativeOriginAggregateView{}, err
	}
	if err := a.local(ctx); err != nil {
		return nativeOriginAggregateView{}, err
	}
	// Cached copies only; original scope wrapper guarantees no Close while this
	// owner mutex is held. No user function/storage/history callback in this phase.
	view := nativeOriginAggregateView{selection: a.source.selection, prefix: cloneNativeOriginAncestryView(a.source.original), namespaces: a.namespaceView}
	view.namespaces.entries = slices.Clone(a.namespaceView.entries)
	for _, entry := range a.entries {
		view.origins = append(view.origins, entry.origin)
	}
	for _, r := range a.records {
		row := nativeOriginAggregateRecord{incarnation: r.entry.origin.Incarnation, currentGeneration: r.current.record.Generation}
		for _, value := range r.values {
			record := value.value.record
			row.generations = append(row.generations, nativeOriginAggregateGeneration{record.Generation, record.Launch.Binding.ControllerGeneration, recordIdentity(record)})
		}
		view.records = append(view.records, row)
	}
	for _, p := range a.payloads {
		copy := p.view
		copy.generations = slices.Clone(p.view.generations)
		view.payloads = append(view.payloads, copy)
	}
	if err := a.usable(ctx, true); err != nil {
		return nativeOriginAggregateView{}, err
	}
	return view, nil
}
func (a *nativeOriginAggregate) beginFinalization() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.usable(a.lifetime, true); err != nil {
		return err
	}
	a.finalizing = true //expire ALL escaped read handles BEFORE final external check
	return nil
}
func (a *nativeOriginAggregate) finalOriginal(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finalizing {
		return errors.New("aggregate missing original finalization phase")
	}
	err := a.local(ctx)
	if err != nil {
		a.failure = err
	}
	return err
}
func (a *nativeOriginAggregate) closeOwned() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	a.finalizing, a.closed = true, true
	for i := len(a.payloads) - 1; i >= 0; i-- {
		a.closeErr = errors.Join(a.closeErr, a.payloads[i].Close())
	}
	for i := len(a.records) - 1; i >= 0; i-- {
		a.closeErr = errors.Join(a.closeErr, a.records[i].Close())
	}
	if a.scaffold != nil {
		a.closeErr = errors.Join(a.closeErr, a.scaffold.Close())
	}
	if a.history != nil {
		a.closeErr = errors.Join(a.closeErr, a.history.closeOwned())
	}
	// Never Close source/catalog/ancestry or any copied borrowed entry handle.
	return a.closeErr
}

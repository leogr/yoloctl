package openshell

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
)

// Primary copied metadata ONLY: origin absence is not legacy/total native
// absence, an original receipt, CID/projection proof or an effect/cleanup ACK.
type nativeOriginScopeObservation struct {
	selection             nativeOriginCatalogSelection
	box                   string
	prefix                nativeOriginAncestryView
	origins               []containerOrigin // ALL same-box incarnations, including any keep
	namespacePinsObserved bool              // always false; sibling union remains separate
}

func (nativeOriginScopeObservation) String() string {
	return "native origin scope metadata [not classification]"
}
func (o nativeOriginScopeObservation) GoString() string { return o.String() }

type nativeOriginScope struct {
	mu                sync.Mutex
	lifetime          context.Context       // original callback execution, never a replacement context
	ancestry          *nativeOriginAncestry // BORROWED original; outer wrapper alone closes
	catalog           *nativeOriginCatalog  // assigned once before active; inner scope alone closes
	selection         nativeOriginCatalogSelection
	controlPath       string                   // fixed diskPath/runtime/openshell, not a caller-selected leaf
	original          nativeOriginAncestryView // copied original state/IDs; never rebaselined
	active, closed    bool
	failure, closeErr error
}

func (*nativeOriginScope) String() string     { return "native origin observation scope [not authority]" }
func (p *nativeOriginScope) GoString() string { return p.String() }

// Private ordinary-filesystem seam. The actual future native caller must derive
// trusted DiskMount/selection and independently admitted current UUID/owner/bind
// verification; this function grants none. Only the ORIGINAL captured ancestry
// supplies source continuity. No arbitrary relative path/FD/opener is accepted.
// The root-present catalog is opened only when all fixed prefixes are present.
// Every outcome expires this scope; only its original catalog is closed here.
func withNativeOriginScopeAt(ctx context.Context, diskPath string, selection nativeOriginCatalogSelection, uid, gid int, verifyCurrent func(context.Context) error, validateHistorical func(containerOrigin) error, use func(*nativeOriginScope) error) error {
	return withNativeOriginScopeRetainedAt(ctx, diskPath, selection, uid, gid, verifyCurrent, validateHistorical, nil, use)
}

// The optional PRIVATE typed owner adds retained child finalization only. It
// never replaces mandatory source.Check or creates admission/cleanup authority.
// Existing origin-only callers pass nil and preserve their original behavior.
func withNativeOriginScopeRetainedAt(ctx context.Context, diskPath string, selection nativeOriginCatalogSelection, uid, gid int, verifyCurrent func(context.Context) error, validateHistorical func(containerOrigin) error, owned *nativeOriginAggregate, use func(*nativeOriginScope) error) (result error) {
	if ctx == nil || verifyCurrent == nil || validateHistorical == nil || use == nil {
		return errors.New("native origin scope lacks original context/current/history/callback")
	}
	if err := selection.validate(); err != nil {
		return err
	}
	// Own ONE original capture here. The standalone ancestry callback wrapper
	// deliberately has its own final Check; invoking it outside this composition
	// would run external verifiers after the catalog below has already closed.
	ancestry, err := captureNativeOriginAncestryAt(ctx, diskPath, uid, gid, verifyCurrent)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, ancestry.Close()) }()
	if err := ancestry.Check(ctx); err != nil {
		return err
	}
	original, err := ancestry.View()
	if err != nil {
		return err
	}
	p := &nativeOriginScope{lifetime: ctx, ancestry: ancestry, selection: selection, controlPath: filepath.Join(diskPath, "runtime", "openshell"), original: original}
	// LIFO: close catalog first, then this function's sole owned ancestry.
	// After final p.Check only these Close operations run, never a verifier.
	defer func() { result = errors.Join(result, p.close()) }()
	if owned != nil {
		if err := owned.attachScope(p); err != nil {
			return err // NEVER close an owner still attached to another scope
		}
		// LIFO: expire/close children before catalog/ancestry. Registered
		// before catalog construction, including historical bootstrap errors.
		defer func() { result = errors.Join(result, owned.closeOwned()) }()
	}
	if original.state == nativeOriginAncestryPresent {
		p.catalog, err = observeNativeOriginCatalogAt(ctx, p.controlPath, selection, uid, gid, func(check context.Context) error { return ancestry.Check(check) }, validateHistorical)
		if err != nil {
			return err
		} // constructor owns failed partial handles
	}
	p.active = true
	if err := p.Check(ctx); err != nil {
		return err
	}
	if err := use(p); err != nil {
		return err
	}
	if owned != nil {
		// No escaped read can start another callback after final local work.
		if err := owned.beginFinalization(); err != nil {
			return err
		}
	}
	if err := p.Check(ctx); err != nil {
		return err
	}
	if owned != nil {
		return owned.finalOriginal(ctx) //LOCAL only; ALL children still live
	}
	return nil
}

func sameNativeOriginAncestryView(a, b nativeOriginAncestryView) bool {
	return a.state == b.state && a.missingComponent == b.missingComponent && a.disk == b.disk && a.control == b.control && a.containers == b.containers && a.namespacePinsObserved == b.namespacePinsObserved && slices.Equal(a.presentPrefix, b.presentPrefix)
}
func cloneNativeOriginAncestryView(v nativeOriginAncestryView) nativeOriginAncestryView {
	v.presentPrefix = slices.Clone(v.presentPrefix)
	return v
}
func (p *nativeOriginScope) usable() error {
	if p == nil || !p.active || p.closed || p.lifetime == nil || p.ancestry == nil {
		return errors.New("native origin scope expired/closed/unavailable")
	}
	if p.failure != nil {
		return errors.Join(errors.New("native origin scope failed"), p.failure)
	}
	if err := p.lifetime.Err(); err != nil {
		return errors.Join(errors.New("native origin original execution expired"), err)
	}
	if err := p.selection.validate(); err != nil {
		return err
	}
	switch p.original.state {
	case nativeOriginAncestryPresent:
		if p.catalog == nil {
			return errors.New("present native origin scope lost original catalog")
		}
	case nativeOriginAncestryMissingRuntime, nativeOriginAncestryMissingControl, nativeOriginAncestryMissingContainers:
		if p.catalog != nil {
			return errors.New("missing-prefix origin scope acquired unexpected catalog")
		}
	default:
		return errors.New("native origin scope has unknown original prefix")
	}
	if p.original.namespacePinsObserved {
		return errors.New("origin-only scope acquired unsupported sibling claim")
	}
	return nil
}

func (p *nativeOriginScope) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil native origin scope")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.usable(); err != nil {
		return err
	}
	if ctx == nil {
		p.failure = errors.New("missing native origin check context")
		return p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p.failure = p.check(ctx)
	return p.failure
}

// p.mu serializes scope use/expiry. No ancestry mutex is held across a catalog
// call. Catalog.Check's independent verifier may lock ancestry; it cannot call
// this scope or acquire p.mu. This is scope -> catalog -> ancestry, no inverse.
func (p *nativeOriginScope) check(ctx context.Context) error {
	if err := p.usable(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.ancestry.Check(ctx); err != nil {
		return err
	}
	before, err := p.ancestry.View()
	if err != nil {
		return err
	}
	if !sameNativeOriginAncestryView(p.original, before) {
		return errors.New("native origin scope changed original ancestry")
	}
	if p.catalog != nil {
		if err := p.bindCatalog(before); err != nil {
			return err
		}
		if err := p.catalog.Check(ctx); err != nil {
			return err
		}
		if err := p.bindCatalog(before); err != nil {
			return err
		}
	}
	if err := p.ancestry.Check(ctx); err != nil {
		return err
	}
	after, err := p.ancestry.View()
	if err != nil {
		return err
	}
	if !sameNativeOriginAncestryView(p.original, after) {
		return errors.New("native origin ancestry changed across catalog observation")
	}
	if p.catalog != nil {
		// The trailing ancestry check itself calls the external storage
		// verifier. Revalidate catalog members after that interval too.
		if err := p.catalog.Check(ctx); err != nil {
			return err
		}
	}
	// Finish with mapping-only ancestry checks: no further external callback
	// may mutate a catalog after its last member validation. These are still
	// bracketed observations, never an atomic filesystem snapshot.
	if err := p.finalAncestryMappings(ctx); err != nil {
		return err
	}
	after, err = p.ancestry.View()
	if err != nil {
		return err
	}
	if !sameNativeOriginAncestryView(p.original, after) {
		return errors.New("native origin final ancestry changed")
	}
	if p.catalog != nil {
		if err := p.bindCatalog(after); err != nil {
			return err
		}
	}
	if err := p.usable(); err != nil {
		return err
	}
	return ctx.Err()
}

// Scope mutex is held. Take only ancestry here, without calling any storage
// verifier or catalog method under its mutex. The outer owner still closes it.
func (p *nativeOriginScope) finalAncestryMappings(ctx context.Context) error {
	a := p.ancestry
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.complete || a.failure != nil {
		return errors.Join(errors.New("original ancestry unavailable at completion"), a.failure)
	}
	a.failure = func() error {
		if err := a.usable(); err != nil {
			return err
		}
		if err := a.checkNodes(ctx); err != nil {
			return err
		}
		if err := a.checkMissing(); err != nil {
			return err
		}
		if err := a.checkNodes(ctx); err != nil {
			return err
		}
		if err := a.usable(); err != nil {
			return err
		}
		return ctx.Err()
	}()
	return a.failure
}

// Pure original-object metadata comparison under catalog.mu. In particular
// this never calls ancestry.Check while separately holding ancestry.mu, never
// opens a replacement catalog or assigns new matching identities to p.original.
func (p *nativeOriginScope) bindCatalog(prefix nativeOriginAncestryView) error {
	c := p.catalog
	if c == nil || prefix.state != nativeOriginAncestryPresent {
		return errors.New("native catalog join lacks original present ancestry")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.complete || c.failure != nil || c.root == nil || c.collection == nil || c.selection != p.selection || c.rootPath != p.controlPath || c.rootID != p.original.control || c.collectionID != p.original.containers || c.rootID != prefix.control || c.collectionID != prefix.containers {
		return errors.Join(errors.New("native catalog differs from SAME original ancestry/selection"), c.failure)
	}
	return nil
}

// Observation brackets the copied data with original checks. No keep parameter
// can hide another same-box origin. Missing-prefix observations have explicit
// prefix state and empty origin metadata ONLY; no catalog opener is attempted.
func (p *nativeOriginScope) Observation(box string) (nativeOriginScopeObservation, error) {
	if p == nil {
		return nativeOriginScopeObservation{}, errors.New("nil native origin scope")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.usable(); err != nil {
		return nativeOriginScopeObservation{}, err
	}
	if err := layout.ValidateName("agentbox", box); err != nil {
		return nativeOriginScopeObservation{}, err
	}
	checkContext, cancel := context.WithTimeout(p.lifetime, nativeOriginReadBudget)
	defer cancel()
	if err := p.check(checkContext); err != nil {
		p.failure = err
		return nativeOriginScopeObservation{}, err
	}
	origins := []containerOrigin{}
	if p.catalog != nil {
		var err error
		origins, err = p.catalog.ForBox(box)
		if err != nil {
			p.failure = err
			return nativeOriginScopeObservation{}, err
		}
	}
	observation := nativeOriginScopeObservation{selection: p.selection, box: box, prefix: cloneNativeOriginAncestryView(p.original), origins: origins, namespacePinsObserved: false}
	if err := p.check(checkContext); err != nil {
		p.failure = err
		return nativeOriginScopeObservation{}, err
	}
	return observation, nil
}

// This scope owns catalog Close; its creator alone closes the borrowed ancestry
// in the outer lexical defer. Mark expired BEFORE releasing anything.
func (p *nativeOriginScope) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.active, p.closed = false, true
	if p.catalog != nil {
		p.closeErr = p.catalog.Close()
	}
	return p.closeErr
}

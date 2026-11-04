package openshell

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// NativeOriginIdentity copies ONLY the seven actual nonsecret stored fields.
// There is no recorded helper_protocol in origin.json. Possessing these values
// cannot qualify a release/protocol, reconstruct custody or authorize effects.
type NativeOriginIdentity struct{ Space, DiskID, DiskUUID, Box, Incarnation, Qualification, HelperSHA256 string }

func (NativeOriginIdentity) String() string     { return "native origin identity [metadata only]" }
func (i NativeOriginIdentity) GoString() string { return i.String() }

type NativeOriginPrefixState string

const (
	NativeOriginMissingRuntime    NativeOriginPrefixState = "missing-runtime"
	NativeOriginMissingControl    NativeOriginPrefixState = "missing-openshell"
	NativeOriginMissingContainers NativeOriginPrefixState = "missing-containers"
	NativeOriginPrefixPresent     NativeOriginPrefixState = "present"
)

// NativeOriginObservation is copied primary metadata, never legacy mode,
// total native absence, original receipt/CID/projection/consumer proof or ACK.
type NativeOriginObservation struct {
	Space, DiskID, DiskUUID, ControllerGeneration, Box string
	PrefixState                                        NativeOriginPrefixState
	MissingComponent                                   string
	PresentPrefix                                      []string
	Origins                                            []NativeOriginIdentity // all same-box incarnations, including keep
	NamespacePinsObserved                              bool                   // false; sibling namespace/orphan union is separate
}

func (NativeOriginObservation) String() string {
	return "native origin observation [not classification]"
}
func (o NativeOriginObservation) GoString() string { return o.String() }

type nativeOriginObservationBoundary struct {
	original      *spaceenv.Context
	snapshot      spaceenv.Context
	verifyCurrent func(context.Context) error
}

func captureNativeOriginObservationBoundary(verifyCurrent func(context.Context) error) (*nativeOriginObservationBoundary, error) {
	if err := nativePrecreateBoundary(verifyCurrent); err != nil {
		return nil, err
	}
	c := spaceenv.Current()
	if c == nil {
		return nil, errors.New("native origin trusted context unavailable")
	}
	snapshot := *c
	snapshot.Mounts = slices.Clone(c.Mounts)
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	if layout.ValidateName("space", snapshot.Space) != nil || snapshot.DiskMount != layout.AccountDiskMount(snapshot.DiskID) {
		return nil, errors.New("native origin trusted disk/layout selection mismatch")
	}
	b := &nativeOriginObservationBoundary{original: c, snapshot: snapshot, verifyCurrent: verifyCurrent}
	if err := b.check(); err != nil {
		return nil, err
	}
	return b, nil
}
func (b *nativeOriginObservationBoundary) check() error {
	if b == nil || b.original == nil {
		return errors.New("native origin boundary unavailable")
	}
	if err := nativePrecreateBoundary(b.verifyCurrent); err != nil {
		return err
	}
	current := spaceenv.Current()
	if current != b.original || !reflect.DeepEqual(*current, b.snapshot) {
		return errors.New("native origin ORIGINAL trusted context changed")
	}
	root, err := layout.OpenShellControlDir()
	if err != nil || root != filepath.Join(b.snapshot.DiskMount, "runtime", "openshell") {
		return errors.Join(errors.New("native origin fixed control layout changed"), err)
	}
	return nil // mapping/context check only; never an external callback
}
func (b *nativeOriginObservationBoundary) verify(ctx context.Context) error {
	if ctx == nil {
		return errors.New("native origin storage check context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.check(); err != nil {
		return err
	}
	if err := b.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := b.check(); err != nil {
		return err
	}
	return ctx.Err()
}
func copyNativeOriginIdentity(o containerOrigin) NativeOriginIdentity {
	return NativeOriginIdentity(o)
}

// NativeOriginRead is borrowed ONLY for the original callback. It exposes no
// FD/opener/dup/Close, native classification, mutation or recovery interface.
type NativeOriginRead struct {
	mu       sync.Mutex
	original *nativeOriginScope
	boundary *nativeOriginObservationBoundary
	lifetime context.Context
	box      string
	active   bool
	failure  error
}

func (*NativeOriginRead) String() string     { return "native origin read [borrowed metadata only]" }
func (s *NativeOriginRead) GoString() string { return s.String() }
func (s *NativeOriginRead) check(ctx context.Context) error {
	if s == nil || !s.active || s.original == nil || s.boundary == nil || s.lifetime == nil {
		return errors.New("native origin read expired/incomplete")
	}
	if s.failure != nil {
		return s.failure
	}
	if err := s.lifetime.Err(); err != nil {
		return errors.Join(errors.New("native origin original execution expired"), err)
	}
	if ctx == nil {
		return errors.New("native origin read context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.boundary.check(); err != nil {
		return err
	}
	if err := s.original.Check(ctx); err != nil {
		return err
	}
	if err := s.boundary.check(); err != nil {
		return err
	}
	return ctx.Err()
}
func (s *NativeOriginRead) Check(ctx context.Context) error {
	if s == nil {
		return errors.New("nil native origin read")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.check(ctx)
	if err != nil {
		s.failure = err
	}
	return err
}
func (s *NativeOriginRead) Observation(ctx context.Context) (NativeOriginObservation, error) {
	if s == nil {
		return NativeOriginObservation{}, errors.New("nil native origin read")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(err error) (NativeOriginObservation, error) {
		s.failure = err
		return NativeOriginObservation{}, err
	}
	if err := s.check(ctx); err != nil {
		return fail(err)
	}
	raw, err := s.original.Observation(s.box)
	if err != nil {
		return fail(err)
	}
	c := s.boundary.snapshot
	if raw.box != s.box || raw.selection != (nativeOriginCatalogSelection{c.Space, c.DiskID, c.DiskUUID}) || raw.namespacePinsObserved || raw.prefix.namespacePinsObserved {
		return fail(errors.New("native origin copied scope differs from ORIGINAL selection"))
	}
	var state NativeOriginPrefixState
	switch raw.prefix.state {
	case nativeOriginAncestryMissingRuntime:
		state = NativeOriginMissingRuntime
	case nativeOriginAncestryMissingControl:
		state = NativeOriginMissingControl
	case nativeOriginAncestryMissingContainers:
		state = NativeOriginMissingContainers
	case nativeOriginAncestryPresent:
		state = NativeOriginPrefixPresent
	default:
		return fail(errors.New("native origin prefix state unavailable"))
	}
	observation := NativeOriginObservation{Space: c.Space, DiskID: c.DiskID, DiskUUID: c.DiskUUID, ControllerGeneration: c.Generation, Box: s.box, PrefixState: state, MissingComponent: raw.prefix.missingComponent, PresentPrefix: slices.Clone(raw.prefix.presentPrefix), Origins: []NativeOriginIdentity{}, NamespacePinsObserved: false}
	for _, origin := range raw.origins {
		if origin.Space != c.Space || origin.DiskID != c.DiskID || origin.DiskUUID != c.DiskUUID || origin.Box != s.box {
			return fail(errors.New("native origin member differs from ORIGINAL expected box/disk"))
		}
		observation.Origins = append(observation.Origins, copyNativeOriginIdentity(origin))
	}
	if err := s.check(ctx); err != nil {
		return fail(err)
	}
	return observation, nil
}

// WithNativeOriginObservation derives ALL paths/native selection internally.
// expectedBox and verifyCurrent MUST belong to the ACTUAL original caller's
// independently admitted receipt/storage/box-effect scope; their types do not
// prove that authority. validateHistorical must independently qualify each
// recorded qualification/helper pair AND its supported protocol/architecture,
// without substituting current helper/source. It receives copied metadata only.
// No public CLI/RPC caller, original-receipt factory or native activation is
// installed here. This wrapper adds NO owner/Close beyond the private callback.
func WithNativeOriginObservation(ctx context.Context, expectedBox string, verifyCurrent func(context.Context) error, validateHistorical func(NativeOriginIdentity) error, use func(*NativeOriginRead) error) error {
	if ctx == nil || validateHistorical == nil || use == nil {
		return errors.New("native origin observation requires original context/history/read callback")
	}
	if err := layout.ValidateName("agentbox", expectedBox); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	boundary, err := captureNativeOriginObservationBoundary(verifyCurrent)
	if err != nil {
		return err
	}
	c := boundary.snapshot
	history := func(origin containerOrigin) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := boundary.check(); err != nil {
			return err
		}
		if err := validateHistorical(copyNativeOriginIdentity(origin)); err != nil {
			return err
		}
		if err := boundary.check(); err != nil {
			return err
		}
		return ctx.Err()
	}
	return withNativeOriginScopeAt(ctx, c.DiskMount, nativeOriginCatalogSelection{c.Space, c.DiskID, c.DiskUUID}, 0, 0, boundary.verify, history, func(original *nativeOriginScope) error {
		read := &NativeOriginRead{original: original, boundary: boundary, lifetime: ctx, box: expectedBox, active: true}
		defer func() {
			read.mu.Lock()
			defer read.mu.Unlock()
			read.active = false
			read.original = nil
			read.boundary = nil
			read.lifetime = nil
		}()
		if err := read.Check(ctx); err != nil {
			return err
		}
		if err := use(read); err != nil {
			return err
		}
		return read.Check(ctx)
	})
}

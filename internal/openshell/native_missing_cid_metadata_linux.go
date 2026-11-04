package openshell

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
)

type MissingCIDNativeMetadata struct {
	Box                     string
	Space, DiskID, DiskUUID string
	Prefix                  NativeOriginPrefixState
	Origins                 []NativeOriginIdentity //ALL original Boxes/incarnations, including keep
	BareScaffolds           int                    //neutral metadata only; no Box or ACK inferred
	PayloadIncarnations     []string               //already joined all originals/records/history
}

// Borrowed PRIMARY read only. No public constructor from copied metadata.
type MissingCIDNativeRead struct {
	mu       sync.Mutex
	original *nativeOriginAggregateRead
	box      string
	active   bool
}

func WithMissingCIDNativeMetadata(ctx context.Context, box string, verifyCurrent func(context.Context) error, use func(*MissingCIDNativeRead) error) error {
	if ctx == nil || layout.ValidateName("agentbox", box) != nil || use == nil {
		return errors.New("missing-CID metadata lacks actual selected scope")
	}
	return withNativeOriginAggregate(ctx, verifyCurrent, func(original *nativeOriginAggregateRead) error {
		read := &MissingCIDNativeRead{original: original, box: box, active: true}
		defer func() { read.mu.Lock(); read.active = false; read.mu.Unlock() }()
		return use(read)
	}) //actual mandatory source finalizer/owned Close remains unchanged
}
func (r *MissingCIDNativeRead) Observation(ctx context.Context) (MissingCIDNativeMetadata, error) {
	if r == nil {
		return MissingCIDNativeMetadata{}, errors.New("nil native metadata read")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.original == nil {
		return MissingCIDNativeMetadata{}, errors.New("native metadata read outside original lifetime")
	}
	original, err := r.original.Observation(ctx)
	if err != nil {
		return MissingCIDNativeMetadata{}, err
	}
	view := MissingCIDNativeMetadata{Box: r.box, Space: original.selection.space, DiskID: original.selection.diskID, DiskUUID: original.selection.diskUUID}
	switch original.prefix.state {
	case nativeOriginAncestryMissingRuntime:
		view.Prefix = NativeOriginMissingRuntime
	case nativeOriginAncestryMissingControl:
		view.Prefix = NativeOriginMissingControl
	case nativeOriginAncestryMissingContainers:
		view.Prefix = NativeOriginMissingContainers
	case nativeOriginAncestryPresent:
		view.Prefix = NativeOriginPrefixPresent
	default:
		return MissingCIDNativeMetadata{}, errors.New("unknown original native prefix")
	}
	for _, origin := range original.origins {
		view.Origins = append(view.Origins, copyNativeOriginIdentity(origin))
	}
	for _, entry := range original.namespaces.entries {
		if entry.bareMetadata {
			view.BareScaffolds++
		}
	}
	for _, payload := range original.payloads {
		view.PayloadIncarnations = append(view.PayloadIncarnations, payload.origin.Incarnation)
	}
	view.PayloadIncarnations = slices.Clone(view.PayloadIncarnations)
	return view, nil //copied metadata, NOT eligibility/receipt/ACK/cleanup proof
}

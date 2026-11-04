package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/operator"
)

// NativeOperatorSelection is copied OCI metadata, never live mount, grant,
// registration, consumer, Stop/rm ACK or deletion authority. Disabled means
// only that this particular CID has no recorded operator projection; older
// registrations for the same box must still be independently inventoried.
type NativeOperatorSelection struct {
	ContainerID string
	Box         string
	Enabled     bool
	Endpoint    operator.Endpoint
	Mount       NativeMount
}

func (NativeOperatorSelection) String() string     { return "native operator metadata [not authority]" }
func (s NativeOperatorSelection) GoString() string { return s.String() }

// NativeOperatorProjection retains a successful fixed-CID observation. A zero
// value cannot fabricate a successful disabled selection.
type NativeOperatorProjection struct {
	selection NativeOperatorSelection
	captured  bool
}

func (*NativeOperatorProjection) String() string     { return "native operator projection [not authority]" }
func (p *NativeOperatorProjection) GoString() string { return p.String() }

func (p *NativeOperatorProjection) Selection() (NativeOperatorSelection, error) {
	if p == nil || !p.captured || !nativeMountCID(p.selection.ContainerID) || layout.ValidateName("agentbox", p.selection.Box) != nil {
		return NativeOperatorSelection{}, errors.New("native operator projection lacks captured identity")
	}
	result := p.selection
	result.Mount.Options = append([]string(nil), result.Mount.Options...)
	return result, nil
}

// NativeOperatorProjection reads the actual native containerd label and OCI
// Spec twice for one original full CID in the client's configured namespace.
// Docker-compatible Mounts, mutable nerdctl mount labels and desired config are
// never substitutes. The caller independently binds box/CID/storage/admission;
// two matching reads do not establish kernel enforcement or lifetime ownership.
func (c *Client) NativeOperatorProjection(ctx context.Context, cid, box string) (*NativeOperatorProjection, error) {
	if c == nil || !nativeMountCID(cid) || layout.ValidateName("agentbox", box) != nil {
		return nil, errors.New("native operator projection requires original full CID and box")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	remaining := 16 << 20
	read := func() (NativeOperatorSelection, NativeMountEntry, error) {
		raw, err := c.nativeMountRead(ctx, &remaining, 8<<20, "inspect", "--mode", "native", cid)
		if err != nil {
			return NativeOperatorSelection{}, NativeMountEntry{}, err
		}
		defer clear(raw)
		return parseNativeOperatorProjection(raw, cid, box)
	}
	first, mounts, err := read()
	if err != nil {
		return nil, err
	}
	second, repeated, err := read()
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(mounts, repeated) {
		return nil, errors.New("native operator projection changed during observation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &NativeOperatorProjection{selection: second, captured: true}, nil
}

func parseNativeOperatorProjection(raw []byte, cid, box string) (NativeOperatorSelection, NativeMountEntry, error) {
	fail := func() (NativeOperatorSelection, NativeMountEntry, error) {
		return NativeOperatorSelection{}, NativeMountEntry{}, errors.New("native operator projection has inconsistent label/OCI bind evidence")
	}
	// Reuse the bounded, duplicate-free native OCI parser, including exact ID,
	// Spec/root/mount fields. Parse Labels from that same single native object.
	entries, err := parseNativeMountEntries(raw, []string{cid})
	if err != nil || layout.ValidateName("agentbox", box) != nil {
		return fail()
	}
	var objects []json.RawMessage
	if json.Unmarshal(raw, &objects) != nil || len(objects) != 1 {
		return fail()
	}
	object, err := nativeMountObject(objects[0], "ID", "Spec", "Labels")
	if err != nil {
		return fail()
	}
	var incarnation string
	if labels, exists := object["Labels"]; exists && !bytes.Equal(bytes.TrimSpace(labels), []byte("null")) {
		values, err := nativeMountObject(labels, operator.IncarnationLabel)
		if err != nil {
			return fail()
		}
		if value, exists := values[operator.IncarnationLabel]; exists {
			incarnation, err = nativeMountString(value, false)
			if err != nil {
				return fail()
			}
		}
	}
	selection := NativeOperatorSelection{ContainerID: cid, Box: box}
	if incarnation != "" {
		selection.Endpoint = operator.Endpoint{Box: box, Incarnation: incarnation}
		if selection.Endpoint.Validate() != nil {
			return fail()
		}
		selection.Enabled = true
	}
	count := 0
	for _, mount := range entries[0].Mounts {
		clean := path.Clean(mount.Destination)
		// Refuse duplicate targets, projected children and noncanonical
		// target aliases. Ancestor mounts are ordinary OCI root layout;
		// this metadata reader does not prove which bind is live beneath
		// any overlay. That requires the independent actual kernel proof.
		related := clean == operator.Mount || strings.HasPrefix(clean, operator.Mount+"/")
		if !related {
			continue
		}
		// nerdctl 2.3.5 ProcessFlagV leaves Linux DefaultMountType
		// ("none") when it appends an implicit rbind for -v src:dst:ro.
		// Explicit bind/rbind opts produce type "bind". In both profiles
		// the actual bind flag AND ro are required below, independently.
		if !selection.Enabled || mount.Destination != operator.Mount || count != 0 || mount.Source != selection.Endpoint.Directory() || (mount.Type != "bind" && mount.Type != "none") {
			return fail()
		}
		readOnly, bind := 0, 0
		for _, option := range mount.Options {
			switch option {
			case "ro":
				readOnly++
			case "bind", "rbind":
				bind++
			case "rw":
				return fail()
			}
		}
		if readOnly != 1 || bind != 1 {
			return fail()
		}
		selection.Mount = mount
		count++
	}
	if selection.Enabled && count != 1 {
		return fail()
	}
	return selection, entries[0], nil
}

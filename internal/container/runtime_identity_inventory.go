package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
)

type runtimeContainerIdentity struct{ id, name string }

// RuntimeIdentityInventory is a complete bracketed ID/name observation in this
// client's configured containerd namespace. It is not a global process census,
// native/legacy classification, registration/origin/receipt proof or effect ACK.
// In particular no matching CID does not authorize generic endpoint cleanup.
// The zero value cannot claim a successful empty observation.
type RuntimeIdentityInventory struct {
	entries  []runtimeContainerIdentity
	captured bool
}

func (*RuntimeIdentityInventory) String() string     { return "runtime identity inventory [not authority]" }
func (p *RuntimeIdentityInventory) GoString() string { return p.String() }

// BoxCandidates returns copied full CIDs matching the actual recorded name or
// a numeric-ID lookup prefix. A disabled/missing label cannot hide a container.
// Empty means only current absence from THIS successful bounded observation;
// later destructive use requires fresh storage/admission and other inventories.
func (p *RuntimeIdentityInventory) BoxCandidates(box string) ([]string, error) {
	if p == nil || !p.captured || layout.ValidateName("agentbox", box) != nil {
		return nil, errors.New("runtime identity inventory or selected box is unavailable")
	}
	result := []string{}
	for _, entry := range p.entries {
		if entry.name == box || strings.HasPrefix(entry.id, box) {
			result = append(result, entry.id)
		}
	}
	return result, nil
}

// RuntimeIdentityInventory never uses best-effort RuntimeStates or a failed
// inspect-by-name result as absence. Three successful unfiltered full-ID lists
// bracket two complete fixed-CID inspection passes. Any missing/extra/repeated
// identity, command failure or changed ID/name refuses the entire observation.
// Matching repeats detect changes; they do not make the read globally atomic.
func (c *Client) RuntimeIdentityInventory(ctx context.Context) (*RuntimeIdentityInventory, error) {
	if c == nil {
		return nil, errors.New("runtime identity inventory requires a client")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	remaining := 32 << 20
	list := func() ([]string, error) {
		raw, err := c.runtimeIdentityRead(ctx, &remaining, 128<<10, "ps", "-a", "-q", "--no-trunc")
		if err != nil {
			return nil, err
		}
		defer clear(raw)
		ids := strings.Fields(string(raw))
		if len(ids) > nativeMountInventoryLimit {
			return nil, errors.New("runtime identity inventory exceeds bound")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !nativeMountCID(id) || seen[id] {
				return nil, errors.New("runtime identity inventory requires unique full canonical CIDs")
			}
			seen[id] = true
		}
		sort.Strings(ids)
		return ids, nil
	}
	ids, err := list()
	if err != nil {
		return nil, err
	}
	read := func() ([]runtimeContainerIdentity, error) {
		result := []runtimeContainerIdentity{}
		for start := 0; start < len(ids); start += 32 {
			end := min(start+32, len(ids))
			args := append([]string{"inspect", "--mode", "dockercompat"}, ids[start:end]...)
			raw, err := c.runtimeIdentityRead(ctx, &remaining, 8<<20, args...)
			if err != nil {
				return nil, err
			}
			entries, err := decodeRuntimeIdentityBatch(raw, ids[start:end])
			clear(raw)
			if err != nil {
				return nil, err
			}
			result = append(result, entries...)
		}
		return result, nil
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	middle, err := list()
	if err != nil {
		return nil, err
	}
	if !slices.Equal(ids, middle) {
		return nil, errors.New("runtime identity inventory changed container set")
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	last, err := list()
	if err != nil {
		return nil, err
	}
	if !slices.Equal(ids, last) || !reflect.DeepEqual(first, second) {
		return nil, errors.New("runtime identity inventory changed container set or names")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &RuntimeIdentityInventory{entries: second, captured: true}, nil
}

// The command deadline bounds execution; WaitDelay separately bounds pipe
// settlement when a descendant inherits output after the adapter exits or is
// cancelled. Neither a valid prefix nor a successful direct exit repairs an
// incomplete read. Existing best-effort/OCI transports are unchanged.
func (c *Client) runtimeIdentityRead(ctx context.Context, remaining *int, limit int, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if remaining == nil || *remaining <= 0 || limit <= 0 {
		return nil, errors.New("runtime identity inventory exhausted output budget")
	}
	output := &nativeMountOutput{limit: min(limit, *remaining)}
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout, cmd.Stderr = output, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("runtime identity inventory command/pipe settlement failed: %w", errors.Join(err, ctx.Err()))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*remaining -= output.buffer.Len()
	return output.buffer.Bytes(), nil
}

func decodeRuntimeIdentityBatch(raw []byte, ids []string) ([]runtimeContainerIdentity, error) {
	fail := func() ([]runtimeContainerIdentity, error) {
		return nil, errors.New("runtime identity inventory has incomplete or ambiguous ID/name evidence")
	}
	var rows []json.RawMessage
	if len(ids) == 0 || len(ids) > 32 || json.Unmarshal(raw, &rows) != nil || len(rows) != len(ids) {
		return fail()
	}
	result := make([]runtimeContainerIdentity, 0, len(ids))
	seen := map[string]bool{}
	for _, row := range rows {
		object, err := nativeMountObject(row, "Id", "Name")
		if err != nil {
			return fail()
		}
		id, err := nativeMountString(object["Id"], true)
		if err != nil || !nativeMountCID(id) || !slices.Contains(ids, id) || seen[id] {
			return fail()
		}
		// An unnamed external container is still a positive CID observation. Do
		// not require yoloctl labels, a box-shaped name, or an active process.
		name, err := nativeMountString(object["Name"], false)
		if len(object["Name"]) == 0 || err != nil {
			return fail()
		}
		result = append(result, runtimeContainerIdentity{id: id, name: strings.TrimPrefix(name, "/")})
		seen[id] = true
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

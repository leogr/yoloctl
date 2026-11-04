package container

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/distribution/reference"
)

// ImageDetails includes the immutable identity and aliases needed for GC.
type ImageDetails struct {
	ID          string `json:"Id"`
	RepoTags    []string
	RepoDigests []string
	Config      struct{ Labels map[string]string }
	RootFS      struct {
		Type   string
		Layers []string
	}
}

// SnapshotParent obtains the immutable rootfs when nerdctl reports a mutable
// Image name: always on 2.3.5, also on 2.4+ for containers without its digest label.
// The overlayfs snapshot parent is the OCI ChainID of its original base layers.
func (c *Client) SnapshotParent(ctx context.Context, name string) (string, error) {
	raw, err := c.command(ctx, "sudo", "nerdctl", "container", "inspect", "--mode", "native", name).Output()
	if err != nil {
		return "", fmt.Errorf("inspect native container %s: %w", name, err)
	}
	key, err := snapshotKey(raw)
	if err != nil {
		return "", fmt.Errorf("container %s: %w", name, err)
	}
	raw, err = c.command(ctx, "sudo", "ctr", "--namespace", "default", "snapshots", "--snapshotter", "overlayfs", "info", "--", key).Output()
	if err != nil {
		return "", fmt.Errorf("inspect snapshot of %s: %w", name, err)
	}
	return snapshotParent(raw, key)
}

func snapshotKey(raw []byte) (string, error) {
	var info []struct{ Snapshotter, SnapshotKey string }
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", err
	}
	if len(info) != 1 || info[0].Snapshotter != "overlayfs" || info[0].SnapshotKey == "" {
		return "", fmt.Errorf("immutable image identity unavailable; expected one overlayfs container snapshot")
	}
	return info[0].SnapshotKey, nil
}

func snapshotParent(raw []byte, key string) (string, error) {
	var info struct{ Name, Kind, Parent string }
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", err
	}
	if info.Name != key || (info.Kind != "Active" && info.Kind != "View") {
		return "", fmt.Errorf("unexpected container snapshot identity or kind")
	}
	return info.Parent, nil
}

// InspectImages amortizes CLI startup while bounding argv and response size per
// call. A partial/error response never becomes a deletion inventory.
func (c *Client) InspectImages(ctx context.Context, refs []string) ([]ImageDetails, error) {
	var all []ImageDetails
	for batch := range slices.Chunk(refs, 64) {
		args := append([]string{"sudo", "nerdctl", "image", "inspect", "--"}, batch...)
		raw, err := c.command(ctx, args...).Output()
		if err != nil {
			return nil, fmt.Errorf("inspect image batch: %w", err)
		}
		entries, err := parseImageInspects(raw, batch)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	return all, nil
}

func parseImageInspects(raw []byte, refs []string) ([]ImageDetails, error) {
	var entries []ImageDetails
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse image inspection: %w", err)
	}
	if len(entries) != len(refs) {
		return nil, fmt.Errorf("image inspection returned %d of %d entries", len(entries), len(refs))
	}
	for i, ref := range refs {
		want, err := reference.ParseDockerRef(ref)
		if err != nil {
			return nil, err
		}
		match := false
		for _, tag := range entries[i].RepoTags {
			got, err := reference.ParseDockerRef(tag)
			if err == nil && got.String() == want.String() {
				match = true
			}
		}
		if !match || entries[i].ID == "" {
			return nil, fmt.Errorf("image %s has missing or mismatched inspection metadata", ref)
		}
	}
	return entries, nil
}

type buildGuardKey struct{}
type BuildGuard func(context.Context, []string) error

// WithBuildGuard supplies a controller-owned preflight while its mutation lock
// is held. This context never comes from a host/operator wire payload.
func WithBuildGuard(ctx context.Context, guard BuildGuard) context.Context {
	return context.WithValue(ctx, buildGuardKey{}, guard)
}

func checkBuildSpace(ctx context.Context, refs []string) error {
	if guard, ok := ctx.Value(buildGuardKey{}).(BuildGuard); ok {
		return guard(ctx, refs)
	}
	return nil
}

// ContainerSnapshots maps each named container's full ID to its overlayfs
// snapshot key, the writable layer holding everything the box wrote outside
// its mounts. A container missing from the reply is absent from the map.
// `nerdctl ps` truncates IDs to 12 characters; match them with SnapshotFor.
func (c *Client) ContainerSnapshots(ctx context.Context, names []string) (map[string]string, error) {
	out := map[string]string{}
	for batch := range slices.Chunk(names, 64) {
		args := append([]string{"sudo", "nerdctl", "container", "inspect", "--mode", "native", "--"}, batch...)
		raw, err := c.command(ctx, args...).Output()
		if err != nil {
			return nil, fmt.Errorf("inspect native containers: %w", err)
		}
		if err := parseContainerSnapshots(raw, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SnapshotFor finds a container's snapshot key by its full ID or by the
// truncated ID `nerdctl ps` prints, which must prefix exactly one full ID.
func SnapshotFor(keys map[string]string, id string) (string, bool) {
	if key, ok := keys[id]; ok {
		return key, true
	}
	if len(id) < 12 {
		return "", false
	}
	found, n := "", 0
	for full, key := range keys {
		if strings.HasPrefix(full, id) {
			found, n = key, n+1
		}
	}
	return found, n == 1
}

func parseContainerSnapshots(raw []byte, out map[string]string) error {
	var info []struct{ ID, Snapshotter, SnapshotKey string }
	if err := json.Unmarshal(raw, &info); err != nil {
		return fmt.Errorf("parse native container inspection: %w", err)
	}
	for _, i := range info {
		if i.ID == "" || i.Snapshotter != "overlayfs" || i.SnapshotKey == "" {
			continue
		}
		out[i.ID] = i.SnapshotKey
	}
	return nil
}

// SnapshotUsages reports the bytes each overlayfs snapshot holds, by key.
// containerd measures an active snapshot's upper directory on demand and
// stores a committed one's size, so this walks only writable layers. One
// snapshot that disappears mid-walk fails the whole call: the result is
// never partial.
func (c *Client) SnapshotUsages(ctx context.Context) (map[string]uint64, error) {
	raw, err := c.command(ctx, "sudo", "ctr", "--namespace", "default", "snapshots", "--snapshotter", "overlayfs", "usage", "-b").Output()
	if err != nil {
		return nil, fmt.Errorf("measure container snapshots: %w", err)
	}
	return parseSnapshotUsages(raw)
}

// parseSnapshotUsages reads `ctr snapshots usage -b` from the right: a
// transient unpack key contains a space, size and inode count never do.
func parseSnapshotUsages(raw []byte) (map[string]uint64, error) {
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") != "KEY SIZE INODES" {
		return nil, fmt.Errorf("unexpected snapshot usage header")
	}
	out := map[string]uint64{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 3 {
			return nil, fmt.Errorf("malformed snapshot usage line %q", l)
		}
		size, err := strconv.ParseUint(f[len(f)-2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("malformed snapshot usage line %q", l)
		}
		if _, err := strconv.ParseUint(f[len(f)-1], 10, 64); err != nil {
			return nil, fmt.Errorf("malformed snapshot usage line %q", l)
		}
		key := strings.Join(f[:len(f)-2], " ")
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("snapshot %q reported twice", key)
		}
		out[key] = size
	}
	return out, nil
}

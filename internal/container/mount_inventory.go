package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
)

// NativeMountEntry is read-only containerd OCI-spec observation, including
// stopped and unlabelled containers in the client's configured namespace.
// It grants no admission, process/kernel,
// Stop/rm ACK or deletion authority. Native infrastructure must independently
// compare actual protected source identities, not only these path strings.
type NativeMountEntry struct {
	ContainerID string
	RootPath    string
	Mounts      []NativeMount
}

type NativeMount struct {
	Type, Source, Destination string
	Options                   []string
}

// Arbitrary existing OCI mount text can itself contain private options. It is
// used only for infrastructure comparison and never rendered as diagnostics.
func (NativeMountEntry) String() string     { return "native OCI mount observation [not authority]" }
func (e NativeMountEntry) GoString() string { return e.String() }
func (NativeMount) String() string          { return "native OCI mount [private text]" }
func (m NativeMount) GoString() string      { return m.String() }

const nativeMountInventoryLimit = 1024

// NativeMountInventory requires complete successful native inspections between
// three matching unfiltered full-ID listings. Two matching spec reads detect
// mount/root edits without treating mutable nerdctl label summaries as OCI
// mount evidence. It never omits a vanished/uninspectable container, retries or
// substitutes another query after a failed/lost response.
// This is not cross-namespace enumeration; complete consumer callers must
// independently prove all affected namespaces and actual kernel projections.
func (c *Client) NativeMountInventory(ctx context.Context) ([]NativeMountEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	remaining := 32 << 20 // total captured command bytes across this observation
	list := func() ([]string, error) {
		raw, err := c.nativeMountRead(ctx, &remaining, 128<<10, "ps", "-a", "-q", "--no-trunc")
		if err != nil {
			return nil, err
		}
		ids := strings.Fields(string(raw))
		if len(ids) > nativeMountInventoryLimit {
			return nil, errors.New("native mount inventory exceeds container bound")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !nativeMountCID(id) || seen[id] {
				return nil, errors.New("native mount inventory requires unique full canonical CIDs")
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
	read := func() ([]NativeMountEntry, error) {
		result := make([]NativeMountEntry, 0, len(ids))
		mounts := 0
		for start := 0; start < len(ids); start += 32 {
			end := min(start+32, len(ids))
			args := append([]string{"inspect", "--mode", "native"}, ids[start:end]...)
			raw, err := c.nativeMountRead(ctx, &remaining, 8<<20, args...)
			if err != nil {
				return nil, err
			}
			entries, err := parseNativeMountEntries(raw, ids[start:end])
			clear(raw)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				mounts += len(entry.Mounts)
				if mounts > 4096 {
					return nil, errors.New("native mount inventory exceeds total mount bound")
				}
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
	if !reflect.DeepEqual(ids, middle) {
		return nil, errors.New("native mount inventory container set changed")
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	last, err := list()
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(ids, last) || !reflect.DeepEqual(first, second) {
		return nil, errors.New("native mount inventory container set or OCI mounts changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return second, nil
}

type nativeMountOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (w *nativeMountOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		return 0, errors.New("native mount inventory output exceeds bound")
	}
	return w.buffer.Write(p)
}

func (c *Client) nativeMountRead(ctx context.Context, remaining *int, limit int, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if remaining == nil || *remaining <= 0 {
		return nil, errors.New("native mount inventory exhausted output budget")
	}
	output := &nativeMountOutput{limit: min(limit, *remaining)}
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout, cmd.Stderr = output, io.Discard
	// A direct adapter exit/deadline does not settle pipes inherited by its
	// descendants. Bound that separate wait; partial output remains unavailable.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("native mount inventory command/pipe settlement failed: %w", errors.Join(err, ctx.Err()))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*remaining -= output.buffer.Len()
	return output.buffer.Bytes(), nil
}

func nativeMountCID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Fields not interpreted here may be present, but duplicate objects or folded
// aliases of evidence fields are refused. No Config.Env/secret is decoded or
// included in returned data or errors. OCI mounts are lower-case Spec fields;
// Docker-compatible Mounts/Id and mutable label summaries are not a fallback.
func nativeMountObject(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	object, ok := uniqueInspectionObject(raw)
	if !ok {
		return nil, errors.New("native mount inventory has invalid/duplicate JSON object")
	}
	for key := range object {
		for _, wanted := range fields {
			if key != wanted && strings.EqualFold(key, wanted) {
				return nil, errors.New("native mount inventory has a noncanonical evidence field")
			}
		}
	}
	return object, nil
}

func nativeMountString(raw json.RawMessage, required bool) (string, error) {
	if len(raw) == 0 && !required {
		return "", nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", errors.New("native mount inventory lacks typed string evidence")
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) > 4096 || strings.ContainsRune(value, 0) || required && value == "" {
		return "", errors.New("native mount inventory has invalid string evidence")
	}
	return value, nil
}

func parseNativeMountEntries(raw []byte, ids []string) ([]NativeMountEntry, error) {
	if len(ids) == 0 || len(ids) > 32 {
		return nil, errors.New("native mount inventory invalid inspection selection")
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || len(entries) != len(ids) {
		return nil, errors.New("native mount inventory inspection is incomplete")
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if !nativeMountCID(id) || wanted[id] {
			return nil, errors.New("native mount inventory invalid original full IDs")
		}
		wanted[id] = true
	}
	found := map[string]NativeMountEntry{}
	for _, entry := range entries {
		object, err := nativeMountObject(entry, "ID", "Spec")
		if err != nil {
			return nil, err
		}
		id, err := nativeMountString(object["ID"], true)
		if err != nil {
			return nil, err
		}
		if !wanted[id] || found[id].ContainerID != "" {
			return nil, errors.New("native mount inventory inspection changed/repeated full CID")
		}
		spec, err := nativeMountObject(object["Spec"], "ociVersion", "root", "mounts")
		if err != nil {
			return nil, err
		}
		version, err := nativeMountString(spec["ociVersion"], true)
		if err != nil || !strings.HasPrefix(version, "1.") {
			return nil, errors.New("native mount inventory requires an observed OCI1 spec")
		}
		root, err := nativeMountObject(spec["root"], "path")
		if err != nil {
			return nil, err
		}
		rootPath, err := nativeMountString(root["path"], true)
		if err != nil {
			return nil, err
		}
		result := NativeMountEntry{ContainerID: id, RootPath: rootPath}
		var mounts []json.RawMessage
		// OCI's mounts/source/type are omitempty. An omitted/null list is
		// empty in the actual OCI spec; a missing Spec/root is never empty.
		if value, exists := spec["mounts"]; exists {
			if json.Unmarshal(value, &mounts) != nil {
				return nil, errors.New("native mount inventory has invalid OCI mount list")
			}
		}
		if len(mounts) > 1024 {
			return nil, errors.New("native mount inventory exceeds per-container mount bound")
		}
		for _, value := range mounts {
			m, err := nativeMountObject(value, "type", "source", "destination", "options")
			if err != nil {
				return nil, err
			}
			kind, err := nativeMountString(m["type"], false)
			if err != nil {
				return nil, err
			}
			source, err := nativeMountString(m["source"], false)
			if err != nil {
				return nil, err
			}
			destination, err := nativeMountString(m["destination"], true)
			if err != nil {
				return nil, err
			}
			var options []json.RawMessage
			if value, exists := m["options"]; exists {
				if json.Unmarshal(value, &options) != nil || len(options) > 1024 {
					return nil, errors.New("native mount inventory has invalid/beyond-bound OCI mount options")
				}
			}
			mount := NativeMount{Type: kind, Source: source, Destination: destination}
			for _, option := range options {
				text, err := nativeMountString(option, true)
				if err != nil {
					return nil, err
				}
				mount.Options = append(mount.Options, text)
			}
			result.Mounts = append(result.Mounts, mount)
		}
		found[id] = result
	}
	result := make([]NativeMountEntry, 0, len(ids))
	for _, id := range ids {
		entry, exists := found[id]
		if !exists {
			return nil, errors.New("native mount inventory omitted original full CID")
		}
		result = append(result, entry)
	}
	return result, nil
}

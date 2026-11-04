package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/vmapi"
	"gopkg.in/yaml.v3"
)

// SetInstanceResources changes only a stopped instance's cpus and memory in
// its lima.yaml; Lima applies them on the next start. A running, starting,
// broken or unlisted state, or a surviving VM process, refuses the change
// before anything is written.
func (c *Client) SetInstanceResources(ctx context.Context, name string, r vmapi.Resources) error {
	if err := vmapi.ValidateResources("lima", r); err != nil {
		return err
	}
	rows, err := c.List(ctx)
	if err != nil {
		return fmt.Errorf("%w: cannot establish the VM state: %v", vmapi.ErrNotStopped, err)
	}
	var found *Instance
	for i := range rows {
		if rows[i].Name == name {
			found = &rows[i]
		}
	}
	if found == nil {
		return fmt.Errorf("lima VM for space %q does not exist", name)
	}
	if found.Status != "Stopped" {
		return fmt.Errorf("%w: Lima reports %q", vmapi.ErrNotStopped, found.Status)
	}
	// Even an empty first snapshot is rechecked, and an unknown VM type
	// cannot prove its processes stopped.
	pids, err := VMProcessIDs(name)
	if err == nil {
		err = RequireVMProcessesStopped(name, found.VMType, pids)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", vmapi.ErrNotStopped, err)
	}
	// The owned instance directory is read and replaced without following a
	// symlinked lima.yaml.
	dir, err := InstanceDir(name)
	if err != nil {
		return err
	}
	raw, err := safepath.ReadFile(dir, "lima.yaml", 1<<20)
	if err != nil {
		return err
	}
	updated, err := setResourcesInYAML(raw, r)
	if err != nil {
		return fmt.Errorf("rewrite resources in %s: %w", filepath.Join(dir, "lima.yaml"), err)
	}
	if bytes.Equal(raw, updated) {
		return nil
	}
	return safepath.WriteFile(dir, "lima.yaml", updated, 0o600)
}

// setResourcesInYAML sets the top-level cpus and memory keys, appending any
// that is missing; other keys and comments survive the yaml.v3 round trip.
// Equal values return raw unchanged. Only one document is accepted, since
// yaml.Unmarshal would silently ignore later ones.
func setResourcesInYAML(raw []byte, r vmapi.Resources) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var root, extra yaml.Node
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one YAML document")
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected top-level mapping, got kind %d", doc.Kind)
	}
	want := map[string]*yaml.Node{
		"cpus":   {Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(r.CPUs)},
		"memory": {Kind: yaml.ScalarNode, Tag: "!!str", Value: r.Memory, Style: yaml.DoubleQuotedStyle},
	}
	changed, seen := false, map[string]bool{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, value := doc.Content[i].Value, doc.Content[i+1]
		node := want[key]
		if node == nil {
			continue
		}
		if seen[key] {
			// Rewriting one of two keys would leave a contradictory mapping.
			return nil, fmt.Errorf("duplicate %s key", key)
		}
		seen[key] = true
		if value.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s is not a scalar", key)
		}
		if value.Value == node.Value && value.Tag == node.Tag {
			continue // a quoted "4" still becomes the integer 4
		}
		value.Value, value.Tag, value.Style = node.Value, node.Tag, node.Style
		changed = true
	}
	for _, key := range []string{"cpus", "memory"} {
		if node := want[key]; !seen[key] {
			doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return marshalYAML(&root)
}

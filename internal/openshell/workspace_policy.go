package openshell

import (
	"errors"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidateWorkspacePolicy verifies the bootstrap's explicit workspace intent
// against the exact compiled YAML selected for this generation. The upstream
// strict whole-policy parser remains authoritative for every other field.
// This check never adds a grant or repairs policy/mount/launch disagreement.
func (c LaunchConfig) ValidateWorkspacePolicy(raw []byte) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("missing or unbounded runtime policy")
	}
	var document struct {
		Filesystem *struct {
			IncludeWorkdir *bool    `yaml:"include_workdir"`
			ReadOnly       []string `yaml:"read_only"`
			ReadWrite      []string `yaml:"read_write"`
		} `yaml:"filesystem_policy"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return errors.New("invalid runtime workspace policy")
	}
	f := document.Filesystem
	if f == nil || f.IncludeWorkdir == nil {
		return errors.New("runtime policy must explicitly select include_workdir")
	}
	covered := func(parent, child string) bool {
		return parent == child || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
	}
	readable, writable := false, false
	for _, entry := range f.ReadOnly {
		if !path.IsAbs(entry) || path.Clean(entry) != entry {
			return errors.New("invalid runtime policy filesystem path")
		}
		readable = readable || covered(entry, c.Workdir)
	}
	for _, entry := range f.ReadWrite {
		if !path.IsAbs(entry) || path.Clean(entry) != entry {
			return errors.New("invalid runtime policy filesystem path")
		}
		if c.WorkspaceReadOnly && (covered(entry, c.Workdir) || covered(c.Workdir, entry)) {
			return errors.New("read-only runtime policy contains an overlapping write grant")
		}
		writable = writable || covered(entry, c.Workdir)
	}
	if c.WorkspaceReadOnly {
		if *f.IncludeWorkdir || !readable {
			return errors.New("read-only runtime bootstrap disagrees with workspace policy")
		}
	} else if !writable {
		return errors.New("writable runtime bootstrap lacks its compiled workspace grant")
	}
	return nil
}

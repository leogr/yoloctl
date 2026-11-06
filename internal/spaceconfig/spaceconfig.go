// Package spaceconfig stores a space's host mount grants, default account,
// and persistent native account disk identity. It is the desired-state source
// for creating and replacing the VM. Schema 2 requires fresh space creation;
// host-global accounts and old space schemas are not migrated.
package spaceconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

const (
	// SchemaVersion is the only space-config schema this build accepts.
	SchemaVersion = 2

	ext      = ".yaml"
	dirMode  = 0o755
	fileMode = 0o600
)

// Mount is one host→VM share grant. Source is a canonical (absolute,
// symlink-resolved) host path Lima makes available at the SAME path inside
// the space VM; Writable grants write-through (read-only otherwise).
type Mount struct {
	Source   string `yaml:"source"`
	Writable bool   `yaml:"writable"`
}

// Config is a space's persistent declaration.
// AuditSettings is native desired state, not boot authority or a box setting.
type AuditSettings struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	RulesSHA256 string `yaml:"rules_sha256,omitempty" json:"rules_sha256,omitempty"`
}

func (a *AuditSettings) Validate() error {
	if a == nil {
		return nil
	}
	if len(a.Version) > 32 {
		return errors.New("invalid audit version")
	}
	if a.RulesSHA256 != "" {
		raw, err := hex.DecodeString(a.RulesSHA256)
		if err != nil || len(raw) != 32 {
			return errors.New("invalid audit rules digest")
		}
	}
	if a.Enabled && (a.Version == "" || a.RulesSHA256 == "") {
		return errors.New("enabled audit settings need version and rules digest")
	}
	return nil
}

type Config struct {
	Audit           *AuditSettings `yaml:"audit,omitempty"`
	SchemaVersion   int            `yaml:"schema_version"`
	Name            string         `yaml:"name"`
	DefaultAccount  string         `yaml:"default_account"`
	AccountDiskID   string         `yaml:"account_disk_id"`
	AccountDiskUUID string         `yaml:"account_disk_uuid,omitempty"`
	// Initialization authority is granted only after allocating a new blank disk.
	// Retry additionally checks that the disk is unattached and its guest-visible
	// contents are entirely zero (raw or standalone QCOW2).
	AccountDiskInitializing bool `yaml:"account_disk_initializing,omitempty"`
	// DefaultWorkdir is the canonical host path box create / run bind at
	// /workdir when neither --workdir nor --workdir-mode ephemeral is given.
	// Required at space creation, and always covered by a Mounts grant
	// (Validate enforces the invariant, so the default bind is guaranteed
	// mountable).
	DefaultWorkdir string `yaml:"default_workdir"`
	// Mounts are the user-facing host→VM grants (the default workdir plus any
	// added via `yoloctl space mount add`). Managed internal mounts are added
	// at render time and not stored here.
	Mounts []Mount `yaml:"mounts"`

	path string // absolute path this config was loaded from / will save to
}

// Load reads the named space config. Returns fs.ErrNotExist if the space has
// not been created (caller decides whether that's an error).
func Load(name string) (*Config, error) {
	if spaceenv.Native() {
		return loadNative(name)
	}
	path, err := layout.SpaceConfigPath(name)
	if err != nil {
		return nil, err
	}
	c, err := Open(path)
	if err != nil {
		return nil, err
	}
	if c.Name != name {
		return nil, fmt.Errorf("space manifest name %q does not match %q", c.Name, name)
	}
	return c, nil
}

// Open reads a config from an explicit path. Rejects any schema version other
// than the current one and validates the declaration.
func Open(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{path: path}
	if len(raw) > 0 {
		if err := strictyaml.Decode(raw, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if c.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%s: schema_version %d not supported (this build supports %d); no migration is supported; use the previous yoloctl version to remove this old space, then create it with this version using space create --workdir PATH --account NAME",
			path, c.SchemaVersion, SchemaVersion)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Exists reports whether the named space has a saved config.
func Exists(name string) (bool, error) {
	path, err := layout.SpaceConfigPath(name)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// List returns the names of all configured spaces, sorted. A space counts
// only once its bundle holds a space.yaml (a bare directory — e.g. an
// incomplete or externally modified bundle with only a boxes/ subtree —
// is skipped). Empty slice (no error) if the spaces dir doesn't exist yet.
func List() ([]string, error) {
	if c := spaceenv.Current(); c != nil {
		return []string{c.Space}, nil
	}
	dir, err := layout.SpacesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if layout.ValidateExistingSpaceName(name) != nil {
			continue
		}
		cfgPath, err := layout.SpaceConfigPath(name)
		if err != nil {
			continue
		}
		if _, err := os.Stat(cfgPath); err != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Path returns the file path this config loads from / saves to ("" until
// Save resolves it).
func (c *Config) Path() string { return c.path }

// Validate checks the config's internal consistency: a valid name, an
// absolute required default_workdir, absolute non-duplicate mount sources,
// and — the key invariant — that default_workdir is covered by a grant, so
// the default /workdir bind is always mountable.
func (c *Config) Validate() error {
	if err := c.Audit.Validate(); err != nil {
		return err
	}
	if c.DefaultAccount == "none" {
		return errors.New("default_account must name a real account")
	}
	if err := layout.ValidateName("account", c.DefaultAccount); err != nil {
		return fmt.Errorf("default_account: %w", err)
	}
	if b, err := hex.DecodeString(c.AccountDiskID); err != nil || len(b) != 16 {
		return errors.New("account_disk_id must be a 32-character hexadecimal identity")
	}
	if err := layout.ValidateExistingSpaceName(c.Name); err != nil {
		return err
	}
	if c.DefaultWorkdir == "" {
		return errors.New("default_workdir is required")
	}
	if !filepath.IsAbs(c.DefaultWorkdir) {
		return fmt.Errorf("default_workdir %q must be an absolute path", c.DefaultWorkdir)
	}
	seen := make(map[string]bool, len(c.Mounts))
	for i, m := range c.Mounts {
		if m.Source == "" {
			return fmt.Errorf("mounts[%d].source is required", i)
		}
		if !filepath.IsAbs(m.Source) {
			return fmt.Errorf("mounts[%d].source %q must be an absolute path", i, m.Source)
		}
		clean := filepath.Clean(m.Source)
		if seen[clean] {
			return fmt.Errorf("duplicate mount source %q", m.Source)
		}
		seen[clean] = true
	}
	if !c.PathUnderGrant(c.DefaultWorkdir) {
		return fmt.Errorf("default_workdir %q is not under any mount grant", c.DefaultWorkdir)
	}
	return nil
}

// Save writes the config atomically (tmp + rename). Auto-fills SchemaVersion
// if unset, then Validates.
func (c *Config) Save() error {
	if spaceenv.Native() {
		return saveNative(c)
	}
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if err := c.Validate(); err != nil {
		return err
	}
	path := c.path
	if path == "" {
		resolved, err := layout.SpaceConfigPath(c.Name)
		if err != nil {
			return err
		}
		path = resolved
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, raw, fileMode); err != nil {
		return err
	}
	c.path = path
	return nil
}

// Delete removes the space config YAML. Returns fs.ErrNotExist if absent
// (caller decides whether that matters).
func (c *Config) Delete() error {
	path := c.path
	if path == "" {
		resolved, err := layout.SpaceConfigPath(c.Name)
		if err != nil {
			return err
		}
		path = resolved
	}
	return os.Remove(path)
}

// PathUnderGrant reports whether path is equal to, or nested under, one of
// the config's mount grants. Inputs should be clean absolute paths — callers
// canonicalize host paths (symlink-resolved) before checking, and grant
// sources are stored canonical. This is the basis of the "is this bind
// covered by a configured space mount?" check that box create/run performs
// before saving anything.
func (c *Config) PathUnderGrant(path string) bool {
	_, ok := c.GrantFor(path)
	return ok
}

// GrantFor returns the grant covering path and whether one exists. When
// grants nest (e.g. a read-only parent and a writable child), the MOST
// SPECIFIC grant — the one with the longest matching source — wins. That
// mirrors Lima's overlay semantics, where the deeper mount governs a given
// path, so a writable bind under a writable child grant isn't masked by a
// read-only ancestor.
func (c *Config) GrantFor(path string) (Mount, bool) {
	p := filepath.Clean(path)
	best := -1
	bestLen := -1
	for i, m := range c.Mounts {
		src := filepath.Clean(m.Source)
		if pathWithin(p, src) && len(src) > bestLen {
			best = i
			bestLen = len(src)
		}
	}
	if best < 0 {
		return Mount{}, false
	}
	return c.Mounts[best], true
}

// AddMount adds a grant for source, or updates an existing grant's writable
// flag. source is cleaned; the grant list stays sorted by source. Returns
// whether the config changed (false when the identical grant already exists).
func (c *Config) AddMount(source string, writable bool) bool {
	clean := filepath.Clean(source)
	for i := range c.Mounts {
		if filepath.Clean(c.Mounts[i].Source) == clean {
			if c.Mounts[i].Writable != writable {
				c.Mounts[i].Writable = writable
				return true
			}
			return false
		}
	}
	c.Mounts = append(c.Mounts, Mount{Source: clean, Writable: writable})
	sort.Slice(c.Mounts, func(i, j int) bool { return c.Mounts[i].Source < c.Mounts[j].Source })
	return true
}

// RemoveMount drops the grant whose source equals source (cleaned). Returns
// whether a grant was removed.
func (c *Config) RemoveMount(source string) bool {
	clean := filepath.Clean(source)
	for i := range c.Mounts {
		if filepath.Clean(c.Mounts[i].Source) == clean {
			c.Mounts = append(c.Mounts[:i], c.Mounts[i+1:]...)
			return true
		}
	}
	return false
}

// pathWithin reports whether p is base or a descendant of base. Both must be
// clean absolute paths.
func pathWithin(p, base string) bool {
	if p == base {
		return true
	}
	return strings.HasPrefix(p, base+string(filepath.Separator))
}

// New declares a new space and allocates its persistent storage identity.
func New(name, workdir, acct string) (*Config, error) {
	if err := layout.ValidateName("space", name); err != nil {
		return nil, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	c := &Config{SchemaVersion: SchemaVersion, Name: name, DefaultWorkdir: workdir, DefaultAccount: acct, AccountDiskID: hex.EncodeToString(id[:]), Mounts: []Mount{{Source: workdir, Writable: true}}}
	return c, c.Validate()
}

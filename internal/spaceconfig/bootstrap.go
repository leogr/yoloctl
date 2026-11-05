package spaceconfig

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

const BootstrapVersion = 3

var ErrLegacySchema = errors.New("schema-2 spaces are unsupported in this build; use the previous build to recover their data")

// Bootstrap is the complete host-owned record needed to reconstruct a VM.
// Defaults, boxes, accounts and credentials deliberately have no fields here.
type Bootstrap struct {
	SchemaVersion int            `yaml:"schema_version" json:"schema_version"`
	Name          string         `yaml:"name" json:"name"`
	DiskID        string         `yaml:"disk_id" json:"disk_id"`
	DiskUUID      string         `yaml:"disk_uuid,omitempty" json:"disk_uuid,omitempty"`
	Initializing  bool           `yaml:"initializing,omitempty" json:"initializing,omitempty"`
	UID           int            `yaml:"developer_uid" json:"developer_uid"`
	GID           int            `yaml:"developer_gid" json:"developer_gid"`
	Mounts        []Mount        `yaml:"mounts" json:"mounts"`
	Bundle        string         `yaml:"bundle,omitempty" json:"bundle,omitempty"`
	Generation    string         `yaml:"generation" json:"generation"`
	VM            VMSettings     `yaml:"vm" json:"vm"`
	Migration     string         `yaml:"migration,omitempty" json:"migration,omitempty"` // Read compatibility for completed older migrations.
	Import        *ImportState   `yaml:"import,omitempty" json:"import,omitempty"`
	ImageUpdate   *VMImageUpdate `yaml:"image_update,omitempty" json:"image_update,omitempty"`
	DataDiskGrow  *DataDiskGrow  `yaml:"data_disk_grow,omitempty" json:"data_disk_grow,omitempty"`
	// ImageLayerCompression is empty (new private image layers are written
	// uncompressed) or "gzip"; the controller receives it at the next start.
	ImageLayerCompression string `yaml:"image_layer_compression,omitempty" json:"image_layer_compression,omitempty"`
}

// VMImageUpdate checkpoints an explicitly requested system-image replacement.
// It grants no native disk initialization authority and never rolls data back.
type VMImageUpdate struct {
	Previous string `yaml:"previous" json:"previous"`
	Target   string `yaml:"target" json:"target"`
	Bundle   string `yaml:"bundle" json:"bundle"`
	Phase    string `yaml:"phase" json:"phase"` // prepared or selected
}

// ImportState is temporary host lifecycle authority for a fresh disk restore.
// Its private staged payload is never a declaration fallback.
type ImportState struct {
	SHA256 string `yaml:"sha256" json:"sha256"`
	Size   int64  `yaml:"size" json:"size"`
}

type VMSettings struct {
	// LimaHome is fixed at new macOS space admission; empty retains legacy storage.
	LimaHome string `yaml:"lima_home,omitempty" json:"lima_home,omitempty"`
	Backend  string `yaml:"backend,omitempty" json:"backend,omitempty"`
	Image    string `yaml:"image,omitempty" json:"image,omitempty"`
	CPUs     int    `yaml:"cpus" json:"cpus"`
	Memory   string `yaml:"memory" json:"memory"`
	Disk     string `yaml:"disk" json:"disk"`
	// DataDisk is the data disk's size; empty is the original 10GiB.
	DataDisk string `yaml:"data_disk,omitempty" json:"data_disk,omitempty"`
}

// Settings is the controller-owned portion of a space declaration. Boot
// authority (including host grants) cannot be changed by writing this file.
type Settings struct {
	Audit          *AuditSettings `yaml:"audit,omitempty" json:"audit,omitempty"`
	SchemaVersion  int            `yaml:"schema_version" json:"schema_version"`
	Name           string         `yaml:"name" json:"name"`
	DefaultAccount string         `yaml:"default_account" json:"default_account"`
	DefaultWorkdir string         `yaml:"default_workdir" json:"default_workdir"`
}

func (b *Bootstrap) Validate() error {
	if b.SchemaVersion != BootstrapVersion {
		return fmt.Errorf("unsupported bootstrap schema %d", b.SchemaVersion)
	}
	if err := layout.ValidateExistingSpaceName(b.Name); err != nil {
		return err
	}
	if id, err := hex.DecodeString(b.DiskID); err != nil || len(id) != 16 {
		return errors.New("disk_id must be a 32-character hexadecimal identity")
	}
	if b.DiskUUID == "" && !b.Initializing {
		return errors.New("initialized data disk requires its filesystem UUID")
	}
	if b.UID <= 0 || b.GID <= 0 {
		return errors.New("developer UID/GID must be positive")
	}
	if b.Generation == "" {
		return errors.New("bootstrap generation is required")
	}
	if b.ImageLayerCompression != "" && b.ImageLayerCompression != spaceenv.ImageLayerGzip {
		return fmt.Errorf("unsupported image layer compression %q", b.ImageLayerCompression)
	}
	if b.VM.Backend != "" && b.VM.Backend != "lima" && b.VM.Backend != "firecracker" {
		return fmt.Errorf("unsupported VM backend %q", b.VM.Backend)
	}
	if b.VM.LimaHome != "" {
		if b.VM.Backend == "firecracker" {
			return errors.New("lima_home applies only to Lima")
		}
		if _, err := layout.ResolveLimaHome(b.VM.LimaHome); err != nil {
			return err
		}
	}
	if b.VM.Backend == "firecracker" && (!filepath.IsAbs(b.VM.Image) || filepath.Clean(b.VM.Image) != b.VM.Image) {
		return errors.New("firecracker requires a clean absolute image bundle path")
	}
	if b.VM.Backend != "firecracker" && b.VM.Image != "" {
		return errors.New("image bundle applies only to Firecracker")
	}
	if u := b.ImageUpdate; u != nil {
		if b.VM.Backend != "firecracker" || b.Initializing || b.DiskUUID == "" || b.Import != nil || u.Bundle == "" {
			return errors.New("VM image update requires an initialized Firecracker space")
		}
		for _, image := range []string{u.Previous, u.Target} {
			if !filepath.IsAbs(image) || filepath.Clean(image) != image {
				return errors.New("invalid VM image update path")
			}
		}
		if u.Phase != "prepared" && u.Phase != "selected" {
			return errors.New("invalid VM image update phase")
		}
		if (u.Phase == "prepared" && b.VM.Image != u.Previous) || (u.Phase == "selected" && b.VM.Image != u.Target) {
			return errors.New("VM image update differs from selected image")
		}
	}
	if b.VM.CPUs < 1 || b.VM.Memory == "" || b.VM.Disk == "" {
		return errors.New("VM boot settings are required")
	}
	if _, err := VMDiskBytes(b.VM.Disk); err != nil {
		return err
	}
	data, err := DataDiskBytes(b.VM.DataDisk)
	if err != nil {
		return err
	}
	if g := b.DataDiskGrow; g != nil {
		if err := g.validate(b, data); err != nil {
			return err
		}
	}
	if b.Import != nil {
		sum, err := hex.DecodeString(b.Import.SHA256)
		if err != nil || len(sum) != 32 || b.Import.Size <= 0 || b.Import.Size > 16<<30 {
			return errors.New("invalid pending space import")
		}
	}
	seen := map[string]bool{}
	for _, m := range b.Mounts {
		if !filepath.IsAbs(m.Source) || filepath.Clean(m.Source) != m.Source {
			return errors.New("mount grants require clean absolute paths")
		}
		if b.VM.Backend == "firecracker" && (b.VM.Image == m.Source || strings.HasPrefix(b.VM.Image, strings.TrimRight(m.Source, "/")+"/") || strings.HasPrefix(m.Source, strings.TrimRight(b.VM.Image, "/")+"/")) {
			return errors.New("host grants must not intersect the prepared VM image")
		}
		if u := b.ImageUpdate; u != nil {
			for _, image := range []string{u.Previous, u.Target} {
				if image == m.Source || strings.HasPrefix(image, strings.TrimRight(m.Source, "/")+"/") || strings.HasPrefix(m.Source, strings.TrimRight(image, "/")+"/") {
					return errors.New("host grants must not intersect pending VM images")
				}
			}
		}
		if seen[m.Source] {
			return fmt.Errorf("duplicate mount grant %q", m.Source)
		}
		seen[m.Source] = true
	}
	return nil
}

func ParseBootstrap(raw []byte) (*Bootstrap, error) {
	var header struct {
		SchemaVersion int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(raw, &header); err != nil {
		return nil, err
	}
	if header.SchemaVersion == SchemaVersion {
		return nil, ErrLegacySchema
	}
	var b Bootstrap
	if err := strictyaml.Decode(raw, &b); err != nil {
		return nil, fmt.Errorf("decode space bootstrap: %w", err)
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

func LoadBootstrap(name string) (*Bootstrap, error) {
	if spaceenv.Native() {
		// The guest's config/space.yaml holds native settings; the trusted
		// execution context is its only view of host bootstrap data.
		return nil, errors.New("VM controller cannot read host bootstrap configuration")
	}
	dir, err := layout.SpaceBundleDir(name)
	if err != nil {
		return nil, err
	}
	raw, err := safepath.ReadFile(dir, "space.yaml", 1<<20)
	if err != nil {
		return nil, err
	}
	b, err := ParseBootstrap(raw)
	if err != nil {
		return nil, err
	}
	if b.Name != name {
		return nil, errors.New("bootstrap space identity mismatch")
	}
	return b, nil
}

func (b *Bootstrap) Save() error {
	if spaceenv.Native() {
		return errors.New("VM controller cannot save host bootstrap configuration")
	}
	if err := b.Validate(); err != nil {
		return err
	}
	dir, err := layout.SpaceBundleDir(b.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := yaml.Marshal(b)
	if err != nil {
		return err
	}
	if err := safepath.WriteFile(dir, "space.yaml", raw, 0o600); err != nil {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(dir))
}

func Split(c *Config, uid, gid int, generation string) (*Bootstrap, *Settings, error) {
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	b := &Bootstrap{
		SchemaVersion: BootstrapVersion, Name: c.Name, DiskID: c.AccountDiskID,
		DiskUUID: c.AccountDiskUUID, Initializing: c.AccountDiskInitializing,
		UID: uid, GID: gid, Mounts: append([]Mount(nil), c.Mounts...), Generation: generation,
		VM: VMSettings{CPUs: 4, Memory: "4GiB", Disk: DiskSize(AutoDiskMin)},
	}
	s := &Settings{SchemaVersion: 1, Name: c.Name, DefaultAccount: c.DefaultAccount, DefaultWorkdir: c.DefaultWorkdir, Audit: c.Audit}
	if err := b.Validate(); err != nil {
		return nil, nil, err
	}
	return b, s, nil
}

func (b *Bootstrap) Context(hostRoot string) spaceenv.Context {
	disk := layout.AccountDiskMount(b.DiskID)
	c := spaceenv.Context{
		Schema: 1, Space: b.Name, DiskID: b.DiskID, DiskUUID: b.DiskUUID,
		DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot,
		HostConfigRoot: hostRoot, UID: b.UID, GID: b.GID, Bundle: b.Bundle, Generation: b.Generation,
		ImageLayerCompression: b.ImageLayerCompression,
	}
	if b.VM.Backend == "firecracker" {
		c.ServiceManager = "microvm"
	}
	for _, m := range b.Mounts {
		c.Mounts = append(c.Mounts, spaceenv.Mount{Source: m.Source, Writable: m.Writable})
	}
	return c
}

func (s *Settings) Compose(b *Bootstrap) (*Config, error) {
	if s.SchemaVersion != 1 || s.Name != b.Name {
		return nil, errors.New("space settings schema/identity mismatch")
	}
	c := &Config{
		SchemaVersion: SchemaVersion, Name: s.Name, DefaultAccount: s.DefaultAccount, Audit: s.Audit,
		DefaultWorkdir: s.DefaultWorkdir, AccountDiskID: b.DiskID, AccountDiskUUID: b.DiskUUID,
		AccountDiskInitializing: b.Initializing, Mounts: append([]Mount(nil), b.Mounts...),
	}
	return c, c.Validate()
}

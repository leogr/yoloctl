package spaceconfig

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

func nativeBootstrap(c *spaceenv.Context) *Bootstrap {
	b := &Bootstrap{Name: c.Space, DiskID: c.DiskID, DiskUUID: c.DiskUUID}
	for _, m := range c.Mounts {
		b.Mounts = append(b.Mounts, Mount{Source: m.Source, Writable: m.Writable})
	}
	return b
}

func loadNative(name string) (*Config, error) {
	if err := spaceenv.RequireSpace(name); err != nil {
		return nil, err
	}
	ctx := spaceenv.Current()
	raw, err := safepath.ReadFile(ctx.ConfigDir, "space.yaml", 1<<20)
	if err != nil {
		return nil, err
	}
	var settings Settings
	if err := strictyaml.Decode(raw, &settings); err != nil {
		return nil, err
	}
	c, err := settings.Compose(nativeBootstrap(ctx))
	if err != nil {
		return nil, err
	}
	c.path = filepath.Join(ctx.ConfigDir, "space.yaml")
	return c, nil
}

func saveNative(c *Config) error {
	if err := spaceenv.RequireSpace(c.Name); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	ctx := spaceenv.Current()
	boot := nativeBootstrap(ctx)
	if c.AccountDiskID != boot.DiskID || c.AccountDiskUUID != boot.DiskUUID ||
		c.AccountDiskInitializing || !reflect.DeepEqual(c.Mounts, boot.Mounts) {
		return errors.New("controller cannot change host-owned disk or mount authority")
	}
	s := Settings{SchemaVersion: 1, Name: c.Name, DefaultAccount: c.DefaultAccount, DefaultWorkdir: c.DefaultWorkdir}
	raw, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := safepath.WriteFile(ctx.ConfigDir, "space.yaml", raw, 0o600); err != nil {
		return err
	}
	c.path = filepath.Join(ctx.ConfigDir, "space.yaml")
	return nil
}

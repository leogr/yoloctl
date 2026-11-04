// Package spaceenv carries the trusted, process-local VM execution context.
// Only controller infrastructure installs it. Box clients never configure
// native execution; they communicate with their provisioned endpoint instead.
package spaceenv

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
)

const (
	ContextPath = "/etc/yoloctl/controller.json"
	CatalogRoot = "/etc/yoloctl/catalogs"
	RunRoot     = "/run/yoloctl/controller"
	ContextEnv  = "YOLOCTL_CONTROLLER_CONTEXT"
)

type Mount struct {
	Source   string `json:"source"`
	Writable bool   `json:"writable"`
}

// Context contains host-owned boot inputs, never credentials or user-supplied
// operator identity. Configuration is immutable for the lifetime of a worker.
type Context struct {
	ServiceManager string  `json:"service_manager,omitempty"`
	Schema         int     `json:"schema"`
	Space          string  `json:"space"`
	DiskID         string  `json:"disk_id"`
	DiskUUID       string  `json:"disk_uuid"`
	DiskMount      string  `json:"disk_mount"`
	ConfigDir      string  `json:"config_dir"`
	CatalogDir     string  `json:"catalog_dir"`
	HostConfigRoot string  `json:"host_config_root"`
	UID            int     `json:"uid"`
	GID            int     `json:"gid"`
	Mounts         []Mount `json:"mounts"`
	Bundle         string  `json:"bundle"`
	Generation     string  `json:"generation"`
}

var current atomic.Pointer[Context]
var catalogSnapshot atomic.Pointer[string]

func Current() *Context { return current.Load() }
func Native() bool      { return Current() != nil }

func CatalogDirectory() string {
	if snapshot := catalogSnapshot.Load(); snapshot != nil {
		return *snapshot
	}
	if c := Current(); c != nil {
		return c.CatalogDir
	}
	return ""
}

// UseCatalogSnapshot is called by a trusted worker after copying the catalog
// inputs. It is never selected from client argv or environment.
func UseCatalogSnapshot(path string) { catalogSnapshot.Store(&path) }

// Configure is called once by a trusted controller/worker entry point, after
// validating the root-owned bootstrap. Disk verification follows before any
// declaration or runtime operation. No environment variable
// by itself changes layout or runtime behavior.
func Configure(c Context) error {
	if current.Load() != nil {
		return errors.New("VM execution context already configured")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	c.Mounts = append([]Mount(nil), c.Mounts...)
	if !current.CompareAndSwap(nil, &c) {
		return errors.New("VM execution context already configured")
	}
	return nil
}

func RequireSpace(space string) error {
	if c := Current(); c != nil && c.Space != space {
		return fmt.Errorf("controller is scoped to space %q, not %q", c.Space, space)
	}
	return nil
}

func Identity() (int, int) {
	if c := Current(); c != nil {
		return c.UID, c.GID
	}
	return os.Getuid(), os.Getgid()
}

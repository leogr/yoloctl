// Package vm selects the space's persisted platform adapter. It owns no VM
// controller or declaration cache: ordinary operations still require the guest.
package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmapi"
)

type Mount = vmapi.Mount
type Instance = vmapi.Instance
type DiskInfo = vmapi.DiskInfo
type Spec = vmapi.Spec
type Resources = vmapi.Resources

const GuestProbeTimeout = lima.GuestProbeTimeout
const DefaultBinary = lima.DefaultBinary

var registry = struct {
	sync.RWMutex
	factories map[string]func() vmapi.Driver
}{factories: map[string]func() vmapi.Driver{
	"lima": func() vmapi.Driver { return limaDriver{lima.New()} },
}}

// Register is for linked-in adapters, never dynamically loaded guest code.
func Register(kind string, factory func() vmapi.Driver) {
	registry.Lock()
	defer registry.Unlock()
	if kind == "" || factory == nil || registry.factories[kind] != nil {
		panic("invalid or duplicate VM adapter: " + kind)
	}
	registry.factories[kind] = factory
}

// BackendNames returns the adapters linked into this host binary. Listing
// choices never instantiates a driver or probes installed tools/VMs.
func BackendNames() []string {
	registry.RLock()
	names := make([]string, 0, len(registry.factories))
	for name := range registry.factories {
		names = append(names, name)
	}
	registry.RUnlock()
	sort.Strings(names)
	return names
}

// DefaultBackend applies only to new spaces. Empty persisted backends still
// identify legacy Lima spaces and must never select a different hypervisor.
func DefaultBackend() string {
	registry.RLock()
	defer registry.RUnlock()
	if registry.factories["firecracker"] != nil {
		return "firecracker"
	}
	return "lima"
}

func Driver(kind string) (vmapi.Driver, error) {
	if kind == "" {
		kind = "lima"
	}
	registry.RLock()
	f := registry.factories[kind]
	registry.RUnlock()
	if f == nil {
		return nil, fmt.Errorf("VM backend %q is unavailable in this build", kind)
	}
	return f(), nil
}

// ValidateInstancePaths runs an adapter's optional read-only boot preflight.
// Empty backend selection retains the legacy Lima meaning.
func ValidateInstancePaths(kind, space string) error {
	d, err := Driver(kind)
	if err != nil {
		return err
	}
	if validator, ok := d.(interface{ ValidateInstancePaths(string) error }); ok {
		return validator.ValidateInstancePaths(space)
	}
	return nil
}

// ValidateNewInstancePaths checks the destination home before a new bootstrap
// exists. Existing-space checks always use its recorded/legacy placement.
func ValidateNewInstancePaths(kind, space string) error {
	if err := layout.ValidateName("space", space); err != nil {
		return err
	}
	d, err := Driver(kind)
	if err != nil {
		return err
	}
	if d.Kind() == "lima" {
		return lima.ValidateNewInstancePaths(space)
	}
	return nil
}

func ForSpace(space string) (vmapi.Driver, error) {
	if err := spaceenv.RequireSpace(space); spaceenv.Native() && err != nil {
		return nil, err
	}
	if spaceenv.Native() {
		return Driver("lima")
	}
	b, err := spaceconfig.LoadBootstrap(space)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, spaceconfig.ErrLegacySchema) {
		return Driver("lima")
	}
	if err != nil {
		return nil, err
	}
	return Driver(b.VM.Backend)
}

// ResolveImage is explicit lifecycle preparation. Read/start paths use the
// persisted immutable path and never fetch or select a different image.
func ResolveImage(ctx context.Context, kind, source string) (string, error) {
	d, err := Driver(kind)
	if err != nil {
		return "", err
	}
	if resolver, ok := d.(interface {
		ResolveImage(context.Context, string) (string, error)
	}); ok {
		return resolver.ResolveImage(ctx, source)
	}
	if source != "" {
		return "", errors.New("this VM adapter does not accept an image bundle")
	}
	return "", nil
}
func forDisk(id string) (vmapi.Driver, error) {
	d, _, err := forDiskBootstrap(id)
	return d, err
}

// forDiskBootstrap also returns the bootstrap declaring the disk, or nil for
// legacy standalone disk tooling.
func forDiskBootstrap(id string) (vmapi.Driver, *spaceconfig.Bootstrap, error) {
	found, err := spaceconfig.BootstrapForDisk(id)
	if err != nil {
		return nil, nil, err
	}
	if found != nil {
		d, err := Driver(found.VM.Backend)
		return d, found, err
	}
	d, err := Driver("lima") // legacy standalone disk tooling
	return d, nil, err
}
func failedCommand(ctx context.Context, err error) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$1" >&2; exit 2`, "vm-error", err.Error())
}
func ShellCommand(ctx context.Context, space string, args ...string) *exec.Cmd {
	return ShellCommandWithWorkdir(ctx, space, "", args...)
}
func ShellCommandWithWorkdir(ctx context.Context, space, dir string, args ...string) *exec.Cmd {
	d, err := ForSpace(space)
	if err != nil {
		return failedCommand(ctx, err)
	}
	return d.Shell(ctx, space, dir, args)
}
func ControllerService(ctx context.Context, space, action string) *exec.Cmd {
	d, err := ForSpace(space)
	if err != nil {
		return failedCommand(ctx, err)
	}
	return d.ControllerService(ctx, space, action)
}
func InstanceDir(space string) (string, error) {
	d, e := ForSpace(space)
	if e != nil {
		return "", e
	}
	return d.InstanceDir(space)
}
func InstanceConfigPath(space string) (string, error) {
	d, e := ForSpace(space)
	if e != nil {
		return "", e
	}
	return d.ConfigPath(space)
}
func LogPath(space string) (string, error) {
	d, e := ForSpace(space)
	if e != nil {
		return "", e
	}
	return d.LogPath(space)
}
func VMProcessIDs(space string) ([]int, error) {
	d, e := ForSpace(space)
	if e != nil {
		return nil, e
	}
	return d.ProcessIDs(space)
}
func RequireVMProcessesStopped(space, kind string, pids []int) error {
	d, e := ForSpace(space)
	if e != nil {
		return e
	}
	return d.RequireStopped(space, kind, pids)
}
func AccountDiskDir(id string) (string, error) {
	d, e := forDisk(id)
	if e != nil {
		return "", e
	}
	return d.AccountDiskDir(id)
}

type Client struct{}

func New() *Client { return &Client{} }
func (*Client) CreateWithSpec(ctx context.Context, name string, spec Spec) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.Create(ctx, name, spec)
}
func (*Client) Start(ctx context.Context, name string) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.Start(ctx, name)
}
func (*Client) Stop(ctx context.Context, name string) error { return New().ForceStop(ctx, name, false) }
func (*Client) ForceStop(ctx context.Context, name string, force bool) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.Stop(ctx, name, force)
}
func (*Client) Delete(ctx context.Context, name string, force bool) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.Delete(ctx, name, force)
}
func (*Client) CopyTo(ctx context.Context, name, src, dst string) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.CopyTo(ctx, name, src, dst)
}
func (*Client) SetInstanceMounts(name string, m []Mount) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.SetMounts(name, m)
}

// SetResources applies a stopped space's boot CPU count and memory through its
// persisted backend. The caller holds the host lifecycle lock.
func (*Client) SetResources(ctx context.Context, name string, r Resources) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.SetResources(ctx, name, r)
}
func (*Client) SetAccountDisk(name, id string, format bool) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.SetAccountDisk(name, id, format)
}
func (*Client) AccountDisk(ctx context.Context, id string) (*DiskInfo, error) {
	d, e := forDisk(id)
	if e != nil {
		return nil, e
	}
	return d.AccountDisk(ctx, id)
}
func (*Client) RequireAccountDisk(ctx context.Context, name, id string) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	return d.RequireAccountDisk(ctx, name, id)
}

// CreateAccountDisk allocates the data disk at the size its space saved.
func (*Client) CreateAccountDisk(ctx context.Context, id string) error {
	d, b, e := forDiskBootstrap(id)
	if e != nil {
		return e
	}
	size := spaceconfig.DefaultDataDisk
	if b != nil {
		if size, e = spaceconfig.DataDiskBytes(b.VM.DataDisk); e != nil {
			return e
		}
	}
	return d.CreateAccountDisk(ctx, id, size)
}
func (*Client) DeleteAccountDisk(ctx context.Context, id string) error {
	d, e := forDisk(id)
	if e != nil {
		return e
	}
	return d.DeleteAccountDisk(ctx, id)
}
func (*Client) AccountDiskBlank(ctx context.Context, id string) (bool, error) {
	d, e := forDisk(id)
	if e != nil {
		return false, e
	}
	return d.AccountDiskBlank(ctx, id)
}
func (*Client) List(ctx context.Context) ([]Instance, error) {
	if spaceenv.Native() {
		return lima.New().List(ctx)
	}
	registry.RLock()
	kinds := make([]string, 0, len(registry.factories))
	for k := range registry.factories {
		kinds = append(kinds, k)
	}
	registry.RUnlock()
	sort.Strings(kinds)
	var out []Instance
	seen := map[string]bool{}
	for _, kind := range kinds {
		d, err := Driver(kind)
		if err != nil {
			return nil, err
		}
		rows, err := d.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if seen[row.Name] {
				return nil, fmt.Errorf("space %q has conflicting VM runtimes", row.Name)
			}
			seen[row.Name] = true
			out = append(out, row)
		}
	}
	return out, nil
}

// Lookup consults only the selected adapter. Foreign instance metadata is a
// conflict even when that backend is unavailable: never hide an orphan runtime
// or require unrelated hypervisors just to operate an existing space.
func (c *Client) Lookup(ctx context.Context, name string) (Instance, bool, error) {
	d, err := ForSpace(name)
	if err != nil {
		return Instance{}, false, err
	}
	return lookup(ctx, name, d)
}

// LookupBackend checks a requested backend before a new bootstrap exists.
// Creation/import must not implicitly select Lima during that interval.
func (c *Client) LookupBackend(ctx context.Context, name, backend string) (Instance, bool, error) {
	d, err := Driver(backend)
	if err != nil {
		return Instance{}, false, err
	}
	return lookup(ctx, name, d)
}

func lookup(ctx context.Context, name string, d vmapi.Driver) (Instance, bool, error) {
	list := d.List
	if spaceenv.Native() {
		// The guest is the selected VM. Host adapter inventories, such as
		// Lima's missing-limactl bootstrap scan, never run inside it.
		list = New().List
	} else {
		registry.RLock()
		factories := make([]func() vmapi.Driver, 0, len(registry.factories))
		for kind, factory := range registry.factories {
			if kind != d.Kind() {
				factories = append(factories, factory)
			}
		}
		registry.RUnlock()
		for _, factory := range factories {
			other := factory()
			dir, err := other.InstanceDir(name)
			if err != nil {
				return Instance{}, false, err
			}
			if _, err := os.Lstat(dir); err == nil {
				return Instance{}, false, fmt.Errorf("space %q has conflicting %s VM metadata", name, other.Kind())
			} else if !errors.Is(err, os.ErrNotExist) {
				return Instance{}, false, err
			}
		}
	}
	rows, err := list(ctx)
	if err != nil {
		return Instance{}, false, err
	}
	for _, row := range rows {
		if row.Name == name {
			return row, true, nil
		}
	}
	return Instance{}, false, nil
}

// Legacy YAML creation supports existing internal callers. New lifecycle
// uses CreateWithSpec, so hypervisor configuration never crosses that boundary.
func (*Client) Create(ctx context.Context, name string, template []byte) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	if d.Kind() != "lima" {
		return errors.New("legacy Lima template cannot configure another VM backend")
	}
	return lima.New().Create(ctx, name, template)
}
func (*Client) Ensure(ctx context.Context, name string, template []byte) error {
	d, e := ForSpace(name)
	if e != nil {
		return e
	}
	if d.Kind() != "lima" {
		return errors.New("legacy Lima ensure cannot configure another VM backend")
	}
	return lima.New().Ensure(ctx, name, template)
}

func InteractiveShell(ctx context.Context, space string, args []string) *exec.Cmd {
	d, e := ForSpace(space)
	if e != nil {
		return failedCommand(ctx, e)
	}
	return d.InteractiveShell(ctx, space, args)
}

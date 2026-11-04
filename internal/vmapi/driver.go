package vmapi

import (
	"context"
	"os/exec"
)

// Driver is a platform implementation of VM boot, owned storage and transport.
// Business operations and workload confinement remain in the guest controller.
// Implementations must never boot on read, shell, or copy operations.
type Driver interface {
	Kind() string
	List(context.Context) ([]Instance, error)
	Create(context.Context, string, Spec) error
	Start(context.Context, string) error
	Stop(context.Context, string, bool) error
	Delete(context.Context, string, bool) error
	Shell(context.Context, string, string, []string) *exec.Cmd
	InteractiveShell(context.Context, string, []string) *exec.Cmd
	CopyTo(context.Context, string, string, string) error
	InstanceDir(string) (string, error)
	ConfigPath(string) (string, error)
	LogPath(string) (string, error)
	SetMounts(string, []Mount) error
	// SetResources replaces only a stopped VM's boot CPU count and memory; its
	// disks, grants, image and identity stay. It refuses a live, paused,
	// starting or uncertain VM with ErrNotStopped and takes effect on the next
	// start. Equal values rewrite nothing.
	SetResources(context.Context, string, Resources) error
	SetAccountDisk(string, string, bool) error
	AccountDisk(context.Context, string) (*DiskInfo, error)
	RequireAccountDisk(context.Context, string, string) error
	// CreateAccountDisk allocates a new, unformatted data disk of size bytes.
	CreateAccountDisk(context.Context, string, uint64) error
	DeleteAccountDisk(context.Context, string) error
	AccountDiskBlank(context.Context, string) (bool, error)
	AccountDiskDir(string) (string, error)
	ProcessIDs(string) ([]int, error)
	RequireStopped(string, string, []int) error
	// ControllerService manages only the fixed controller service. The command
	// result/stdio conventions match the existing deployment transaction.
	ControllerService(context.Context, string, string) *exec.Cmd
}

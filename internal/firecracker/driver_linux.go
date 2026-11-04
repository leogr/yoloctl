//go:build linux && amd64

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

type Driver struct{}

var _ vmapi.Driver = Driver{}

func (Driver) Kind() string                            { return "firecracker" }
func (Driver) InstanceDir(name string) (string, error) { return layout.FirecrackerInstanceDir(name) }
func (d Driver) ConfigPath(name string) (string, error) {
	p, e := d.InstanceDir(name)
	return filepath.Join(p, "vm.json"), e
}
func (d Driver) LogPath(name string) (string, error) {
	p, e := d.InstanceDir(name)
	return filepath.Join(p, "vm.log"), e
}
func (d Driver) List(ctx context.Context) ([]vmapi.Instance, error) {
	home, e := layout.FirecrackerHome()
	if e != nil {
		return nil, e
	}
	items, e := os.ReadDir(filepath.Join(home, "instances"))
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var rows []vmapi.Instance
	for _, item := range items {
		if !item.IsDir() {
			return nil, errors.New("invalid Firecracker instance directory")
		}
		dir, e := d.InstanceDir(item.Name())
		if e != nil {
			return nil, e
		}
		c, e := ReadConfig(dir)
		if e != nil {
			return nil, e
		}
		if c.Name != item.Name() {
			return nil, errors.New("VM identity mismatch")
		}
		live, e := running(dir)
		if e != nil {
			return nil, e
		}
		status := "Stopped"
		detail := ""
		if live {
			status = "Running"
			s, err := readState(dir)
			if err != nil {
				return nil, err
			}
			if err := checkHostStoragePause(ctx, s); err != nil {
				if !errors.Is(err, vmapi.ErrHostStoragePaused) {
					return nil, err
				}
				status, detail = "Paused", err.Error()
			}
		}
		mem, e := Bytes(c.Spec.Memory)
		if e != nil {
			return nil, e
		}
		disk, e := Bytes(c.Spec.Disk)
		if e != nil {
			return nil, e
		}
		rows = append(rows, vmapi.Instance{Name: c.Name, Status: status, StatusDetail: detail, VMType: d.Kind(), Arch: "x86_64", CPUs: c.Spec.CPUs, Memory: mem, Disk: disk, Dir: dir})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}
func (d Driver) Create(ctx context.Context, name string, spec vmapi.Spec) error {
	image, files, e := cachedImage(spec.Image)
	if e != nil {
		return e
	}
	if e = vmapi.ValidateResources(d.Kind(), vmapi.Resources{CPUs: spec.CPUs, Memory: spec.Memory}); e != nil {
		return e
	}
	size, e := Bytes(spec.Disk)
	if e != nil {
		return e
	}
	boot, e := spaceconfig.LoadBootstrap(name)
	if e != nil {
		return e
	}
	if boot.DiskID != spec.DiskID || boot.VM.Backend != d.Kind() {
		return errors.New("VM spec differs from bootstrap")
	}
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return e
	}
	if e = requireHostStorageFor(vmapi.FormatAllocation(d.Kind(), uint64(size)), filepath.Dir(dir)); e != nil {
		return e
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	c := Config{Schema: 1, Name: name, Spec: spec, UID: boot.UID, GID: boot.GID, Executable: exe, Image: image, ImageFiles: files}
	// Publish recoverable metadata before allocating the disposable system disk.
	if e = WriteConfig(dir, c); e != nil {
		return e
	}
	if e = sparse(filepath.Join(dir, "system.ext4"), int64(size)); e != nil {
		return e
	}
	if e = formatDisk(ctx, filepath.Join(dir, "system.ext4")); e != nil {
		return e
	}
	return createKeys(dir)
}
func (d Driver) SetMounts(name string, m []vmapi.Mount) error {
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	live, e := running(dir)
	if e != nil {
		return e
	}
	if live {
		return errors.New("stop the VM before changing host grants")
	}
	c, e := ReadConfig(dir)
	if e != nil {
		return e
	}
	c.Spec.Mounts = m
	return WriteConfig(dir, c)
}

// SetResources holds the instance lock, as a starting or running helper does,
// so no launch can begin between the stopped check and the rewrite.
func (d Driver) SetResources(_ context.Context, name string, r vmapi.Resources) error {
	if e := vmapi.ValidateResources(d.Kind(), r); e != nil {
		return e
	}
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	if _, e = ReadConfig(dir); e != nil {
		return e // no lock file is created for a missing or invalid instance
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if errors.Is(e, unix.EWOULDBLOCK) {
		return fmt.Errorf("%w: %v", vmapi.ErrNotStopped, e)
	}
	if e != nil {
		return e
	}
	defer func() { _ = lock.Close() }()
	live, e := running(dir)
	if e != nil {
		return fmt.Errorf("%w: %v", vmapi.ErrNotStopped, e)
	}
	if live {
		return vmapi.ErrNotStopped
	}
	c, e := ReadConfig(dir)
	if e != nil {
		return e
	}
	if c.Name != name {
		return errors.New("firecracker VM configuration names another space")
	}
	if c.Spec.CPUs == r.CPUs && c.Spec.Memory == r.Memory {
		return nil
	}
	c.Spec.CPUs, c.Spec.Memory = r.CPUs, r.Memory
	return WriteConfig(dir, c)
}
func (d Driver) SetAccountDisk(name, id string, format bool) error {
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	c, e := ReadConfig(dir)
	if e != nil {
		return e
	}
	if c.Spec.DiskID != id {
		return errors.New("cannot replace a VM's owned disk")
	}
	if format {
		live, e := running(dir)
		if e != nil {
			return e
		}
		if live {
			return errors.New("cannot initialize a running VM's disk")
		}
		b, e := spaceconfig.LoadBootstrap(name)
		if e != nil {
			return e
		}
		if !b.Initializing || b.DiskUUID != "" {
			return errors.New("disk initialization is not authorized")
		}
	}
	c.Spec.Format = format
	return WriteConfig(dir, c)
}
func (d Driver) Start(ctx context.Context, name string) error {
	kvm, e := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if e != nil {
		return fmt.Errorf("firecracker requires accessible Linux /dev/kvm: %w", e)
	}
	_ = kvm.Close()
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	live, e := running(dir)
	if e != nil {
		return e
	}
	if live {
		s, err := readState(dir)
		if err != nil {
			return err
		}
		return checkHostStoragePause(ctx, s)
	}
	c, e := ReadConfig(dir)
	if e != nil {
		return e
	}
	dataDir, e := d.AccountDiskDir(c.Spec.DiskID)
	if e != nil {
		return e
	}
	if e = requireHostStorage(filepath.Join(dir, "system.ext4"), filepath.Join(dataDir, "data.ext4")); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	c.Executable = exe
	if e = WriteConfig(dir, c); e != nil {
		return e
	}
	log, e := os.OpenFile(filepath.Join(dir, "vm.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = log.Close() }()
	helper := exec.Command(exe, "vm-internal", "helper", name)
	helper.Stdout, helper.Stderr = log, log
	helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = helper.Start(); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- helper.Wait() }()
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		probe, stop := context.WithTimeout(ready, time.Second)
		out, err := d.Shell(probe, name, "", []string{"test", "-f", "/run/yoloctl-vm/ready"}).CombinedOutput()
		stop()
		if err == nil {
			return nil
		}
		// A paused boot remains live with its RAM intact. Never turn storage
		// pressure into forced termination at the normal readiness deadline.
		if s, err := readState(dir); err == nil {
			if err = checkHostStoragePause(ctx, s); errors.Is(err, vmapi.ErrHostStoragePaused) {
				return err
			}
		}
		select {
		case e := <-done:
			return fmt.Errorf("VM helper exited (%v); inspect %s: %s", e, filepath.Join(dir, "vm.log"), out)
		case <-ready.Done():
			// The caller may already be canceled. Use a separate bounded read
			// before destroying a child which the storage guard may have paused.
			if s, err := readState(dir); err == nil {
				if err = checkHostStoragePause(context.WithoutCancel(ctx), s); err != nil {
					return errors.Join(ready.Err(), err)
				}
			}
			// The helper can still be preparing boot media before runtime.json
			// exists. Stop the process we launched even in that publication gap.
			_ = helper.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = helper.Process.Kill()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					return errors.Join(ready.Err(), errors.New("VM helper termination could not be established"))
				}
			}
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return errors.Join(ready.Err(), d.Stop(cleanup, name, true))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (d Driver) Stop(ctx context.Context, name string, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	s, e := readState(dir)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = checkHostStoragePause(ctx, s); e != nil {
		return e
	}
	var shutdownErr error
	if force {
		if live, e := s.VM.alive(); e != nil {
			return e
		} else if live {
			if e = s.VM.signal(unix.SIGKILL); e != nil {
				return e
			}
		}
		if live, e := s.Helper.alive(); e != nil {
			return e
		} else if live {
			if e = s.Helper.signal(unix.SIGTERM); e != nil {
				return e
			}
		}
	} else {
		live, e := s.VM.alive()
		if e != nil {
			return e
		}
		if live {
			cmd := d.Shell(ctx, name, "", []string{"sudo", "/sbin/yoloctl-init", "poweroff"})
			if b, e := cmd.CombinedOutput(); e != nil {
				// Poweroff can close SSH before its successful reply arrives.
				// Observe termination without replaying or racing a disk flush.
				shutdownErr = fmt.Errorf("guest shutdown: %w: %s", e, b)
			}
		}
	}
	for {
		live, e := running(dir)
		if e != nil {
			return e
		}
		if !live {
			return nil
		}
		if e = checkHostStoragePause(ctx, s); e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), shutdownErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (d Driver) Delete(ctx context.Context, name string, force bool) error {
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	live, e := running(dir)
	if e != nil {
		return e
	}
	if live {
		if !force {
			return errors.New("VM is still running")
		}
		if e = d.Stop(ctx, name, true); e != nil {
			return e
		}
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if e != nil {
		return e
	}
	defer func() { _ = lock.Close() }()
	if e = os.RemoveAll(dir); e != nil {
		return e
	}
	return syncDir(filepath.Dir(dir))
}
func (d Driver) ProcessIDs(name string) ([]int, error) {
	dir, e := d.InstanceDir(name)
	if e != nil {
		return nil, e
	}
	s, e := readState(dir)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var ids []int
	for _, p := range []process{s.Helper, s.VM} {
		live, e := p.alive()
		if e != nil {
			return nil, e
		}
		if live {
			ids = append(ids, p.PID)
		}
	}
	return ids, nil
}
func (d Driver) RequireStopped(name, kind string, before []int) error {
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	live, e := running(dir)
	if e != nil {
		return e
	}
	if live {
		return errors.New("VM processes have not terminated")
	}
	return nil
}
func fail(_ context.Context, e error) *exec.Cmd {
	return &exec.Cmd{Path: "yoloctl-vm-error", Err: e}
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (d Driver) Shell(ctx context.Context, name, workdir string, args []string) *exec.Cmd {
	return d.shell(ctx, name, workdir, args, len(args) == 0)
}
func (d Driver) shell(ctx context.Context, name, workdir string, args []string, pty bool) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return fail(ctx, err)
	}
	mode := "pipe"
	if pty {
		mode = "tty"
	}
	tail := []string{"vm-internal", "ssh", name, workdir, mode}
	tail = append(tail, args...)
	return exec.CommandContext(ctx, exe, tail...)
}
func (d Driver) CopyTo(ctx context.Context, name, src, dst string) error {
	f, e := os.Open(src)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	cmd := d.Shell(ctx, name, "", []string{"sh", "-c", "umask 077; cat > " + quote(dst)})
	cmd.Stdin = f
	out, e := cmd.CombinedOutput()
	if e != nil {
		return fmt.Errorf("copy VM file: %w: %s", e, out)
	}
	return nil
}
func (d Driver) ControllerService(ctx context.Context, name, action string) *exec.Cmd {
	switch action {
	case "state", "stop", "start", "install":
		return d.Shell(ctx, name, "", []string{"sudo", "/sbin/yoloctl-init", "controller", action})
	default:
		return fail(ctx, errors.New("invalid controller service action"))
	}
}

func (d Driver) InteractiveShell(ctx context.Context, name string, args []string) *exec.Cmd {
	return d.shell(ctx, name, "", args, true)
}

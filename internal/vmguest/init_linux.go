//go:build linux

// Package vmguest is the guest's PID 1 and fixed service supervisor. It never
// runs on the host, resolves declarations or launches agent workloads itself.
package vmguest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

var shutdownOnce sync.Once

func Run(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("guest supervisor requires root")
	}
	if len(args) > 0 {
		if len(args) == 1 && args[0] == "unit" {
			var req vmwire.UnitRequest
			dec := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
			dec.DisallowUnknownFields()
			if e := dec.Decode(&req); e != nil {
				return e
			}
			return sendControl(vmwire.ControlRequest{Action: "unit", Unit: &req})
		}

		if args[0] == "buildkit" && len(args) == 1 {
			return control("buildkit")
		}
		if args[0] == "ports" && len(args) == 1 {
			return control("ports")
		}
		if args[0] == "poweroff" && len(args) == 1 {
			return control("poweroff")
		}
		if args[0] == "controller" && len(args) == 2 {
			return control(args[1])
		}
		return errors.New("unknown guest service action")
	}
	if os.Getpid() != 1 {
		return errors.New("guest init requires PID 1")
	}
	if e := boot(); e != nil {
		fmt.Fprintln(os.Stderr, "BOOT FAILED:", e)
		unix.Sync()
		_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
		return e
	}
	select {}
}
func mount(source, target, kind, data string, flags uintptr) error {
	if kind == "tmpfs" && data == "" {
		data = "mode=0755"
	}
	if e := os.MkdirAll(target, 0755); e != nil {
		return e
	}
	e := unix.Mount(source, target, kind, flags, data)
	if errors.Is(e, unix.EBUSY) && kind == "devtmpfs" {
		var st unix.Statfs_t
		if unix.Statfs(target, &st) == nil && st.Type == unix.TMPFS_MAGIC {
			return nil
		}
	}
	return e
}
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("%s: %w: %s", name, e, out)
	}
	return nil
}
func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if e := startOwned(cmd); e != nil {
		return e
	}
	go func() {
		e := cmd.Wait()
		ownedChildren.Delete(cmd.Process.Pid)
		fmt.Fprintln(os.Stderr, "critical service exited:", name, e)
		shutdown()
	}()
	return nil
}
func copyFile(src, dst string, mode os.FileMode) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer func() { _ = in.Close() }()
	return replaceFile(dst, in, mode)
}
func writeFile(dst string, data []byte, mode os.FileMode) error {
	return replaceFile(dst, strings.NewReader(string(data)), mode)
}
func replaceFile(dst string, src io.Reader, mode os.FileMode) error {
	out, e := os.CreateTemp(filepath.Dir(dst), ".yoloctl-")
	if e != nil {
		return e
	}
	defer func() { _ = os.Remove(out.Name()) }()
	_, e = io.Copy(out, src)
	e = errors.Join(e, out.Chmod(mode), out.Sync(), out.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(out.Name(), dst); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(dst))
	if e != nil {
		return e
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func boot() error {
	if e := mount("proc", "/proc", "proc", "", 0); e != nil {
		return e
	}
	line, e := os.ReadFile("/proc/cmdline")
	if e != nil || !strings.Contains(" "+string(line), " yoloctl.microvm=1") {
		return errors.New("dedicated microVM kernel marker missing")
	}
	for _, m := range [][3]string{{"sysfs", "/sys", "sysfs"}, {"devtmpfs", "/dev", "devtmpfs"}, {"tmpfs", "/run", "tmpfs"}, {"tmpfs", "/tmp", "tmpfs"}, {"tmpfs", "/mnt", "tmpfs"}} {
		if e = mount(m[0], m[1], m[2], "", 0); e != nil {
			return e
		}
	}
	if e = os.Chmod("/tmp", 01777); e != nil {
		return e
	}
	if e = mount("devpts", "/dev/pts", "devpts", "newinstance,ptmxmode=0666,mode=0620", 0); e != nil {
		return e
	}
	if e = mount("cgroup2", "/sys/fs/cgroup", "cgroup2", "", 0); e != nil {
		return e
	}
	if e = resources.PrepareWorkloads(); e != nil {
		return fmt.Errorf("prepare workload memory pool: %w", e)
	}
	if e = writableRoot(); e != nil {
		return e
	}
	for _, p := range []string{"/run/ssh", "/run/yoloctl-vm", "/run/sshd"} {
		if e = os.MkdirAll(p, 0755); e != nil {
			return e
		}
	}
	if e = mount("/dev/vdd", "/run/boot", "ext4", "", unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID); e != nil {
		return e
	}
	raw, e := os.ReadFile("/run/boot/boot.json")
	if e != nil {
		return e
	}
	var b vmwire.Boot
	if e = json.Unmarshal(raw, &b); e != nil {
		return e
	}
	if b.Schema != vmwire.Protocol || b.UID < 1 || b.GID < 1 {
		return errors.New("invalid boot record")
	}
	if _, e = layout.AccountDiskDir(b.DiskID); e != nil {
		return e
	}
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < 3 {
		return fmt.Errorf("landlock ABI >= 3 required: ABI %d, error %v", abi, errno)
	}
	now := b.UnixNano
	for _, word := range strings.Fields(string(line)) {
		if value, ok := strings.CutPrefix(word, "yoloctl.boot_unix_ns="); ok {
			now, e = strconv.ParseInt(value, 10, 64)
			if e != nil {
				return e
			}
		}
	}
	if now <= 0 {
		return errors.New("host boot clock missing")
	}
	clock := unix.NsecToTimeval(now)
	if e = unix.Settimeofday(&clock); e != nil {
		return e
	}
	for _, p := range []string{"/var/log", "/var/tmp", "/opt/yoloctl", "/etc/yoloctl", "/etc/openshell-tls", "/home/yolo"} {
		if e = os.MkdirAll(p, 0755); e != nil {
			return e
		}
	}
	if g := b.DataDiskGrow; g != nil {
		if e = growDataDisk(dataDiskDevice, *g, runGrowTool); e != nil {
			// The disk stays unmounted and the boot fails visibly; the
			// host keeps the grow pending, so the next start retries.
			return fmt.Errorf("data disk grow %d → %d bytes refused; the disk was not mounted: %w", g.From, g.To, e)
		}
	}
	if e = mount(dataDiskDevice, layout.AccountDiskMount(b.DiskID), "ext4", "", unix.MS_NOATIME); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(vmwire.GuestBinary), 0755); e != nil {
		return e
	}
	// The first install comes from this invocation's private boot disk. Existing
	// installations are upgraded only by the normal explicit controller flow.
	if _, e = os.Stat(vmwire.GuestBinary); errors.Is(e, os.ErrNotExist) {
		if e = copyFile("/run/boot/yoloctl", vmwire.GuestBinary, 0755); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	if e = unix.Sethostname([]byte(b.Name)); e != nil {
		return e
	}
	if e = os.WriteFile("/etc/hosts", []byte("127.0.0.1 localhost\n127.0.1.1 "+b.Name+"\n::1 localhost\n"), 0644); e != nil {
		return e
	}
	if e = identityFiles(b); e != nil {
		return e
	}
	if e = os.Setenv("PATH", vmwire.GuestSystemPath); e != nil {
		return e
	}
	if e = run("ip", "link", "set", "lo", "up"); e != nil {
		return e
	}
	if e = prepareTransport(); e != nil {
		return e
	}
	if e = network(); e != nil {
		return e
	}
	if e = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644); e != nil {
		return e
	}
	if e = os.WriteFile("/etc/resolv.conf", []byte("nameserver 192.168.127.1\n"), 0644); e != nil {
		return e
	}
	for i, m := range b.Mounts {
		if e = share(uint32(vmwire.SharePort+i), m.Location, m.MountPoint, m.Writable); e != nil {
			return e
		}
	}
	// Container snapshot layers must be outside the root overlay's upperdir.
	// Bind sibling directories on the disposable ext4 disk directly into /var.
	for _, path := range []string{"/var/lib/containerd", "/var/lib/buildkit"} {
		source := "/run/system/runtime/" + filepath.Base(path)
		if e = os.MkdirAll(filepath.Dir(source), 0755); e != nil {
			return e
		}
		if _, e = os.Lstat(source); errors.Is(e, os.ErrNotExist) {
			// Preserve disposable image/runtime data from earlier trial images.
			legacy := "/run/system/upper" + path
			if _, err := os.Lstat(legacy); err == nil {
				if e = os.Rename(legacy, source); e != nil {
					return e
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if e != nil {
			return e
		}
		if e = os.MkdirAll(source, 0755); e != nil {
			return e
		}
		if e = mount(source, path, "", "", unix.MS_BIND); e != nil {
			return e
		}
	}
	if e = configureBuilder(); e != nil {
		return e
	}
	if e = start("/usr/local/bin/containerd", "--config", "/etc/containerd/config.toml"); e != nil {
		return e
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if e = run("nerdctl", "info"); e == nil {
			break
		}
		if time.Now().After(deadline) {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e = start("/usr/sbin/sshd", "-D", "-e", "-f", "/etc/ssh/sshd_microvm_config"); e != nil {
		return e
	}
	if e = sshBridge(); e != nil {
		return e
	}
	if e = forwardBridge(); e != nil {
		return e
	}
	captureControllerBootSource()
	if e = listenControl(); e != nil {
		return e
	}
	go reapOrphans()
	fmt.Printf("YOLOCTL_VM_READY landlock_abi=%d\n", abi)
	return os.WriteFile("/run/yoloctl-vm/ready", []byte("1\n"), 0644)
}
func identityFiles(b vmwire.Boot) error {
	var e error
	for _, f := range []struct {
		path, entry  string
		field, value int
	}{{"/etc/passwd", fmt.Sprintf("yolo:x:%d:%d:yoloctl:/home/yolo:/bin/bash", b.UID, b.GID), 2, b.UID}, {"/etc/group", fmt.Sprintf("yolo:x:%d:", b.GID), 2, b.GID}} {
		old, e := os.ReadFile("/run/yoloctl-base/" + filepath.Base(f.path))
		if e != nil {
			return e
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(string(old)), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) > f.field && fields[0] != "yolo" && fields[f.field] != strconv.Itoa(f.value) {
				out = append(out, line)
			}
		}
		out = append(out, f.entry)
		if e = writeFile(f.path, []byte(strings.Join(out, "\n")+"\n"), 0644); e != nil {
			return e
		}
	}
	if e = os.Chown("/home/yolo", b.UID, b.GID); e != nil {
		return e
	}
	if e = writeFile("/etc/sudoers.d/yoloctl", []byte("yolo ALL=(ALL) NOPASSWD: ALL\n"), 0440); e != nil {
		return e
	}
	shadow, e := os.ReadFile("/run/yoloctl-base/shadow")
	if e != nil {
		return e
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(shadow)), "\n") {
		if !strings.HasPrefix(line, "yolo:") {
			lines = append(lines, line)
		}
	}
	lines = append(lines, "yolo::20000:0:99999:7:::")
	if e = writeFile("/etc/shadow", []byte(strings.Join(lines, "\n")+"\n"), 0600); e != nil {
		return e
	}
	if e = copyFile("/run/boot/authorized_keys", "/run/ssh/authorized_keys", 0644); e != nil {
		return e
	}
	if e = copyFile("/run/boot/host_ed25519", "/run/ssh/host_ed25519", 0600); e != nil {
		return e
	}
	return nil
}
func share(port uint32, source, dest string, writable bool) error {
	if dest == "" {
		dest = source
	}
	if !filepath.IsAbs(dest) || filepath.Clean(dest) != dest {
		return errors.New("invalid share destination")
	}
	if e := os.MkdirAll(dest, 0755); e != nil {
		return e
	}
	conn, e := dialHost(port)
	if e != nil {
		return e
	}
	opts := "slave,allow_other,default_permissions,cache=no,attr_timeout=0,entry_timeout=0,negative_timeout=0"
	if shareDebug {
		opts += ",debug,sshfs_debug"
	}
	if !writable {
		opts += ",ro"
	}
	cmd := exec.Command("sshfs", "-f", "-o", opts, ":/", dest)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = conn, conn, os.Stderr
	if e = startOwned(cmd); e != nil {
		_ = conn.Close()
		return e
	}
	done := make(chan error, 1)
	go func() { e := cmd.Wait(); ownedChildren.Delete(cmd.Process.Pid); _ = conn.Close(); done <- e }()
	until := time.Now().Add(5 * time.Second)
	for {
		var st unix.Statfs_t
		if unix.Statfs(dest, &st) == nil && st.Type == unix.FUSE_SUPER_MAGIC {
			go func() {
				e := <-done
				fmt.Fprintln(os.Stderr, "required host share exited:", e)
				shutdown()
			}()
			return nil
		}
		select {
		case e := <-done:
			return fmt.Errorf("mount host grant: %w", e)
		default:
		}
		if time.Now().After(until) {
			_ = cmd.Process.Kill()
			raw, _ := os.ReadFile("/proc/mounts")
			fmt.Fprintln(os.Stderr, string(raw))
			return errors.New("host grant mount timed out: " + dest)
		}
		time.Sleep(time.Millisecond)
	}
}
func sshBridge() error {
	ln, e := listenSSH(vmwire.SSHPort)
	if e != nil {
		return e
	}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			if !hostPeer(c) {
				_ = c.Close()
				continue
			}
			go func() {
				defer func() { _ = c.Close() }()
				target, e := net.Dial("tcp", "127.0.0.1:2222")
				if e != nil {
					return
				}
				defer func() { _ = target.Close() }()
				go func() { _, _ = io.Copy(target, c); _ = target.(*net.TCPConn).CloseWrite() }()
				_, _ = io.Copy(c, target)
			}()
		}
	}()
	return nil
}
func control(action string) error { return sendControl(vmwire.ControlRequest{Action: action}) }
func sendControl(req vmwire.ControlRequest) error {
	conn, e := net.Dial("unix", vmwire.GuestControl)
	if e != nil {
		return e
	}
	defer func() { _ = conn.Close() }()
	if e = json.NewEncoder(conn).Encode(req); e != nil {
		return e
	}
	var result vmwire.ControlReply
	if e = json.NewDecoder(conn).Decode(&result); e != nil {
		return e
	}
	if result.Error != "" {
		return errors.New(result.Error)
	}
	if result.State != "" {
		fmt.Println(result.State)
	}
	return nil
}
func listenControl() error {
	ln, e := net.Listen("unix", vmwire.GuestControl)
	if e != nil {
		return e
	}
	if e = os.Chmod(vmwire.GuestControl, 0600); e != nil {
		return e
	}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				var req vmwire.ControlRequest
				dec := json.NewDecoder(io.LimitReader(c, 1<<20))
				dec.DisallowUnknownFields()
				if dec.Decode(&req) != nil {
					return
				}
				var state string
				var e error
				if req.Action == "unit" && req.Unit != nil {
					state, e = manageUnit(*req.Unit)
				} else {
					state, e = service(req.Action)
				}
				result := vmwire.ControlReply{State: state}
				if e != nil {
					result.Error = e.Error()
				}
				_ = json.NewEncoder(c).Encode(result)

				if req.Action == "poweroff" && e == nil {
					go shutdown()
				}
			}()
		}
	}()
	return nil
}

func service(action string) (string, error) {
	if action == "buildkit" {
		return "", ensureBuilder()
	}
	if action == "ports" {
		ports, e := portInventory()
		if e != nil {
			return "", e
		}
		raw, e := json.Marshal(ports)
		return string(raw), e
	}
	switch action {
	case "install":
		return "", nil
	case "state":
		return controllers.state()
	case "start":
		return "", controllers.start()
	case "poweroff":
		controllers.shutdown()
		return "", controllers.stop()
	case "stop":
		return "", controllers.stop()
	default:
		return "", errors.New("unknown controller service action")
	}
}
func shutdown() {
	controllers.shutdown()
	// Explicit host lifecycle already drains sessions. Stop all remaining guest
	// processes before flushing native filesystems; PID 1 is excluded by kill(-1).
	shutdownOnce.Do(func() {
		_ = controllers.stop()
		_ = unix.Kill(-1, unix.SIGTERM)
		time.Sleep(100 * time.Millisecond)
		_ = unix.Kill(-1, unix.SIGKILL)
		unix.Sync()
		_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	})
}

// The base stays immutable. A separate disposable ext4 disk supplies the root
// overlay; the account/declaration disk is mounted independently afterwards.
func writableRoot() error {
	// Always derive infrastructure identities from the immutable base. A hard
	// poweroff during first boot must not make an incomplete upper file the
	// source for subsequent boots.
	if e := os.Mkdir("/run/yoloctl-base", 0700); e != nil {
		return e
	}
	for _, name := range []string{"passwd", "group", "shadow"} {
		if e := copyFile("/etc/"+name, "/run/yoloctl-base/"+name, 0600); e != nil {
			return e
		}
	}
	if e := mount("/dev/vdc", "/run/system", "ext4", "", unix.MS_NOATIME); e != nil {
		return e
	}
	for _, p := range []string{"/run/system/upper", "/run/system/work", "/run/root"} {
		if e := os.MkdirAll(p, 0755); e != nil {
			return e
		}
	}
	if e := mount("overlay", "/run/root", "overlay", "lowerdir=/,upperdir=/run/system/upper,workdir=/run/system/work", 0); e != nil {
		return e
	}
	if e := os.MkdirAll("/run/root/.oldroot", 0700); e != nil {
		return e
	}
	if e := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); e != nil {
		return e
	}
	if e := unix.PivotRoot("/run/root", "/run/root/.oldroot"); e != nil {
		return e
	}
	if e := os.Chdir("/"); e != nil {
		return e
	}
	for _, p := range []string{"proc", "sys", "dev", "run", "tmp", "mnt"} {
		if e := unix.Mount("/.oldroot/"+p, "/"+p, "", unix.MS_MOVE, ""); e != nil {
			return e
		}
	}
	return unix.Unmount("/.oldroot", unix.MNT_DETACH)
}

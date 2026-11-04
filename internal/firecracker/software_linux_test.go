//go:build linux && amd64

package firecracker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmshare"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

// Explicit isolated software gate. It substitutes a TCP carrier for vsock in a
// separately tagged guest, so it cannot establish Firecracker/KVM performance.
func TestSoftwareVM(t *testing.T) {
	image := os.Getenv("YOLOCTL_TEST_MICROVM_IMAGE")
	if image == "" {
		t.Skip("isolated software guest gate is opt-in")
	}
	artifact := os.Getenv("YOLOCTL_TEST_MICROVM_OUTPUT")
	if artifact == "" {
		t.Fatal("exclusive output directory required")
	}
	resume := os.Getenv("YOLOCTL_TEST_MICROVM_RESUME") == "1"
	if e := os.Mkdir(artifact, 0700); e != nil && !resume {
		t.Fatal(e)
	}
	bin := os.Getenv("YOLOCTL_TEST_MICROVM_CLI")
	guest := os.Getenv("YOLOCTL_TEST_MICROVM_GUEST")
	qemu := os.Getenv("YOLOCTL_TEST_MICROVM_QEMU")
	if bin == "" || guest == "" || qemu == "" {
		t.Fatal("CLI, tagged guest and QEMU paths required")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(artifact, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(artifact, "cache"))
	t.Setenv("LIMA_HOME", filepath.Join(artifact, "config/yoloctl/lima"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cli := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		// The normal CLI must not borrow the harness's Python/filesystem tools.
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(artifact, "empty-path"))
		out, e := cmd.CombinedOutput()
		t.Logf("CLI %v: %s", args, out)
		return out, e
	}
	project := filepath.Join(artifact, "project")
	if e := os.Mkdir(project, 0755); e != nil && !resume {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(project, "host.txt"), []byte("host grant\n"), 0644); e != nil {
		t.Fatal(e)
	}
	featureDir, e := layout.FeaturesDir()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(featureDir, 0700); e != nil {
		t.Fatal(e)
	}
	// An offline feature exercises the real controller -> container.Build path.
	// The loopback-only base below cannot be recovered by a registry pull when
	// BuildKit accidentally uses a different containerd namespace than nerdctl.
	if e = os.WriteFile(filepath.Join(featureDir, "local-build-probe.yaml"), []byte(`name: local-build-probe
install:
  kind: dockerfile
  dockerfile: |
    COPY <<EOF /local-build-probe
    local-build
    EOF
    LABEL org.yoloctl.test.local-build=verified
`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(featureDir, "local-fixture-probe.yaml"), []byte("name: local-fixture-probe\ninstall:\n  kind: dockerfile\n  dockerfile: 'RUN mkdir -p /home/sandbox/.npm-global && printf built > /home/sandbox/build-probe.txt'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	// Normal create publishes recoverable bootstrap and seed, then stops at the
	// unavailable KVM boundary. The harness supplies only the VM process/carrier.
	if !resume {
		if out, e := cli("space", "create", "software", "--account", "trial", "--workdir", project, "--backend", "firecracker", "--vm-image", image); e == nil || !strings.Contains(string(out), "/dev/kvm") {
			t.Fatalf("expected KVM-only boundary: %v %s", e, out)
		}
	}
	d := Driver{}
	dir, e := d.InstanceDir("software")
	if e != nil {
		t.Fatal(e)
	}
	if live, err := running(dir); err != nil || live {
		t.Fatalf("refusing to alter a running or uncertain test VM: %v", err)
	}
	c, e := ReadConfig(dir)
	if e != nil {
		t.Fatal(e)
	}
	previousCLI := c.Executable
	c.Executable = bin
	dataDir, e := d.AccountDiskDir(c.Spec.DiskID)
	if e != nil {
		t.Fatal(e)
	}
	if clean, err := blank(filepath.Join(dataDir, "data.ext4")); err != nil {
		t.Fatal(err)
	} else if clean {
		if e = formatDisk(ctx, filepath.Join(dataDir, "data.ext4")); e != nil {
			t.Fatal(e)
		}
	}
	c.Spec.Format = false
	if e = WriteConfig(dir, c); e != nil {
		t.Fatal(e)
	}
	if e = bootImage(ctx, dir, c); e != nil {
		t.Fatal(e)
	}
	rootfs := filepath.Join(artifact, "rootfs.ext4")
	var cmd *exec.Cmd
	if !resume {
		cmd = exec.CommandContext(ctx, "cp", "--reflink=auto", "--sparse=always", filepath.Join(image, "rootfs.ext4"), rootfs)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("copy: %s %v", out, e)
		}
	}
	if e = os.Chmod(rootfs, 0600); e != nil {
		t.Fatal(e)
	}
	cmds := filepath.Join(artifact, "guest.debugfs")
	if e = os.WriteFile(cmds, []byte("rm /usr/sbin/yoloctl-init\nwrite "+guest+" /usr/sbin/yoloctl-init\nset_inode_field /usr/sbin/yoloctl-init mode 0100755\nset_inode_field /usr/sbin/yoloctl-init uid 0\nset_inode_field /usr/sbin/yoloctl-init gid 0\n"), 0600); e != nil {
		t.Fatal(e)
	}
	cmd = exec.CommandContext(ctx, "debugfs", "-w", "-f", cmds, rootfs)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("guest patch: %s %v", out, e)
	}
	nw, e := virtualnetwork.New(&types.Configuration{MTU: 1500, Subnet: "192.168.127.0/24", GatewayIP: "192.168.127.1", GatewayMacAddress: "5a:94:ef:e4:0c:dd", GatewayVirtualIPs: []string{"192.168.127.1"}, Protocol: types.QemuProtocol})
	if e != nil {
		t.Fatal(e)
	}
	serve := func(port int, f func(net.Conn)) {
		ln, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, e := ln.Accept()
				if e != nil {
					return
				}
				go func() { defer func() { _ = c.Close() }(); f(c) }()
			}
		}()
	}
	serve(vmwire.NetworkPort, func(c net.Conn) { _ = nw.AcceptQemu(ctx, c) })
	for i, m := range c.Spec.Mounts {
		s, e := vmshare.Open(m.Location, m.Writable)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = s.Close() })
		serve(vmwire.SharePort+i, func(c net.Conn) {
			fmt.Fprintln(os.Stderr, "test share connected:", m.Location)
			e := s.Serve(c)
			fmt.Fprintln(os.Stderr, "test share ended:", e)
		})
	}
	portReservation, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := portReservation.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	_ = portReservation.Close()
	forwardReservation, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	forwardAddr := forwardReservation.Addr().String()
	_, forwardPort, _ := net.SplitHostPort(forwardAddr)
	_ = forwardReservation.Close()
	log, e := os.Create(filepath.Join(artifact, "serial.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = log.Close() }()
	args := []string{"-machine", "q35", "-accel", "tcg,tb-size=32", "-cpu", "max", "-m", "768", "-smp", "2", "-nodefaults", "-display", "none", "-serial", "stdio", "-no-reboot", "-monitor", "none", "-kernel", filepath.Join(image, "kernel"), "-append", "console=ttyS0 panic=-1 root=/dev/vda ro init=/sbin/yoloctl-init yoloctl.microvm=1 yoloctl.boot_unix_ns=" + strconv.FormatInt(time.Now().UnixNano(), 10), "-netdev", "user,id=test,hostfwd=tcp:127.0.0.1:" + port + "-:2223", "-device", "virtio-net-pci,netdev=test,romfile="}
	if data := os.Getenv("YOLOCTL_TEST_MICROVM_QEMU_DATA"); data != "" {
		args = append(args, "-L", data)
	}
	for i := range args {
		if strings.HasPrefix(args[i], "user,id=test,") {
			args[i] += ",hostfwd=tcp:127.0.0.1:" + forwardPort + "-:2224"
		}
	}
	for i, disk := range []string{rootfs, filepath.Join(dataDir, "data.ext4"), filepath.Join(dir, "system.ext4"), filepath.Join(dir, "boot.ext4")} {
		args = append(args, "-drive", fmt.Sprintf("file=%s,if=none,format=raw,cache=writeback,discard=unmap,id=d%d", disk, i), "-device", fmt.Sprintf("virtio-blk-pci,drive=d%d", i))
	}
	vm := exec.CommandContext(ctx, qemu, args...)
	vm.Stdout, vm.Stderr = log, log
	if e = vm.Start(); e != nil {
		t.Fatal(e)
	}
	vmDone := make(chan error, 1)
	go func() { vmDone <- vm.Wait() }()
	t.Cleanup(func() {
		select {
		case e := <-vmDone:
			t.Logf("VM exited before cleanup: %v", e)
		default:
			_ = vm.Process.Kill()
			t.Logf("test cleanup stopped VM: %v", <-vmDone)
		}
	})
	runtime, e := os.MkdirTemp("", "yfc-soft-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	me, e := identity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	vmpid, e := identity(vm.Process.Pid)
	if e != nil {
		t.Fatal(e)
	}
	if e = writeState(dir, runtimeState{Helper: me, VM: vmpid, Dir: runtime}); e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("unix", filepath.Join(runtime, "vsock"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				r := bufio.NewReader(c)
				line, e := r.ReadString('\n')
				if e != nil {
					return
				}
				destination := addr
				switch line {
				case "CONNECT 5253\n":
				case "CONNECT 5254\n":
					destination = forwardAddr
				default:
					return
				}
				target, e := net.Dial("tcp", destination)
				if e != nil {
					return
				}
				defer func() { _ = target.Close() }()
				_, _ = fmt.Fprint(c, "OK 1234\n")
				go func() { _, _ = io.Copy(target, r); _ = target.(*net.TCPConn).CloseWrite() }()
				_, _ = io.Copy(c, target)
			}()
		}
	}()
	defer func() {
		if live, _ := vmpid.alive(); !live {
			return
		}
		shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		cmd := exec.CommandContext(shutdown, bin, "space", "ssh", "software", "--root", "--", "/sbin/yoloctl-init", "poweroff")
		out, err := cmd.CombinedOutput()
		t.Logf("guest poweroff: %v %s", err, out)
		select {
		case e := <-vmDone:
			vmDone <- e
		case <-shutdown.Done():
		}
	}()
	// Wait using the actual host binary's SSH adapter, without asking it to boot.
	ready := time.Now().Add(90 * time.Second)
	for {
		if live, e := vmpid.alive(); e != nil || !live {
			t.Fatalf("VM exited: %v; inspect %s", e, artifact)
		}
		cmd := exec.CommandContext(ctx, bin, "space", "ssh", "software", "--", "test", "-f", "/run/yoloctl-vm/ready")
		out, e := cmd.CombinedOutput()
		if e == nil {
			break
		}
		if strings.Contains(string(out), "Permission denied (publickey)") || time.Now().After(ready) {
			t.Fatalf("guest did not become ready: %v %s; inspect %s", e, out, artifact)
		}
		time.Sleep(100 * time.Millisecond)
	}
	go forwardPorts(ctx, dir, runtime, func() bool { return false })
	startArgs := []string{"space", "start", "software"}
	if resume && previousCLI != bin {
		old := exec.CommandContext(ctx, previousCLI, startArgs...)
		if out, e := old.CombinedOutput(); e != nil {
			t.Fatalf("start previous controller before explicit update: %v %s", e, out)
		}
		startArgs = append(startArgs, "--update-controller")
	}
	if out, e := cli(startArgs...); e != nil {
		_, _ = cli("space", "ssh", "software", "--root", "--", "sh", "-c", "ps -eo pid,ppid,pgid,sid,args; cat /sys/fs/cgroup/yoloctl-controller/cgroup.events; cat /proc/meminfo; dmesg | tail -40")
		t.Fatalf("controller startup: %v %s", e, out)
	}
	if out, e := cli("account", "list", "--space", "software", "--json"); e != nil || !strings.Contains(string(out), "trial") {
		t.Fatalf("controller inventory: %v %s", e, out)
	}
	// No builder daemon is needed for a ready controller. Verify its installed
	// policy before the offline image builds below start it on demand.
	if out, e := cli("space", "ssh", "software", "--root", "--", "python3", "-c", `import os,tomllib
assert not os.path.exists('/run/buildkit/buildkitd.sock')
with open('/etc/buildkit/buildkitd.toml','rb') as f: c=tomllib.load(f)
w=c['worker']['containerd']; p,=w['gcpolicy']; s=os.statvfs('/'); size=s.f_blocks*s.f_frsize
assert w['enabled'] and w['namespace']=='default' and w['gc']
assert p['maxUsedSpace']==min(4*1024**3,size//10) and p['minFreeSpace']==size//5
print('LAZY_BUILDER_GC_OK')`); e != nil || !strings.Contains(string(out), "LAZY_BUILDER_GC_OK") {
		t.Fatalf("lazy builder GC: %v %s", e, out)
	}
	// This validates ext4 -> virtio discard -> host file reclamation through
	// QEMU's equivalent block settings; it is not a Firecracker/KVM gate.
	for _, disk := range []struct{ file, mount string }{
		{filepath.Join(dir, "system.ext4"), "/run/system"},
		{filepath.Join(dataDir, "data.ext4"), layout.AccountDiskMount(c.Spec.DiskID)},
	} {
		probe := disk.mount + "/.yoloctl-trim-probe"
		if out, err := cli("space", "ssh", "software", "--root", "--", "python3", "-c", `import os,sys
with open(sys.argv[1],'xb') as f:
    for _ in range(64): f.write(b'x'*1024**2)
    f.flush(); os.fsync(f.fileno())`, probe); err != nil {
			t.Fatalf("write trim fixture: %v %s", err, out)
		}
		var before, after unix.Stat_t
		if err := unix.Stat(disk.file, &before); err != nil {
			t.Fatal(err)
		}
		if out, err := cli("space", "ssh", "software", "--root", "--", "sh", "-c", "rm -- "+quote(probe)+" && sync && fstrim -- "+quote(disk.mount)); err != nil {
			t.Fatalf("guest trim: %v %s", err, out)
		}
		if err := unix.Stat(disk.file, &after); err != nil {
			t.Fatal(err)
		}
		if (before.Blocks-after.Blocks)*512 < 32<<20 {
			t.Fatalf("trim did not return host blocks for %s: before=%d after=%d", disk.file, before.Blocks, after.Blocks)
		}
		t.Logf("PASS software discard: %s reclaimed %d bytes", disk.mount, (before.Blocks-after.Blocks)*512)
	}
	if out, e := cli("space", "ssh", "software", "--", "sh", "-c", "cat "+quote(filepath.Join(project, "host.txt"))+"; printf guest > "+quote(filepath.Join(project, "guest.txt"))); e != nil || !strings.Contains(string(out), "host grant") {
		t.Fatalf("project share: %v %s", e, out)
	}
	if out, e := os.ReadFile(filepath.Join(project, "guest.txt")); e != nil || string(out) != "guest" {
		t.Fatalf("guest write missing: %v %s", e, out)
	}
	localBase := "127.0.0.1:1/yoloctl-local-base:gate"
	for _, build := range []struct{ base, tag string }{
		{"scratch", localBase},
		{localBase, "yoloctl/local-derived:gate"},
	} {
		if out, e := cli("image", "build", "--space", "software", "--base", build.base, "--tag", build.tag, "--features", "local-build-probe"); e != nil {
			t.Fatalf("offline build FROM %s: %v %s", build.base, e, out)
		}
		if out, e := cli("space", "ssh", "software", "--root", "--", "nerdctl", "image", "inspect", "--format", `{{index .Config.Labels "org.yoloctl.test.local-build"}}`, build.tag); e != nil || strings.TrimSpace(string(out)) != "verified" {
			t.Fatalf("built image not in the runtime store: %v %s", e, out)
		}
	}
	t.Log("PASS offline local base -> derived image through the controller build path")

	if fixture := os.Getenv("YOLOCTL_TEST_MICROVM_FIXTURE"); fixture != "" {
		if resume {
			// This explicit fixture name is owned only by this isolated gate.
			if out, e := cli("box", "list", "--space", "software", "--json"); e != nil {
				t.Fatalf("fixture inventory: %v %s", e, out)
			} else if strings.Contains(string(out), `"probe"`) {
				_, _ = cli("box", "logs", "probe", "--space", "software")
				if out, e = cli("box", "destroy", "probe", "--space", "software", "--force"); e != nil {
					t.Fatalf("old fixture cleanup: %v %s", e, out)
				}
			}
		}
		in, e := os.Open(fixture)
		if e != nil {
			t.Fatal(e)
		}
		load := exec.CommandContext(ctx, bin, "space", "ssh", "software", "--root", "--", "nerdctl", "load")
		load.Stdin = in
		raw, e := load.CombinedOutput()
		_ = in.Close()
		t.Logf("offline fixture import: %s", raw)
		if e != nil {
			t.Fatalf("offline image preparation: %v %s", e, raw)
		}
		if out, e := cli("box", "create", "probe", "--space", "software", "--agent", "none", "--account", "none", "--image", "yfc-fixture:latest", "--features", "local-fixture-probe", "--workdir", project, "--capability", "fs.pty.allocate", "--no-attach", "--no-autoupdate"); e != nil {
			_, _ = cli("box", "logs", "probe", "--space", "software")
			t.Fatalf("managed box create: %v %s", e, out)
		}
		// A finite setup-path diagnostic establishes that ordinary DAC permits
		// access; workload probes below use OpenShell's SSH launch path.
		if out, e := cli("space", "ssh", "software", "--root", "--", "nerdctl", "exec", "--user", "sandbox", "probe", "sh", "-c", "test -r /var/forbidden.txt && test -w /var/forbidden.txt"); e != nil {
			t.Fatalf("Landlock DAC control: %v %s", e, out)
		}
		probe := `import errno,os,pathlib,socket
assert os.getuid()!=0
assert pathlib.Path("host.txt").read_text()=="host grant\n"
assert pathlib.Path("/home/sandbox/build-probe.txt").read_text()=="built"
pathlib.Path("box.txt").write_text("confined")
s=pathlib.Path("/proc/self/status").read_text()
assert "NoNewPrivs:\t1" in s and "Seccomp:\t2" in s and "CapEff:\t0000000000000000" in s
for mode in ("r","a"):
 try:
  open("/var/forbidden.txt",mode)
 except PermissionError:
  pass
 else:
  raise AssertionError("Landlock allowed forbidden access")
try:
 socket.socket(socket.AF_VSOCK,socket.SOCK_STREAM)
except OSError as e:
 assert e.errno==errno.EPERM,e
else:
 raise AssertionError("workload can open host transport")
m,p=os.openpty(); os.write(p,b"pty"); assert os.read(m,3)==b"pty"; os.close(m); os.close(p)
print("CONFINED_BOX_OK",os.getuid(),os.getcwd())`
		if out, e := cli("box", "exec", "probe", "--space", "software", "--", "python3", "-c", probe); e != nil || !strings.Contains(string(out), "CONFINED_BOX_OK") {
			t.Fatalf("confined command: %v %s", e, out)
		}
		if out, e := os.ReadFile(filepath.Join(project, "box.txt")); e != nil || string(out) != "confined" {
			t.Fatalf("confined write missing: %v %s", e, out)
		}
		bridgeScript, e := os.ReadFile("../../internal/session/host_bridge.py")
		if e != nil {
			t.Fatal(e)
		}
		if out, e := cli("box", "exec", "probe", "--space", "software", "--", "python3", "-u", "-c", string(bridgeScript), "/tmp/yoloctl-browser-preflight"); e != nil || !strings.Contains(string(out), `"directory"`) {
			t.Fatalf("confined browser helper preflight: %v %s", e, out)
		}
		attach := exec.CommandContext(ctx, "python3", "../../scripts/smoke/pty-run.py", "--timeout", "180", "--output", filepath.Join(artifact, "attach.txt"), "--ready-text", "sandbox@", "--", bin, "box", "attach", "probe", "--space", "software", "--no-autoupdate")
		attach.Stdin = strings.NewReader("printf 'SESSION_OK\\n'; exit 0\r")
		if out, e := attach.CombinedOutput(); e != nil || !strings.Contains(string(out), "SESSION_OK") {
			_, _ = cli("space", "ssh", "software", "--root", "--", "sh", "-c", "ps -eo pid,ppid,pcpu,wchan:24,args; cat /var/log/yoloctl-units/yoloctl-session-probe.service.log")
			t.Fatalf("managed holder and terminal attach: %v %s", e, out)
		}
		reservation, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		published := reservation.Addr().(*net.TCPAddr).Port
		_ = reservation.Close()
		mapping := fmt.Sprintf("%d:18080", published)
		if out, e := cli("port", "forward", "probe", mapping, "--space", "software"); e != nil {
			t.Fatalf("port service: %v %s", e, out)
		}
		server := exec.CommandContext(ctx, bin, "box", "exec", "probe", "--space", "software", "--", "python3", "-c", `import socket; s=socket.socket(); s.bind(("0.0.0.0",18080)); s.listen(); c,_=s.accept(); c.sendall(b"FORWARD_OK"); c.close(); s.close()`)
		serverLog, e := os.Create(filepath.Join(artifact, "port-server.log"))
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = serverLog.Close() }()
		server.Stdout, server.Stderr = serverLog, serverLog
		if e = server.Start(); e != nil {
			t.Fatal(e)
		}
		defer func() { _ = server.Process.Kill() }()
		forwardOK := false
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", published), time.Second)
			if err == nil {
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				out, err := io.ReadAll(conn)
				_ = conn.Close()
				if err == nil && string(out) == "FORWARD_OK" {
					forwardOK = true
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !forwardOK {
			t.Fatal("host-to-confined-box forward failed; inspect port-server.log")
		}
		if e = server.Wait(); e != nil {
			t.Fatal("confined port server", e)
		}
		if out, e := cli("port", "unforward", "probe", mapping, "--space", "software"); e != nil {
			t.Fatalf("port teardown: %v %s", e, out)
		}
		if out, e := cli("box", "stop", "probe", "--space", "software"); e != nil {
			t.Fatalf("box stop: %v %s", e, out)
		}
		if out, e := cli("box", "start", "probe", "--space", "software", "--no-attach", "--no-autoupdate"); e != nil {
			t.Fatalf("box start: %v %s", e, out)
		}
		if out, e := cli("box", "exec", "probe", "--space", "software", "--", "cat", "box.txt"); e != nil || !strings.Contains(string(out), "confined") {
			t.Fatalf("box restart persistence: %v %s", e, out)
		}
		t.Log("PASS managed box, Landlock/seccomp/vsock denial, PTY allocation, terminal holder, port forwarding and box restart under QEMU TCG")
	}
	boot, e := spaceconfig.LoadBootstrap("software")
	if e != nil || boot.DiskUUID == "" {
		t.Fatal("native disk identity not published", e)
	}
	configRoot, _ := layout.ConfigRoot()
	t.Logf("PASS controller and project transport under QEMU TCG; config=%s. Firecracker/KVM execution and timing remain separate gates.", configRoot)
}

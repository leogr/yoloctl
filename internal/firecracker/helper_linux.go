//go:build linux && amd64

package firecracker

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/rotatelog"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmshare"
	"github.com/leogr/yoloctl/internal/vmwire"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

func createKeys(dir string) error {
	for _, name := range []string{"id_ed25519", "host_ed25519"} {
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		block, e := ssh.MarshalPrivateKey(key, "")
		if e != nil {
			return e
		}
		if e = safepath.WriteFile(dir, name, pem.EncodeToMemory(block), 0600); e != nil {
			return e
		}
		public, e := ssh.NewPublicKey(pub)
		if e != nil {
			return e
		}
		data := ssh.MarshalAuthorizedKey(public)
		leaf := "authorized_keys"
		if name == "host_ed25519" {
			leaf = "known_hosts"
			data = append([]byte("microvm "), data...)
		}
		if e = safepath.WriteFile(dir, leaf, data, 0600); e != nil {
			return e
		}
	}
	return nil
}
func bootImage(ctx context.Context, dir string, c Config) error {
	artifact, e := guestbinary.Resolve(c.Executable, "amd64")
	if e != nil {
		return e
	}
	b := vmwire.Boot{Schema: vmwire.Protocol, Name: c.Name, DiskID: c.Spec.DiskID, UID: c.UID, GID: c.GID, Mounts: c.Spec.Mounts}
	if b.DataDiskGrow, e = pendingDataDiskGrow(c); e != nil {
		return e
	}
	raw, e := json.Marshal(b)
	if e != nil {
		return e
	}
	h := sha256.New()
	_, _ = h.Write(raw)
	_, _ = h.Write([]byte(artifact.Sum))
	for _, key := range []string{"authorized_keys", "host_ed25519"} {
		keyBytes, err := safepath.ReadFile(dir, key, 16384)
		if err != nil {
			return err
		}
		_, _ = h.Write(keyBytes)
	}
	want := fmt.Sprintf("%x", h.Sum(nil))
	var cached struct {
		Digest string
		File   artifactStamp
	}
	if data, err := safepath.ReadFile(dir, "boot.identity.json", 8192); err == nil && strictJSON(data, &cached) == nil && cached.Digest == want {
		var st unix.Stat_t
		if unix.Lstat(filepath.Join(dir, "boot.ext4"), &st) == nil && stamp(st) == cached.File {
			return nil
		}
	}
	tree := imageTree{}
	tree.put("boot.json", 0600, raw)
	for dst, src := range map[string]string{"authorized_keys": filepath.Join(dir, "authorized_keys"), "host_ed25519": filepath.Join(dir, "host_ed25519"), "yoloctl": artifact.Path} {
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		tree[dst] = imageEntry{header: tar.Header{Name: dst, Mode: 0600, Size: st.Size(), Typeflag: tar.TypeReg}, open: func() (io.ReadCloser, error) { return os.Open(src) }}
	}
	tmp := filepath.Join(dir, "boot.new.ext4")
	if e = os.Remove(tmp); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = tree.ext4(ctx, tmp, 256<<20); e != nil {
		return fmt.Errorf("prepare boot disk: %w", e)
	}
	if e = os.Rename(tmp, filepath.Join(dir, "boot.ext4")); e != nil {
		return e
	}
	var st unix.Stat_t
	if e = unix.Lstat(filepath.Join(dir, "boot.ext4"), &st); e != nil {
		return e
	}
	cached.Digest, cached.File = want, stamp(st)
	record, e := json.Marshal(cached)
	if e != nil {
		return e
	}
	return safepath.WriteFile(dir, "boot.identity.json", record, 0600)
}
func Proxy(ctx context.Context, name string) error {
	dir, e := (Driver{}).InstanceDir(name)
	if e != nil {
		return e
	}
	s, e := readState(dir)
	if e != nil {
		return e
	}
	live, e := s.Helper.alive()
	if e != nil {
		return e
	}
	if !live {
		return errors.New("VM helper stopped")
	}
	if e = checkHostStoragePause(ctx, s); e != nil {
		return e
	}
	conn, e := DialGuest(ctx, filepath.Join(s.Dir, "vsock"), vmwire.SSHPort)
	if e != nil {
		return e
	}
	defer func() { _ = conn.Close() }()
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if half, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
	}()
	_, e = io.Copy(os.Stdout, conn)
	return e
}

// Helper owns exactly one VM, its private network and fixed host grants. It
// has no declaration/controller API and never installs a service on the host.
func Helper(parent context.Context, name string) (result error) {
	// Linux parent-death signals track the creating thread. Keep that thread
	// alive until its VMM has exited, even if Go retires other runtime threads.
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	d := Driver{}
	dir, e := d.InstanceDir(name)
	if e != nil {
		return e
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if e != nil {
		return e
	}
	defer func() { _ = lock.Close() }()
	if live, e := running(dir); e != nil {
		return e
	} else if live {
		return errors.New("VM is already running")
	}
	c, e := ReadConfig(dir)
	if e != nil {
		return e
	}
	if e = unchangedImage(c); e != nil {
		return e
	}
	dataDir, e := d.AccountDiskDir(c.Spec.DiskID)
	if e != nil {
		return e
	}
	dataLock, e := lockFile(filepath.Join(dataDir, "lock"))
	if e != nil {
		return e
	}
	defer func() { _ = dataLock.Close() }()
	storage, e := openBackingStorage(filepath.Join(dir, "system.ext4"), filepath.Join(dataDir, "data.ext4"))
	if e != nil {
		return e
	}
	defer storage.close()
	if e = storage.require(hostStorageStartFree); e != nil {
		return e
	}
	// This process lives with the VM. Redirect its inherited descriptors
	// here, never through a copier in the short-lived Start caller. The cap
	// preserves the existing four-MiB disk-grow search window across rolls.
	logWriter, e := rotatelog.Open(filepath.Join(dir, "vm.log"), logTail)
	if e != nil {
		return e
	}
	closeLogOutput, logsDrained, e := rotatelog.CaptureStdio(logWriter.BestEffort())
	if e != nil {
		_ = logWriter.Close()
		return e
	}
	producerRetired := true // before Firecracker starts
	// Own records enter the same pipe as complete raw marker writes. A
	// direct file path could splice a record between the relay's copy chunks.
	fileLog := logging.InitFile(os.Stderr)
	log := fileLog.Slog().With(prettylog.Actor(logging.VM), prettylog.Executor(logging.Firecracker))
	finishLog := helperLogFinalizer(fileLog, log, &result)
	defer func() {
		finishLog() // fallback for returns before the VM starts
		// Preserve the existing five-second retirement bound on unknown exit.
		// Do not close the reader or its sink under an unretired producer.
		closeLogOutput()
		if producerRetired {
			<-logsDrained
			_ = logWriter.Close()
		} else {
			go func() { <-logsDrained; _ = logWriter.Close() }()
		}
	}()
	if c.Spec.Format {
		boot, e := spaceconfig.LoadBootstrap(name)
		if e != nil {
			return e
		}
		// Formatting needs initialization authority and never meets a
		// grow, which exists only for an initialized disk.
		if !boot.Initializing || boot.DiskUUID != "" || boot.DataDiskGrow != nil {
			return errors.New("data disk initialization authority missing")
		}
		if e = formatDisk(ctx, filepath.Join(dataDir, "data.ext4")); e != nil {
			return e
		}
		c.Spec.Format = false
		if e = WriteConfig(dir, c); e != nil {
			return e
		}
	}
	if e = bootImage(ctx, dir, c); e != nil {
		return e
	}
	if e = storage.require(hostStorageStartFree); e != nil {
		return e
	}
	runtime, e := os.MkdirTemp("", "yfc-")
	if e != nil {
		return e
	}
	defer func() { _ = os.RemoveAll(runtime) }()
	me, e := identity(os.Getpid())
	if e != nil {
		return e
	}
	state := runtimeState{Helper: me, Dir: runtime}
	if e = writeState(dir, state); e != nil {
		return e
	}
	network, e := virtualnetwork.New(&types.Configuration{MTU: 1500, Subnet: "192.168.127.0/24", GatewayIP: "192.168.127.1", GatewayMacAddress: "5a:94:ef:e4:0c:dd", GatewayVirtualIPs: []string{"192.168.127.1"}, DHCPStaticLeases: map[string]string{"192.168.127.2": "5a:94:ef:e4:0c:ee"}, Protocol: types.QemuProtocol})
	if e != nil {
		return e
	}
	// Firecracker connects guest-initiated vsock ports to path_PORT. These
	// sockets reside in a random 0700 directory, never a public host listener.
	serve := func(port int, handler func(net.Conn)) error {
		ln, e := net.Listen("unix", filepath.Join(runtime, "vsock_")+strconv.Itoa(port))
		if e != nil {
			return e
		}
		go func() { <-ctx.Done(); _ = ln.Close() }()
		go func() {
			for {
				conn, e := ln.Accept()
				if e != nil {
					return
				}
				go func() { defer func() { _ = conn.Close() }(); handler(conn) }()
			}
		}()
		return nil
	}
	if e = serve(vmwire.NetworkPort, func(conn net.Conn) {
		if e := network.AcceptQemu(ctx, conn); ctx.Err() == nil {
			log.Warn("VM network", prettylog.Err(e))
			cancel()
		}
	}); e != nil {
		return e
	}
	for i, m := range c.Spec.Mounts {
		share, e := vmshare.Open(m.Location, m.Writable)
		if e != nil {
			return e
		}
		defer func() { _ = share.Close() }()
		if e = serve(vmwire.SharePort+i, func(conn net.Conn) {
			if e := share.Serve(conn); e != nil && e != io.EOF && ctx.Err() == nil {
				log.Warn("VM share", prettylog.Err(e))
			}
		}); e != nil {
			return e
		}
	}
	fc := exec.Command(filepath.Join(c.Spec.Image, "firecracker"), "--api-sock", filepath.Join(runtime, "api.sock"))
	fc.Stdout, fc.Stderr = os.Stdout, os.Stderr
	fc.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	state.LaunchPending = true
	if e = writeState(dir, state); e != nil {
		return e
	}
	if e = fc.Start(); e != nil {
		state.LaunchPending = false
		return errors.Join(e, writeState(dir, state))
	}
	producerRetired = false
	wait := make(chan error, 1)
	go func() { wait <- fc.Wait() }()
	defer func() {
		_ = fc.Process.Kill()
		select {
		case <-wait:
			producerRetired = true
			state.LaunchPending = false
			if e := writeState(dir, state); e != nil {
				log.Warn("Record VM termination", prettylog.Err(e))
			}
		case <-time.After(5 * time.Second):
		}
	}()
	// LIFO: enqueue/flush the final record before the existing kill/wait, not
	// just before process exit when unknown retirement cannot join the relay.
	defer finishLog()
	state.VM, e = identity(fc.Process.Pid)
	if e != nil {
		return e
	}
	state.LaunchPending = false
	if e = writeState(dir, state); e != nil {
		return e
	}
	api := filepath.Join(runtime, "api.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e = os.Stat(api); e == nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("firecracker API did not start")
		}
		select {
		case e := <-wait:
			producerRetired = true
			wait <- e
			return fmt.Errorf("firecracker exited: %w", e)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	mem, e := Bytes(c.Spec.Memory)
	if e != nil {
		return e
	}
	configs := []struct {
		path  string
		value any
	}{
		{"/machine-config", map[string]any{"vcpu_count": c.Spec.CPUs, "mem_size_mib": mem >> 20}},
		{"/boot-source", map[string]any{"kernel_image_path": filepath.Join(c.Spec.Image, "kernel"), "boot_args": "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro init=/sbin/yoloctl-init random.trust_cpu=on yoloctl.microvm=1 yoloctl.boot_unix_ns=" + strconv.FormatInt(time.Now().UnixNano(), 10)}},
	}
	for i, disk := range []struct {
		path string
		ro   bool
	}{{filepath.Join(c.Spec.Image, "rootfs.ext4"), true}, {filepath.Join(dataDir, "data.ext4"), false}, {filepath.Join(dir, "system.ext4"), false}, {filepath.Join(dir, "boot.ext4"), true}} {
		id := strconv.Itoa(i)
		configs = append(configs, struct {
			path  string
			value any
		}{"/drives/" + id, driveConfig(id, disk.path, i == 0, disk.ro)})
	}
	configs = append(configs, struct {
		path  string
		value any
	}{"/vsock", map[string]any{"guest_cid": 3, "uds_path": filepath.Join(runtime, "vsock")}})
	for _, req := range configs {
		if e = API(ctx, api, "PUT", req.path, req.value); e != nil {
			return e
		}
	}
	guard := &hostStorageGuard{check: storage.require, signal: state.VM.signal, log: log}
	if e = guard.serve(ctx, runtime); e != nil {
		return e
	}
	if e = storage.require(hostStorageStartFree); e != nil {
		return e
	}
	if e = API(ctx, api, "PUT", "/actions", map[string]string{"action_type": "InstanceStart"}); e != nil {
		return e
	}
	monitor, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); guard.watch(monitor) }()
	defer func() { stopMonitor(); <-monitorDone }()
	go forwardPorts(ctx, dir, runtime, func() bool { return guard.snapshot().Paused })
	select {
	case e := <-wait:
		producerRetired = true
		wait <- e
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
}

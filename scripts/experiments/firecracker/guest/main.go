//go:build linux

// A disposable Firecracker guest's PID 1. Never run on the host.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

const port = 5252

type request struct {
	Action     string `json:"action"`
	Nonce      string `json:"nonce"`
	DiskID     string `json:"disk_id"`
	DiskUUID   string `json:"disk_uuid"`
	Generation string `json:"generation"`
	UnixNano   int64  `json:"unix_nano"`
}

type response struct {
	OK          bool               `json:"ok"`
	Error       string             `json:"error,omitempty"`
	Nonce       string             `json:"nonce,omitempty"`
	Stage       string             `json:"stage"`
	Bundle      string             `json:"bundle"`
	StepsMS     map[string]float64 `json:"steps_ms,omitempty"`
	Meminfo     string             `json:"meminfo,omitempty"`
	Accounts    []string           `json:"accounts,omitempty"`
	Generation  string             `json:"generation,omitempty"`
	LandlockABI int                `json:"landlock_abi,omitempty"`
	OpenShell   bool               `json:"openshell_confinement_passed"`
}

type service struct {
	mu        sync.Mutex
	activated bool // no retry of an activation after an uncertain outcome
	ready     bool
	err       error
	bundle    string
}

func main() {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "this executable only runs as root PID 1 inside the disposable PoC VM")
		os.Exit(2)
	}
	if err := boot(); err != nil {
		fmt.Fprintln(os.Stderr, "POC FATAL:", err)
	}
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	for {
		time.Sleep(time.Hour)
	}
}

func mount(source, target, kind, data string, flags uintptr) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	err := unix.Mount(source, target, kind, flags, data)
	if errors.Is(err, unix.EBUSY) && kind == "devtmpfs" {
		// CONFIG_DEVTMPFS_MOUNT kernels mount /dev before invoking PID 1.
		// Accept only that exact existing filesystem, not arbitrary EBUSY.
		raw, readErr := os.ReadFile("/proc/mounts")
		if readErr == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 3 && fields[1] == target && fields[2] == kind {
					return nil
				}
			}
		}
	}
	return err
}

func boot() error {
	if err := mount("proc", "/proc", "proc", "", 0); err != nil {
		return err
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil || !strings.Contains(" "+string(cmdline), " yoloctl.firecracker-poc=1") {
		return errors.New("missing dedicated PoC kernel marker")
	}
	for _, m := range [][3]string{{"sysfs", "/sys", "sysfs"}, {"devtmpfs", "/dev", "devtmpfs"}} {
		if err := mount(m[0], m[1], m[2], "", 0); err != nil {
			return fmt.Errorf("mount %s: %w", m[1], err)
		}
	}
	for _, p := range []string{"/run", "/tmp", "/var", "/mnt", "/opt/yoloctl", "/etc/yoloctl", "/etc/openshell-tls", "/poc-workdir"} {
		if err := mount("tmpfs", p, "tmpfs", "mode=0755", 0); err != nil {
			return err
		}
	}
	if err := os.Chmod("/tmp", 0o1777); err != nil {
		return err
	}
	if err := os.Symlink("/run", "/var/run"); err != nil {
		return err
	}
	if err := mount("devpts", "/dev/pts", "devpts", "newinstance,ptmxmode=0666,mode=0620", 0); err != nil {
		return err
	}
	if err := mount("cgroup2", "/sys/fs/cgroup", "cgroup2", "", 0); err != nil {
		return err
	}
	for _, p := range []string{"agents", "features", "capabilities"} {
		if err := os.MkdirAll(filepath.Join(spaceenv.CatalogRoot, p), 0o755); err != nil {
			return err
		}
	}
	// All binaries are baked; no package manager, SSH or systemd runs at boot.
	if err := os.Setenv("PATH", "/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"); err != nil {
		return err
	}
	// A missing/disabled Landlock implementation can never produce ready.
	if _, err := landlockABI(); err != nil {
		return err
	}
	softwareSmoke := strings.Contains(string(cmdline), "yoloctl.software-smoke=1")
	fullSmoke := strings.Contains(string(cmdline), "yoloctl.full-smoke=1")
	if softwareSmoke && !fullSmoke {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := securityProbe(ctx); err != nil {
			return err
		}
		fmt.Println("POC_SOFTWARE_SECURITY_PASS")
		return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	}
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		return err
	}
	s := &service{bundle: bundle}
	if err := startService("containerd", "/usr/local/bin/containerd", "--config", "/poc/containerd.toml"); err != nil {
		return err
	}
	if softwareSmoke {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := until(ctx, func() error { return command(ctx, nil, "nerdctl", "info") }); err != nil {
			return err
		}
		var req request
		for _, field := range strings.Fields(string(cmdline)) {
			if value, found := strings.CutPrefix(field, "yoloctl.activation="); found {
				raw, err := hex.DecodeString(value)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					return err
				}
			}
		}
		s.ready = true
		result := s.handle(req)
		if !result.OK {
			return errors.New(result.Error)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Println("POC_SOFTWARE_CONTROLLER_PASS " + string(raw))
		return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		err := until(ctx, func() error { return command(ctx, nil, "nerdctl", "info") })
		s.mu.Lock()
		s.ready, s.err = err == nil, err
		s.mu.Unlock()
	}()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		return err
	}
	if err := unix.Listen(fd, 16); err != nil {
		return err
	}
	for {
		client, peer, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if err != nil {
			return err
		}
		addr, ok := peer.(*unix.SockaddrVM)
		if !ok || addr.CID != unix.VMADDR_CID_HOST {
			_ = unix.Close(client)
			continue
		}
		go func() {
			f := os.NewFile(uintptr(client), "host-vsock")
			defer func() { _ = f.Close() }()
			var req request
			dec := json.NewDecoder(io.LimitReader(f, 4096))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				_ = json.NewEncoder(f).Encode(response{Error: "invalid request"})
				return
			}
			_ = json.NewEncoder(f).Encode(s.handle(req))
		}()
	}
}

func command(ctx context.Context, input any, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewReader(raw)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, output)
	}
	return nil
}

func startService(label, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		err := cmd.Wait()
		fmt.Fprintln(os.Stderr, "POC service exited:", label, err)
		// A failed infrastructure service is fatal, never a successful benchmark.
		_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	}()
	return nil
}

func until(ctx context.Context, f func() error) error {
	for {
		if err := f(); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func validateActivation(r request) error {
	for _, value := range []string{r.DiskID, r.Generation, r.Nonce} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 16 {
			return errors.New("activation identities must be 16 random bytes in hex")
		}
	}
	if len(r.DiskUUID) != 36 || r.DiskUUID[8] != '-' || r.DiskUUID[13] != '-' || r.DiskUUID[18] != '-' || r.DiskUUID[23] != '-' || r.UnixNano <= 0 {
		return errors.New("invalid UUID or host clock")
	}
	if value, err := hex.DecodeString(strings.ReplaceAll(r.DiskUUID, "-", "")); err != nil || len(value) != 16 {
		return errors.New("invalid UUID")
	}
	return nil
}

func initialStore() controller.StoreBundle {
	return controller.StoreBundle{Schema: 1,
		Settings: spaceconfig.Settings{SchemaVersion: 1, Name: "fc-poc", DefaultAccount: "trial", DefaultWorkdir: "/poc-workdir"},
		Accounts: map[string][]byte{"trial": []byte("schema_version: 1\nname: trial\n")}, Boxes: map[string][]byte{}}
}

func landlockABI() (int, error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, fmt.Errorf("mandatory Landlock probe: %w", errno)
	}
	// Pinned OpenShell v0.0.72 targets ABI 2. Require at least ABI 3 here to
	// retain truncation support in the selected kernel as well.
	if abi < 3 {
		return int(abi), errors.New("PoC requires Landlock ABI >= 3")
	}
	return int(abi), nil
}

func securityProbe(ctx context.Context) error {
	if _, err := landlockABI(); err != nil {
		return err
	}
	if err := os.Chown("/poc-workdir", 2000, 2000); err != nil {
		return err
	}
	if err := os.WriteFile("/var/poc-forbidden.txt", []byte("synthetic fixture\n"), 0o666); err != nil {
		return err
	}
	if err := os.Chmod("/var/poc-forbidden.txt", 0o666); err != nil {
		return err
	}
	netns, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return err
	}
	if err := os.WriteFile("/tmp/poc-outer-netns", []byte(netns), 0o644); err != nil {
		return err
	}
	// Demonstrate ordinary DAC permits the same unprivileged UID's access.
	// This finite test control is not an agent workload or a sidecar.
	control := exec.CommandContext(ctx, "python3", "/poc/security-probe.py")
	control.Env = append(os.Environ(), "POC_DAC_CONTROL=1")
	control.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 2000, Gid: 2000}}
	if raw, err := control.CombinedOutput(); err != nil || !bytes.Contains(raw, []byte("POC_DAC_CONTROL_OK")) {
		return fmt.Errorf("unconfined DAC control failed: %v: %s", err, raw)
	}
	cmd := exec.CommandContext(ctx, "/usr/local/bin/openshell-sandbox", "--policy-rules", "/poc/policy.rego",
		"--policy-data", "/poc/security-policy.yaml", "--workdir", "/poc-workdir", "--log-level", "info",
		"--timeout", "30", "--", "/usr/bin/python3", "/poc/security-probe.py")
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	trace := strings.Contains(string(cmdline), "yoloctl.trace=1")
	if trace {
		args := append([]string{"-f", "-tt", "-s", "96", "-o", "/tmp/openshell.strace"}, cmd.Args...)
		cmd = exec.CommandContext(ctx, "strace", args...)
	}
	raw, err := cmd.CombinedOutput()
	// Keep all confinement diagnostics in serial.log on successes and failures.
	_, _ = os.Stdout.Write(raw)
	if trace {
		data, _ := os.ReadFile("/tmp/openshell.strace")
		if len(data) > 24000 {
			data = data[len(data)-24000:]
		}
		_, _ = os.Stdout.Write(data)
	}
	if err != nil {
		return fmt.Errorf("mandatory OpenShell security probe: %w", err)
	}
	if !bytes.Contains(raw, []byte("POC_OPENSHELL_SECURITY_OK ")) {
		return errors.New("OpenShell exited without the confinement proof")
	}
	// These messages are emitted by the pinned supervisor only after successful
	// setup. It otherwise continues with degraded network protection, which this
	// experiment must never count as a successful OpenShell compatibility gate.
	for _, proof := range []string{"Bypass detection rules installed", "TLS termination enabled: ephemeral CA generated"} {
		if !bytes.Contains(raw, []byte(proof)) {
			return fmt.Errorf("OpenShell network setup proof missing: %s", proof)
		}
	}
	return nil
}

func (s *service) handle(req request) response {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := response{Bundle: s.bundle, Nonce: req.Nonce, Stage: "runtime-starting"}
	if s.err != nil {
		r.Error = s.err.Error()
		return r
	}
	if s.ready {
		r.Stage = "runtime-ready"
	}
	if req.Action == "status" {
		r.OK = s.ready && !s.activated
		return r
	}
	if req.Action != "activate" || !s.ready || s.activated {
		r.Error = "activation requires a fresh runtime-ready guest; replay is forbidden"
		return r
	}
	if err := validateActivation(req); err != nil {
		r.Error = err.Error()
		return r
	}
	s.activated = true
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	steps, accounts, err := activate(ctx, req, s.bundle)
	r.StepsMS = steps
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.OK, r.Stage, r.Accounts, r.Generation = true, "controller-ready", accounts, req.Generation
	r.LandlockABI, err = landlockABI()
	if err != nil {
		r.OK, r.Error = false, err.Error()
	}
	r.OpenShell = true
	mem, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		r.OK, r.Error = false, err.Error()
	}
	r.Meminfo = string(mem)
	return r
}

func activate(ctx context.Context, req request, bundle string) (map[string]float64, []string, error) {
	steps := map[string]float64{}
	start := time.Now()
	mark := func(name string) { steps[name] = float64(time.Since(start).Microseconds()) / 1000 }
	// Re-establish wall time after a cloned snapshot. No credentials are baked.
	clock := unix.NsecToTimeval(req.UnixNano)
	if err := unix.Settimeofday(&clock); err != nil {
		return steps, nil, err
	}
	if err := securityProbe(ctx); err != nil {
		return steps, nil, err
	}
	mark("openshell_confinement")
	// The template never mounts the data disk. Discard kernel probe caches
	// before using the fresh same-capacity backing file selected by the host.
	f, err := os.OpenFile("/dev/vdb", os.O_RDWR, 0)
	if err != nil {
		return steps, nil, err
	}
	err = unix.IoctlSetInt(int(f.Fd()), unix.BLKFLSBUF, 0)
	_ = f.Close()
	if err != nil {
		return steps, nil, err
	}
	disk := layout.AccountDiskMount(req.DiskID)
	if err := mount("/dev/vdb", disk, "ext4", "", unix.MS_NOATIME); err != nil {
		return steps, nil, err
	}
	c := spaceenv.Context{Schema: 1, Space: "fc-poc", DiskID: req.DiskID, DiskUUID: req.DiskUUID,
		DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot,
		HostConfigRoot: "/poc-unused-host-config", UID: 1000, GID: 1000, Bundle: bundle, Generation: req.Generation,
		Mounts: []spaceenv.Mount{{Source: "/poc-workdir", Writable: true}}}
	if err := spaceenv.Configure(c); err != nil {
		return steps, nil, err
	}
	result, err := accountstate.ExecuteForDisk(ctx, c.Space, c.DiskID, "", c.UID, c.GID, accountstate.Request{Action: "initialize"})
	if err != nil {
		return steps, nil, err
	}
	if result.UUID != req.DiskUUID {
		return steps, nil, errors.New("fresh disk UUID mismatch")
	}
	mark("native_storage")
	if err := command(ctx, c, "/usr/local/bin/yoloctl", "controller-internal", "bootstrap"); err != nil {
		return steps, nil, err
	}
	b := initialStore()
	for _, action := range []string{"initialize", "activate"} {
		var input any
		if action == "initialize" {
			input = b
		}
		if err := command(ctx, input, "/usr/local/bin/yoloctl", "controller-internal", action); err != nil {
			return steps, nil, err
		}
	}
	mark("store_activated")
	if err := startService("controller", "/usr/local/bin/yoloctl", "controller-internal", "serve"); err != nil {
		return steps, nil, err
	}
	var conn net.Conn
	if err := until(ctx, func() error {
		var err error
		conn, err = (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(spaceenv.RunRoot, "admin.sock"))
		return err
	}); err != nil {
		return steps, nil, err
	}
	var out, stderr bytes.Buffer
	hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: bundle, Space: c.Space, Generation: c.Generation}
	rpc := controller.Request{ID: req.Nonce, Method: "account.list", Space: c.Space, Flags: map[string][]string{"json": {"true"}}, Dash: -1}
	if err := controller.Call(ctx, conn, hello, rpc, controller.ClientIO{Out: &out, Err: &stderr}); err != nil {
		return steps, nil, fmt.Errorf("real controller read: %w: %s", err, stderr.String())
	}
	var inventory []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out.Bytes(), &inventory); err != nil {
		return steps, nil, fmt.Errorf("decode account.list: %w", err)
	}
	if len(inventory) != 1 || inventory[0].Name != "trial" {
		return steps, nil, errors.New("unexpected controller inventory")
	}
	mark("controller_read")
	if err := command(ctx, nil, "nerdctl", "info"); err != nil {
		return steps, nil, err
	}
	mark("runtime_rechecked")
	return steps, []string{inventory[0].Name}, nil
}

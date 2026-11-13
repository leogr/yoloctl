package agentbox

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

func ownForwardProcess(t *testing.T) (forwardProcessIdentity, os.FileInfo) {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	start, err := forwardProcessStart(raw, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Stat("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	return forwardProcessIdentity{os.Getpid(), start}, executable
}

func TestForwardProcessPinsActualLocalProcessAndRejectsStaleMetadata(t *testing.T) {
	requireForwardProcessKernel(t)
	identity, executable := ownForwardProcess(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := pinForwardProcess(ctx, identity, os.Args, executable, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Close() }()
	for _, fd := range []int{process.process, process.pidfd} {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("process proof FD is not close-on-exec: %v", err)
		}
	}
	if err := process.verify(ctx); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	hostPort := listener.Addr().(*net.TCPAddr).Port
	var socket forwardListenerIdentity
	if err := withForwardListener(listener, hostPort, func(identity forwardListenerIdentity) error { socket = identity; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := process.verifyListener(ctx, hostPort, socket); err != nil {
		t.Fatal("actual owned listening socket refused", err)
	}
	wrongPort := hostPort%65535 + 1
	if err := process.verifyListener(ctx, wrongPort, socket); err == nil {
		t.Fatal("different published port accepted")
	}
	changedSocket := socket
	changedSocket.Inode++
	if err := process.verifyListener(ctx, hostPort, changedSocket); err == nil {
		t.Fatal("foreign socket inode accepted")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.verifyListener(ctx, hostPort, socket); err == nil {
		t.Fatal("live process with a closed listener was accepted")
	}
	// Exact executable inode/owner and write permissions matter. A second
	// trusted hardlink is not a second executable or a metadata authority file.
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "same-executable")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if err := process.verify(ctx); err != nil {
		t.Fatal("same selected executable with another hardlink refused", err)
	}
	changed := identity
	n, _ := strconv.ParseUint(changed.StartTicks, 10, 64)
	changed.StartTicks = strconv.FormatUint(n+1, 10)
	if p, err := pinForwardProcess(ctx, changed, os.Args, executable, uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
		_ = p.Close()
		t.Fatal("reused/stale PID start accepted")
	}
	if p, err := pinForwardProcess(ctx, identity, append(append([]string(nil), os.Args...), "--foreign-attempt"), executable, uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
		_ = p.Close()
		t.Fatal("different argv/attempt accepted")
	}
	foreign, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p, err := pinForwardProcess(ctx, identity, os.Args, foreign, uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
		_ = p.Close()
		t.Fatal("different executable inode accepted")
	}
}

func TestForwardProcessFixtureChild(t *testing.T) {
	if os.Getenv("YOLOCTL_FORWARD_PROCESS_TEST_CHILD") != "1" {
		t.Skip("ordinary local subprocess fixture only")
	}
	if _, err := io.WriteString(os.Stdout, "ready\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestForwardProcessPinnedDescriptorRejectsExitedChild(t *testing.T) {
	requireForwardProcessKernel(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestForwardProcessFixtureChild$")
	cmd.Env = append(os.Environ(), "YOLOCTL_FORWARD_PROCESS_TEST_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			_ = cmd.Wait()
		}
	}()
	// Observe the fixture itself running with its final argv/executable before
	// pinning procfs, rather than relying on the parent returning from Start.
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("child readiness: %q %v", ready, err)
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	start, err := forwardProcessStart(raw, cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	process, err := pinForwardProcess(ctx, forwardProcessIdentity{cmd.Process.Pid, start}, cmd.Args, info, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Close() }()
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	if err := process.verify(ctx); err == nil {
		t.Fatal("exited process accepted through old PID/proc descriptors")
	}
}

func requireForwardProcessKernel(t *testing.T) {
	t.Helper()
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err == nil {
		_ = unix.Close(fd)
		return
	}
	if (errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS)) && os.Getenv("YOLOCTL_REQUIRE_FORWARD_PROCESS_KERNEL") != "1" {
		t.Skipf("pidfd_open unavailable in this confined executor; native G6 must require these cases: %v", err)
	}
	t.Fatalf("required forward process kernel prerequisite: %v", err)
}

func TestForwardProcessStartMetadataRejectsDeadAndMissingIdentity(t *testing.T) {
	// A data-only procfs observation does not require pidfd permission and
	// cannot authorize readiness by itself.
	identity, err := observedForwardProcessIdentity(os.Getpid())
	if err != nil || identity.PID != os.Getpid() || identity.StartTicks == "" {
		t.Fatalf("actual data-only identity: %+v %v", identity, err)
	}
	if _, err := observedForwardProcessIdentity(1); err == nil {
		t.Fatal("init accepted as a forward worker candidate")
	}
	fields := strings.Fields("S 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 123456")
	stat := "712 (name ) with spaces) " + strings.Join(fields, " ")
	if start, err := forwardProcessStart([]byte(stat), 712); err != nil || start != "123456" {
		t.Fatalf("kernel comm boundary: %q %v", start, err)
	}
	for _, changed := range []string{
		strings.Replace(stat, "712 (", "713 (", 1),
		strings.Replace(stat, ") S ", ") Z ", 1),
		strings.Replace(stat, ") S ", ") X ", 1),
		strings.Replace(stat, "123456", "0", 1),
		strings.Replace(stat, "123456", "invalid", 1),
		"712 (missing fields) S 0",
	} {
		if _, err := forwardProcessStart([]byte(changed), 712); err == nil {
			t.Fatal("dead/changed/incomplete process stat accepted")
		}
	}
}

func TestForwardProcessOwnershipAndPrivateEntryRefusals(t *testing.T) {
	const owners = "Name:\tfictional\nUid:\t0\t0\t0\t0\nGid:\t0\t0\t0\t0\n"
	if err := verifyForwardProcessOwners([]byte(owners), 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(owners, "0\t0\t0\t0", "0\t1\t0\t0", 1),
		owners + "Uid:\t0\t0\t0\t0\n",
		strings.Replace(owners, "Gid:\t0\t0\t0\t0\n", "", 1),
		strings.Replace(owners, "0\t0\t0\t0", "0\t0\t0", 1),
	} {
		if err := verifyForwardProcessOwners([]byte(changed), 0, 0); err == nil {
			t.Fatal("changed/incomplete process credentials accepted")
		}
	}
	// No trusted native context is installed by this test, even in root CI.
	if p, err := pinNativeForwardProcess(context.Background(), vmwire.UnitProcess{}, forwardProcessIdentity{}, port.Spec{}, port.ForwardBinding{}); err == nil {
		_ = p.Close()
		t.Fatal("unconfigured native process proof accepted")
	}
}

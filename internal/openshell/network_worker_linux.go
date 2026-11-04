package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type networkRequest struct {
	Schema    int               `json:"schema"`
	Namespace namespaceIdentity `json:"namespace"`
	Inputs    EgressInputs      `json:"inputs"`
}
type networkResult struct {
	Schema    int               `json:"schema"`
	Namespace namespaceIdentity `json:"namespace"`
	Inputs    EgressInputs      `json:"inputs"`
	Readback  json.RawMessage   `json:"readback"`
}

// NetworkWorkerMain is a one-shot root native infrastructure entry, selected
// before CLI/context initialization. It is never an agent or container exec.
// Every failure exits this child; its locked thread must not rejoin a caller.
func NetworkWorkerMain(args []string) int {
	mode, modeErr := nativeNetworkWorkerMode(args)
	if modeErr != nil || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		_, _ = fmt.Fprintln(os.Stderr, "native egress child requires root and exactly its private namespace descriptor")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var err error
	if mode == "apply" || mode == "retire" {
		err = plannedNetworkWorker(ctx, mode)
	} else {
		err = networkWorker(ctx, mode == "plan")
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "native egress child:", err)
		return 1
	}
	return 0
}

func networkWorker(ctx context.Context, planning bool) error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("unbounded native egress request")
	}
	if err := checkJSONKeys(raw); err != nil {
		return err
	}
	var request networkRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if request.Schema != BootstrapSchema || request.Namespace.Inode == 0 {
		return errors.New("unsupported native egress request")
	}
	if _, err := prepareEgress(request.Inputs); err != nil {
		return err
	}
	nft, err := enterNetworkWorkerNamespace(request.Namespace)
	if err != nil {
		return err
	}
	defer func() { _ = nft.Close() }()
	prefixes, err := discoverLocalPrefixes()
	if err != nil {
		return err
	}
	if planning {
		plan, err := newNativeEgressPlan(request.Namespace, request.Inputs, prefixes)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	request.Inputs.LocalPrefixes = compactPrefixes(append(request.Inputs.LocalPrefixes, prefixes...))
	policy, err := GenerateEgressPolicy(request.Inputs)
	if err != nil {
		return err
	}
	if _, err := runNativeNFT(ctx, nft, []string{"-f", "-"}, strings.NewReader(policy)); err != nil {
		return err
	}
	readback, err := runNativeNFT(ctx, nft, []string{"-j", "list", "table", "inet", EgressTable}, nil)
	if err != nil {
		return err
	}
	if err := VerifyEgressPolicyJSON(readback, request.Inputs); err != nil {
		return err
	}
	result := networkResult{BootstrapSchema, request.Namespace, request.Inputs, readback}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// Only called in the dedicated child which exits after the operation. Its
// locked thread never rejoins the controller or returns to its scheduler.
func enterNetworkWorkerNamespace(expected namespaceIdentity) (_ *os.File, err error) {
	namespace, err := networkNamespaceIdentity(3)
	if err != nil || namespace != expected {
		return nil, errors.New("native egress descriptor differs from the independently pinned namespace")
	}
	// A Go process may lock a non-leader thread; /proc/self/ns/net names the
	// leader's namespace. thread-self is the actual thread which calls setns.
	runtime.LockOSThread()
	currentFD, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current, currentErr := networkNamespaceIdentity(currentFD)
	_ = unix.Close(currentFD)
	if currentErr != nil || current == namespace {
		return nil, errors.New("native egress refuses the caller/VM namespace")
	}
	nft, err := openNativeNFT()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = nft.Close()
		}
	}()
	if err := unix.Setns(3, unix.CLONE_NEWNET); err != nil {
		return nil, err
	}
	if err := unix.Close(3); err != nil {
		return nil, err
	}
	actualFD, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	actual, actualErr := networkNamespaceIdentity(actualFD)
	_ = unix.Close(actualFD)
	if actualErr != nil || actual != namespace {
		return nil, errors.New("native egress did not enter the selected namespace")
	}
	return nft, nil
}

func openNativeNFT() (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range []string{"usr", "sbin"} {
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || stat.Mode&0o022 != 0 {
			_ = unix.Close(fd)
			return nil, errors.New("native nft directory has an untrusted writer")
		}
	}
	file, err := unix.Openat(fd, "nft", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, fmt.Errorf("verified native nft is unavailable: %w", err)
	}
	var stat unix.Stat_t
	if unix.Fstat(file, &stat) != nil || stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o022 != 0 || stat.Mode&0o111 == 0 {
		_ = unix.Close(file)
		return nil, errors.New("native nft is not a root-owned executable")
	}
	return os.NewFile(uintptr(file), "native-nft"), nil
}

func runNativeNFT(ctx context.Context, binary *os.File, args []string, input io.Reader) ([]byte, error) {
	// nft resolves '-f -' through /dev/stdin, which need not exist in the
	// selected minimal VM. Its actual inherited input is always descriptor0;
	// use genuine procfs directly without creating a device alias or tempfile.
	if len(args) == 2 && args[0] == "-f" && args[1] == "-" {
		args = slices.Clone(args)
		args[1] = "/proc/self/fd/0"
	}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	cmd.ExtraFiles = []*os.File{binary}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Stdin = input
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	out, stderr := boundedCommandOutput(2<<20), boundedCommandOutput(64<<10)
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("native nft failed: %w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
}

type commandOutput struct {
	bytes.Buffer
	limit int
}

func boundedCommandOutput(limit int) *commandOutput { return &commandOutput{limit: limit} }
func (b *commandOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("native egress command output exceeds bound")
	}
	return b.Buffer.Write(p)
}

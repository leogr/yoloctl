package openshell

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// runtimeChildHandoff owns all four inherited application descriptors. A
// boundary receives its control FD twice, so no unused low slot can be reused
// by Go initialization and mistaken for authority. It receives no supervisor
// run FD. Every source descriptor is closed before either final exec path.
type runtimeChildHandoff struct {
	payload   runtimeChildPayload
	shared    runtimeShared
	artifacts *os.File
	proof     activationProof
}

func (runtimeChildHandoff) String() string     { return "OpenShell child handoff [redacted]" }
func (h runtimeChildHandoff) GoString() string { return h.String() }
func (h runtimeChildHandoff) closeSources() error {
	err := h.shared.close()
	if h.artifacts != nil {
		err = errors.Join(err, h.artifacts.Close())
	}
	return err
}

// readRuntimeChildHandoff is an initial private re-exec precondition. Calling
// the helper by name, copying its binary or setting environment variables
// supplies none of the required parent, descriptor or generation authority.
func readRuntimeChildHandoff(role runtimeChildRole, launch HelperLaunch, release QualifiedRelease) (_ runtimeChildHandoff, err error) {
	if err := verifyRuntimeChildParent(launch); err != nil {
		return runtimeChildHandoff{}, err
	}
	if _, err := runtimeChildFiles(role); err != nil {
		return runtimeChildHandoff{}, err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return runtimeChildHandoff{}, err
	}
	file := os.NewFile(3, "inherited-runtime-authority")
	payload, err := readRuntimeChild(file, 0, 0, release, launch, role)
	err = errors.Join(err, file.Close())
	if err != nil {
		return runtimeChildHandoff{}, err
	}
	control := os.NewFile(4, "inherited-runtime-control")
	run := os.NewFile(5, "inherited-runtime-run")
	artifacts := os.NewFile(6, "inherited-runtime-artifacts")
	result := runtimeChildHandoff{payload: payload, shared: runtimeShared{control: control, supervisorRun: run}, artifacts: artifacts}
	defer func() {
		if err != nil {
			err = errors.Join(err, result.closeSources())
		}
	}()
	if role == runtimeBoundary {
		var first, second unix.Stat_t
		if unix.Fstat(4, &first) != nil || unix.Fstat(5, &second) != nil || first.Dev != second.Dev || first.Ino != second.Ino {
			return runtimeChildHandoff{}, errors.New("boundary received unexpected private source authority")
		}
		if err := run.Close(); err != nil {
			return runtimeChildHandoff{}, err
		}
		result.shared.supervisorRun = nil
	}
	if err := verifyRuntimeChildSources(payload, result.shared, artifacts); err != nil {
		return runtimeChildHandoff{}, err
	}
	namespace, err := currentRuntimeNetwork()
	if err != nil {
		return runtimeChildHandoff{}, err
	}
	proof, _, err := loadActivationProof(filepath.Join(NativeProjection, "generations", payload.Record.Generation), payload.Record, namespace)
	if err != nil {
		return runtimeChildHandoff{}, err
	}
	result.proof = proof
	return result, nil
}

func verifyRuntimeChildParent(launch HelperLaunch) error {
	if os.Getpid() == 1 || os.Getppid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 || launch.Protocol != HelperProtocol {
		return errors.New("private runtime child requires its matching root PID 1 parent")
	}
	if err := launch.validate(); err != nil {
		return err
	}
	proc, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(proc) }()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(proc, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return errors.New("private runtime child requires genuine procfs")
	}
	statusFD, err := unix.Openat(proc, "1/status", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	statusFile := os.NewFile(uintptr(statusFD), "runtime-parent-status")
	if unix.Fstatfs(statusFD, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		_ = statusFile.Close()
		return errors.New("private runtime parent status is outside procfs")
	}
	statusRaw, statusErr := io.ReadAll(io.LimitReader(statusFile, (64<<10)+1))
	if err := errors.Join(statusErr, statusFile.Close()); err != nil {
		return err
	}
	if err := matchRuntimeParentStatus(statusRaw); err != nil {
		return err
	}
	// The parent is PID 1 in this child's unchanged PID namespace. The exact
	// process executable and complete nonsecret launch must both match; no
	// numeric PID lookup selects an arbitrary former container process.
	parent, err := unix.Openat(proc, "1/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	self, err := unix.Openat(proc, "self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(self) }()
	var parentInfo, selfInfo unix.Stat_t
	if unix.Fstat(parent, &parentInfo) != nil || unix.Fstat(self, &selfInfo) != nil || parentInfo.Dev != selfInfo.Dev || parentInfo.Ino != selfInfo.Ino || parentInfo.Uid != 0 || parentInfo.Gid != 0 || parentInfo.Mode&unix.S_IFMT != unix.S_IFREG || parentInfo.Mode&0o7777 != 0o555 || parentInfo.Nlink != 1 {
		return errors.New("private runtime child executable differs from its exact-build parent")
	}
	helper, err := openRuntimeInfrastructure(HelperDirectory)
	if err != nil {
		return err
	}
	defer func() { _ = helper.Close() }()
	if err := verifyInstalledFile(helper, HelperMember, launch.HelperSHA256, nil, "", "", 0o555, 0, 0); err != nil {
		return err
	}
	var projected unix.Stat_t
	if unix.Fstatat(int(helper.Fd()), HelperMember, &projected, unix.AT_SYMLINK_NOFOLLOW) != nil || projected.Dev != selfInfo.Dev || projected.Ino != selfInfo.Ino {
		return errors.New("private runtime child is outside its projected helper")
	}
	cmdline, err := unix.Openat(proc, "1/cmdline", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(cmdline), "runtime-parent-command")
	if unix.Fstatfs(cmdline, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		_ = file.Close()
		return errors.New("private runtime parent command is outside procfs")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return err
	}
	return matchRuntimeParentCommand(raw, launch)
}

func matchRuntimeParentCommand(raw []byte, launch HelperLaunch) error {
	if len(raw) == 0 || len(raw) > 64<<10 || raw[len(raw)-1] != 0 {
		return errors.New("missing or unbounded runtime parent command")
	}
	expected, err := launch.Args()
	if err != nil {
		return err
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(parts) != len(expected) {
		return errors.New("runtime parent command has unexpected arguments")
	}
	actual := make([]string, len(parts))
	for n, part := range parts {
		actual[n] = string(part)
	}
	if !slices.Equal(actual, expected) {
		return errors.New("runtime parent command differs from the selected launch")
	}
	return nil
}

func matchRuntimeParentStatus(raw []byte) error {
	if len(raw) == 0 || len(raw) > 64<<10 || bytes.IndexByte(raw, 0) >= 0 {
		return errors.New("missing or unbounded runtime parent status")
	}
	found := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || (name != "Uid" && name != "Gid" && name != "PPid" && name != "NSpid") {
			continue
		}
		if found[name] {
			return errors.New("duplicate runtime parent identity field")
		}
		found[name] = true
		fields := strings.Fields(value)
		switch name {
		case "Uid", "Gid":
			if !slices.Equal(fields, []string{"0", "0", "0", "0"}) {
				return errors.New("runtime parent retains a non-root real/effective/saved/filesystem identity")
			}
		case "PPid":
			if !slices.Equal(fields, []string{"0"}) {
				return errors.New("runtime parent is not the PID namespace init")
			}
		case "NSpid":
			if len(fields) == 0 || fields[len(fields)-1] != "1" {
				return errors.New("runtime parent is not PID 1 in the selected namespace")
			}
		}
	}
	if len(found) != 4 {
		return errors.New("runtime parent identity fields are incomplete")
	}
	return nil
}

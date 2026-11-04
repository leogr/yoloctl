package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"golang.org/x/sys/unix"
)

type runtimeChildRole string

const (
	runtimeSupervisor    runtimeChildRole = "supervisor"
	runtimeBoundary      runtimeChildRole = "boundary"
	maxRuntimeChildBytes                  = 4 << 20
)

// runtimeChildPayload contains only this child's authority, with no native
// activation key or other child's credential bytes. A sealed inherited memfd
// carries it through re-exec; there is no environment/argv/path override.
type runtimeChildPayload struct {
	Role        runtimeChildRole  `json:"role"`
	Record      RuntimeRecord     `json:"record"`
	Files       map[string][]byte `json:"files"`
	BoundaryEnv []string          `json:"boundary_env,omitempty"`
}

func (runtimeChildPayload) String() string     { return "OpenShell child payload [redacted]" }
func (p runtimeChildPayload) GoString() string { return p.String() }

func runtimeChildFiles(role runtimeChildRole) ([]string, error) {
	switch role {
	case runtimeSupervisor:
		return []string{"supervisor/runtime-descriptor.json", "supervisor/auth.json", "supervisor/policy.yaml", "supervisor/sandbox-policy.rego"}, nil
	case runtimeBoundary:
		return []string{"boundary/bootstrap.json", "boundary/server.crt", "boundary/server.key"}, nil
	default:
		return nil, errors.New("invalid private runtime child role")
	}
}

func (m runtimeMaterial) childPayload(role runtimeChildRole) (runtimeChildPayload, error) {
	names, err := runtimeChildFiles(role)
	if err != nil {
		return runtimeChildPayload{}, err
	}
	payload := runtimeChildPayload{Role: role, Record: m.record, Files: map[string][]byte{}}
	if role == runtimeBoundary {
		if err := validateBoundaryEnvironment(m.boundaryEnv); err != nil {
			return runtimeChildPayload{}, err
		}
		payload.BoundaryEnv = slices.Clone(m.boundaryEnv)
	}
	for _, name := range names {
		body, ok := m.files[name]
		if !ok || sha256Hex(body) != m.record.Files[name] {
			return runtimeChildPayload{}, errors.New("private child source differs from the selected runtime")
		}
		payload.Files[name] = bytes.Clone(body)
	}
	return payload, nil
}

func (p runtimeChildPayload) validate(release QualifiedRelease, launch HelperLaunch, role runtimeChildRole) error {
	if p.Role != role {
		return errors.New("runtime child role differs from its inherited authority")
	}
	if role != runtimeBoundary && len(p.BoundaryEnv) != 0 {
		return errors.New("workload environment cannot enter the supervisor role")
	}
	if err := validateBoundaryEnvironment(p.BoundaryEnv); err != nil {
		return err
	}
	names, err := runtimeChildFiles(role)
	if err != nil {
		return err
	}
	if err := p.Record.Validate(release, launch.HelperSHA256); err != nil {
		return err
	}
	if p.Record.Launch.HelperLaunch() != launch {
		return errors.New("runtime child launch differs from its selected generation")
	}
	if len(p.Files) != len(names) {
		return errors.New("runtime child has incomplete or excessive private authority")
	}
	for name, body := range p.Files {
		if !slices.Contains(names, name) || len(body) == 0 || len(body) > 1<<20 || sha256Hex(body) != p.Record.Files[name] {
			return errors.New("runtime child authority differs from its selected role or hashes")
		}
	}
	return nil
}

func sealedRuntimeChild(payload runtimeChildPayload) (_ *os.File, err error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxRuntimeChildBytes {
		return nil, errors.New("runtime child authority exceeds its private channel bound")
	}
	fd, err := unix.MemfdCreate("yoloctl-openshell-child", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create private child memfd: %w", err)
	}
	file := os.NewFile(uintptr(fd), "openshell-private-child")
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	if _, err = file.Write(raw); err != nil {
		return nil, fmt.Errorf("write private child memfd: %w", err)
	}
	if err = file.Chmod(0o400); err != nil {
		return nil, fmt.Errorf("protect private child memfd: %w", err)
	}
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		return nil, fmt.Errorf("seal private child memfd: %w", err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return file, nil
}

// readRuntimeChild is used only by a root re-exec child, with its parent/fixed
// launch independently verified. uid/gid parameters permit unprivileged file
// tests; the runtime entry supplies 0:0. The caller always closes this FD before
// starting the supervisor or boundary, including on every refusal path.
func readRuntimeChild(file *os.File, uid, gid int, release QualifiedRelease, launch HelperLaunch, role runtimeChildRole) (runtimeChildPayload, error) {
	var stat unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o400 || stat.Nlink != 0 || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Size <= 0 || stat.Size > maxRuntimeChildBytes {
		return runtimeChildPayload{}, errors.New("runtime child channel is not a bounded private anonymous file")
	}
	if unix.Fstatfs(int(file.Fd()), &filesystem) != nil || filesystem.Type != unix.TMPFS_MAGIC {
		return runtimeChildPayload{}, errors.New("runtime child channel is not memory backed")
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals != unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL {
		return runtimeChildPayload{}, errors.New("runtime child channel is not completely sealed")
	}
	// ReadAt avoids reliance on the inherited open-file description's offset.
	raw := make([]byte, int(stat.Size))
	if _, err := io.ReadFull(io.NewSectionReader(file, 0, stat.Size), raw); err != nil {
		return runtimeChildPayload{}, err
	}
	if err := checkJSONKeys(raw); err != nil {
		return runtimeChildPayload{}, err
	}
	var payload runtimeChildPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return runtimeChildPayload{}, err
	}
	if err := payload.validate(release, launch, role); err != nil {
		return runtimeChildPayload{}, err
	}
	return payload, nil
}

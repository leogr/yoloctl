package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// runtimeMaterial remains private to the root helper until copies are placed
// in separate child mount views. It must never enter a log, argv or label.
type runtimeMaterial struct {
	record      RuntimeRecord
	files       map[string][]byte
	boundaryEnv []string
}

func (runtimeMaterial) String() string     { return "OpenShell runtime material [redacted]" }
func (m runtimeMaterial) GoString() string { return m.String() }

type runtimeOwners struct {
	rootUID, rootGID             int
	boundaryUID, boundaryGID     int
	supervisorUID, supervisorGID int
}

// readRuntimeMaterial verifies fixed mounts and the exact running helper before
// reading any credentials. It is an initial PID 1 precondition, not readiness
// or permission to launch. Native activation remains independently required.
func readRuntimeMaterial(launch HelperLaunch, release QualifiedRelease) (runtimeMaterial, error) {
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 || launch.Protocol != HelperProtocol {
		return runtimeMaterial{}, errors.New("runtime material requires the matching root PID 1 helper")
	}
	if err := launch.validate(); err != nil {
		return runtimeMaterial{}, err
	}
	if release.Identity() == "" || release.Identity() != launch.Qualification {
		return runtimeMaterial{}, errors.New("runtime material requires the selected qualified release")
	}
	// Tighten PID 1 before reading either private child environment or keys.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return runtimeMaterial{}, err
	}
	helper, err := openRuntimeDirectory(HelperDirectory)
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = helper.Close() }()
	if err := verifyInstalledFile(helper, HelperMember, launch.HelperSHA256, nil, "static", runtime.GOARCH, 0o555, 0, 0); err != nil {
		return runtimeMaterial{}, err
	}
	projected, err := runtimeRegularAt(helper, HelperMember, 0o555, 0, 0, maxBinaryBytes)
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = projected.Close() }()
	actual, err := os.Open("/proc/self/exe") // intentional kernel executable alias
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = actual.Close() }()
	projectedInfo, err := projected.Stat()
	if err != nil {
		return runtimeMaterial{}, err
	}
	actualInfo, err := actual.Stat()
	if err != nil {
		return runtimeMaterial{}, err
	}
	if !os.SameFile(projectedInfo, actualInfo) {
		return runtimeMaterial{}, errors.New("PID 1 is not the projected exact-build helper")
	}
	native, err := openRuntimeDirectory(NativeProjection)
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = native.Close() }()
	// Read the bounded nonsecret selection first, then qualify all mounts before
	// loading child credentials. None of its values are authority on their own.
	raw, err := runtimeReadAt(native, "current.json", 0, 0, 1<<20)
	if err != nil {
		return runtimeMaterial{}, err
	}
	record, err := decodeRuntimeRecord(raw, release, launch)
	if err != nil {
		return runtimeMaterial{}, err
	}
	if err := verifyRuntimeMounts(record.Launch); err != nil {
		return runtimeMaterial{}, err
	}
	owners := runtimeOwners{0, 0, int(record.Launch.UID), int(record.Launch.GID), int(SupervisorUID), int(SupervisorGID)}
	material, err := readRuntimeMaterialAt(native, release, launch, owners)
	if err != nil {
		return runtimeMaterial{}, err
	}
	// Verify the installed pair again in its selected container projection; a
	// version label or native installation receipt alone cannot qualify a bind.
	metadata, err := json.Marshal(installedRecord{release.Identity(), runtime.GOARCH, material.record.Artifacts, release.document.Pins.RegoSHA256})
	if err != nil {
		return runtimeMaterial{}, err
	}
	// verifyInstalledAt expects a directory member; use the fixed projection's
	// parent and its fixed final name, preserving the same complete inventory.
	parent, err := openRuntimeDirectory("/.openshell")
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = parent.Close() }()
	if err := verifyInstalledAt(parent, "runtime", metadata, material.record.Artifacts, runtime.GOARCH, material.files["supervisor/sandbox-policy.rego"], release.manifest, 0, 0); err != nil {
		return runtimeMaterial{}, err
	}
	material.boundaryEnv, err = captureBoundaryEnvironment(os.Environ())
	if err != nil {
		return runtimeMaterial{}, err
	}
	return material, nil
}

func decodeRuntimeRecord(raw []byte, release QualifiedRelease, launch HelperLaunch) (RuntimeRecord, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return RuntimeRecord{}, errors.New("missing or unbounded runtime record")
	}
	if err := checkJSONKeys(raw); err != nil {
		return RuntimeRecord{}, err
	}
	var record RuntimeRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return RuntimeRecord{}, err
	}
	if err := record.Validate(release, launch.HelperSHA256); err != nil {
		return RuntimeRecord{}, err
	}
	if record.Launch.HelperLaunch() != launch {
		return RuntimeRecord{}, errors.New("runtime record differs from the fixed PID 1 launch")
	}
	return record, nil
}

// DecodeRuntimeRecord validates nonsecret metadata against an already selected
// release and inspected launch. It grants no authority and performs no I/O;
// native callers still verify storage, ownership, generation files and live
// container binding independently. Helpers still require native activation.
func DecodeRuntimeRecord(raw []byte, release QualifiedRelease, launch HelperLaunch) (RuntimeRecord, error) {
	return decodeRuntimeRecord(raw, release, launch)
}

// readRuntimeMaterialAt uses an already opened private native projection.
// Owner parameters exist only for unprivileged filesystem fixtures; production
// always supplies root and the two recorded, distinct runtime identities.
func readRuntimeMaterialAt(root *os.File, release QualifiedRelease, launch HelperLaunch, owners runtimeOwners) (runtimeMaterial, error) {
	if err := runtimeDirectoryOwner(root, owners.rootUID, owners.rootGID); err != nil {
		return runtimeMaterial{}, err
	}
	raw, err := runtimeReadAt(root, "current.json", owners.rootUID, owners.rootGID, 1<<20)
	if err != nil {
		return runtimeMaterial{}, err
	}
	record, err := decodeRuntimeRecord(raw, release, launch)
	if err != nil {
		return runtimeMaterial{}, err
	}
	generations, err := runtimeDirectoryAt(root, "generations", owners.rootUID, owners.rootGID)
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = generations.Close() }()
	generation, err := runtimeDirectoryAt(generations, record.Generation, owners.rootUID, owners.rootGID)
	if err != nil {
		return runtimeMaterial{}, err
	}
	defer func() { _ = generation.Close() }()
	selected, err := runtimeReadAt(generation, "record.json", owners.rootUID, owners.rootGID, 1<<20)
	if err != nil {
		return runtimeMaterial{}, err
	}
	if !bytes.Equal(raw, selected) {
		return runtimeMaterial{}, errors.New("selected runtime record differs from its immutable generation")
	}
	material := runtimeMaterial{record: record, files: map[string][]byte{}}
	for _, leaf := range []struct {
		name     string
		uid, gid int
		names    []string
	}{
		{"boundary", owners.boundaryUID, owners.boundaryGID, []string{"bootstrap.json", "server.crt", "server.key"}},
		{"supervisor", owners.supervisorUID, owners.supervisorGID, []string{"runtime-descriptor.json", "auth.json", "policy.yaml", "sandbox-policy.rego"}},
		{"status", owners.rootUID, owners.rootGID, []string{"activation.key"}},
	} {
		dir, err := runtimeDirectoryAt(generation, leaf.name, leaf.uid, leaf.gid)
		if err != nil {
			return runtimeMaterial{}, err
		}
		for _, name := range leaf.names {
			body, err := runtimeReadAt(dir, name, leaf.uid, leaf.gid, 1<<20)
			if err != nil {
				_ = dir.Close()
				return runtimeMaterial{}, err
			}
			key := leaf.name + "/" + name
			if sha256Hex(body) != record.Files[key] {
				_ = dir.Close()
				return runtimeMaterial{}, errors.New("runtime authority bytes differ from the selected generation")
			}
			material.files[key] = body
		}
		if err := dir.Close(); err != nil {
			return runtimeMaterial{}, err
		}
	}
	if len(material.files["status/activation.key"]) != 32 {
		return runtimeMaterial{}, errors.New("invalid runtime activation key size")
	}
	if err := record.Launch.ValidateWorkspacePolicy(material.files["supervisor/policy.yaml"]); err != nil {
		return runtimeMaterial{}, err
	}
	return material, nil
}

func runtimeDirectoryOwner(file *os.File, uid, gid int) error {
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Mode&0o7777 != 0o700 {
		return errors.New("runtime authority directory has unexpected ownership, type or mode")
	}
	return nil
}

func runtimeDirectoryAt(parent *os.File, name string, uid, gid int) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, errors.New("invalid runtime authority directory member")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err := runtimeDirectoryOwner(file, uid, gid); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func runtimeRegularAt(parent *os.File, name string, mode uint32, uid, gid int, limit int64) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, errors.New("invalid runtime authority file member")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Nlink != 1 || stat.Mode&0o7777 != mode || stat.Size <= 0 || stat.Size > limit {
		_ = file.Close()
		return nil, errors.New("runtime authority file has unexpected ownership, type, links, size or mode")
	}
	return file, nil
}

func runtimeReadAt(parent *os.File, name string, uid, gid int, limit int64) ([]byte, error) {
	file, err := runtimeRegularAt(parent, name, 0o600, uid, gid, limit)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("runtime authority file grew beyond its limit")
	}
	return raw, nil
}

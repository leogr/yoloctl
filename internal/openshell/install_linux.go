package openshell

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// ArtifactFetcher supplies untrusted archive bytes for a qualified URL. Hash,
// member, architecture and ELF checks remain mandatory even for a local cache.
type ArtifactFetcher func(context.Context, string) (io.ReadCloser, error)

type installedRecord struct {
	Qualification string            `json:"qualification"`
	Architecture  string            `json:"architecture"`
	Artifacts     PlatformArtifacts `json:"artifacts"`
	RegoSHA256    string            `json:"rego_sha256"`
}

// InstallNativeRelease publishes a complete immutable pair in a versioned
// directory. It neither selects a container generation nor changes legacy
// runtime bytes. Missing production qualification fails before any download.
func InstallNativeRelease(ctx context.Context, release QualifiedRelease, rego []byte) (string, error) {
	return installNativeRelease(ctx, release, rego, "")
}

// installNativeRelease with an existing box name checks (and normally just
// verifies) that box's already-installed recorded pair: a release from an
// earlier build gives a notice instead of the strict source refusal.
func installNativeRelease(ctx context.Context, release QualifiedRelease, rego []byte, existingBox string) (string, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return "", errors.New("OpenShell installation requires the native root controller")
	}
	if release.Identity() == "" {
		return "", errors.New("OpenShell release is not qualified")
	}
	if err := release.checkHelperSourceFor(existingBox); err != nil {
		return "", err
	}
	root, err := openRuntimeInstallRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := installReleaseAt(ctx, root, release, runtime.GOARCH, rego, fetchReleaseArchive, 0, 0); err != nil {
		return "", err
	}
	return RuntimeDirectory(release.Identity())
}

// openRuntimeInstallRoot walks only the fixed native path, preserving existing
// ownership and refusing links or writable ancestors rather than repairing them.
func openRuntimeInstallRoot() (*os.File, error) {
	return openNativeInstallRoot([]string{"opt", "openshell", "runtime"})
}

func openNativeInstallRoot(components []string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, name := range components {
		created := false
		if err := unix.Mkdirat(fd, name, 0o755); err == nil {
			created = true
		} else if !errors.Is(err, os.ErrExist) {
			_ = unix.Close(fd)
			return nil, err
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == nil && created {
			err = unix.Fchmod(next, 0o755)
		}
		if err == nil && created {
			err = unix.Fsync(next)
		}
		if err == nil && created {
			if syncErr := unix.Fsync(fd); syncErr != nil {
				err = &atomicfile.CommitError{Err: syncErr}
			}
		}
		_ = unix.Close(fd)
		if err != nil {
			if next >= 0 {
				_ = unix.Close(next)
			}
			return nil, err
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Gid != 0 || st.Mode&0o7022 != 0 {
			_ = unix.Close(fd)
			return nil, errors.New("OpenShell runtime root has an untrusted owner or writer")
		}
	}
	return os.NewFile(uintptr(fd), "openshell-runtime-root"), nil
}

// fetchReleaseArchive reads a qualified archive from the CLI's embedded
// artifacts. There is no network fallback: a binary built without them
// refuses native installation.
func fetchReleaseArchive(ctx context.Context, location string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := embeddedArchiveName(location)
	if err != nil {
		return nil, err
	}
	if embeddedArchives == nil {
		return nil, errors.New("this yoloctl was built without its embedded OpenShell artifacts")
	}
	raw, err := embeddedArchives(name)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxArchiveBytes {
		return nil, errors.New("embedded OpenShell archive exceeds its bound")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

var installedNames = []string{"install.json", "openshell-sandbox", "openshell-supervisor", "qualification.json", "sandbox-policy.rego"}

// installReleaseAt is also used by non-root filesystem tests. Its root FD must
// be an already verified trusted directory; it never accepts archive paths.
func installReleaseAt(ctx context.Context, root *os.File, release QualifiedRelease, arch string, rego []byte, fetch ArtifactFetcher, uid, gid int) (err error) {
	platform, err := release.Platform(arch)
	if err != nil {
		return err
	}
	if sha256Hex(rego) != release.document.Pins.RegoSHA256 || len(rego) > 1<<20 || fetch == nil || sha256Hex(release.manifest) != release.Identity() {
		return errors.New("OpenShell installation lacks its matching qualified Rego or archive source")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := json.Marshal(installedRecord{release.Identity(), arch, platform, release.document.Pins.RegoSHA256})
	if err != nil {
		return err
	}
	if err := verifyInstalledAt(root, release.Identity(), record, platform, arch, rego, release.manifest, uid, gid); err == nil {
		// A no-replace publication by a concurrent installer might have
		// completed before its directory fsync. Complete that durability check.
		if err := root.Sync(); err != nil {
			return &atomicfile.CommitError{Err: err}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Only absence of the directory permits construction. A missing member
	// in an existing directory is corruption, never an invitation to repair it.
	var present unix.Stat_t
	if err := unix.Fstatat(int(root.Fd()), release.Identity(), &present, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if err := verifyInstalledAt(root, release.Identity(), record, platform, arch, rego, release.manifest, uid, gid); err != nil {
			return err
		}
		if err := root.Sync(); err != nil {
			return &atomicfile.CommitError{Err: err}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	stageName := ".stage-" + hex.EncodeToString(nonce[:])
	if err := unix.Mkdirat(int(root.Fd()), stageName, 0o700); err != nil {
		return err
	}
	fd, err := unix.Openat(int(root.Fd()), stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		cleanup := unix.Unlinkat(int(root.Fd()), stageName, unix.AT_REMOVEDIR)
		return errors.Join(err, cleanup, root.Sync())
	}
	stage := os.NewFile(uintptr(fd), stageName)
	published := false
	defer func() {
		if !published {
			if e := unix.Fchmod(fd, 0o700); e != nil {
				err = errors.Join(err, e)
			}
			// Exact files in our exclusive, unpublished stage only. No glob or
			// recursive deletion and no cleanup of an existing released tree.
			for _, name := range installedNames {
				if e := unix.Unlinkat(fd, name, 0); e != nil && !errors.Is(e, os.ErrNotExist) {
					err = errors.Join(err, e)
				}
			}
			if e := unix.Unlinkat(int(root.Fd()), stageName, unix.AT_REMOVEDIR); e != nil {
				err = errors.Join(err, e)
			}
			if e := root.Sync(); e != nil {
				err = errors.Join(err, e)
			}
		}
		if e := stage.Close(); e != nil {
			err = errors.Join(err, e)
		}
	}()
	for _, artifact := range []Artifact{platform.Boundary, platform.Supervisor} {
		if err := ctx.Err(); err != nil {
			return err
		}
		input, err := fetch(ctx, artifact.URL)
		if err != nil {
			return err
		}
		err = stageInstallFile(stage, artifact.Member, 0o555, func(file *os.File) error { return VerifyArchive(input, artifact, file) })
		err = errors.Join(err, input.Close())
		if err != nil {
			return err
		}
	}
	for name, raw := range map[string][]byte{"install.json": record, "sandbox-policy.rego": rego, "qualification.json": release.manifest} {
		if err := stageInstallFile(stage, name, 0o444, func(file *os.File) error { _, err := file.Write(raw); return err }); err != nil {
			return err
		}
	}
	if err := unix.Fchmod(fd, 0o555); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := verifyInstalledAt(root, stageName, record, platform, arch, rego, release.manifest, uid, gid); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat2(int(root.Fd()), stageName, int(root.Fd()), release.Identity(), unix.RENAME_NOREPLACE); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		// Compare the complete concurrent winner; never overwrite or adopt
		// a partial/mismatched directory. Our stage is still unpublished.
		if err := verifyInstalledAt(root, release.Identity(), record, platform, arch, rego, release.manifest, uid, gid); err != nil {
			return err
		}
	} else {
		published = true
	}
	if err := root.Sync(); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	return nil
}

func stageInstallFile(stage *os.File, name string, mode uint32, write func(*os.File) error) (err error) {
	fd, err := unix.Openat(int(stage.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := write(file); err != nil {
		return err
	}
	if err := file.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	return file.Sync()
}

func verifyInstalledAt(root *os.File, name string, record []byte, platform PlatformArtifacts, arch string, rego, manifest []byte, uid, gid int) error {
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	defer func() { _ = dir.Close() }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Mode&0o7777 != 0o555 {
		return errors.New("OpenShell release directory has unexpected ownership or mode")
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	slices.Sort(names)
	if !slices.Equal(names, installedNames) {
		return errors.New("OpenShell release directory has missing or unexpected members")
	}
	for _, artifact := range []Artifact{platform.Boundary, platform.Supervisor} {
		loader := platform.BoundaryLoader
		if artifact.Member == "openshell-supervisor" {
			loader = platform.SupervisorLoader
		}
		if err := verifyInstalledFile(dir, artifact.Member, artifact.BinarySHA256, nil, loader, arch, 0o555, uid, gid); err != nil {
			return err
		}
	}
	for name, raw := range map[string][]byte{"install.json": record, "sandbox-policy.rego": rego, "qualification.json": manifest} {
		if err := verifyInstalledFile(dir, name, sha256Hex(raw), raw, "", arch, 0o444, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func verifyInstalledFile(dir *os.File, name, expectedHash string, expected []byte, loader, arch string, mode uint32, uid, gid int) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != mode || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > maxBinaryBytes {
		return errors.New("OpenShell installed file has unexpected type, links, ownership, mode or size")
	}
	hash := sha256.New()
	if expected != nil {
		raw, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
		if err != nil || !bytes.Equal(raw, expected) {
			return errors.New("OpenShell installed metadata differs from qualification")
		}
		_, _ = hash.Write(raw)
	} else if _, err := io.Copy(hash, io.LimitReader(file, maxBinaryBytes+1)); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return errors.New("OpenShell installed binary hash differs from qualification")
	}
	if loader != "" {
		contract, err := ELFContract(file, arch)
		if err != nil {
			return err
		}
		if contract != loader {
			return errors.New("OpenShell installed ELF loader contract differs from qualification")
		}
	}
	return nil
}

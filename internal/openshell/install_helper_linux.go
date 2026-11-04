package openshell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"slices"

	helperinputs "github.com/leogr/yoloctl"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

type installedHelperRecord struct {
	Architecture string                `json:"architecture"`
	BinarySHA256 string                `json:"binary_sha256"`
	Protocol     string                `json:"protocol"`
	Inputs       helperinputs.Identity `json:"inputs"`
	GoVersion    string                `json:"go_version"`
}

// InstallNativeHelper is a preparation operation, before any destructive
// replacement. It selects immutable qualified static bytes independently of
// guestbinary.Resolve's valid dynamic CLI path. No runtime compiler is used.
func InstallNativeHelper(ctx context.Context, release QualifiedRelease) (string, error) {
	return installNativeHelper(ctx, release, "")
}

// installNativeHelper with an existing box name verifies that box's installed,
// hash-pinned helper without the strict source refusal (see installNativeRelease).
func installNativeHelper(ctx context.Context, release QualifiedRelease, existingBox string) (string, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return "", errors.New("helper installation requires the native root controller")
	}
	if err := release.checkHelperSourceFor(existingBox); err != nil {
		return "", err
	}
	artifact, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	root, err := openNativeInstallRoot([]string{"opt", "yoloctl", "openshell-helpers"})
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := installHelperAt(ctx, root, release, runtime.GOARCH, fetchReleaseArchive, 0, 0); err != nil {
		return "", err
	}
	return HelperSourceDirectory(artifact.BinarySHA256)
}

var installedHelperNames = []string{"helper.json", HelperMember}

func installHelperAt(ctx context.Context, root *os.File, release QualifiedRelease, arch string, fetch ArtifactFetcher, uid, gid int) (err error) {
	artifact, err := release.Helper(arch)
	if err != nil {
		return err
	}
	if fetch == nil {
		return errors.New("no qualified static helper archive source")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	selected := release.document.Pins.Helper
	record, err := json.Marshal(installedHelperRecord{arch, artifact.BinarySHA256, selected.Protocol, selected.Inputs, selected.GoVersion})
	if err != nil {
		return err
	}
	verify := func(name string) error { return verifyInstalledHelperAt(root, name, record, artifact, arch, uid, gid) }
	if err := verify(artifact.BinarySHA256); err == nil {
		if err := root.Sync(); err != nil {
			return &atomicfile.CommitError{Err: err}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Absence of a member is not absence of the selected immutable directory.
	var st unix.Stat_t
	if err := unix.Fstatat(int(root.Fd()), artifact.BinarySHA256, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if err := verify(artifact.BinarySHA256); err != nil {
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
	name := ".stage-" + hex.EncodeToString(nonce[:])
	if err := unix.Mkdirat(int(root.Fd()), name, 0o700); err != nil {
		return err
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(err, unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR), root.Sync())
	}
	stage := os.NewFile(uintptr(fd), name)
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, unix.Fchmod(fd, 0o700))
			for _, member := range installedHelperNames {
				if e := unix.Unlinkat(fd, member, 0); e != nil && !errors.Is(e, os.ErrNotExist) {
					err = errors.Join(err, e)
				}
			}
			err = errors.Join(err, unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR), root.Sync())
		}
		err = errors.Join(err, stage.Close())
	}()
	input, err := fetch(ctx, artifact.URL)
	if err != nil {
		return err
	}
	err = stageInstallFile(stage, HelperMember, 0o555, func(file *os.File) error { return VerifyArchive(input, artifact, file) })
	err = errors.Join(err, input.Close())
	if err != nil {
		return err
	}
	if err := stageInstallFile(stage, "helper.json", 0o444, func(file *os.File) error { _, err := file.Write(record); return err }); err != nil {
		return err
	}
	if err := unix.Fchmod(fd, 0o555); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := verify(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat2(int(root.Fd()), name, int(root.Fd()), artifact.BinarySHA256, unix.RENAME_NOREPLACE); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := verify(artifact.BinarySHA256); err != nil {
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

func verifyInstalledHelperAt(root *os.File, name string, record []byte, artifact Artifact, arch string, uid, gid int) error {
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	defer func() { _ = dir.Close() }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o7777 != 0o555 {
		return errors.New("static helper directory has unexpected ownership or mode")
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	slices.Sort(names)
	if !slices.Equal(names, installedHelperNames) {
		return errors.New("static helper directory has missing or unexpected members")
	}
	if err := verifyInstalledFile(dir, "helper.json", sha256Hex(record), record, "", arch, 0o444, uid, gid); err != nil {
		return err
	}
	if err := verifyInstalledFile(dir, HelperMember, artifact.BinarySHA256, nil, "static", arch, 0o555, uid, gid); err != nil {
		return err
	}
	var selected installedHelperRecord
	if err := json.Unmarshal(record, &selected); err != nil {
		return err
	}
	file, err := runtimeRegularAt(dir, HelperMember, 0o555, uid, gid, maxBinaryBytes)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return CheckHelperProducer(file, HelperPins{Inputs: selected.Inputs, GoVersion: selected.GoVersion}, arch)
}

//go:build linux && amd64

package firecracker

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

// The guest installs /opt/yoloctl/bin/yoloctl from its boot disk when the
// system disk's overlay upper layer lacks it (vmguest boot). On slow nested
// hosts that in-guest copy dominates the first boot, so the helper may place
// the same artifact into a system disk that no VMM has ever opened. A host
// record (the pristine marker) is the only evidence of "never opened"; it is
// written when Create formats the disk and durably removed by the helper
// before it starts any VMM. Selection is still settled after boot by
// session.EnsureVMBinary, so seeding is only a speed optimization.
const (
	pristineName    = "system.pristine.json"
	seedPrefix      = "system.seed-"
	seedSuffix      = ".ext4"
	seededBinary    = "upper/opt/yoloctl/bin/yoloctl"
	pristineState   = "formatted"
	seededState     = "seeded"
	seedProgressGap = 30 * time.Second
)

// diskIdentity excludes ctime: renaming the verified seed over system.ext4
// changes it, while inode, size, mode and mtime survive the rename.
type diskIdentity struct {
	Device   uint64        `json:"device"`
	Inode    uint64        `json:"inode"`
	Size     int64         `json:"size"`
	Mode     uint32        `json:"mode"`
	Modified unix.Timespec `json:"modified"`
}

type pristineMarker struct {
	Schema         int          `json:"schema"`
	State          string       `json:"state"`
	ArtifactSHA256 string       `json:"artifact_sha256,omitempty"`
	Identity       diskIdentity `json:"identity"`
}

// bootArtifact is the guest CLI that bootImage places on the boot disk and
// that seeding installs: the start invocation's companion.
var bootArtifact = func(c Config) (guestbinary.Artifact, error) {
	return guestbinary.Resolve(c.Executable, "amd64")
}

// seedReport writes stage records into the helper's log (vm.log). They are
// actual progress: a stage begins, a bounded number of completed-work counts,
// and a stage ends. Nothing is emitted while no work completes.
var seedReport = func(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "yoloctl: system disk: "+format+"\n", args...)
}

func identityOf(path string) (diskIdentity, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return diskIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return diskIdentity{}, fmt.Errorf("%s is not a regular file", path)
	}
	if int(st.Uid) != os.Getuid() {
		return diskIdentity{}, fmt.Errorf("%s is not owned by this user", path)
	}
	return diskIdentity{st.Dev, st.Ino, st.Size, st.Mode, st.Mtim}, nil
}

// writePristine publishes the marker atomically and durably.
func writePristine(dir string, m pristineMarker) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pristine-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(raw)
	if err = errors.Join(err, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), filepath.Join(dir, pristineName)); err != nil {
		return err
	}
	return syncDir(dir)
}

// readPristine returns found=false only when no marker exists.
func readPristine(dir string) (pristineMarker, bool, error) {
	raw, err := safepath.ReadFile(dir, pristineName, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return pristineMarker{}, false, nil
	}
	if err != nil {
		return pristineMarker{}, true, err
	}
	var m pristineMarker
	if err = strictJSON(raw, &m); err != nil {
		return pristineMarker{}, true, err
	}
	if m.Schema != 1 || (m.State != pristineState && m.State != seededState) ||
		(m.State == seededState) != (len(m.ArtifactSHA256) == 64) {
		return pristineMarker{}, true, errors.New("invalid system disk marker")
	}
	return m, true, nil
}

// recordPristine is called by Create right after formatting system.ext4.
func recordPristine(dir string) error {
	id, err := identityOf(filepath.Join(dir, "system.ext4"))
	if err != nil {
		return err
	}
	return writePristine(dir, pristineMarker{Schema: 1, State: pristineState, Identity: id})
}

// invalidatePristine is the helper's last action before it starts the VMM.
// A failure must keep the VMM from starting (fail closed).
func invalidatePristine(dir string) error {
	if err := os.Remove(filepath.Join(dir, pristineName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("invalidate system disk marker: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("invalidate system disk marker: %w", err)
	}
	return nil
}

func seedOrphans(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), seedPrefix) {
			found = append(found, e.Name())
		}
	}
	return found, nil
}

// seedSystemDisk runs in Helper under the instance lock, after the running
// refusal and before the backing storage is opened. It returns an error only
// when the helper must not start a VMM (cancellation, an unpublishable
// marker, a failed publication); every other problem skips seeding and the
// normal boot continues with the guest's own copy.
func seedSystemDisk(ctx context.Context, dir string, c Config) error {
	m, found, err := readPristine(dir)
	if !found {
		return nil
	}
	if err != nil {
		seedReport("skipped: %v", err)
		return nil
	}
	path := filepath.Join(dir, "system.ext4")
	id, err := identityOf(path)
	if err != nil || id != m.Identity {
		seedReport("skipped: system.ext4 does not match its creation record")
		return nil
	}
	orphans, err := seedOrphans(dir)
	if err != nil {
		seedReport("skipped: %v", err)
		return nil
	}
	if len(orphans) > 0 {
		seedReport("skipped: unexplained %s left in place", strings.Join(orphans, ", "))
		return nil
	}
	artifact, err := bootArtifact(c)
	if err != nil {
		seedReport("skipped: %v", err)
		return nil
	}
	if m.State == seededState && m.ArtifactSHA256 == artifact.Sum {
		seedReport("already holds this build's guest binary")
		return nil
	}
	info, err := os.Stat(artifact.Path)
	if err != nil {
		seedReport("skipped: %v", err)
		return nil
	}
	if err = requireHostStorageFor(vmapi.FormatAllocation(Driver{}.Kind(), uint64(id.Size))+uint64(info.Size()), dir); err != nil {
		seedReport("skipped: %v", err)
		return nil
	}
	seed, seedID, err := buildSeed(ctx, dir, id.Size, artifact, info.Size())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		seedReport("skipped: %v", err)
		return nil
	}
	seedReport("publish begin")
	next := pristineMarker{Schema: 1, State: seededState, ArtifactSHA256: artifact.Sum, Identity: seedID}
	if err = writePristine(dir, next); err != nil {
		removeOwned(seed, seedID)
		return fmt.Errorf("record seeded system disk: %w", err)
	}
	if err = os.Rename(seed, path); err != nil {
		removeOwned(seed, seedID)
		return fmt.Errorf("publish seeded system disk: %w", err)
	}
	if err = syncDir(dir); err != nil {
		return fmt.Errorf("publish seeded system disk: %w", err)
	}
	seedReport("publish end")
	return nil
}

// removeOwned unlinks a seed this attempt created, only while the path still
// names that exact file.
func removeOwned(path string, id diskIdentity) {
	if current, err := identityOf(path); err == nil && current.Device == id.Device && current.Inode == id.Inode {
		_ = os.Remove(path)
	}
}

func buildSeed(ctx context.Context, dir string, size int64, artifact guestbinary.Artifact, binarySize int64) (string, diskIdentity, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", diskIdentity{}, err
	}
	path := filepath.Join(dir, seedPrefix+hex.EncodeToString(suffix[:])+seedSuffix)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", diskIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	owned, err := identityOf(path)
	if err != nil {
		_ = f.Close()
		return "", diskIdentity{}, err
	}
	fail := func(err error) (string, diskIdentity, error) {
		removeOwned(path, owned)
		return "", diskIdentity{}, err
	}
	seedReport("build begin (%d bytes of guest binary)", binarySize)
	tree := imageTree{}
	tree[seededBinary] = imageEntry{header: tar.Header{Name: seededBinary, Mode: 0755, Size: binarySize, Typeflag: tar.TypeReg},
		open: func() (io.ReadCloser, error) {
			in, err := os.Open(artifact.Path)
			if err != nil {
				return nil, err
			}
			return &seedProgress{ReadCloser: in, total: binarySize, last: time.Now()}, nil
		}}
	err = tree.writeExt4(ctx, f, size, systemDiskParams())
	_ = f.Close()
	if err != nil {
		return fail(err)
	}
	seedReport("build end; verify begin")
	if err = verifySeed(ctx, path, artifact.Sum, binarySize); err != nil {
		return fail(err)
	}
	seedReport("verify end")
	id, err := identityOf(path)
	if err != nil || id.Device != owned.Device || id.Inode != owned.Inode {
		return fail(errors.Join(errors.New("seed changed during preparation"), err))
	}
	return path, id, nil
}

// seedProgress reports completed copy work at most once per seedProgressGap.
type seedProgress struct {
	io.ReadCloser
	total, done int64
	last        time.Time
}

func (p *seedProgress) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	p.done += int64(n)
	if n > 0 && time.Since(p.last) >= seedProgressGap {
		p.last = time.Now()
		seedReport("build progress %d/%d bytes", p.done, p.total)
	}
	return n, err
}

// verifySeed re-reads the finished image: the directories and the binary
// with root ownership and exact bytes. Its output is fsynced by writeExt4.
func verifySeed(ctx context.Context, path, sum string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	fsys, err := ext4.Read(backendfile.New(f, true), st.Size(), 0, 512)
	if err != nil {
		return err
	}
	for _, p := range []string{"upper", "upper/opt", "upper/opt/yoloctl", "upper/opt/yoloctl/bin", seededBinary} {
		info, err := fsys.Stat(p)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*ext4.StatT)
		if !ok || stat.UID != 0 || stat.GID != 0 || info.Mode().Perm() != 0755 || (p != seededBinary) != info.IsDir() {
			return fmt.Errorf("seeded %s has unexpected type, mode or owner", p)
		}
		if p == seededBinary && info.Size() != size {
			return errors.New("seeded guest binary size differs")
		}
	}
	in, err := fsys.OpenFile(seededBinary, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, imageReader{ctx, in}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		return errors.New("seeded guest binary differs from the boot artifact")
	}
	return nil
}

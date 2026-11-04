//go:build linux && amd64

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

func (Driver) AccountDiskDir(id string) (string, error) { return layout.FirecrackerDiskDir(id) }
func (d Driver) AccountDisk(_ context.Context, id string) (*vmapi.DiskInfo, error) {
	dir, e := d.AccountDiskDir(id)
	if e != nil {
		return nil, e
	}
	ok, e := safepath.RegularFileExists(dir, "data.ext4")
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, nil
	}
	return &vmapi.DiskInfo{Name: layout.AccountDiskName(id), Format: "raw"}, nil
}
func (d Driver) RequireAccountDisk(ctx context.Context, name, id string) error {
	v, e := d.AccountDisk(ctx, id)
	if e != nil {
		return e
	}
	if v == nil {
		return errors.New("owned data disk is missing; refusing an empty replacement")
	}
	return nil
}
func (d Driver) CreateAccountDisk(_ context.Context, id string, size uint64) error {
	dir, e := d.AccountDiskDir(id)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return e
	}
	if e = requireHostStorage(filepath.Dir(dir)); e != nil {
		return e
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return e
	}
	if size == 0 || size > 1<<50 {
		return fmt.Errorf("invalid data disk size %d", size)
	}
	return sparse(filepath.Join(dir, "data.ext4"), int64(size))
}
func sparse(path string, size int64) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	if e = f.Truncate(size); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
func (d Driver) DeleteAccountDisk(ctx context.Context, id string) error {
	dir, e := d.AccountDiskDir(id)
	if e != nil {
		return e
	}
	rows, e := d.List(ctx)
	if e != nil {
		return e
	}
	for _, row := range rows {
		c, e := ReadConfig(row.Dir)
		if e != nil {
			return e
		}
		if c.Spec.DiskID == id {
			return errors.New("remove VM metadata before deleting its data disk")
		}
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if e != nil {
		return e
	}
	defer func() { _ = lock.Close() }()
	if e = safepath.RemoveFile(dir, "data.ext4"); e != nil {
		return e
	}
	if e = os.Remove(filepath.Join(dir, "lock")); e != nil {
		return e
	}
	if e = os.Remove(dir); e != nil {
		return e
	}
	return syncDir(filepath.Dir(dir))
}
func (d Driver) AccountDiskBlank(ctx context.Context, id string) (bool, error) {
	dir, e := d.AccountDiskDir(id)
	if e != nil {
		return false, e
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if e != nil {
		return false, e
	}
	defer func() { _ = lock.Close() }()
	return blankContext(ctx, filepath.Join(dir, "data.ext4"))
}

// Sparse holes are zero by definition. Every allocated extent is read; merely
// lacking an ext4 signature never grants permission to format an existing disk.
func blank(path string) (bool, error) {
	return blankContext(context.Background(), path)
}
func blankContext(ctx context.Context, path string) (bool, error) {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return false, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	st, e := f.Stat()
	if e != nil {
		return false, e
	}
	if !st.Mode().IsRegular() {
		return false, errors.New("disk must be a regular file")
	}
	var before unix.Stat_t
	if e = unix.Fstat(fd, &before); e != nil {
		return false, e
	}
	unchanged := func() (bool, error) {
		var after unix.Stat_t
		if e := unix.Fstat(fd, &after); e != nil {
			return false, e
		}
		if stamp(before) != stamp(after) {
			return false, errors.New("disk changed during zero verification")
		}
		return true, ctx.Err()
	}
	buf := make([]byte, 1<<20)
	for off := int64(0); off < st.Size(); {
		if e = ctx.Err(); e != nil {
			return false, e
		}
		data, e := unix.Seek(fd, off, unix.SEEK_DATA)
		if errors.Is(e, unix.ENXIO) {
			return unchanged()
		}
		if e != nil && !errors.Is(e, unix.EINVAL) {
			return false, e
		}
		if e == nil {
			off = data
		}
		end := st.Size()
		if e == nil {
			end, e = unix.Seek(fd, off, unix.SEEK_HOLE)
			if e != nil {
				return false, e
			}
		}
		for off < end {
			if e = ctx.Err(); e != nil {
				return false, e
			}
			n := min(int64(len(buf)), end-off)
			_, e = f.ReadAt(buf[:n], off)
			if e != nil && e != io.EOF {
				return false, e
			}
			for _, b := range buf[:n] {
				if b != 0 {
					return false, nil
				}
			}
			off += n
		}
	}
	return unchanged()
}
func formatDisk(ctx context.Context, path string) error {
	fd, e := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	var before, after, linked unix.Stat_t
	if e = unix.Fstat(fd, &before); e != nil {
		return e
	}
	clean, e := blankContext(ctx, path)
	if e != nil {
		return e
	}
	if !clean {
		return errors.New("refusing to format a nonblank disk")
	}
	if e = unix.Fstat(fd, &after); e != nil {
		return e
	}
	if e = unix.Lstat(path, &linked); e != nil {
		return e
	}
	if stamp(before) != stamp(after) || stamp(before) != stamp(linked) {
		return errors.New("disk changed before formatting")
	}
	st, e := f.Stat()
	if e != nil {
		return e
	}
	_, e = ext4.Create(backendfile.New(&contextDisk{File: f, ctx: ctx}, false), st.Size(), 0, 512, &ext4.Params{
		SectorsPerBlock: 8, InodeRatio: vmapi.FormatInodeRatio,
		// The library's reserved-GDT option does not create a valid resize
		// inode on these sizes. Capacity is explicit; preserve journalled ext4.
		Features: []ext4.FeatureOpt{ext4.WithFeatureReservedGDTBlocksForExpansion(false)},
	})
	if e != nil {
		return e
	}
	return f.Sync()
}

// Formatting stays cancellable without an external process. This wrapper also
// ensures the backend cannot bypass cancellation with WriterAt operations.
type contextDisk struct {
	*os.File
	ctx context.Context
	// Both callers provide a freshly created or fully zero-verified file. Track
	// pages touched by this formatter so zero initialization can leave untouched
	// sparse holes intact, without dropping later zero overwrites of metadata.
	written []uint64
	size    int64
}

func (f *contextDisk) WriteAt(p []byte, off int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.written == nil {
		st, err := f.Stat()
		if err != nil {
			return 0, err
		}
		f.written = make([]uint64, (st.Size()+4096*64-1)/(4096*64))
		f.size = st.Size()
	}
	if off < 0 || off > f.size-int64(len(p)) {
		return 0, errors.New("filesystem write outside disk")
	}
	first, last := off/4096, (off+int64(len(p))-1)/4096
	zero := true
	for _, b := range p {
		if b != 0 {
			zero = false
			break
		}
	}
	untouched := true
	for page := first; page <= last; page++ {
		if f.written[page/64]&(1<<uint(page%64)) != 0 {
			untouched = false
			break
		}
	}
	if zero && untouched {
		return len(p), nil
	}
	n, err := f.File.WriteAt(p, off)
	if n > 0 {
		for page := first; page <= (off+int64(n)-1)/4096; page++ {
			f.written[page/64] |= 1 << uint(page%64)
		}
	}
	return n, err
}
func (f *contextDisk) Write(p []byte) (int, error) {
	off, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	n, err := f.WriteAt(p, off)
	if err != nil {
		return n, err
	}
	_, err = f.Seek(off+int64(n), io.SeekStart)
	return n, err
}

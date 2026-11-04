package lima

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/lima-vm/go-qcow2reader/image/qcow2"
	"golang.org/x/sys/unix"
)

// AccountDiskBlank grants retry formatting only to a disk whose guest-visible
// contents are entirely zero, with no Lima owner. Lima may create QCOW2 even
// when --format raw is requested. Never infer blankness from a missing filesystem
// signature: even an interrupted partition table is existing data. The caller
// holds the space lock and has established that its VM is stopped or absent.
func (c *Client) AccountDiskBlank(ctx context.Context, id string) (bool, error) {
	disk, err := c.AccountDisk(ctx, id)
	if err != nil {
		return false, err
	}
	if disk == nil {
		return false, errors.New("pending account disk is missing or unreadable")
	}
	if disk.Format != "raw" && disk.Format != "qcow2" {
		return false, fmt.Errorf("pending account disk format %q is unsupported; expected raw or qcow2", disk.Format)
	}
	if disk.Instance != "" {
		return false, fmt.Errorf("account disk is still attached to %s; stop that VM before retrying", disk.Instance)
	}
	dir, err := layout.AccountDiskDir(id)
	if err != nil {
		return false, err
	}
	root, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(root) }()
	fd, err := unix.Openat(root, "datadisk", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(fd), "account datadisk")
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, errors.New("pending account disk must be a nonempty regular file")
	}
	var content io.ReaderAt = f
	size := info.Size()
	if disk.Format == "qcow2" {
		img, err := openAccountQCOW2(f, info.Size())
		if err != nil {
			return false, fmt.Errorf("inspect pending qcow2 account disk: %w", err)
		}
		content, size = img, img.Size()
	}
	reader := io.NewSectionReader(content, 0, size)
	zeros, buf := make([]byte, 1024*1024), make([]byte, 1024*1024)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := reader.Read(buf)
		read += int64(n)
		if !bytes.Equal(buf[:n], zeros[:n]) {
			return false, nil
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, err
		}
	}
	if read != size {
		return false, errors.New("account disk ended before its declared size")
	}
	after, err := f.Stat()
	if err != nil {
		return false, err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return false, errors.New("account disk changed during blank check")
	}
	return true, nil
}

// Inspect the header before opening the QCOW2 reader: backing files must never
// be followed, and snapshots or unsupported/dirty metadata are not proof that
// an image is safe to initialize. The reader owns no file descriptors; f stays
// open under the caller's no-follow handle for the entire scan.
func openAccountQCOW2(f *os.File, physicalSize int64) (*qcow2.Qcow2, error) {
	reader := io.NewSectionReader(f, 0, physicalSize)
	var header qcow2.HeaderFieldsV2
	if err := binary.Read(reader, binary.BigEndian, &header); err != nil {
		return nil, err
	}
	if header.Magic.String() != qcow2.Magic || (header.Version != 2 && header.Version != 3) {
		return nil, errors.New("invalid or unsupported qcow2 header")
	}
	if header.BackingFileOffset != 0 || header.BackingFileSize != 0 || header.NbSnapshots != 0 || header.SnapshotsOffset != 0 || header.CryptMethod != qcow2.CryptMethodNone {
		return nil, errors.New("qcow2 backing files, snapshots and encryption are not permitted for pending account initialization")
	}
	if header.Size == 0 || header.Size > math.MaxInt64 || header.ClusterBits < 9 || header.ClusterBits > 21 {
		return nil, errors.New("invalid qcow2 virtual size or cluster size")
	}
	// Bound the reader's table allocation and reject truncated metadata.
	if header.L1Size == 0 || header.L1Size > 1<<20 || header.L1TableOffset > uint64(physicalSize) || uint64(header.L1Size)*8 > uint64(physicalSize)-header.L1TableOffset {
		return nil, errors.New("invalid or truncated qcow2 L1 table")
	}
	if header.Version == 3 {
		var v3 qcow2.HeaderFieldsV3
		if err := binary.Read(reader, binary.BigEndian, &v3); err != nil {
			return nil, err
		}
		if v3.IncompatibleFeatures != 0 || v3.HeaderLength < 104 || uint64(v3.HeaderLength) > uint64(physicalSize) || v3.HeaderLength > 1<<header.ClusterBits {
			return nil, errors.New("unsupported or dirty qcow2 metadata")
		}
	}
	img, err := qcow2.Open(io.NewSectionReader(f, 0, physicalSize), nil)
	if err != nil {
		return nil, err
	}
	if err := img.Readable(); err != nil {
		return nil, err
	}
	return img, nil
}

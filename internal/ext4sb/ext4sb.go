// Package ext4sb reads the identity and size of an ext4 filesystem from its
// primary superblock, natively, so neither the host nor guest init needs
// e2fsprogs to recognize a disk.
package ext4sb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Superblock is what is read of an ext4 filesystem to identify it and
// learn its size, without e2fsprogs: the filesystem UUID (as findmnt prints
// it) and its size in bytes.
type Superblock struct {
	UUID string
	Size uint64
}

// Read reads the primary ext4 superblock at byte 1024. It checks
// the magic number and a supported block size; anything else is an error,
// never a guess.
func Read(r io.ReaderAt) (Superblock, error) {
	var b [1024]byte
	if _, err := r.ReadAt(b[:], 1024); err != nil {
		return Superblock{}, fmt.Errorf("read ext4 superblock: %w", err)
	}
	le := binary.LittleEndian
	if le.Uint16(b[0x38:]) != 0xEF53 {
		return Superblock{}, errors.New("not an ext4 filesystem")
	}
	logBlock := le.Uint32(b[0x18:])
	if logBlock > 6 { // 1 KiB << 6 = 64 KiB, ext4's largest block
		return Superblock{}, errors.New("unsupported ext4 block size")
	}
	blocks := uint64(le.Uint32(b[0x04:]))
	if le.Uint32(b[0x60:])&0x80 != 0 { // INCOMPAT_64BIT
		blocks |= uint64(le.Uint32(b[0x150:])) << 32
	}
	u := b[0x68:0x78]
	return Superblock{
		UUID: fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]),
		Size: blocks << (10 + logBlock),
	}, nil
}

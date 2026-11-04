//go:build linux

package vmshare

import (
	"context"
	"os"
	"syscall"

	"github.com/pkg/sftp/v2/encoding/ssh/filexfer/openssh"
	"golang.org/x/sys/unix"
)

func (s *Share) StatVFS(_ context.Context, _ *openssh.StatVFSExtendedPacket) (*openssh.StatVFSExtendedReplyPacket, error) {
	f, e := s.root.Open(".")
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	var st unix.Statfs_t
	if e = unix.Fstatfs(int(f.Fd()), &st); e != nil {
		return nil, e
	}
	flags := uint64(st.Flags)
	if !s.writable {
		flags |= unix.ST_RDONLY
	}
	return &openssh.StatVFSExtendedReplyPacket{BlockSize: uint64(st.Bsize), FragmentSize: uint64(st.Frsize), Blocks: st.Blocks, BlocksFree: st.Bfree, BlocksAvail: st.Bavail, Files: st.Files, FilesFree: st.Ffree, FilesAvail: st.Ffree, MountFlags: flags, MaxNameLength: uint64(st.Namelen)}, nil
}

func accessTime(st os.FileInfo) uint32 {
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint32(stat.Atim.Sec)
	}
	return uint32(st.ModTime().Unix())
}
